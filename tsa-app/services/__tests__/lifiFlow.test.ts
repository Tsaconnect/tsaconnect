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
  isSolanaLiFiChain: (id: number) => id === 1151111081099710,
  isBtcLiFiChain: (id: number) => id === 20000000000001,
  isSuiLiFiChain: (id: number) => id === 9270000000000000,
}));

const mockExecuteLiFiSolanaSwap = jest.fn();
jest.mock('../chains/solanaLifi', () => ({
  executeLiFiSolanaSwap: (...a: any[]) => mockExecuteLiFiSolanaSwap(...a),
}));

const mockExecuteLiFiBtcSwap = jest.fn();
jest.mock('../chains/btcLifi', () => ({
  executeLiFiBtcSwap: (...a: any[]) => mockExecuteLiFiBtcSwap(...a),
}));

// Mocked for the same reason as the two above, and one more: the real module
// reaches AsyncStorage through hooks/useNetwork at import time, which throws
// under this suite's environment before a single test can run.
const mockExecuteLiFiSuiSwap = jest.fn();
jest.mock('../chains/suiLifi', () => ({
  executeLiFiSuiSwap: (...a: any[]) => mockExecuteLiFiSuiSwap(...a),
}));

jest.mock('../submitErrors', () => ({
  mapSubmitError: (m?: string) => m || 'mapped error',
}));

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import { executeLiFiSwap, SwapBroadcastUnconfirmedError } from '../lifiFlow';
// eslint-disable-next-line import/first -- as above.
import { keccak256 } from 'ethers';

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
    // Submit must declare the wallet the nonce was fetched for, so the BE's
    // signer guard validates against it (not the registered wallet).
    expect(mockSubmitTransaction.mock.calls[0][6]).toBe(WALLET);
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
    expect(mockSubmitTransaction.mock.calls[0][6]).toBe(WALLET); // approve declares the sender
    expect(mockSubmitTransaction.mock.calls[1][6]).toBe(WALLET); // swap too
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

describe('executeLiFiSwap — Solana source routes', () => {
  const SOLANA_CHAIN_ID = 1151111081099710;

  it('delegates to the Solana executor with raw amount + tx data, skipping the EVM path', async () => {
    mockExecuteLiFiSolanaSwap.mockResolvedValue('5solhash');
    const q = quote({ transactionRequest: { data: 'c29sYW5hdHg=' } }); // no EVM fields at all

    const hash = await executeLiFiSwap(params({
      quote: q, fromSymbol: 'SOL', fromDecimals: 9, fromAmount: '1.5', fromChainId: SOLANA_CHAIN_ID,
    }));

    expect(hash).toBe('5solhash');
    expect(mockExecuteLiFiSolanaSwap).toHaveBeenCalledWith(expect.objectContaining({
      txDataB64: 'c29sYW5hdHg=',
      fromAmountRaw: '1500000000', // 1.5 SOL in lamports
      fromSymbol: 'SOL',
      walletAddress: WALLET,
    }));
    // The EVM machinery must never run for a Solana source.
    expect(mockFetchPendingNonce).not.toHaveBeenCalled();
    expect(mockSignLiFiTransaction).not.toHaveBeenCalled();
    expect(mockSubmitTransaction).not.toHaveBeenCalled();
  });

  it('still runs the EVM path when fromChainId is an EVM chain (EVM→SOL direction)', async () => {
    const hash = await executeLiFiSwap(params({ fromChainId: 56 }));
    expect(hash).toBe('0xswaphash');
    expect(mockExecuteLiFiSolanaSwap).not.toHaveBeenCalled();
  });
});

