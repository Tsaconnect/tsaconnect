// services/__tests__/liquidityTx.test.ts — the LP's own vault txs: approve
// then deposit, and never "failed" for a tx that may still land.
const mockPrepareDeposit = jest.fn();
const mockPrepareWithdraw = jest.fn();
const mockPrepareClaim = jest.fn();
jest.mock('../liquidityApi', () => ({
  liquidityApi: {
    prepareDeposit: (...a: any[]) => mockPrepareDeposit(...a),
    prepareWithdraw: (...a: any[]) => mockPrepareWithdraw(...a),
    prepareClaim: (...a: any[]) => mockPrepareClaim(...a),
  },
}));
const mockSign = jest.fn();
jest.mock('../p2pTx', () => ({ signAndBroadcast: (...a: any[]) => mockSign(...a) }));
const mockWait = jest.fn();
jest.mock('../txWait', () => {
  class TxRevertedError extends Error {}
  return { TxRevertedError, waitMined: (...a: any[]) => mockWait(...a) };
});
const mockGas = jest.fn();
jest.mock('../wallet', () => ({ ensureGas: (...a: any[]) => mockGas(...a) }));
jest.mock('../walletApi', () => ({ getAuthHeaders: jest.fn().mockResolvedValue({}) }));
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn(), setItem: jest.fn() }));

/* eslint-disable import/first */
import { runLiquidityTx } from '../liquidityTx';
import { TxRevertedError } from '../txWait';
import type { LiquidityMe } from '../liquidityApi';
/* eslint-enable import/first */

const me = { chain: { id: 56, key: 'bsc', name: 'BNB Smart Chain' } } as LiquidityMe;
const tx = (data: string) => ({ to: '0xv', value: '0', data, chainId: '56' });
const plan = (step: string, data = step) => ({ success: true, data: { step, txs: [tx(data)] } });

beforeEach(() => {
  jest.resetAllMocks();
  mockGas.mockResolvedValue(true);
  mockSign.mockImplementation(async (_c: number, t: { data: string }) => ({ hash: `0x${t.data}` }));
  mockWait.mockResolvedValue(undefined);
});

it('approves, waits for it to mine, then prepares and sends the deposit', async () => {
  mockPrepareDeposit.mockResolvedValueOnce(plan('approve')).mockResolvedValueOnce(plan('deposit'));
  const r = await runLiquidityTx({ me, kind: 'deposit', amount: '10', wallet: '0xabc' });
  expect(r).toEqual({ status: 'done', txHash: '0xdeposit' });
  expect(mockPrepareDeposit).toHaveBeenCalledTimes(2);
  expect(mockPrepareDeposit).toHaveBeenCalledWith('10');
  expect(mockSign.mock.calls.map((c) => c[1].data)).toEqual(['approve', 'deposit']);
  expect(mockWait).toHaveBeenCalledTimes(2);
});

const sleep = jest.fn(async () => {});

it('never signs a second approve while the API node lags: waits, then asks the LP to try again', async () => {
  mockPrepareDeposit.mockResolvedValue(plan('approve'));
  const r = await runLiquidityTx({ me, kind: 'deposit', amount: '10', wallet: '0xabc', sleep });
  expect(r).toEqual({ status: 'refused', message: 'Your approval is confirming on the network. Try again in a moment.' });
  expect(mockSign.mock.calls.map((c) => c[1].data)).toEqual(['approve']);
  expect(mockPrepareDeposit).toHaveBeenCalledTimes(5); // the first, the one after the approve, 3 retries
  expect(sleep).toHaveBeenCalledTimes(3);
  expect(sleep).toHaveBeenCalledWith(2000);
});

it('re-prepares after a lagging node catches up, then sends the deposit', async () => {
  mockPrepareDeposit
    .mockResolvedValueOnce(plan('approve'))
    .mockResolvedValueOnce(plan('approve'))
    .mockResolvedValueOnce(plan('deposit'));
  const r = await runLiquidityTx({ me, kind: 'deposit', amount: '10', wallet: '0xabc', sleep });
  expect(r).toEqual({ status: 'done', txHash: '0xdeposit' });
  expect(mockSign.mock.calls.map((c) => c[1].data)).toEqual(['approve', 'deposit']);
  expect(sleep).toHaveBeenCalledTimes(1);
});

it('reports a slow receipt as pending, never failed', async () => {
  mockPrepareWithdraw.mockResolvedValue(plan('withdraw'));
  mockWait.mockRejectedValue(new Error('timed out'));
  expect(await runLiquidityTx({ me, kind: 'withdraw', amount: '1', wallet: '0xabc' }))
    .toEqual({ status: 'pending', txHash: '0xwithdraw', step: 'withdraw' });
});

it('says which tx is pending when the approval is slow to mine', async () => {
  mockPrepareDeposit.mockResolvedValue(plan('approve'));
  mockWait.mockRejectedValue(new Error('timed out'));
  expect(await runLiquidityTx({ me, kind: 'deposit', amount: '10', wallet: '0xabc' }))
    .toEqual({ status: 'pending', txHash: '0xapprove', step: 'approve' });
  expect(mockPrepareDeposit).toHaveBeenCalledTimes(1);
});

it('reports a mined revert so the LP can retry', async () => {
  mockPrepareClaim.mockResolvedValue(plan('claim'));
  mockWait.mockRejectedValue(new TxRevertedError('0xclaim'));
  expect(await runLiquidityTx({ me, kind: 'claim', wallet: '0xabc' })).toEqual({ status: 'reverted', txHash: '0xclaim' });
});

it('passes on an API refusal with its code, signing nothing', async () => {
  mockPrepareDeposit.mockResolvedValue({ success: false, message: 'Your liquidity is paused', code: 'liquidity_paused' });
  const r = await runLiquidityTx({ me, kind: 'deposit', amount: '10', wallet: '0xabc' });
  expect(r).toEqual({ status: 'refused', message: 'Your liquidity is paused', code: 'liquidity_paused' });
  expect(mockSign).not.toHaveBeenCalled();
});

it('stops before preparing when there is no gas', async () => {
  mockGas.mockResolvedValue(false);
  expect(await runLiquidityTx({ me, kind: 'withdraw', amount: '1', wallet: '0xabc' })).toEqual({ status: 'aborted' });
  expect(mockPrepareWithdraw).not.toHaveBeenCalled();
});

it('refuses a tx built for another chain', async () => {
  mockPrepareWithdraw.mockResolvedValue({ success: true, data: { step: 'withdraw', txs: [{ ...tx('w'), chainId: '97' }] } });
  expect((await runLiquidityTx({ me, kind: 'withdraw', amount: '1', wallet: '0xabc' })).status).toBe('refused');
  expect(mockSign).not.toHaveBeenCalled();
});

it('answers busy to a second tap while the first runs', async () => {
  let release!: () => void;
  mockGas.mockReturnValue(new Promise((r) => { release = () => r(true); }));
  mockPrepareClaim.mockResolvedValue(plan('claim'));
  const first = runLiquidityTx({ me, kind: 'claim', wallet: '0xABC' });
  expect(await runLiquidityTx({ me, kind: 'claim', wallet: '0xabc' })).toEqual({ status: 'busy' });
  release();
  expect((await first).status).toBe('done');
});
