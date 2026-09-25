/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * The one guard on sending a Paycrest deposit twice, shared by a sale (held
 * under its order) and a TSA Pay naira payment (held under the payment). The
 * send beneath it is the REAL sendSponsoredPaycrestDeposit — only its
 * collaborators (prepare / sign / submit, the sponsorship API) are stubbed — so
 * where the hold is written relative to the broadcast is what actually runs.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

const mockSignTransaction = jest.fn();
const mockSignTypedData = jest.fn();
jest.mock('../wallet', () => ({
  signTransaction: (...a: any[]) => mockSignTransaction(...a),
  signTypedData: (...a: any[]) => mockSignTypedData(...a),
}));

const mockPrepare = jest.fn();
const mockSubmit = jest.fn();
jest.mock('../walletApi', () => ({
  ...jest.requireActual('../walletApi'),
  prepareSendTransaction: (...a: any[]) => mockPrepare(...a),
  submitTransaction: (...a: any[]) => mockSubmit(...a),
}));

const mockGetOffer = jest.fn();
const mockRelay = jest.fn();
jest.mock('../gasSponsorship', () => ({
  ...jest.requireActual('../gasSponsorship'),
  getSponsorshipOffer: (...a: any[]) => mockGetOffer(...a),
  relayAuthorization: (...a: any[]) => mockRelay(...a),
  requestGasTopUp: jest.fn(),
}));

import AsyncStorage from '@react-native-async-storage/async-storage';
import { depositMayBeOut, depositOutcomeUnknown, sameDeposit, sendHeldDeposit, stillToSend } from '../depositGuard';
import { DepositOutcomeUnknownError } from '../paycrestDeposit';
import { PaycrestError, type PaycrestOrder } from '../paycrestApi';
import { depositHoldKey, HOLD_MAX_MS, payHoldKey, readHold, writeHold } from '../payHold';

const SIGNER = '0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A';
const order = {
  id: 'o1',
  direction: 'sell',
  status: 'awaiting_deposit',
  chain: 'base',
  token: 'USDC',
  tokenAmount: '36.421800',
  depositAddress: '0x2222222222222222222222222222222222222222',
  walletAddress: SIGNER,
} as PaycrestOrder;
const KEY = depositHoldKey('o1');

beforeEach(async () => {
  jest.clearAllMocks();
  await AsyncStorage.clear();
  mockGetOffer.mockRejectedValue(new PaycrestError('off', 404)); // self-paid
  mockPrepare.mockResolvedValue({
    success: true,
    data: { to: '0xusdc', data: '0xa9059cbb', value: '0', gasLimit: '65000', gasPrice: '1000000000', nonce: 7, chainId: 8453 },
  });
  mockSignTransaction.mockResolvedValue('0xsigned');
  mockSubmit.mockResolvedValue({ success: true, data: { txHash: '0xhash' } });
});

