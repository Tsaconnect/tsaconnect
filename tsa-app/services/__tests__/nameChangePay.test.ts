/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * Paying a name change's fee from this phone. What this pins: nothing is
 * signed here but the transfer (the account's wallet is proven through the
 * wallet-link flow); only the transfer she was shown is
 * sent (from her proven wallet, that contract, network, decimals and exact base
 * units), built for that contract and held before the broadcast; a payment the
 * chain hasn't caught up with is "sent, confirming" and confirmed again with the
 * SAME hash — never paid twice; a refuted one is kept for support; one taken
 * only to be refunded keeps its hash; a send the server refused lifts the hold,
 * one whose outcome is unknown keeps it.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

const mockSend = jest.fn();
jest.mock('../paycrestDeposit', () => ({
  ...jest.requireActual('../paycrestDeposit'),
  sendPaycrestDeposit: (...a: any[]) => mockSend(...a),
}));
const mockBalances = jest.fn();
jest.mock('../walletApi', () => ({ __esModule: true, readWalletBalances: (...a: any[]) => mockBalances(...a) }));
const mockConfirm = jest.fn();
jest.mock('../nameChangeApi', () => ({
  ...jest.requireActual('../nameChangeApi'),
  confirmNameChangePayment: (...a: any[]) => mockConfirm(...a),
}));

import AsyncStorage from '@react-native-async-storage/async-storage';
import { DepositOutcomeUnknownError } from '../paycrestDeposit';
import type { NameChange, StablecoinOption, StablecoinTransfer } from '../nameChangeApi';
import {
  PAYMENT_CHANGED, confirmRecordedPayment, nameChangeChainKey, optionFor,
  otherWalletMessage, payNameChangeInStablecoin, paymentPending, shownFromChange, stablecoinShortfall,
} from '../nameChangePay';

const WALLET = '0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A';
const TREASURY = '0x52908400098527886E0F7030069857D2E4169EE7';
const USDC = '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913';
const shown: StablecoinOption = {
  chain: 'base', chainId: 8453, token: 'USDC', tokenAddress: USDC, amount: '1.00', amountBaseUnits: '1000000', decimals: 6,
};
const transfer: StablecoinTransfer = {
  chain: 'base', chainId: 8453, token: 'USDC', tokenAddress: USDC, from: WALLET, to: TREASURY,
  amount: '1.00', amountBaseUnits: '1000000', decimals: 6,
};
const HASH = `0x${'ab'.repeat(32)}`;
const paid = { id: 'nc1', status: 'paid' };
const coded = (message: string, code: string, status = 409) => Object.assign(new Error(message), { code, status });

beforeEach(async () => {
  jest.clearAllMocks();
  await AsyncStorage.clear();
  mockBalances.mockResolvedValue([
    { chainKey: 'base', symbol: 'USDC', balance: '5' },
    { chainKey: 'base', symbol: 'ETH', balance: '0.01' },
  ]);
  mockSend.mockImplementation(async (_o: unknown, _w: string, _n: unknown, opts: any) => {
    await opts?.beforeBroadcast?.();
    return HASH;
  });
  mockConfirm.mockResolvedValue(paid);
});

describe('the paying wallet', () => {
  it('sends nothing from a wallet that is not the one the server named', async () => {
    const other = '0x1111111111111111111111111111111111111111';
    await expect(payNameChangeInStablecoin('nc1', transfer, shown, other)).rejects.toThrow(otherWalletMessage(WALLET));
    expect(mockSend).not.toHaveBeenCalled();
  });
});

