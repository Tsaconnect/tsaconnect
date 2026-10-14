/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * A Tron-source LiFi swap: approve only when the backend says the allowance
 * falls short, wait for it, then sign the route's own transaction against the
 * contract the route names.
 */
jest.mock('../../../hooks/useNetwork', () => ({ getNetwork: () => 'mainnet' }));
jest.mock('../../lifi', () => ({ TRON_NATIVE_ADDRESS: 'T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb' }));
jest.mock('../tronWallet', () => ({ getTronAddress: jest.fn(async () => 'TUSER') }));
const mockPrepare = jest.fn();
const mockBroadcast = jest.fn();
jest.mock('../tronSend', () => ({
  prepareTron: (...a: any[]) => mockPrepare(...a),
  broadcastTron: (...a: any[]) => mockBroadcast(...a),
}));
const mockStatus = jest.fn();
jest.mock('../nonevmApi', () => ({
  registerNonEVMAddress: jest.fn(async () => undefined),
  getNonEVMTxStatus: (...a: any[]) => mockStatus(...a),
}));

import { executeLiFiTronSwap } from '../tronLifi';

const USDT = 'TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t';
const DIAMOND = 'TU3ymitEKCWQFtASkEeHaPb8NfZcJtCHLt';
const quote = {
  approvalAddress: DIAMOND,
  transactionRequest: { to: DIAMOND, data: '0x0a02abcd', chainId: 728126428 },
} as any;
const payload = { family: 'tron', raw: '{}', payloads: [{ index: 0, hashHex: 'aa', curve: 'secp256k1' }] };
const run = (over: Record<string, unknown> = {}) => executeLiFiTronSwap({
  quote, fromAmountRaw: '50000000', fromSymbol: 'USDT', fromTokenAddr: USDT,
  walletAddress: '0xWALLET', sleep: async () => undefined, ...over,
});

beforeEach(() => {
  mockPrepare.mockReset();
  mockBroadcast.mockReset();
  mockStatus.mockReset();
});

it('skips the approval the backend reports as already covered', async () => {
  mockPrepare
    .mockResolvedValueOnce({ prepared: { ...payload, payloads: [] } })
    .mockResolvedValueOnce({ prepared: payload });
  mockBroadcast.mockResolvedValueOnce({ txHash: 'swaptx' });

  await expect(run()).resolves.toBe('swaptx');
  expect(mockBroadcast).toHaveBeenCalledTimes(1);
  expect(mockPrepare.mock.calls[0][0]).toMatchObject({ approve: true, to: DIAMOND, tokenAddr: USDT, amount: '50000000' });
  // The swap names the route's contract and carries the raw tx without 0x.
  expect(mockPrepare.mock.calls[1][0]).toMatchObject({ to: DIAMOND, tronRawHex: '0a02abcd', network: 'mainnet' });
});

const none = { prepared: { ...payload, payloads: [] } };
const confirmed = { found: true, confirmed: true, success: true };

it('approves, waits for the approval to confirm, then swaps', async () => {
  mockPrepare
    .mockResolvedValueOnce({ prepared: payload })
    .mockResolvedValueOnce(none)
    .mockResolvedValueOnce({ prepared: payload });
  mockBroadcast.mockResolvedValueOnce({ txHash: 'approvetx' }).mockResolvedValueOnce({ txHash: 'swaptx' });
  mockStatus.mockResolvedValueOnce({ found: false }).mockResolvedValueOnce(confirmed);

  await expect(run()).resolves.toBe('swaptx');
  expect(mockStatus).toHaveBeenCalledWith('tron', 'approvetx');
  expect(mockBroadcast).toHaveBeenCalledTimes(2);
  // The approval is recorded as moving nothing, not as a second transfer.
  expect(mockBroadcast.mock.calls[0][1]).toMatchObject({ approve: true, amount: '0' });
});

it('signs a reset to 0 and then the exact amount when the backend asks for both', async () => {
  mockPrepare
    .mockResolvedValueOnce({ prepared: payload })
    .mockResolvedValueOnce({ prepared: payload })
    .mockResolvedValueOnce(none)
    .mockResolvedValueOnce({ prepared: payload });
  mockBroadcast
    .mockResolvedValueOnce({ txHash: 'reset' }).mockResolvedValueOnce({ txHash: 'approve' })
    .mockResolvedValueOnce({ txHash: 'swaptx' });
  mockStatus.mockResolvedValue(confirmed);

  await expect(run()).resolves.toBe('swaptx');
  expect(mockBroadcast).toHaveBeenCalledTimes(3);
});

it('gives up rather than approving forever', async () => {
  mockPrepare.mockResolvedValue({ prepared: payload });
  mockBroadcast.mockResolvedValue({ txHash: 'approvetx' });
  mockStatus.mockResolvedValue(confirmed);

  await expect(run()).rejects.toThrow(/did not take effect/);
  expect(mockPrepare.mock.calls.every(([p]) => p.approve)).toBe(true);
});

it('refuses a route that approves one contract and calls another', async () => {
  await expect(run({ quote: { ...quote, approvalAddress: 'TATTACKER' } })).rejects.toThrow(/different contract/);
  expect(mockPrepare).not.toHaveBeenCalled();
});

it('stops at a reverted approval instead of swapping', async () => {
  mockPrepare.mockResolvedValue({ prepared: payload });
  mockBroadcast.mockResolvedValueOnce({ txHash: 'approvetx' });
  mockStatus.mockResolvedValueOnce({ found: true, confirmed: true, success: false });

  await expect(run()).rejects.toThrow(/Approval failed/);
  expect(mockBroadcast).toHaveBeenCalledTimes(1);
});

it('needs no approval to sell native TRX', async () => {
  mockPrepare.mockResolvedValueOnce({ prepared: payload });
  mockBroadcast.mockResolvedValueOnce({ txHash: 'swaptx' });

  await run({ fromSymbol: 'TRX', fromTokenAddr: 'T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb' });
  expect(mockPrepare).toHaveBeenCalledTimes(1);
  expect(mockPrepare.mock.calls[0][0]).toMatchObject({ tokenAddr: '', tronRawHex: '0a02abcd' });
});
