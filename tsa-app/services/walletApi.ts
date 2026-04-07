// services/walletApi.ts
// API calls to the Go backend for wallet operations
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';
import { getNetwork } from '../hooks/useNetwork';

export interface WalletBalance {
  symbol: string;
  name: string;
  balance: string;
  usdValue: string;
  contractAddress: string;
  decimals: number;
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
 */
export async function getWalletBalances(chainId?: number): Promise<ApiResponse<WalletBalance[]>> {
  try {
    const headers = await getAuthHeaders();
    const qp = new URLSearchParams({ network: getNetwork() });
    if (chainId) qp.append('chainId', String(chainId));
    const params = `?${qp.toString()}`;
    const response = await fetch(`${API_BASE_URL}/wallet/balances${params}`, {
      method: 'GET',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Get wallet balances error:', error);
    return { success: false, message: error.message || 'Failed to fetch balances' };
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
    return await response.json();
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
    return await response.json();
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