// The backend's submit-time fee fallback multiplies `amount` by 1% as though
// it were dollars. For a swap that amount is a quantity of the from-token, so
// the guess is wrong by whatever the token is worth — pennies on WETH,
// thousands on a memecoin. Swaps defer it and settle a verified figure.
describe('executeLiFiSwap — fee handling', () => {
  it('marks the swap submission fee-deferred', async () => {
    await executeLiFiSwap(params());
    const swapCall = mockSubmitTransaction.mock.calls.find(c => c[1] === 'swap');
    // Defaults to the cross-chain bucket when the caller names none.
    expect(swapCall?.[7]).toEqual({ feeDeferred: true, feeFeature: 'allswap' });
  });

  // The backend records the debt under this label at submit time, so the fee
  // survives a client that never calls settle.
  it('names the fee bucket so the debt can be recorded server-side', async () => {
    await executeLiFiSwap(params({ feeFeature: 'spot' }));
    const swapCall = mockSubmitTransaction.mock.calls.find(c => c[1] === 'swap');
    expect(swapCall?.[7]).toEqual({ feeDeferred: true, feeFeature: 'spot' });
  });

  // The brokered stablecoin ramp charges no platform fee. It must say so, or
  // the sweeper later collects on a flow that is meant to be free.
  it('passes through "none" for a swap that owes nothing', async () => {
    await executeLiFiSwap(params({ feeFeature: 'none' }));
    const swapCall = mockSubmitTransaction.mock.calls.find(c => c[1] === 'swap');
    expect(swapCall?.[7]).toEqual({ feeDeferred: true, feeFeature: 'none' });
  });

  it('leaves the approval alone — it never accrued a fee', async () => {
    mockSubmitTransaction.mockResolvedValue({ success: true, data: { txHash: '0xapprovehash' } });
    mockWithRpcFallback.mockResolvedValue({ status: 1 });
    await executeLiFiSwap(params({
      quote: quote({ approvalAddress: '0xspender', approvalToken: '0xtoken' }),
    }));
    const approveCall = mockSubmitTransaction.mock.calls.find(c => c[1] === 'approve');
    expect(approveCall).toBeDefined();
    expect(approveCall?.[7]).toBeUndefined();
  });
});

describe('executeLiFiSwap — Sui source routes', () => {
  const SUI_CHAIN_ID = 9270000000000000;

  it('delegates to the Sui executor with the coin type, so gas is told apart from the swap', async () => {
    mockExecuteLiFiSuiSwap.mockResolvedValue('suidigest');
    const q = quote({ transactionRequest: { data: 'c3VpdHg=' } }); // BCS bytes, nothing else
    const USDC = '0xdba34672e30cb065b1f93e3ab55318768fd6fef66c15942c9f7cb846e2f900e7::usdc::USDC';

    const hash = await executeLiFiSwap(params({
      quote: q, fromSymbol: 'USDC', fromDecimals: 6, fromAmount: '1',
      fromTokenAddress: USDC, fromChainId: SUI_CHAIN_ID,
    }));

    expect(hash).toBe('suidigest');
    expect(mockExecuteLiFiSuiSwap).toHaveBeenCalledWith(expect.objectContaining({
      txBytesB64: 'c3VpdHg=',
      fromAmountRaw: '1000000',
      fromSymbol: 'USDC',
      // Without this the backend cannot tell which coin the amount bounds, and
      // bounds the SUI spent on gas by it too — refusing the swap.
      fromTokenAddr: USDC,
      walletAddress: WALLET,
    }));
    expect(mockFetchPendingNonce).not.toHaveBeenCalled();
    expect(mockSignLiFiTransaction).not.toHaveBeenCalled();
    expect(mockSubmitTransaction).not.toHaveBeenCalled();
  });

  it('still runs the EVM path when fromChainId is an EVM chain (EVM→SUI direction)', async () => {
    const hash = await executeLiFiSwap(params({ fromChainId: 56 }));
    expect(hash).toBe('0xswaphash');
    expect(mockExecuteLiFiSuiSwap).not.toHaveBeenCalled();
  });
});

