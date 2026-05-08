// services/serviceContactApi.ts
// API calls to the Go backend for service contact fee operations
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';
import type { UnsignedTx } from './orderApi';

export type { UnsignedTx };

export interface ContactInfo {
  name?: string;
  phone?: string;
  email?: string;
  address?: string;
}

interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  data?: T;
}

/**
 * Get authorization headers from stored token.
 * Throws if no auth token is found.
 */
async function getAuthHeaders(): Promise<Record<string, string>> {
  const token = await AsyncStorage.getItem('authToken');
  if (!token) {
    throw new Error('Not authenticated. Please log in.');
  }
  return {
    'Content-Type': 'application/json',
    Authorization: `Bearer ${token}`,
  };
}

/**
 * Parse API response, handling non-OK HTTP statuses gracefully.
 */
async function handleResponse<T>(response: Response): Promise<ApiResponse<T>> {
  let data;
  try {
    data = await response.json();
  } catch {
    return { success: false, message: `HTTP ${response.status}: ${response.statusText}` };
  }
  // The backend always returns { success, message, data } — pass it through.
  // For non-OK statuses (e.g. 402), data still has useful info.
  return data;
}

/**
 * Prepare contact fee transactions (approve + payContactFee)
 */
export async function prepareContactFee(
  serviceId: string,
  token?: string
): Promise<
  ApiResponse<{
    approveTx: UnsignedTx;
    payFeeTx: UnsignedTx;
    feeAmount: string;
    token: string;
  }>
> {
  try {
    const headers = await getAuthHeaders();
    const params = token ? `?token=${token}` : '';
    const response = await fetch(
      `${API_BASE_URL}/services/${serviceId}/prepare-contact-fee${params}`,
      {
        method: 'POST',
        headers,
      }
    );
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Prepare contact fee error:', error);
    return { success: false, message: error.message || 'Failed to prepare contact fee' };
  }
}

/**
 * Submit signed contact fee transactions for verification
 */
export async function submitContactFee(
  serviceId: string,
  approveTxHash: string,
  payFeeTxHash: string,
  token?: string
): Promise<
  ApiResponse<{
    payment: any;
    contact: ContactInfo;
  }>
> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(
      `${API_BASE_URL}/services/${serviceId}/submit-contact-fee`,
      {
        method: 'POST',
        headers,
        body: JSON.stringify({ approveTxHash, payFeeTxHash, token }),
      }
    );
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Submit contact fee error:', error);
    return { success: false, message: error.message || 'Failed to submit contact fee' };
  }
}

/**
 * One row from GET /services/contact-payments. Each entry corresponds to a
 * single ContactFeePaid event recorded on-chain and persisted to the DB.
 */
export interface ContactPayment {
  id: string;
  serviceId: string;
  service?: { id: string; name: string };
  counterparty?: {
    id: string;
    name?: string;
    username?: string;
    email?: string;
    phone?: string;
  };
  token: string;
  feeAmount: string;
  feeUSD: number;
  /** Merchant's 25% on-chain split converted to USD; identical to feeUSD * 0.25. */
  providerUSD: number;
  approveTxHash: string;
  payFeeTxHash: string;
  createdAt: string;
}

export interface ContactPaymentsPage {
  items: ContactPayment[];
  pagination: {
    page: number;
    limit: number;
    total: number;
    totalPages: number;
  };
}

/**
 * List the user's contact-fee payments. Default role=provider returns
 * service requests received (for merchants); role=caller returns the
 * buyer's own unlock history.
 */
export async function listContactPayments(params: {
  role?: 'provider' | 'caller';
  page?: number;
  limit?: number;
} = {}): Promise<ApiResponse<ContactPaymentsPage>> {
  try {
    const headers = await getAuthHeaders();
    const query = new URLSearchParams();
    if (params.role) query.set('role', params.role);
    if (params.page) query.set('page', String(params.page));
    if (params.limit) query.set('limit', String(params.limit));
    const qs = query.toString();
    const response = await fetch(
      `${API_BASE_URL}/services/contact-payments${qs ? `?${qs}` : ''}`,
      { method: 'GET', headers },
    );
    return await handleResponse(response);
  } catch (error: any) {
    console.error('List contact payments error:', error);
    return {
      success: false,
      message: error.message || 'Failed to load contact payments',
    };
  }
}

/**
 * Check if contact fee has been paid and get contact details if so
 */
export async function getServiceContact(
  serviceId: string
): Promise<
  ApiResponse<{
    paid: boolean;
    contact?: ContactInfo;
    feeAmount?: string;
    token?: string;
  }>
> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(
      `${API_BASE_URL}/services/${serviceId}/contact`,
      {
        method: 'GET',
        headers,
      }
    );
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Get service contact error:', error);
    return { success: false, message: error.message || 'Failed to get service contact' };
  }
}
