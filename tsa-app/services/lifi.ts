import { ethers } from 'ethers';
import { API_BASE_URL } from '../constants/api/config';
import { getAuthHeaders } from './walletApi';
import { getNetwork } from '../hooks/useNetwork';
import { getChainByChainId } from '../constants/chains';

// LiFi's non-numeric chain id for Solana (SVM family). Safe as a JS number
// (~2^50 < 2^53) but has no entry in the EVM CHAINS map — branch on it before
// any getChainByChainId lookup.
export const SOLANA_LIFI_CHAIN_ID = 1151111081099710;

// LiFi's address for native SOL (the system program id), the SVM analogue of
// 0xeeee…/0x0000… native markers on EVM chains.
export const SOLANA_NATIVE_ADDRESS = '11111111111111111111111111111111';

export function isSolanaLiFiChain(chainId: number): boolean {
  return chainId === SOLANA_LIFI_CHAIN_ID;
}

// LiFi's non-numeric chain id for Bitcoin (UTXO family). Safe as a JS number
// (2e13 < 2^53) and, like Solana's, absent from the EVM CHAINS map — branch on
// it before any getChainByChainId lookup.
export const BTC_LIFI_CHAIN_ID = 20000000000001;

// Ids LiFi reports as chainType 'UTXO'. Seeded with the known Bitcoin id and
// re-learned from every /chains response (see getLiFiChains) so the app follows
// LiFi's own answer rather than a constant that can go stale.
const utxoChainIds = new Set<number>([BTC_LIFI_CHAIN_ID]);

export function isBtcLiFiChain(chainId: number): boolean {
  return utxoChainIds.has(chainId);
}

// LiFi's chain id for Sui (MVM family).
//
// NOTE this one exceeds Number.MAX_SAFE_INTEGER (9007199254740991). It survives
// JSON and String() exactly because it is even and the float64 gap at that
// magnitude is 2 — but `Number.isSafeInteger` is false for it and arithmetic is
// lossy (id + 1 === id). Compare it and pass it through; never compute with it.
export const SUI_LIFI_CHAIN_ID = 9270000000000000;

// Ids LiFi reports as chainType 'MVM', re-learned from its own answer like the
// UTXO set below.
const mvmChainIds = new Set<number>([SUI_LIFI_CHAIN_ID]);

export function isSuiLiFiChain(chainId: number): boolean {
  return mvmChainIds.has(chainId);
}

/**
 * True for any chain the swap flow signs through the non-EVM path (Bitcoin,
 * Solana or Sui). These have no entry in the EVM CHAINS map and no ERC-20
 * balances, so every EVM-shaped lookup must be guarded by this.
 */
export function isNonEVMLiFiChain(chainId: number): boolean {
  return isSolanaLiFiChain(chainId) || isBtcLiFiChain(chainId) || isSuiLiFiChain(chainId);
}

/**
 * True when this device can SIGN a swap that starts on `chainId`: an EVM chain
 * in the wallet's own registry (the signer needs it for nonce and RPC), or one
 * of the non-EVM families with its own signing path (Solana, Bitcoin, Sui).
 *
 * LiFi routes across ~60 EVM chains; the wallet signs on a handful. The rest
 * are fine DESTINATIONS — LiFi delivers the far leg — but as a SOURCE they give
 * a quote that dies at signing ("Unsupported chain in route"), so the "from"
 * picker offers only these.
 */
export function isSignableSourceChain(chainId: number): boolean {
  return isNonEVMLiFiChain(chainId) || getChainByChainId(chainId) !== undefined;
}

/** True for the two addresses LiFi uses for an EVM chain's native coin. */
export function isNativeTokenAddress(address: string): boolean {
  const a = (address || '').toLowerCase();
  return a === '0x0000000000000000000000000000000000000000' || a === '0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee';
}

export interface LiFiChain {
  id: number;
  name: string;
  key: string;
  coin: string;
  chainType?: string; // 'EVM' | 'SVM'
  nativeToken?: { logoURI?: string };
}

