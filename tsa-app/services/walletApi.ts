// services/walletApi.ts
// API calls to the Go backend for wallet operations
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';
import { CHAINS, CHAIN_KEYS, type ChainKey } from '../constants/chains';
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
  chainKey: ChainKey;
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

  const nonce = Number(tx.nonce ?? 0);
  const chainId = Number(tx.chainId ?? 0);

  return {
    to: tx.to,
    data: withHexPrefix(tx.data),
    value: String(tx.value ?? '0'),
    gasLimit: String(tx.gasLimit ?? '0'),
    gasPrice: String(tx.gasPrice ?? '0'),
    nonce: Number.isFinite(nonce) ? nonce : 0,
    chainId: Number.isFinite(chainId) ? chainId : 0,
  };
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
async function getAuthHeaders(): Promise<Record<string, string>> {
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
  source: 'alchemy' | 'moralis' | string;
}

export interface DiscoveredTokensResponse {
  walletAddress: string;
  /** false when neither ALCHEMY_API_KEY nor MORALIS_API_KEY is configured on the BE. */
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
 * Prepare a send transaction (get unsigned tx from backend)
 */
export async function prepareSendTransaction(
  tokenSymbol: string,
  toAddress: string,
  amount: string,
  chainId: number
): Promise<ApiResponse<PreparedTransaction>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/prepare-tx`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ tokenSymbol, toAddress, amount, chainId }),
    });
    const result = await safeJson<any>(response);
    const normalizedTx = normalizePreparedTransactionData(result?.data);
    if (result?.success && normalizedTx) {
      return { ...result, data: normalizedTx };
    }
    return result;
  } catch (error: any) {
    console.error('Prepare transaction error:', error);
    return { success: false, message: error.message || 'Failed to prepare transaction' };
  }
}

/**
 * Submit a signed transaction to the backend for broadcast
 */
export async function submitTransaction(
  signedTx: string,
  txType: string,
  tokenSymbol: string,
  toAddress: string,
  amount: string,
  chainId: number
): Promise<ApiResponse<{ txHash: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/submit-tx`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ signedTx, txType, tokenSymbol, toAddress, amount, chainId }),
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
    return await response.json();
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
  params: { mcgpAmount?: string; usdcAmount?: string; slippageBps?: number }
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
