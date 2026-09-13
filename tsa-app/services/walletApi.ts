// services/walletApi.ts
// API calls to the Go backend for wallet operations
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';
import { CHAINS, CHAIN_KEYS, type ChainKey, type AnyChainKey } from '../constants/chains';
import { getNetwork } from '../hooks/useNetwork';
import { getActiveWallet } from './wallet';

export interface WalletBalance {
  symbol: string;
  name: string;
  balance: string;
  usdPrice?: number;
  usdValue: string;
  contractAddress: string;
  decimals: number;
}

export interface WalletBalanceWithChain extends WalletBalance {
  chainKey: AnyChainKey;
  chainName: string;
}

export interface Transaction {
  id: string;
  txHash: string;
  type: string;
  tokenSymbol: string;
  amount: string;
  fromAddress: string;
  toAddress: string;
  status: string;
  gasUsed: string;
  chainId?: number;
  createdAt: string;
}

export interface PreparedTransaction {
  to: string;
  data: string;
  value: string;
  gasLimit: string;
  gasPrice: string;
  nonce: number;
  chainId: number;
}

interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  data?: T;
}

function isRecord(value: unknown): value is Record<string, any> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

/**
 * Safely parse a fetch response as JSON. If the server returns non-JSON
 * (e.g. an HTML error page or plain "error: ..." text), surface a clean
 * error message instead of throwing a cryptic "JSON Parse error".
 */
async function safeJson<T = any>(response: Response): Promise<ApiResponse<T>> {
  const text = await response.text();
  if (!text) {
    return { success: false, message: `Empty response (HTTP ${response.status})` };
  }
  try {
    return JSON.parse(text) as ApiResponse<T>;
  } catch {
    const preview = text.length > 200 ? text.slice(0, 200) + '...' : text;
    return {
      success: false,
      message: response.ok
        ? `Unexpected server response: ${preview}`
        : `Server error (HTTP ${response.status}): ${preview}`,
    };
  }
}

function withHexPrefix(value: unknown): string {
  if (typeof value !== 'string' || value.trim() === '') {
    return '0x';
  }
  return value.startsWith('0x') ? value : `0x${value}`;
}

function resolveChainKey(rawKey: string, fallback?: ChainKey): ChainKey | undefined {
  if ((CHAIN_KEYS as string[]).includes(rawKey)) {
    return rawKey as ChainKey;
  }

  const normalized = rawKey.trim().toLowerCase();
  const match = CHAIN_KEYS.find((chainKey) => {
    const chain = CHAINS[chainKey];
    return (
      chainKey === normalized ||
      chain.name.toLowerCase() === normalized ||
      chain.shortName.toLowerCase() === normalized
    );
  });

  return match || fallback;
}

