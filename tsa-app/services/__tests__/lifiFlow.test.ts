// services/__tests__/lifiFlow.test.ts
//
// Covers executeLiFiSwap — the approve→wait→swap core extracted from
// screens/all-swap.tsx and now shared with the Spot Trade desk. Collaborators
// (wallet signing/nonce/rpc, walletApi submit, lifi calldata) are mocked; the
// module under test runs for real so ordering and failure paths are asserted.

const mockFetchPendingNonce = jest.fn();
const mockSignLiFiTransaction = jest.fn();
const mockWithRpcFallback = jest.fn();

jest.mock('../wallet', () => ({
  fetchPendingNonce: (...a: any[]) => mockFetchPendingNonce(...a),
  signLiFiTransaction: (...a: any[]) => mockSignLiFiTransaction(...a),
  withRpcFallback: (...a: any[]) => mockWithRpcFallback(...a),
}));

const mockSubmitTransaction = jest.fn();
jest.mock('../walletApi', () => ({
  submitTransaction: (...a: any[]) => mockSubmitTransaction(...a),
}));

jest.mock('../lifi', () => ({
  buildERC20ApproveCalldata: () => '0xapprovecalldata',
}));

jest.mock('../swapErrors', () => ({
  mapSubmitError: (m?: string) => m || 'mapped error',
}));

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import { executeLiFiSwap } from '../lifiFlow';

const WALLET = '0xTRADER';

function quote(over: Record<string, any> = {}) {
  return {
    toAmount: '1000',
    toAmountUSD: '1',
    transactionRequest: {
      to: '0xrouter',
      data: '0xswap',
      value: '0x0',
      chainId: 56, // BSC — resolvable via getChainByChainId
      gasLimit: '200000',
      gasPrice: '1000000000',
    },
    ...over,
  } as any;
}

function params(over: Record<string, any> = {}) {
  return {
    quote: quote(),
    fromSymbol: 'USDT',
    fromDecimals: 18,
    fromAmount: '1',
    walletAddress: WALLET,
    ...over,
  };
}

beforeEach(() => {
  jest.clearAllMocks();
  mockFetchPendingNonce.mockResolvedValue(7);
  mockSignLiFiTransaction.mockResolvedValue('0xsigned');
  mockSubmitTransaction.mockResolvedValue({ success: true, data: { txHash: '0xswaphash' } });
  mockWithRpcFallback.mockResolvedValue({ status: 1 });
});

describe('executeLiFiSwap — no approval needed', () => {
  it('signs and submits only the swap, returns its hash', async () => {
    const hash = await executeLiFiSwap(params());

    expect(hash).toBe('0xswaphash');
    expect(mockSignLiFiTransaction).toHaveBeenCalledTimes(1);
    expect(mockSignLiFiTransaction).toHaveBeenCalledWith(expect.objectContaining({ nonce: 7, to: '0xrouter' }));
    expect(mockSubmitTransaction).toHaveBeenCalledTimes(1);
    expect(mockWithRpcFallback).not.toHaveBeenCalled(); // no approve → no wait
  });
});

describe('executeLiFiSwap — with approval', () => {
  const approvalQuote = () =>
    quote({ approvalAddress: '0xspender', approvalToken: '0xtoken' });

  it('approves, waits for it to mine, re-fetches the nonce, then swaps', async () => {
    mockFetchPendingNonce.mockResolvedValueOnce(7).mockResolvedValueOnce(8);
    mockSubmitTransaction
      .mockResolvedValueOnce({ success: true, data: { txHash: '0xapprovehash' } })
      .mockResolvedValueOnce({ success: true, data: { txHash: '0xswaphash' } });

    const stages: string[] = [];
    const hash = await executeLiFiSwap(params({ quote: approvalQuote(), onStage: (s: string) => stages.push(s) }));

    expect(hash).toBe('0xswaphash');
    expect(mockSignLiFiTransaction).toHaveBeenCalledTimes(2);
    // Approve carries the first nonce, swap the re-fetched one.
    expect(mockSignLiFiTransaction.mock.calls[0][0]).toMatchObject({ nonce: 7, to: '0xtoken' });
    expect(mockSignLiFiTransaction.mock.calls[1][0]).toMatchObject({ nonce: 8, to: '0xrouter' });
    expect(mockWithRpcFallback).toHaveBeenCalledTimes(1);
    expect(stages.some((s) => /approving/i.test(s))).toBe(true);
  });

  it('throws (and never swaps) when the approve reverts on-chain', async () => {
    mockSubmitTransaction.mockResolvedValueOnce({ success: true, data: { txHash: '0xapprovehash' } });
    mockWithRpcFallback.mockResolvedValue({ status: 0 });

    await expect(executeLiFiSwap(params({ quote: approvalQuote() }))).rejects.toThrow(/reverted/i);
    expect(mockSubmitTransaction).toHaveBeenCalledTimes(1); // approve only
  });

  it('throws when the approve never confirms within the wait window', async () => {
    mockSubmitTransaction.mockResolvedValueOnce({ success: true, data: { txHash: '0xapprovehash' } });
    mockWithRpcFallback.mockResolvedValue(null); // waitForTransaction timeout

    await expect(executeLiFiSwap(params({ quote: approvalQuote() }))).rejects.toThrow(/taking longer/i);
    expect(mockSubmitTransaction).toHaveBeenCalledTimes(1);
  });

  it('surfaces a backend approve rejection instead of advancing with a stale nonce', async () => {
    mockSubmitTransaction.mockResolvedValueOnce({ success: false, message: 'insufficient funds' });

    await expect(executeLiFiSwap(params({ quote: approvalQuote() }))).rejects.toThrow(/insufficient funds/i);
    expect(mockWithRpcFallback).not.toHaveBeenCalled();
    expect(mockSubmitTransaction).toHaveBeenCalledTimes(1);
  });
});

describe('executeLiFiSwap — failure surfaces', () => {
  it('throws a friendly error when the nonce fetch fails', async () => {
    mockFetchPendingNonce.mockRejectedValue(new Error('boom'));
    await expect(executeLiFiSwap(params())).rejects.toThrow(/could not reach the network/i);
    expect(mockSubmitTransaction).not.toHaveBeenCalled();
  });

  it('throws the mapped backend message when the swap submit is rejected', async () => {
    mockSubmitTransaction.mockResolvedValue({ success: false, message: 'nonce too low' });
    await expect(executeLiFiSwap(params())).rejects.toThrow(/nonce too low/i);
  });

  it('throws on an unsupported chain before any signing', async () => {
    const q = quote();
    q.transactionRequest.chainId = 999999;
    await expect(executeLiFiSwap(params({ quote: q }))).rejects.toThrow(/unsupported chain/i);
    expect(mockSignLiFiTransaction).not.toHaveBeenCalled();
  });
});