export interface LiFiToken {
  address: string;
  symbol: string;
  name: string;
  decimals: number;
  logoURI: string;
  priceUSD?: string;
  chainId?: number;
  /** LiFi's id for the same asset across chains (USDC on Base and on BSC). */
  coinKey?: string;
  /** LiFi's check: "verified" or "unverified" (flagged tokens never arrive). */
  verificationStatus?: 'verified' | 'unverified' | string;
}

/**
 * Find `symbol` in `tokens`, refusing the match unless `tokens` was actually
 * fetched FOR `wantChainId`.
 *
 * `getLiFiTokens(chainId)` resolves asynchronously, so a caller holding the
 * result in state can still be showing the PREVIOUS chain's list on the very
 * render where it switches to a new chain — React doesn't apply one effect's
 * setState until the next render, so a sibling effect in the same commit
 * still sees the old list. A symbol like USDT/USDC/DAI recurs on almost
 * every EVM chain, so a naive symbol-only match against that stale list can
 * silently return a token from the WRONG chain. Per-token `chainId` can't
 * catch this either — it isn't reliably populated by a single-chain
 * `getLiFiTokens` response (see getLiFiAllTokens, which has to stamp it on
 * manually). Callers must instead track which chain id the list itself was
 * fetched for and pass it as `tokensChainId`; this refuses rather than
 * guesses when the two disagree.
 */
export function findTokenBySymbolForChain(
  tokens: LiFiToken[],
  tokensChainId: number,
  wantChainId: number,
  symbol: string,
): LiFiToken | null {
  if (tokensChainId !== wantChainId) return null;
  return tokens.find((t) => t.symbol.toUpperCase() === symbol.toUpperCase()) ?? null;
}

export interface LiFiQuoteParams {
  fromChain: number;
  toChain: number;
  fromToken: string;
  toToken: string;
  fromAmount: string;
  fromAddress: string;
  /**
   * Destination-chain recipient. REQUIRED on cross-VM routes (Solana↔EVM),
   * where the sending address can't receive on the destination chain.
   */
  toAddress?: string;
  slippage?: string;
}

export interface LiFiTransactionRequest {
  /**
   * EVM: 0x calldata. Solana: the base64-serialized transaction built by LiFi
   * (and the ONLY field present — no to/value/gas on SVM routes).
   */
  data: string;
  to?: string;
  value?: string;
  chainId?: number;
  gasLimit?: string;
  gasPrice?: string;
  maxFeePerGas?: string;
  maxPriorityFeePerGas?: string;
}

export interface LiFiQuote {
  /**
   * What the route pulls from the source wallet, in the source token's base
   * units (action.fromAmount). For an exact-output quote — TSA Pay — this is
   * the figure LiFi computed, not one the payer typed. Absent when the body
   * carries none.
   */
  fromAmount?: string;
  toAmount: string;
  toAmountUSD: string;
  estimatedDuration: number;
  gasCostUSD: string;
  /**
   * The route's gas cost in the source chain's native base units (sum of
   * estimate.gasCosts), including any L1 data fee. Absent when LiFi reports
   * none or reports it unparseably.
   */
  gasCostWei?: string;
  /**
   * Total platform + protocol fees LiFi reports for this route, in USD
   * (estimate.feeCosts).
   *
   * The UI shows THIS rather than a percentage of its own: the rate lives at
   * portal.li.fi, is configured per chain, and a number hardcoded in the app
   * becomes a lie the moment it is changed there. Undefined when the route
   * reports no fee, in which case the UI shows nothing at all.
   */
  feeCostUSD?: string;
  tool: string;
  transactionRequest: LiFiTransactionRequest;
  approvalAddress?: string;
  approvalToken?: string;
  /** Unix timestamp (seconds) after which this quote is stale */
  validUntil?: number;
}

