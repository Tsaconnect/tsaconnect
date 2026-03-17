// services/orderApi.ts
// API calls to the Go backend for escrow order operations
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';

// --- Types ---

export interface UnsignedTx {
  to: string;
  value: string;
  data: string;
  nonce: number;
  gasPrice: string;
  gasLimit: number;
  chainId: string;
}

export interface Order {
  id: string;
  buyerId: string;
  sellerId: string;
  productId: string;
  quantity: number;
  token: string;
  productAmount: string;
  shippingAmount: string;
  platformFee: string;
  totalAmount: string;
  shippingZone: string;
  contractOrderId?: string;
  escrowTxHash?: string;
  approveTxHash?: string;
  releaseTxHash?: string;
  buyerUpline?: string;
  deliveryProofUrl?: string;
  status: string;
  buyerConfirmedAt?: string;
  sellerDeliveredAt?: string;
  escrowExpiresAt?: string;
  notes?: string;
  createdAt: string;
  updatedAt: string;
}

export interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  data?: T;
}

// --- Auth helper ---

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

// --- Wei formatting ---

export function formatTokenAmount(weiStr: string, token: string): string {
  const decimals = token === 'MCGP' ? 18 : 6;
  try {
    const value = BigInt(weiStr);
    const divisor = BigInt(10 ** decimals);
    const whole = value / divisor;
    const frac = value % divisor;
    const fracStr = frac.toString().padStart(decimals, '0').slice(0, token === 'MCGP' ? 4 : 2);
    return `${whole}.${fracStr} ${token}`;
  } catch {
    return `${weiStr} ${token}`;
  }
}

// --- API functions ---

export async function createOrders(
  token: string,
  buyerCity?: string,
  buyerState?: string,
  buyerCountry?: string
): Promise<ApiResponse<{ orders: Order[] }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ token, buyerCity, buyerState, buyerCountry }),
    });
    return await response.json();
  } catch (error: any) {
    console.error('Create orders error:', error);
    return { success: false, message: error.message || 'Failed to create orders' };
  }
}

export async function getOrders(params: {
  page?: number;
  limit?: number;
  status?: string;
  role?: string;
}): Promise<ApiResponse<{ orders: Order[]; pagination: any }>> {
  try {
    const headers = await getAuthHeaders();
    const queryParams = new URLSearchParams();
    if (params.page) queryParams.append('page', String(params.page));
    if (params.limit) queryParams.append('limit', String(params.limit));
    if (params.status) queryParams.append('status', params.status);
    if (params.role) queryParams.append('role', params.role);
    const response = await fetch(`${API_BASE_URL}/orders?${queryParams.toString()}`, {
      method: 'GET',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Get orders error:', error);
    return { success: false, message: error.message || 'Failed to fetch orders' };
  }
}

export async function getOrderById(id: string): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${id}`, {
      method: 'GET',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Get order error:', error);
    return { success: false, message: error.message || 'Failed to fetch order' };
  }
}

export async function prepareEscrow(
  orderId: string
): Promise<ApiResponse<{ approveTx: UnsignedTx; createOrderTx: UnsignedTx; contractOrderId: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/prepare-escrow`, {
      method: 'POST',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Prepare escrow error:', error);
    return { success: false, message: error.message || 'Failed to prepare escrow' };
  }
}

export async function submitEscrow(
  orderId: string,
  approveTxHash: string,
  escrowTxHash: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/submit-escrow`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ approveTxHash, escrowTxHash }),
    });
    return await response.json();
  } catch (error: any) {
    console.error('Submit escrow error:', error);
    return { success: false, message: error.message || 'Failed to submit escrow' };
  }
}

export async function prepareConfirm(
  orderId: string
): Promise<ApiResponse<{ confirmTx: UnsignedTx }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/prepare-confirm`, {
      method: 'POST',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Prepare confirm error:', error);
    return { success: false, message: error.message || 'Failed to prepare confirm' };
  }
}

export async function submitConfirm(
  orderId: string,
  txHash: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/submit-confirm`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ txHash }),
    });
    return await response.json();
  } catch (error: any) {
    console.error('Submit confirm error:', error);
    return { success: false, message: error.message || 'Failed to submit confirm' };
  }
}

export async function requestRefund(orderId: string): Promise<ApiResponse<any>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/request-refund`, {
      method: 'POST',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Request refund error:', error);
    return { success: false, message: error.message || 'Failed to request refund' };
  }
}

export async function getShippingEstimate(
  productId: string,
  buyerCity: string,
  buyerState: string,
  buyerCountry: string
): Promise<ApiResponse<{ zone: string; shippingCost: number }>> {
  try {
    const headers = await getAuthHeaders();
    const params = new URLSearchParams({
      productId,
      buyerCity,
      buyerState,
      buyerCountry,
    });
    const response = await fetch(`${API_BASE_URL}/orders/shipping-estimate?${params.toString()}`, {
      method: 'GET',
      headers,
    });
    return await response.json();
  } catch (error: any) {
    console.error('Shipping estimate error:', error);
    return { success: false, message: error.message || 'Failed to get shipping estimate' };
  }
}
