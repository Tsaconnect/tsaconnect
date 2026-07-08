// services/__tests__/mcgpFlow.test.ts
//
// Regression tests for the "Approving & buying… forever" bug (PM report
// 2026-07-08): mcgpFlow awaited resp.wait() with NO timeout, no gas preflight,
// and no receipt.status check, so a tx the RPC accepted but never mined (or a
// mined-but-reverted approve) left the buy/sell screens spinning indefinitely.
//
// The module under test runs for real; wallet helpers + the mcgpSwapApi client
// are mocked, mirroring escrowFunding.test.ts.

const mockSignTransaction = jest.fn();
const mockBroadcastTransaction = jest.fn();
const mockGetActiveWallet = jest.fn();
const mockEnsureGas = jest.fn();

jest.mock('../wallet', () => ({
  signTransaction: (...a: any[]) => mockSignTransaction(...a),
  broadcastTransaction: (...a: any[]) => mockBroadcastTransaction(...a),
  getActiveWallet: (...a: any[]) => mockGetActiveWallet(...a),
  ensureGas: (...a: any[]) => mockEnsureGas(...a),
}));

jest.mock('../p2p', () => ({
  unsignedTxToTxLike: (tx: any) => ({ __txLike: true, ...tx }),
}));

const mockPrepareApprove = jest.fn();
const mockPrepareBuy = jest.fn();
const mockPrepareSell = jest.fn();
const mockConfirmSwap = jest.fn();

jest.mock('../mcgpSwapApi', () => ({
  prepareMcgpApprove: (...a: any[]) => mockPrepareApprove(...a),
  prepareMcgpBuy: (...a: any[]) => mockPrepareBuy(...a),
  prepareMcgpSell: (...a: any[]) => mockPrepareSell(...a),
  prepareMcgpWithdraw: jest.fn(),
  prepareMcgpPay: jest.fn(),
  confirmMcgpSwap: (...a: any[]) => mockConfirmSwap(...a),
  confirmMcgpPay: jest.fn(),
}));

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import { executeBuy, executeSellPremium } from '../mcgpFlow';

const FROM = '0xBUYER';
const MCGP = '1000000000000000000'; // 1 MCGP
const USDC = '20000'; // 0.02 USDC

// Happy on-chain world: gas ok, approve needed, everything signs/mines fast.
function arrangeHappyChain() {
  mockGetActiveWallet.mockResolvedValue(FROM);
  mockEnsureGas.mockResolvedValue(true);
  mockSignTransaction.mockResolvedValue('0xsigned');
  mockBroadcastTransaction.mockResolvedValue({
    hash: '0xhash',
    wait: jest.fn().mockResolvedValue({ status: 1 }),
  });
  mockPrepareApprove.mockResolvedValue({
    success: true,
    data: { currentAllowance: '0', tx: { to: '0xusdc', data: 'aa' } },
  });
  mockPrepareBuy.mockResolvedValue({ success: true, data: { to: '0xswap', data: 'bb' } });
  mockPrepareSell.mockResolvedValue({ success: true, data: { to: '0xswap', data: 'cc' } });
  mockConfirmSwap.mockResolvedValue({ success: true, data: {} });
}

beforeAll(() => {
  jest.useFakeTimers();
});
afterAll(() => {
  jest.useRealTimers();
});

beforeEach(() => {
  jest.clearAllMocks();
});

describe('executeBuy — happy path', () => {
  it('approves (short allowance), buys, confirms, returns the tx hash', async () => {
    arrangeHappyChain();

    const hash = await executeBuy(MCGP, USDC);

    expect(hash).toBe('0xhash');
    expect(mockBroadcastTransaction).toHaveBeenCalledTimes(2); // approve + buy
    expect(mockConfirmSwap).toHaveBeenCalledWith('0xhash', 'buy_premium');
  });

  it('skips the approve broadcast when the allowance already covers maxUsdc', async () => {
    arrangeHappyChain();
    mockPrepareApprove.mockResolvedValue({
      success: true,
      data: { currentAllowance: USDC, tx: { to: '0xusdc', data: 'aa' } },
    });

    await executeBuy(MCGP, USDC);

    expect(mockBroadcastTransaction).toHaveBeenCalledTimes(1); // buy only
  });
});

describe('executeBuy — gas preflight', () => {
  it('rejects with a gas message and broadcasts NOTHING when the wallet has no S', async () => {
    arrangeHappyChain();
    mockEnsureGas.mockImplementation(async (_chain: string, _addr: string, onAlert?: any) => {
      onAlert?.('Gas needed', 'You need S to pay network fees on Sonic.');
      return false;
    });

    await expect(executeBuy(MCGP, USDC)).rejects.toThrow(/gas|S /i);
    expect(mockSignTransaction).not.toHaveBeenCalled();
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });
});

describe('executeBuy — hang guard (the PM bug)', () => {
  it('rejects instead of hanging when the tx never mines (wait() never resolves)', async () => {
    arrangeHappyChain();
    mockBroadcastTransaction.mockResolvedValue({
      hash: '0xhash',
      wait: jest.fn().mockReturnValue(new Promise(() => {})), // never settles
    });

    const p = executeBuy(MCGP, USDC);
    const guarded = expect(p).rejects.toThrow(/too long|confirm/i);
    await jest.advanceTimersByTimeAsync(90_000);
    await guarded;

    expect(mockConfirmSwap).not.toHaveBeenCalled();
  });

  it('rejects when the approve mines but REVERTED (status 0), without broadcasting the buy', async () => {
    arrangeHappyChain();
    mockBroadcastTransaction.mockResolvedValueOnce({
      hash: '0xapprove',
      wait: jest.fn().mockResolvedValue({ status: 0 }),
    });

    await expect(executeBuy(MCGP, USDC)).rejects.toThrow(/failed/i);
    expect(mockPrepareBuy).not.toHaveBeenCalled();
    expect(mockBroadcastTransaction).toHaveBeenCalledTimes(1);
  });
});

describe('executeBuy — confirm is best-effort', () => {
  it('still resolves with the hash when the backend confirm fails (funds already moved)', async () => {
    arrangeHappyChain();
    mockConfirmSwap.mockResolvedValue({ success: false, message: 'nope' });

    await expect(executeBuy(MCGP, USDC)).resolves.toBe('0xhash');
  });
});

describe('executeSellPremium — shares the guards', () => {
  it('rejects instead of hanging when the sell tx never mines', async () => {
    arrangeHappyChain();
    mockBroadcastTransaction.mockResolvedValue({
      hash: '0xsell',
      wait: jest.fn().mockReturnValue(new Promise(() => {})),
    });

    const p = executeSellPremium(MCGP, USDC);
    const guarded = expect(p).rejects.toThrow(/too long|confirm/i);
    await jest.advanceTimersByTimeAsync(90_000);
    await guarded;
  });

  it('rejects with a gas message before broadcasting when the wallet has no S', async () => {
    arrangeHappyChain();
    mockEnsureGas.mockResolvedValue(false);

    await expect(executeSellPremium(MCGP, USDC)).rejects.toThrow(/gas|S /i);
    expect(mockBroadcastTransaction).not.toHaveBeenCalled();
  });
});