function inferDecimals(symbol: string, value: Record<string, any>): number {
  if (typeof value.decimals === 'number') {
    return value.decimals;
  }
  if (typeof value.decimals === 'string' && value.decimals.trim() !== '') {
    const parsed = Number(value.decimals);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return symbol === 'USDT' || symbol === 'USDC' ? 6 : 18;
}

function normalizeWalletBalanceEntry(
  symbol: string,
  rawValue: unknown,
  chainKey: ChainKey
): WalletBalanceWithChain | null {
  if (rawValue === null || rawValue === undefined) {
    return null;
  }

  const chain = CHAINS[chainKey];

  if (!isRecord(rawValue)) {
    return {
      symbol,
      name: symbol,
      balance: String(rawValue),
      usdPrice: 0,
      usdValue: '0',
      contractAddress: '',
      decimals: inferDecimals(symbol, {}),
      chainKey,
      chainName: chain.shortName,
    };
  }

  return {
    symbol,
    name: typeof rawValue.name === 'string' && rawValue.name.trim() ? rawValue.name : symbol,
    balance: String(rawValue.balance ?? '0'),
    usdPrice: Number.isFinite(Number(rawValue.usdPrice)) ? Number(rawValue.usdPrice) : 0,
    usdValue: String(rawValue.usdValue ?? '0'),
    contractAddress: typeof rawValue.contractAddress === 'string' ? rawValue.contractAddress : '',
    decimals: inferDecimals(symbol, rawValue),
    chainKey,
    chainName: chain.shortName,
  };
}

function normalizePreparedTransactionData(payload: unknown): PreparedTransaction | null {
  const data = isRecord(payload) ? payload : null;
  const tx = isRecord(data?.transaction) ? data.transaction : data;
  if (!tx || typeof tx.to !== 'string') {
    return null;
  }

  // Nonce and chain id FAIL CLOSED rather than defaulting to 0.
  //
  // A missing nonce read as 0 is not a harmless default: sendPreparedTransaction
  // compares the re-prepared nonce against the first one to decide whether a
  // transaction of ours is already in flight, and 0 === 0 waves that check
  // through exactly when it must not — sending a second transfer. A missing
  // chain id read as 0 signs for no network at all. Neither field is ever
  // absent from a real prepare-tx response (both are non-optional on the Go
  // UnsignedTx), so refusing the payload costs nothing and removes a silent
  // failure mode from the one guard standing between a retry and a double-send.
  const nonce = toFiniteNumber(tx.nonce);
  const chainId = toFiniteNumber(tx.chainId);
  if (nonce === null || chainId === null) {
    return null;
  }

  return {
    to: tx.to,
    data: withHexPrefix(tx.data),
    value: String(tx.value ?? '0'),
    gasLimit: String(tx.gasLimit ?? '0'),
    gasPrice: String(tx.gasPrice ?? '0'),
    nonce,
    chainId,
  };
}

/** A number that was actually present on the wire, or null. */
function toFiniteNumber(value: unknown): number | null {
  if (value === null || value === undefined || value === '') {
    return null;
  }
  const n = Number(value);
  return Number.isFinite(n) ? n : null;
}

function extractTransactionHash(payload: unknown): string | undefined {
  const data = isRecord(payload) ? payload : null;
  if (!data) {
    return undefined;
  }
  if (typeof data.txHash === 'string' && data.txHash.trim()) {
    return data.txHash;
  }
  if (isRecord(data.transaction) && typeof data.transaction.txHash === 'string' && data.transaction.txHash.trim()) {
    return data.transaction.txHash;
  }
  return undefined;
}

function normalizePreparedSwapData(payload: unknown): PreparedSwap | null {
  const data = isRecord(payload) ? payload : null;
  if (!data) {
    return null;
  }

  const swapTx = normalizePreparedTransactionData(data.swapTx);
  if (!swapTx) {
    return null;
  }

  const approveTx = data.approveTx == null
    ? null
    : normalizePreparedTransactionData(data.approveTx);
  if (data.approveTx != null && !approveTx) {
    return null;
  }

  return {
    direction: typeof data.direction === 'string' ? data.direction : '',
    mcgpAmount: String(data.mcgpAmount ?? '0'),
    usdcAmount: String(data.usdcAmount ?? '0'),
    approveTx,
    swapTx,
  };
}

export function normalizeWalletBalances(
  payload: unknown,
  fallbackChainKey?: ChainKey
): WalletBalanceWithChain[] {
  const normalized: WalletBalanceWithChain[] = [];
  const pushEntry = (symbol: string, rawValue: unknown, chainKey: ChainKey | undefined) => {
    if (!chainKey) return;
    const entry = normalizeWalletBalanceEntry(symbol, rawValue, chainKey);
    if (entry) normalized.push(entry);
  };

  if (Array.isArray(payload)) {
    for (const rawEntry of payload) {
      if (!isRecord(rawEntry) || typeof rawEntry.symbol !== 'string') continue;
      const chainKey = resolveChainKey(
        typeof rawEntry.chainKey === 'string' ? rawEntry.chainKey : '',
        fallbackChainKey
      );
      pushEntry(rawEntry.symbol, rawEntry, chainKey);
    }
    return normalized;
  }

  if (!isRecord(payload)) {
    return normalized;
  }

  const wrappedBalances = isRecord(payload.balances) ? payload.balances : null;
  if (wrappedBalances) {
    for (const [rawChainKey, rawBalances] of Object.entries(wrappedBalances)) {
      if (!isRecord(rawBalances)) continue;
      const chainKey = resolveChainKey(rawChainKey, fallbackChainKey);
      for (const [symbol, rawValue] of Object.entries(rawBalances)) {
        pushEntry(symbol, rawValue, chainKey);
      }
    }
    return normalized;
  }

  const looksLikeChainMap = Object.entries(payload).some(([rawChainKey, rawBalances]) => {
    return !!resolveChainKey(rawChainKey) && isRecord(rawBalances);
  });

  if (looksLikeChainMap) {
    for (const [rawChainKey, rawBalances] of Object.entries(payload)) {
      if (!isRecord(rawBalances)) continue;
      const chainKey = resolveChainKey(rawChainKey, fallbackChainKey);
      for (const [symbol, rawValue] of Object.entries(rawBalances)) {
        pushEntry(symbol, rawValue, chainKey);
      }
    }
    return normalized;
  }

  for (const [symbol, rawValue] of Object.entries(payload)) {
    pushEntry(symbol, rawValue, fallbackChainKey);
  }

  return normalized;
}

/**
 * Get authorization headers from stored token
 */
export async function getAuthHeaders(): Promise<Record<string, string>> {
  const token = await AsyncStorage.getItem('authToken');
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
  };
  if (token) {
    // Token may already include "Bearer " prefix from AuthContext
    headers['Authorization'] = token.startsWith('Bearer ') ? token : `Bearer ${token}`;
  }
  return headers;
}