describe('sendHeldDeposit', () => {
  it('holds the order BEFORE the self-paid broadcast, and keeps the hold once it went out', async () => {
    mockSubmit.mockImplementation(async () => {
      expect((await readHold(KEY))?.intentId).toBe('o1');
      return { success: true, data: { txHash: '0xhash' } };
    });
    await expect(sendHeldDeposit(order, SIGNER, 1, KEY)).resolves.toBe('0xhash');
    expect(mockSubmit).toHaveBeenCalledTimes(1);
    expect((await readHold(KEY))?.intentId).toBe('o1');
  });

  it('a broadcast whose answer never came back is an unknown outcome, and its hold stays', async () => {
    mockSubmit.mockResolvedValue({ success: false, message: 'Network request failed', unanswered: true });
    const err = await sendHeldDeposit(order, SIGNER, 1, KEY).catch((e) => e);
    expect(err).toBeInstanceOf(DepositOutcomeUnknownError);
    expect(depositOutcomeUnknown(err)).toBe(true);
    expect((await readHold(KEY))?.intentId).toBe('o1');
  });

  it('a broadcast the server refused lifts the hold — nothing left the wallet', async () => {
    mockSubmit.mockResolvedValue({ success: false, message: 'insufficient funds for transfer' });
    const err = await sendHeldDeposit(order, SIGNER, 1, KEY).catch((e) => e);
    expect(depositOutcomeUnknown(err)).toBe(false);
    expect(await readHold(KEY)).toBeNull();
  });

  it('a send refused before any broadcast (gas) writes no hold at all', async () => {
    await expect(sendHeldDeposit(order, SIGNER, 0.00001, KEY)).rejects.toThrow(/gas/);
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
    expect(mockSubmit).not.toHaveBeenCalled();
  });

  it('a hold that cannot be recorded stops the broadcast', async () => {
    const setItem = AsyncStorage.setItem;
    (AsyncStorage as any).setItem = async () => { throw new Error('disk full'); };
    try {
      await expect(sendHeldDeposit(order, SIGNER, 1, KEY)).rejects.toThrow('disk full');
    } finally {
      (AsyncStorage as any).setItem = setItem;
    }
    expect(mockSubmit).not.toHaveBeenCalled();
  });

  it('the relayed deposit never writes a hold — the server holds one relay per order', async () => {
    mockGetOffer.mockResolvedValue({
      mode: 'authorization', chain: 'base', token: 'USDC', decimals: 6, from: SIGNER, to: order.depositAddress,
      value: '36421800', validBefore: 1790000000,
      domain: { name: 'USD Coin', version: '2', chainId: '8453', verifyingContract: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913' },
    });
    mockSignTypedData.mockResolvedValue('0x' + '00'.repeat(65));
    mockRelay.mockResolvedValue({ txHash: '0xrelayed', alreadyUsed: false });
    await expect(sendHeldDeposit(order, SIGNER, 0, KEY)).resolves.toBe('0xrelayed');
    expect(await AsyncStorage.getItem(KEY)).toBeNull();
    expect(mockSubmit).not.toHaveBeenCalled();
  });

  it("a hold left under the key by a payment's EARLIER order is replaced by this order's", async () => {
    await writeHold(payHoldKey('pay-1'), 'o0', 1000);
    await sendHeldDeposit(order, SIGNER, 1, payHoldKey('pay-1'));
    expect((await readHold(payHoldKey('pay-1')))?.intentId).toBe('o1');
  });
});

describe('depositOutcomeUnknown', () => {
  it('reads the in-progress relay 409 by its code, not its words', () => {
    expect(depositOutcomeUnknown(new PaycrestError('Already sending.', 409, 'sponsorship_in_progress'))).toBe(true);
    expect(depositOutcomeUnknown(new PaycrestError('Already sending.', 409))).toBe(false);
    expect(depositOutcomeUnknown(new Error('nonce too low'))).toBe(false);
  });
});

describe('what counts as "may already be out"', () => {
  it('an active hold for this order', async () => {
    await writeHold(KEY, 'o1');
    expect(await depositMayBeOut(KEY, 'o1')).toBe(true);
    expect(await stillToSend(KEY, order)).toBe(false);
  });

  it('a hold that cannot be read — "I could not look" is not "nothing was sent"', async () => {
    await AsyncStorage.setItem(KEY, 'not json');
    expect(await depositMayBeOut(KEY, 'o1')).toBe(true);
  });

  it('not a hold for another order, nor one past its 24 h', async () => {
    await writeHold(KEY, 'o0');
    expect(await depositMayBeOut(KEY, 'o1')).toBe(false);
    await AsyncStorage.clear();
    await writeHold(KEY, 'o1', Date.now() - HOLD_MAX_MS - 1);
    expect(await depositMayBeOut(KEY, 'o1')).toBe(false);
  });

  it('still to send only while the order waits, unclaimed and unheld', async () => {
    expect(await stillToSend(KEY, order)).toBe(true);
    expect(await stillToSend(KEY, { ...order, paymentClaimedAt: '2026-09-25T10:00:00Z' })).toBe(false);
    expect(await stillToSend(KEY, { ...order, status: 'pending' })).toBe(false);
  });
});

describe('sameDeposit', () => {
  it('holds a resend to the chain, token, address and amount she saw', () => {
    expect(sameDeposit(order, { ...order, depositAddress: order.depositAddress!.toUpperCase().replace('0X', '0x') })).toBe(true);
    expect(sameDeposit(order, { ...order, depositAddress: '0x3333333333333333333333333333333333333333' })).toBe(false);
    expect(sameDeposit(order, { ...order, tokenAmount: '36.5' })).toBe(false);
    expect(sameDeposit(order, { ...order, token: 'USDT' })).toBe(false);
    expect(sameDeposit(order, { ...order, chain: 'polygon' })).toBe(false);
    expect(sameDeposit(order, { ...order, depositAddress: undefined })).toBe(false);
  });
});
