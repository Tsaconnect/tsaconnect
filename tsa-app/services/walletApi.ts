// services/walletApi.ts
// API calls to the Go backend for wallet operations
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';

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
    headers['Authorization'] = `Bearer ${token}`;
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
 * Fetch wallet balances for all supported tokens
 */
export async function getWalletBalances(): Promise<ApiResponse<WalletBalance[]>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/balances`, {
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
  amount: string
): Promise<ApiResponse<PreparedTransaction>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/prepare-tx`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ tokenSymbol, toAddress, amount }),
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
  amount: string
): Promise<ApiResponse<{ txHash: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/wallet/submit-tx`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ signedTx, txType, tokenSymbol, toAddress, amount }),
    });
    return await response.json();
  } catch (error: any) {
    console.error('Submit transaction error:', error);
    return { success: false, message: error.message || 'Failed to submit transaction' };
  }
}

/**
 * Fetch transaction history with pagination
 */
export async function getTransactionHistory(
  page: number = 1,
  limit: number = 10
): Promise<ApiResponse<{ transactions: Transaction[]; total: number }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(
      `${API_BASE_URL}/wallet/transactions?page=${page}&limit=${limit}`,
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