/**
 * Register wallet address with backend after wallet creation/import
 */
export async function registerWalletAddress(address: string): Promise<ApiResponse> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/register`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ walletAddress: address }),
    });
    return await response.json();
  } catch (error: any) {
    console.error('Register wallet address error:', error);
    return { success: false, message: error.message || 'Failed to register wallet' };
  }
}

/**
 * Fetch wallet balances for supported tokens.
 * Pass chainId to filter by chain, or omit to get all balances.
 * Pass address to fetch balances for a specific wallet (multi-wallet support);
 * otherwise falls back to the user's registered address.
 */
export async function getWalletBalances(
  chainId?: number,
  address?: string
): Promise<ApiResponse<WalletBalance[]>> {
  try {
    const headers = await getAuthHeaders();
    const resolvedAddress = address || (await getActiveWallet()) || undefined;
    const qp = new URLSearchParams({ network: getNetwork() });
    if (chainId) qp.append('chainId', String(chainId));
    if (resolvedAddress) qp.append('address', resolvedAddress);
    const params = `?${qp.toString()}`;
    const response = await fetch(`${API_BASE_URL}/wallet/balances${params}`, {
      method: 'GET',
      headers,
    });
    return await safeJson(response);
  } catch (error: any) {
    console.error('Get wallet balances error:', error);
    return { success: false, message: error.message || 'Failed to fetch balances' };
  }
}

/**
 * One row from GET /wallet/discovered-tokens — an ERC-20 the indexer
 * found in the wallet, regardless of whether we have it in supported_tokens.
 */
export interface DiscoveredToken {
  chain: string;
  chainId?: number;
  contractAddress: string;
  symbol: string;
  name: string;
  decimals: number;
  /** Raw on-chain balance (smallest unit, integer string). */
  balance: string;
  /** USD value if the price service knows the symbol; 0 otherwise. */
  usdValue: number;
  source: 'ankr' | 'moralis' | string;
}

export interface DiscoveredTokensResponse {
  walletAddress: string;
  /** false when neither ANKR_API_KEY nor MORALIS_API_KEY is configured on the BE. */
  providerAvailable: boolean;
  tokens: DiscoveredToken[];
}

/**
 * Auto-discover every ERC-20 the wallet holds across all supported
 * chains. Pass `chains` to constrain the fan-out (e.g. ["ethereum",
 * "polygon"]); omit to query everything the BE has indexer coverage for.
 *
 * Slow on cold caches — the BE fans out to indexers in parallel with a
 * 25s budget. Render a skeleton/spinner until this resolves.
 */
export async function getDiscoveredTokens(
  chains?: ChainKey[],
  address?: string,
): Promise<ApiResponse<DiscoveredTokensResponse>> {
  try {
    const headers = await getAuthHeaders();
    const resolvedAddress = address || (await getActiveWallet()) || undefined;
    const qp = new URLSearchParams();
    if (resolvedAddress) qp.append('address', resolvedAddress);
    if (chains && chains.length > 0) qp.append('chains', chains.join(','));
    const qs = qp.toString();
    const response = await fetch(
      `${API_BASE_URL}/wallet/discovered-tokens${qs ? `?${qs}` : ''}`,
      { method: 'GET', headers },
    );
    return await safeJson(response);
  } catch (error: any) {
    console.error('Get discovered tokens error:', error);
    return {
      success: false,
      message: error.message || 'Failed to discover tokens',
    };
  }
}

/**
 * Prepare a send transaction (get unsigned tx from backend).
 *
 * `tokenAddress` and `decimals` are overrides used when sending an
 * auto-discovered ERC-20 — i.e. a token the user holds but isn't in the
 * curated supported_tokens list. When omitted, the BE resolves both from
 * its supported_tokens config.
 */
export async function prepareSendTransaction(
  tokenSymbol: string,
  toAddress: string,
  amount: string,
  chainId: number,
  options?: { tokenAddress?: string; decimals?: number; fromAddress?: string }
): Promise<ApiResponse<PreparedTransaction>> {
  try {
    const headers = await getAuthHeaders();
    const body: Record<string, unknown> = { tokenSymbol, toAddress, amount, chainId };
    if (options?.tokenAddress) body.tokenAddress = options.tokenAddress;
    if (typeof options?.decimals === 'number') body.decimals = options.decimals;
    // Spend from the ACTIVE device wallet (same default as getWalletBalances
    // and signTransaction); the registered wallet remains the RECEIVING
    // identity. The BE computes the nonce for this address and its submit
    // guard requires the signature to recover to it.
    const fromAddress = options?.fromAddress || (await getActiveWallet());
    if (fromAddress) body.fromAddress = fromAddress;
    const response = await fetch(`${API_BASE_URL}/wallet/prepare-tx`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
    });
    const result = await safeJson<any>(response);
    const normalizedTx = normalizePreparedTransactionData(result?.data);
    if (result?.success) {
      // A success we cannot normalise is NOT a success to pass on. Returning
      // the raw payload here would hand the caller an object with no nonce,
      // which the send flow reads as 0 — and 0 defeats the guard that keeps a
      // retry from broadcasting a second transfer.
      if (!normalizedTx) {
        return { success: false, message: 'The server returned an incomplete transaction. Please try again.' };
      }
      return { ...result, data: normalizedTx };
    }
    return result;
  } catch (error: any) {
    console.error('Prepare transaction error:', error);
    return { success: false, message: error.message || 'Failed to prepare transaction' };
  }
}

/**
 * Submit a signed transaction to the backend for broadcast.
 *
 * `fromAddress` is REQUIRED and must be the wallet the tx was PREPARED for —
 * the BE verifies the signature recovers to it. Callers must capture the
 * active wallet ONCE per flow and thread the same address through prepare →
 * sign → submit; re-resolving it at each step lets a mid-flow wallet switch
 * pair another account's nonce with this signature (queued-forever tx).
 */
/**
 * Which fee bucket a deferred swap will be booked to, or 'none' for a swap that
 * owes no platform fee at all (the brokered stablecoin ramp). The backend
 * records the debt under this label at submit time, so a swap whose settle call
 * never arrives is still collectable — and a 'none' swap is never charged.
 */
export type SwapFeeFeature = 'allswap' | 'spot' | 'none';

export async function submitTransaction(
  signedTx: string,
  txType: string,
  tokenSymbol: string,
  toAddress: string,
  amount: string,
  chainId: number,
  fromAddress: string,
  opts: { feeDeferred?: boolean; feeFeature?: SwapFeeFeature } = {}
): Promise<ApiResponse<{ txHash: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/submit-tx`, {
      method: 'POST',
      headers,
      body: JSON.stringify({
        signedTx, txType, tokenSymbol, toAddress, amount, chainId,
        ...(fromAddress ? { fromAddress } : {}),
        // `amount` here is a quantity of tokenSymbol. The backend's fee
        // fallback reads it as USD, which only holds for a stablecoin — so an
        // aggregator swap defers its fee to the server-verified settle.
        ...(opts.feeDeferred ? { feeDeferred: true, feeFeature: opts.feeFeature ?? 'allswap' } : {}),
      }),
    });
    const result = await safeJson<any>(response);
    const txHash = extractTransactionHash(result?.data);
    if (result?.success && txHash) {
      return { ...result, data: { txHash } };
    }
    return result;
  } catch (error: any) {
    console.error('Submit transaction error:', error);
    return { success: false, message: error.message || 'Failed to submit transaction' };
  }
}