describe('sending the fee', () => {
  it('sends exactly the transfer, for that contract, then confirms it with the hash', async () => {
    await expect(payNameChangeInStablecoin('nc1', transfer, shown, WALLET)).resolves.toEqual(paid);
    expect(mockSend).toHaveBeenCalledWith(
      {
        chain: 'base', token: 'USDC', tokenAmount: '1.00', depositAddress: TREASURY,
        tokenAddress: USDC, amountBaseUnits: '1000000', decimals: 6,
      },
      WALLET, 0.01, expect.anything(),
    );
    expect(mockConfirm).toHaveBeenCalledWith(HASH);
    await expect(paymentPending('nc1')).resolves.toBeNull();
  });

  it('sends nothing that is not what she was shown', async () => {
    for (const t of [
      { ...transfer, amountBaseUnits: '1500000' },
      { ...transfer, amountBaseUnits: '999999' },
      { ...transfer, amountBaseUnits: '1.5' },
      { ...transfer, tokenAddress: TREASURY },
      { ...transfer, chainId: 56 },
      { ...transfer, decimals: 18 },
      { ...transfer, to: '' },
    ]) {
      await expect(payNameChangeInStablecoin('nc1', t, shown, WALLET)).rejects.toThrow(PAYMENT_CHANGED);
    }
    expect(mockSend).not.toHaveBeenCalled();
  });

  it('refuses a network this app cannot send on', async () => {
    const odd = { ...transfer, chain: 'tron', chainId: 728126428 };
    await expect(payNameChangeInStablecoin('nc1', odd, { ...shown, chainId: 728126428 }, WALLET)).rejects.toThrow('cannot send on tron');
    expect(mockSend).not.toHaveBeenCalled();
  });

  it('pays only from the proven wallet', async () => {
    await expect(payNameChangeInStablecoin('nc1', { ...transfer, from: TREASURY }, shown, WALLET))
      .rejects.toThrow('Switch to that wallet');
    expect(mockSend).not.toHaveBeenCalled();
  });

  it('not mined yet is "sent, confirming": confirmed again later with the same hash, never paid again', async () => {
    mockConfirm.mockRejectedValueOnce(coded('Not on-chain yet', 'payment_pending_chain'));
    await expect(payNameChangeInStablecoin('nc1', transfer, shown, WALLET)).resolves.toBeNull();
    await expect(paymentPending('nc1')).resolves.toEqual({ kind: 'sent', txHash: HASH });

    mockConfirm.mockRejectedValueOnce(coded('Not on-chain yet', 'payment_pending_chain'));
    await expect(confirmRecordedPayment('nc1')).resolves.toBeNull();
    await expect(confirmRecordedPayment('nc1')).resolves.toEqual(paid);
    expect(mockConfirm).toHaveBeenLastCalledWith(HASH);
    expect(mockSend).toHaveBeenCalledTimes(1);
    await expect(paymentPending('nc1')).resolves.toBeNull();
  });

  it('a payment taken only to be refunded (short, or late) keeps its hash on this phone', async () => {
    mockConfirm.mockResolvedValueOnce({ id: 'nc1', status: 'cancelled', feeStatus: 'refund_sending', message: 'Your payment was less than the fee, so it is being refunded.' });
    await expect(payNameChangeInStablecoin('nc1', transfer, shown, WALLET)).resolves.toMatchObject({ status: 'cancelled' });
    await expect(paymentPending('nc1')).resolves.toEqual({ kind: 'sent', txHash: HASH });
  });

  it('a refuted payment is kept for support, with its hash', async () => {
    mockConfirm.mockRejectedValueOnce(coded('the transaction reverted', 'payment_mismatch', 422));
    await expect(payNameChangeInStablecoin('nc1', transfer, shown, WALLET)).rejects.toThrow('reverted');
    await expect(paymentPending('nc1')).resolves.toEqual({ kind: 'refuted', txHash: HASH });
  });

  it('a send the server refused lifts the hold; an unanswered one keeps it', async () => {
    mockSend.mockImplementationOnce(async (_o: unknown, _w: string, _n: unknown, opts: any) => {
      await opts.beforeBroadcast();
      throw new Error('nonce too low');
    });
    await expect(payNameChangeInStablecoin('nc1', transfer, shown, WALLET)).rejects.toThrow('nonce too low');
    await expect(paymentPending('nc1')).resolves.toBeNull();

    mockSend.mockImplementationOnce(async (_o: unknown, _w: string, _n: unknown, opts: any) => {
      await opts.beforeBroadcast();
      throw new DepositOutcomeUnknownError();
    });
    await expect(payNameChangeInStablecoin('nc1', transfer, shown, WALLET)).rejects.toBeInstanceOf(DepositOutcomeUnknownError);
    await expect(paymentPending('nc1')).resolves.toEqual({ kind: 'unknown' });
    await expect(confirmRecordedPayment('nc1')).rejects.toThrow();
  });
});

it('a change still to be paid is checked against its own price after a restart', () => {
  const change = {
    id: 'nc1', payChain: 'base', payToken: 'USDC', payAmount: '1.00', payAmountBaseUnits: '1000000', payDecimals: 6, transfer,
  } as NameChange;
  expect(shownFromChange(change)).toEqual(shown);
  expect(shownFromChange({ ...change, transfer: undefined })).toBeNull();
});

it('says when her wallet is short before anything is created', async () => {
  await expect(stablecoinShortfall(shown, WALLET)).resolves.toBeNull();
  mockBalances.mockResolvedValue([{ chainKey: 'base', symbol: 'USDC', balance: '0.4' }, { chainKey: 'base', symbol: 'ETH', balance: '1' }]);
  await expect(stablecoinShortfall(shown, WALLET)).resolves.toMatch(/0\.4 USDC on Base — this payment needs 1/);
  mockBalances.mockResolvedValue(null);
  await expect(stablecoinShortfall(shown, WALLET)).resolves.toBeNull();
});

it('matches a picked coin to the option offered, by contract before symbol', () => {
  const usdt = { ...shown, chain: 'bsc', chainId: 56, token: 'USDT', tokenAddress: '0x55d398326f99059fF775485246999027B3197955', decimals: 18 };
  const options = [shown, usdt];
  expect(optionFor(options, 8453, { symbol: 'USDC', address: USDC.toLowerCase() })).toEqual(shown);
  // A lookalike "USDC" on the right chain is not the listed contract.
  expect(optionFor(options, 8453, { symbol: 'USDC', address: TREASURY })).toBeUndefined();
  expect(optionFor(options, 56, { symbol: 'usdt' })).toEqual(usdt);
  expect(optionFor(options, 1, { symbol: 'USDC' })).toBeUndefined();
  expect(nameChangeChainKey('BASE')).toBe('base');
  expect(nameChangeChainKey('8453')).toBe('base');
  expect(nameChangeChainKey(undefined)).toBeUndefined();
});