export async function getLiFiChains(): Promise<LiFiChain[]> {
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/swap/lifi/chains`, { headers });
  if (!res.ok) throw new Error('Failed to fetch LiFi chains');
  const data = await res.json();
  const chains: LiFiChain[] = Array.isArray(data?.chains) ? data.chains : [];
  // Learn LiFi's UTXO chain ids from its own answer so isBtcLiFiChain stays
  // right even if the id changes or a second UTXO chain is added upstream.
  for (const c of chains) {
    if (typeof c.id !== 'number') continue;
    if (c.chainType === 'UTXO') utxoChainIds.add(c.id);
    if (c.chainType === 'MVM') mvmChainIds.add(c.id);
  }
  // Swaps are mainnet-only (LiFi has no testnets). In testnet mode the wallet's
  // Solana side points at devnet and its BTC address is a tb1… testnet one, so
  // hide both from the swap surface rather than let a mainnet swap draw on a
  // testnet-displayed balance.
  return getNetwork() === 'testnet' ? chains.filter(c => !isNonEVMLiFiChain(c.id)) : chains;
}

// LiFi's chain list changes rarely; one read per session serves every picker.
let chainsCache: Promise<LiFiChain[]> | null = null;
/** getLiFiChains, read once and shared. A failed read is not kept. */
export function getLiFiChainsCached(): Promise<LiFiChain[]> {
  if (!chainsCache) chainsCache = getLiFiChains().catch(err => { chainsCache = null; throw err; });
  return chainsCache;
}

export async function getLiFiTokens(chainId: number): Promise<LiFiToken[]> {
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/swap/lifi/tokens?chainId=${chainId}`, { headers });
  if (!res.ok) throw new Error('Failed to fetch token list');
  const data = await res.json();
  const chainTokens = data?.tokens?.[String(chainId)];
  return Array.isArray(chainTokens) ? chainTokens : [];
}

/** Fetch tokens for multiple chains in parallel, stamping chainId on each token. */
export async function getLiFiAllTokens(chainIds: number[]): Promise<LiFiToken[]> {
  const headers = await getAuthHeaders();
  const results = await Promise.allSettled(
    chainIds.map(chainId =>
      fetch(`${API_BASE_URL}/swap/lifi/tokens?chainId=${chainId}`, { headers })
        .then(r => r.json())
        .then(data => {
          const tokens: LiFiToken[] = data?.tokens?.[String(chainId)] ?? [];
          return tokens.map(t => ({ ...t, chainId }));
        })
        .catch(() => [] as LiFiToken[])
    )
  );
  return results
    .filter(r => r.status === 'fulfilled')
    .flatMap(r => (r as PromiseFulfilledResult<LiFiToken[]>).value);
}

export interface AllChainTokensPage {
  tokens: LiFiToken[];
  total: number;
  offset: number;
  limit: number;
}

/**
 * Paginated, optionally-filtered slice of every LiFi-indexed token across
 * every supported chain. Server caches the full LiFi blob for 24h; pages
 * are pure in-memory slices on the BE.
 */
export async function getLiFiAllChainTokens(params: {
  offset?: number;
  limit?: number;
  q?: string;
  chains?: number[];
  /** Only VERIFIED tokens with these coinKeys (the picker's major coins). */
  coinKeys?: string[];
  /** Only these exact tokens (at most 200): which of a wallet's coins LiFi can swap. */
  tokens?: { chainId: number; address: string }[];
}): Promise<AllChainTokensPage> {
  const headers = await getAuthHeaders();
  const qs = new URLSearchParams();
  if (params.offset != null) qs.append('offset', String(params.offset));
  if (params.limit != null) qs.append('limit', String(params.limit));
  if (params.q && params.q.trim()) qs.append('q', params.q.trim());
  if (params.chains && params.chains.length > 0) qs.append('chains', params.chains.join(','));
  if (params.coinKeys && params.coinKeys.length > 0) qs.append('coinKeys', params.coinKeys.join(','));
  if (params.tokens && params.tokens.length > 0) {
    qs.append('tokens', params.tokens.map(t => `${t.chainId}:${t.address}`).join(','));
  }
  const url = `${API_BASE_URL}/swap/lifi/tokens/all${qs.toString() ? `?${qs.toString()}` : ''}`;
  const res = await fetch(url, { headers });
  if (!res.ok) throw new Error('Failed to fetch all-chain token list');
  const data = await res.json();
  return {
    tokens: Array.isArray(data?.tokens) ? data.tokens : [],
    total: typeof data?.total === 'number' ? data.total : 0,
    offset: typeof data?.offset === 'number' ? data.offset : 0,
    limit: typeof data?.limit === 'number' ? data.limit : 0,
  };
}