/** Injectable steps of the prepare → sign → submit send flow. */
export interface SendTxOps {
  /** Fetch a freshly-prepared tx (with a server-computed nonce) from the backend. */
  prepare: () => Promise<ApiResponse<PreparedTransaction>>;
  /** Sign the prepared tx locally, returning the serialized signed tx. */
  sign: (tx: PreparedTransaction) => Promise<string>;
  /** Broadcast the signed tx via the backend. */
  submit: (signedTx: string) => Promise<ApiResponse<{ txHash: string }>>;
  /** Pause before re-preparing after a stale-tx rejection (default 1500ms) — lets a lagging RPC node catch up. */
  retryDelayMs?: number;
}

/**
 * How a prepared transaction went stale between prepare and broadcast. The two
 * kinds are cured by the same action — re-prepare, re-sign, resubmit — but they
 * differ in what a SAFE recovery looks like, which is the whole point of
 * distinguishing them:
 *
 * - 'nonce': something already occupies our nonce. Re-preparing must return a
 *   higher one — enforced below — or the retry is pointless.
 * - 'gas-price': the node refused the tx during pool validation, so it is not
 *   in the mempool and cannot mine. Re-preparing must return the SAME nonce; if
 *   it moved, a transaction of ours reached the pool after all and broadcasting
 *   at the new nonce would send a SECOND one.
 *
 * Deliberately excluded from staleKind: 'underpriced'. "replacement transaction underpriced"
 * means a tx of ours is already pending at this nonce and WILL mine — retrying
 * re-prepares at nonce+1 and broadcasts a second, different transaction. On a
 * transfer that is the user's money sent twice. The plain "transaction
 * underpriced" variant would be safe to retry, but the two are not reliably
 * distinguishable by substring, so neither is.
 */