describe('executeLiFiSwap — Bitcoin source routes', () => {
  const BTC_CHAIN_ID = 20000000000001;

  it('delegates to the Bitcoin executor with the deposit request and sats, skipping the EVM path', async () => {
    mockExecuteLiFiBtcSwap.mockResolvedValue('btctxid');
    // A UTXO route carries deposit instructions, not EVM calldata.
    const q = quote({ transactionRequest: { to: 'bc1qdeposit', value: '0x1e848', data: '0xmemo' } });

    const hash = await executeLiFiSwap(params({
      quote: q, fromSymbol: 'BTC', fromDecimals: 8, fromAmount: '0.00125', fromChainId: BTC_CHAIN_ID,
    }));

    expect(hash).toBe('btctxid');
    expect(mockExecuteLiFiBtcSwap).toHaveBeenCalledWith(expect.objectContaining({
      txRequest: q.transactionRequest,
      fromAmountRaw: '125000', // 0.00125 BTC in sats
      walletAddress: WALLET,
    }));
    expect(mockFetchPendingNonce).not.toHaveBeenCalled();
    expect(mockSignLiFiTransaction).not.toHaveBeenCalled();
    expect(mockSubmitTransaction).not.toHaveBeenCalled();
  });

  it('still runs the EVM path when fromChainId is an EVM chain (EVM→BTC direction)', async () => {
    const hash = await executeLiFiSwap(params({ fromChainId: 56 }));
    expect(hash).toBe('0xswaphash');
    expect(mockExecuteLiFiBtcSwap).not.toHaveBeenCalled();
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

describe('executeLiFiSwap — the swap hash before the broadcast', () => {
  // Any hex serialises to a hash the same way a real signed tx does: keccak256 of its bytes.
  const SIGNED = '0x02f8aa';
  const SIGNED_HASH = keccak256(SIGNED);

  beforeEach(() => { mockSignLiFiTransaction.mockResolvedValue(SIGNED); });

  it('hands over the signed swap\'s hash before submitting it', async () => {
    const onSigned = jest.fn(async () => {
      expect(mockSubmitTransaction).not.toHaveBeenCalled();
    });
    await executeLiFiSwap(params({ onSigned }));
    expect(onSigned).toHaveBeenCalledWith(SIGNED_HASH);
    expect(mockSubmitTransaction).toHaveBeenCalledTimes(1);
  });

  it('says the broadcast is unconfirmed, with the hash, when the reply never came back', async () => {
    mockSubmitTransaction.mockResolvedValue({ success: false, message: 'Network request failed', unanswered: true });
    const err = await executeLiFiSwap(params({ onSigned: jest.fn() })).catch((e) => e);
    expect(err).toBeInstanceOf(SwapBroadcastUnconfirmedError);
    expect(err.txHash).toBe(SIGNED_HASH);
  });

  it('says the broadcast is unconfirmed when the submit call itself throws', async () => {
    mockSubmitTransaction.mockRejectedValue(new Error('socket hang up'));
    const err = await executeLiFiSwap(params({ onSigned: jest.fn() })).catch((e) => e);
    expect(err).toBeInstanceOf(SwapBroadcastUnconfirmedError);
  });

  it('treats the server\'s own refusal as not sent — a plain error, not an unconfirmed one', async () => {
    mockSubmitTransaction.mockResolvedValue({ success: false, message: 'insufficient funds for gas' });
    const err = await executeLiFiSwap(params({ onSigned: jest.fn() })).catch((e) => e);
    expect(err).not.toBeInstanceOf(SwapBroadcastUnconfirmedError);
    expect(err.message).toMatch(/insufficient funds/);
  });

  it('never calls onSigned for a failure before the swap is signed', async () => {
    mockFetchPendingNonce.mockRejectedValue(new Error('boom'));
    const onSigned = jest.fn();
    await expect(executeLiFiSwap(params({ onSigned }))).rejects.toThrow();
    expect(onSigned).not.toHaveBeenCalled();
  });
});

// A marketplace checkout's funding swap is a MAINNET transaction whatever the
// app's testnet toggle says: its nonce and its approval wait must be read on
// mainnet RPCs, never the active (testnet) map.
describe('executeLiFiSwap — network pin', () => {
  const { setActiveNetwork } = jest.requireActual('../../constants/chains');
  afterEach(() => setActiveNetwork('mainnet'));

  it('reads the nonce and waits on the named network, even with the testnet toggle on', async () => {
    setActiveNetwork('testnet');
    mockSubmitTransaction
      .mockResolvedValueOnce({ success: true, data: { txHash: '0xapprovehash' } })
      .mockResolvedValueOnce({ success: true, data: { txHash: '0xswaphash' } });
    await executeLiFiSwap(params({
      quote: quote({ approvalAddress: '0xspender', approvalToken: '0xtoken' }),
      network: 'mainnet',
    }));
    expect(mockFetchPendingNonce).toHaveBeenCalledWith(WALLET, 'bsc', 'mainnet');
    expect(mockWithRpcFallback.mock.calls[0][0]).toBe('bsc');
    expect(mockWithRpcFallback.mock.calls[0][2]).toBe('mainnet');
  });

  it('keeps the active network when none is named', async () => {
    await executeLiFiSwap(params());
    expect(mockFetchPendingNonce).toHaveBeenCalledWith(WALLET, 'bsc', undefined);
  });
});
