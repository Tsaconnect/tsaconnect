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