type StaleKind = 'nonce' | 'gas-price';

const staleKind = (message: string | undefined): StaleKind | null => {
  const low = (message ?? '').toLowerCase();
  if (low.includes('nonce too low')) return 'nonce';
  // Both spellings are in the wild: older geth says "fee cap less than block
  // base fee", current geth and the L2 sequencers say "max fee per gas less
  // than block base fee".
  if (low.includes('less than block base fee')) return 'gas-price';
  return null;
};

/**
 * Run the prepare -> sign -> submit send flow with at most one automatic retry
 * when the broadcast is rejected for having gone stale (see StaleKind).
 *
 * Nonce and gas price are both resolved server-side at prepare time and baked
 * into the tx the wallet signs, but the user signs and broadcasts seconds
 * later. By then the nonce can be below the confirmed count (Sonic's
 * load-balanced public RPC lags between nodes) or the gas price below the
 * current base fee (every chain reprices per block). A signed tx cannot be
 * patched, so recovery means re-preparing and re-signing.
 *
 * The retry is guarded, because re-preparing is not free of consequence: it
 * asks the backend for a fresh nonce, and a nonce that has MOVED means a
 * transaction of ours is already in flight. Broadcasting then sends a second
 * one. So after a gas-price rejection the retry is abandoned unless the nonce
 * comes back unchanged.
 *
 * Note what the retry spends: signTransaction reads the key from storage and
 * never re-prompts, and AuthorizationModal gates entry to the flow rather than
 * each signature — so one authorisation covers both broadcasts. That is what
 * makes the nonce check above load-bearing rather than belt-and-braces.
 *
 * Residual hazard, unfixed and pre-existing: a 'nonce too low' rejection is
 * indistinguishable from the case where the tx occupying that nonce is a
 * duplicate of our own (a submit whose response was lost). Retrying then sends
 * a second transfer. Closing that needs an idempotency key on the backend's
 * submit endpoint, which does not exist yet — and would also retire the
 * single-authorisation concern above, since a replayed key cannot double-spend.
 */