/** Look up a single token by contract address. Returns null if not found. */
export async function getLiFiToken(chainId: number, address: string): Promise<LiFiToken | null> {
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/swap/lifi/token?chain=${chainId}&token=${encodeURIComponent(address)}`, { headers });
  if (!res.ok) return null;
  const data = await res.json();
  return data?.address ? (data as LiFiToken) : null;
}

/** Sums LiFi's estimate.gasCosts amounts (native base units) exactly. */
export function sumGasCostsWei(gasCosts: unknown): string | undefined {
  if (!Array.isArray(gasCosts) || gasCosts.length === 0) return undefined;
  try {
    return gasCosts.reduce((acc: bigint, c: any) => acc + BigInt(c?.amount ?? 0), 0n).toString();
  } catch {
    return undefined;
  }
}

/**
 * The chains a picker leg may offer: every chain as a destination, only the
 * ones this device can sign on as a source. Pure, so a picker can derive it
 * from one fetched list without ever showing the unfiltered one.
 */
export function chainsForLeg(chains: LiFiChain[], mode: 'from' | 'to'): LiFiChain[] {
  return mode === 'from' ? chains.filter(c => isSignableSourceChain(c.id)) : chains;
}

export async function getLiFiQuote(params: LiFiQuoteParams): Promise<LiFiQuote> {
  const headers = await getAuthHeaders();
  const qs = new URLSearchParams({
    fromChain: String(params.fromChain),
    toChain: String(params.toChain),
    fromToken: params.fromToken,
    toToken: params.toToken,
    fromAmount: params.fromAmount,
    fromAddress: params.fromAddress,
    ...(params.toAddress ? { toAddress: params.toAddress } : {}),
    ...(params.slippage ? { slippage: params.slippage } : {}),
  });

  const res = await fetch(`${API_BASE_URL}/swap/lifi/quote?${qs.toString()}`, { headers });
  const data = await res.json();
  if (!res.ok) throw new Error(data?.message || 'Failed to fetch quote');
  return toLiFiQuote(data);
}

/**
 * A raw LiFi quote body as the app's LiFiQuote. Shared by getLiFiQuote and by
 * anything the server hands a LiFi quote through untouched (a sell intent), so
 * the swap executor always sees one shape.
 */
export function toLiFiQuote(raw: unknown): LiFiQuote {
  const data = (raw ?? {}) as any;
  // LiFi v1: toAmount lives in estimate, not at the top level
  return {
    fromAmount: data.action?.fromAmount ?? data.estimate?.fromAmount ?? undefined,
    toAmount: data.estimate?.toAmount ?? data.toAmount ?? '0',
    toAmountUSD: data.estimate?.toAmountUSD ?? data.toAmountUSD ?? '0',
    estimatedDuration: data.estimate?.executionDuration ?? 0,
    gasCostUSD: data.estimate?.gasCosts?.[0]?.amountUSD ?? '0',
    gasCostWei: sumGasCostsWei(data.estimate?.gasCosts),
    feeCostUSD: sumFeeCostsUSD(data.estimate?.feeCosts),
    tool: data.toolDetails?.name ?? data.tool ?? 'LiFi',
    transactionRequest: data.transactionRequest,
    approvalAddress: data.estimate?.approvalAddress || undefined,
    approvalToken: data.action?.fromToken?.address || undefined,
    validUntil: data.estimate?.validUntil ?? undefined,
  };
}

export interface SettleSwapFeeParams {
  /**
   * What makes this swap unique, for idempotency. The source-chain tx hash:
   * it exists only once the swap actually happened, is stable across retries
   * of this call, and differs for every execution — so a retry never
   * double-charges and two real swaps are never collapsed into one.
   */
  quoteId: string;
  /** Route as quoted — the backend re-queries LiFi with exactly these. */
  fromChainId: number;
  toChainId: number;
  fromToken: string; // contract address (what LiFi resolves reliably)
  toToken: string;
  fromAmount: string; // base units
  toAmount?: string;
  /** Settlement token symbol for the fee ledger (the address means nothing there). */
  fromSymbol: string;
  /**
   * Route endpoints. LiFi rejects a quote without fromAddress, so the backend
   * cannot re-query — and cannot verify the fee — without them.
   */
  fromAddress: string;
  toAddress: string;
  /** Lets the backend turn fromAmount (base units) back into whole tokens. */
  fromDecimals: number;
  /** Fee bucket: the cross-chain bridge, or the same-chain trading desk. */
  feature: 'allswap' | 'spot';
}

/**
 * Report an executed swap so the backend can accrue its platform fee.
 *
 * The fee is computed server-side from a fresh LiFi quote — we deliberately
 * send no fee figure, because the client's opinion of it is exactly what the
 * endpoint is designed not to trust.
 *
 * The swap has already happened on-chain by the time this runs, so a failure
 * here must never reach the user as a failed swap; callers fire it and log.
 */
export async function settleSwapFee(params: SettleSwapFeeParams): Promise<void> {
  const headers = await getAuthHeaders();
  const res = await fetch(`${API_BASE_URL}/swap/lifi/settle`, {
    method: 'POST',
    headers,
    body: JSON.stringify({
      quoteId: params.quoteId,
      fromToken: params.fromToken,
      toToken: params.toToken,
      fromAmount: params.fromAmount,
      toAmount: params.toAmount ?? '',
      chainId: params.fromChainId,
      toChainId: params.toChainId,
      fromSymbol: params.fromSymbol,
      feature: params.feature,
      fromAddress: params.fromAddress,
      toAddress: params.toAddress,
      fromDecimals: params.fromDecimals,
    }),
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`settle failed (${res.status}): ${body.slice(0, 200)}`);
  }
}

export type SwapDeliveryStatus = 'PENDING' | 'DONE' | 'FAILED';

export interface SwapDelivery {
  status: SwapDeliveryStatus;
  /**
   * LiFi's qualifier on a DONE: 'COMPLETED' (she got what was quoted),
   * 'PARTIAL' (a different token landed) or 'REFUNDED' (sent back to her).
   * DONE alone is not a statement that the recipient got the quoted token.
   */
  substatus?: string;
  destTxHash?: string;
  receivedAmount?: string;
}

/**
 * Ask the backend what became of a broadcast swap on the DESTINATION chain.
 *
 * A cross-chain route is not finished when its source transaction confirms —
 * the bridge still has to deliver, which takes minutes and can fail. Any error
 * reads as still-pending: right after a broadcast LiFi does not know the hash
 * yet, and that is indistinguishable from a transient failure, so "not yet" is
 * the honest answer rather than "it failed".
 */
export async function getSwapDelivery(txHash: string): Promise<SwapDelivery> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}/swap/lifi/status?txHash=${encodeURIComponent(txHash)}`, { headers });
    if (!res.ok) return { status: 'PENDING' };
    const body = await res.json();
    const d = body?.data ?? {};
    const status: SwapDeliveryStatus =
      d.status === 'DONE' || d.status === 'FAILED' ? d.status : 'PENDING';
    return {
      status,
      ...(typeof d.substatus === 'string' && d.substatus ? { substatus: d.substatus } : {}),
      destTxHash: d.destTxHash,
      receivedAmount: d.receivedAmount,
    };
  } catch {
    return { status: 'PENDING' };
  }
}

/**
 * Total of a quote's feeCosts in USD. Returns undefined when the route reports
 * none, so callers can tell "no fee reported" from "zero fee".
 */
function sumFeeCostsUSD(feeCosts: { amountUSD?: string }[] | undefined): string | undefined {
  if (!Array.isArray(feeCosts) || feeCosts.length === 0) return undefined;
  let total = 0;
  let sawOne = false;
  for (const fc of feeCosts) {
    const amount = Number(fc?.amountUSD);
    if (Number.isFinite(amount)) {
      total += amount;
      sawOne = true;
    }
  }
  return sawOne ? total.toFixed(2) : undefined;
}

/** ABI-encodes approve(address,uint256) calldata for ERC-20 token approval. */
export function buildERC20ApproveCalldata(spender: string, amount: string): string {
  const iface = new ethers.Interface(['function approve(address spender, uint256 amount)']);
  return iface.encodeFunctionData('approve', [spender, BigInt(amount)]);
}
