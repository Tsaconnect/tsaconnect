// services/__tests__/payRefundSend.test.ts
//
// Sending a refund: only from the wallet the server names, a transfer checked to be exactly
// the refund before it is signed, gas checked before signing, and the signed transaction
// handed to the refund's own submit route — never /wallet/submit-tx.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

const OWNER = '0x1111111111111111111111111111111111111111';
const PAYER = '0x2222222222222222222222222222222222222222';
const USDT_BSC = '0x55d398326f99059fF775485246999027B3197955';

const mockSigner = jest.fn();
const mockNative = jest.fn();
const mockSign = jest.fn();
jest.mock('../wallet', () => ({
  getSignableWallet: () => mockSigner(),
  nativeBalance: (...a: any[]) => mockNative(...a),
  signTransaction: (...a: any[]) => mockSign(...a),
  signTypedData: jest.fn(),
  getActiveWallet: async () => '0x1111111111111111111111111111111111111111',
}));

const mockPrepare = jest.fn();
const mockWalletSubmit = jest.fn();
jest.mock('../walletApi', () => {
  const actual = jest.requireActual('../walletApi');
  return { ...actual, prepareSendTransaction: (...a: any[]) => mockPrepare(...a), submitTransaction: (...a: any[]) => mockWalletSubmit(...a) };
});

const mockSubmit = jest.fn();
jest.mock('../payMerchantApi', () => ({ submitPayRefund: (...a: any[]) => mockSubmit(...a) }));

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import { ethers } from 'ethers';
// eslint-disable-next-line import/first
import { RefundWalletUnprovenError, RefundWrongWalletError, sendPayRefund } from '../payRefundSend';
// eslint-disable-next-line import/first
import type { PayRefundOption } from '../payMerchantApi';

const option: PayRefundOption = {
  chainId: 56, chainKey: 'bsc', chainName: 'BNB Smart Chain', tokenSymbol: 'USDT', tokenAddress: USDT_BSC, decimals: 18,
  amountBaseUnits: '12500000000000000000', amountDisplay: '12.5', isDefault: true,
};
const transfer = (to: string, amount: string) =>
  new ethers.Interface(['function transfer(address to, uint256 amount)']).encodeFunctionData('transfer', [to, amount]);
const prepared = (over: Record<string, unknown> = {}) => ({
  success: true,
  data: { to: USDT_BSC, data: transfer(PAYER, option.amountBaseUnits), value: '0', gasLimit: '60000', gasPrice: '1000000000', nonce: 7, chainId: 56, ...over },
});

beforeEach(() => {
  jest.clearAllMocks();
  mockSigner.mockResolvedValue(OWNER.toUpperCase().replace('0X', '0x'));
  mockNative.mockResolvedValue(10n ** 16n); // 0.01 BNB
  mockPrepare.mockResolvedValue(prepared());
  mockSign.mockResolvedValue('0xsigned');
  mockSubmit.mockResolvedValue({ id: 'r1', status: 'submitted' });
});

it('prepares, checks, signs with her wallet and hands the signed transfer to the refund', async () => {
  const fetchSpy = jest.fn();
  global.fetch = fetchSpy as unknown as typeof fetch;
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).resolves.toEqual({ id: 'r1', status: 'submitted' });
  expect(mockPrepare).toHaveBeenCalledWith('USDT', PAYER, '12.5', 56, { tokenAddress: USDT_BSC, decimals: 18, fromAddress: OWNER });
  expect(mockNative).toHaveBeenCalledWith('bsc', OWNER, 'mainnet');
  expect(mockSign).toHaveBeenCalledWith(expect.objectContaining({ to: USDT_BSC, nonce: 7, chainId: 56, type: 0 }), OWNER);
  expect(mockSubmit).toHaveBeenCalledWith('r1', { chainId: 56, tokenSymbol: 'USDT', signedTx: '0xsigned' });
  expect(mockWalletSubmit).not.toHaveBeenCalled();
  // Nothing in the refund path reaches the network on its own — least of all /wallet/submit-tx.
  expect(fetchSpy).not.toHaveBeenCalled();
});

// Review Focus 1.
it('refuses before preparing anything when the device wallet is another one, or none', async () => {
  mockSigner.mockResolvedValue('0x9999999999999999999999999999999999999999');
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toBeInstanceOf(RefundWrongWalletError);
  mockSigner.mockResolvedValue(null);
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('0x1111…1111');
  expect(mockPrepare).not.toHaveBeenCalled();
  expect(mockSign).not.toHaveBeenCalled();
});

// Plans review I10: the server names no wallet until she proves hers.
it('refuses at once, with its own error, when no wallet is named yet', async () => {
  await expect(sendPayRefund('r1', option, PAYER, '')).rejects.toBeInstanceOf(RefundWalletUnprovenError);
  expect(mockSigner).not.toHaveBeenCalled();
  expect(mockPrepare).not.toHaveBeenCalled();
});

it('never signs a prepared transfer that is not exactly the refund', async () => {
  mockPrepare.mockResolvedValue(prepared({ data: transfer('0x3333333333333333333333333333333333333333', option.amountBaseUnits) }));
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('not the one you were shown');
  mockPrepare.mockResolvedValue(prepared({ data: transfer(PAYER, '1') }));
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('not the one you were shown');
  // Another token contract.
  mockPrepare.mockResolvedValue(prepared({ to: '0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d' }));
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('not the one you were shown');
  // Another network.
  mockPrepare.mockResolvedValue(prepared({ chainId: 1 }));
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('not the one you were shown');
  // Native value riding along with the transfer.
  mockPrepare.mockResolvedValue(prepared({ value: '1' }));
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('not the one you were shown');
  expect(mockSign).not.toHaveBeenCalled();
  expect(mockSubmit).not.toHaveBeenCalled();
});

it('stops before signing when she cannot pay the network fee', async () => {
  mockNative.mockResolvedValue(0n);
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('Not enough BNB on BNB Smart Chain to pay the network fee');
  // 60000 gas at 1 gwei is 0.00006 BNB; 0.00005 is short of it.
  mockNative.mockResolvedValue(5n * 10n ** 13n);
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('Not enough BNB');
  expect(mockSign).not.toHaveBeenCalled();
  expect(mockSubmit).not.toHaveBeenCalled();
});

// The gas read follows the refund's chainId: a chainKey naming another network is not trusted for it.
it('never reads gas on a network other than the one the transfer is signed for', async () => {
  await sendPayRefund('r1', { ...option, chainKey: 'base' }, PAYER, OWNER);
  expect(mockNative).not.toHaveBeenCalled();
  expect(mockSign).toHaveBeenCalledWith(expect.objectContaining({ chainId: 56 }), OWNER);
});

it('an unreadable gas balance does not block the send', async () => {
  mockNative.mockResolvedValue(null);
  await sendPayRefund('r1', option, PAYER, OWNER);
  expect(mockSubmit).toHaveBeenCalled();
});

it('a refused prepare says why and submits nothing', async () => {
  mockPrepare.mockResolvedValue({ success: false, message: 'Insufficient USDT balance' });
  await expect(sendPayRefund('r1', option, PAYER, OWNER)).rejects.toThrow('Insufficient USDT balance');
  expect(mockSubmit).not.toHaveBeenCalled();
});