export async function sendPreparedTransaction(
  ops: SendTxOps
): Promise<ApiResponse<{ txHash: string }>> {
  const retryDelayMs = ops.retryDelayMs ?? 1500;
  let lastResult: ApiResponse<{ txHash: string }> = { success: false, message: 'Failed' };
  let rejection: StaleKind | null = null;
  let first: PreparedTransaction | null = null;

  for (let attempt = 0; attempt < 2; attempt++) {
    if (attempt > 0 && retryDelayMs > 0) {
      await new Promise((resolve) => setTimeout(resolve, retryDelayMs));
    }

    const prepared = await ops.prepare();
    if (!prepared.success || !prepared.data) {
      // On a retry the broadcast error says more about what went wrong than
      // "couldn't prepare" does, so it stays the reported one.
      if (attempt > 0) return lastResult;
      return { success: false, message: prepared.message || 'Failed to prepare transaction' };
    }

    if (first !== null) {
      if (rejection === 'gas-price' && prepared.data.nonce !== first.nonce) {
        // The tx we thought was refused is in the mempool after all. Signing
        // this one would transfer the same amount a second time.
        return lastResult;
      }
      if (rejection === 'nonce' && prepared.data.nonce <= first.nonce) {
        // The node that served this prepare is as far behind as the one that
        // rejected the broadcast. Re-signing the same nonce resubmits an
        // identical transaction to fail identically; stop instead of spending
        // a second round trip on it.
        return lastResult;
      }
      // A retry exists to re-price the SAME transfer. Re-pricing moves nonce,
      // gasPrice and gasLimit; everything that decides WHERE the money goes
      // must come back identical, because the wallet authorised this send once
      // (see the note on re-signing above) and the second prepare is a fresh
      // server response that nothing else inspects.
      //
      // chainId counts as destination, not price: the same `to` address on
      // another chain is very often a different contract entirely, so a moved
      // chainId sends the funds somewhere the user never approved.
      if (
        prepared.data.to !== first.to ||
        prepared.data.value !== first.value ||
        prepared.data.data !== first.data ||
        prepared.data.chainId !== first.chainId
      ) {
        return lastResult;
      }
    }
    if (first === null) first = prepared.data;

    const signed = await ops.sign(prepared.data);
    lastResult = await ops.submit(signed);

    if (lastResult.success) return lastResult;
    rejection = staleKind(lastResult.message);
    if (rejection === null) return lastResult;
  }

  return lastResult;
}

/**
 * Fetch transaction history with pagination.
 * Pass chainId to filter by chain.
 */
export async function getTransactionHistory(
  page: number = 1,
  limit: number = 10,
  chainId?: number
): Promise<ApiResponse<{ transactions: Transaction[]; total: number }>> {
  try {
    const headers = await getAuthHeaders();
    const params = new URLSearchParams({ page: String(page), limit: String(limit) });
    if (chainId) params.append('chainId', String(chainId));
    const response = await fetch(
      `${API_BASE_URL}/wallet/transactions?${params.toString()}`,
      {
        method: 'GET',
        headers,
      }
    );
    return await response.json();
  } catch (error: any) {
    console.error('Get transaction history error:', error);
    return { success: false, message: error.message || 'Failed to fetch transactions' };
  }
}

/**
 * Fetch supported tokens from the backend.
 * Returns token configs with their chain availability.
 */
export async function fetchSupportedTokens(): Promise<ApiResponse<SupportedTokenResponse[]>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/supported-tokens`, {
      method: 'GET',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Fetch supported tokens error:', error);
    return { success: false, message: error.message || 'Failed to fetch supported tokens' };
  }
}

export interface SupportedTokenResponse {
  symbol: string;
  name: string;
  decimals: number;
  iconColor: string;
  chains: string[];
}

/**
 * Fetch price history for a token symbol over the given number of days.
 */
export async function getTokenPriceHistory(
  symbol: string,
  days: number = 7
): Promise<ApiResponse<{ timestamp: number; price: number }[]>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(
      `${API_BASE_URL}/market/history/${symbol}?days=${days}`,
      { method: 'GET', headers }
    );
    const json = await response.json();
    // The endpoint wraps the series: data = { symbol, days, interval, history }.
    // Unwrap to the bare point array this function's signature promises, while
    // still accepting a plain array in case the shape is ever flattened.
    const points = Array.isArray(json?.data) ? json.data : json?.data?.history;
    return { ...json, data: Array.isArray(points) ? points : [] };
  } catch (error: any) {
    console.error('Get token price history error:', error);
    return { success: false, message: error.message || 'Failed to fetch price history' };
  }
}

/**
 * Confirm that the user has backed up their seed phrase
 */
export async function confirmSeedPhraseBackup(): Promise<ApiResponse> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/seed-phrase-backed-up`, {
      method: 'POST',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Confirm seed phrase backup error:', error);
    return { success: false, message: error.message || 'Failed to confirm backup' };
  }
}

export interface ResolvedUser {
  username: string;
  name: string;
  walletAddress: string;
  verificationStatus: string;
}

/**
 * Resolve a username to a wallet address for Instant Pay
 */
export async function resolveUsername(username: string): Promise<ApiResponse<ResolvedUser>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/resolve/${encodeURIComponent(username)}`, {
      method: 'GET',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Resolve username error:', error);
    return { success: false, message: error.message || 'Failed to resolve username' };
  }
}

export interface SwapPrice {
  direction: string;
  mcgpAmount: string;
  usdcAmount: string;
  pricePerMCGP: string;
}

export interface PreparedSwap {
  direction: string;
  mcgpAmount: string;
  usdcAmount: string;
  approveTx?: PreparedTransaction | null;
  swapTx: PreparedTransaction;
}

/**
 * Get current swap price from OTC contract.
 * For buy: pass usdcAmount to get mcgpAmount, or mcgpAmount to get usdcAmount.
 * For sell: pass mcgpAmount to get usdcAmount.
 */
export async function getSwapPrice(
  direction: 'buy' | 'sell',
  params: { mcgpAmount?: string; usdcAmount?: string }
): Promise<ApiResponse<SwapPrice>> {
  try {
    const qp = new URLSearchParams({ direction });
    if (params.mcgpAmount) qp.append('mcgpAmount', params.mcgpAmount);
    if (params.usdcAmount) qp.append('usdcAmount', params.usdcAmount);
    const response = await fetch(
      `${API_BASE_URL}/swap/price?${qp.toString()}`,
      { method: 'GET', headers: { 'Content-Type': 'application/json' } }
    );
    return await safeJson(response);
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to get swap price' };
  }
}

/**
 * Prepare a swap transaction (approve + swap tx).
 * For buy: pass usdcAmount (how much USDC to spend) or mcgpAmount.
 * For sell: pass mcgpAmount.
 */
export async function prepareSwap(
  direction: 'buy' | 'sell',
  params: { mcgpAmount?: string; usdcAmount?: string; slippageBps?: number },
  /** Wallet to swap from — capture once per flow and reuse for sign/submit. */
  fromAddress?: string
): Promise<ApiResponse<PreparedSwap>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/swap/prepare`, {
      method: 'POST',
      headers,
      body: JSON.stringify({
        direction,
        mcgpAmount: params.mcgpAmount,
        usdcAmount: params.usdcAmount,
        slippageBps: params.slippageBps || 50,
        ...(fromAddress ? { fromAddress } : {}),
      }),
    });
    const result = await safeJson<PreparedSwap>(response);
    const normalizedSwap = normalizePreparedSwapData(result?.data);
    if (result?.success && normalizedSwap) {
      return { ...result, data: normalizedSwap };
    }
    return result;
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to prepare swap' };
  }
}

// ── OTC listing controls (admin-set) ──
// Per-direction kill-switches + listing metadata for the OTC tab.
export interface OtcSettings {
  buyEnabled: boolean;
  sellEnabled: boolean;
  logoUrl: string;
  assetClass: string;
  category: 'direct' | 'presale' | string;
}

// Fail-OPEN defaults: if the settings can't be fetched, nothing is paused.
const OTC_SETTINGS_OPEN: OtcSettings = {
  buyEnabled: true,
  sellEnabled: true,
  logoUrl: '',
  assetClass: '',
  category: 'direct',
};

const OTC_SETTINGS_TTL_MS = 60_000;
let otcSettingsCache: { data: OtcSettings; at: number } | null = null;

/**
 * Get the OTC listing controls (buy/sell pause + metadata). Cached in memory
 * briefly so re-focusing the tab doesn't refetch; any failure returns the
 * fail-open defaults (everything enabled) and is NOT cached, so a healthy
 * fetch can recover on the next call.
 */
export async function getOtcSettings(): Promise<OtcSettings> {
  if (otcSettingsCache && Date.now() - otcSettingsCache.at < OTC_SETTINGS_TTL_MS) {
    return otcSettingsCache.data;
  }
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/otc/settings`, { method: 'GET', headers });
    const r = await safeJson<Partial<OtcSettings>>(response);
    if (r.success && r.data) {
      const settings: OtcSettings = {
        buyEnabled: r.data.buyEnabled !== false,
        sellEnabled: r.data.sellEnabled !== false,
        logoUrl: r.data.logoUrl || '',
        assetClass: r.data.assetClass || '',
        category: r.data.category || 'direct',
      };
      otcSettingsCache = { data: settings, at: Date.now() };
      return settings;
    }
  } catch {
    // fall through to fail-open defaults
  }
  return OTC_SETTINGS_OPEN;
}
