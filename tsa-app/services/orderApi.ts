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
  trackingNumber?: string;
  merchantApprovedRefund?: boolean;
  shippingCity?: string;
  shippingState?: string;
  shippingCountry?: string;
  status: string;
  buyerConfirmedAt?: string;
  sellerShippedAt?: string;
  sellerDeliveredAt?: string;
  escrowExpiresAt?: string;
  notes?: string;
  createdAt: string;
  updatedAt: string;
  // Enriched (returned by backend with related entities)
  buyer?: { id: string; name?: string; username?: string; email?: string };
  seller?: { id: string; name?: string; username?: string; email?: string };
  product?: { id: string; name: string; price: number; imageUrl?: string };
}

export interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  data?: T;
}

// --- Auth helper ---

async function getAuthHeaders(): Promise<Record<string, string>> {
  const token = await AsyncStorage.getItem('authToken');
  if (!token) {
    throw new Error('Not authenticated. Please log in again.');
  }
  return {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token}`,
  };
}

// --- Response helper ---

async function handleResponse<T>(response: Response): Promise<ApiResponse<T>> {
  let data;
  try {
    data = await response.json();
  } catch {
    return { success: false, message: `HTTP ${response.status}: ${response.statusText}` };
  }
  if (!response.ok) {
    return { success: false, message: data.message || `Request failed with status ${response.status}` };
  }
  return { success: true, ...data };
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
    return await handleResponse(response);
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
    return await handleResponse(response);
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
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Get order error:', error);
    return { success: false, message: error.message || 'Failed to fetch order' };
  }
}

export async function prepareApprove(
  orderId: string
): Promise<ApiResponse<{ approveTx: UnsignedTx }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/prepare-approve`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Prepare approve error:', error);
    return { success: false, message: error.message || 'Failed to prepare approval' };
  }
}

export async function prepareEscrow(
  orderId: string
): Promise<ApiResponse<{ createOrderTx: UnsignedTx; contractOrderId: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/prepare-escrow`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
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
    return await handleResponse(response);
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
    return await handleResponse(response);
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
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Submit confirm error:', error);
    return { success: false, message: error.message || 'Failed to submit confirm' };
  }
}

/**
 * Seller marks an escrowed order as shipped. Optional tracking number + notes.
 */
export async function markOrderShipped(
  orderId: string,
  params?: { trackingNumber?: string; notes?: string }
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/ship`, {
      method: 'POST',
      headers,
      body: JSON.stringify(params || {}),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Mark shipped error:', error);
    return { success: false, message: error.message || 'Failed to mark as shipped' };
  }
}

/**
 * Seller marks an order as delivered. Requires a delivery proof URL.
 */
export async function markOrderDelivered(
  orderId: string,
  deliveryProofUrl: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/deliver`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ deliveryProofUrl }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Mark delivered error:', error);
    return { success: false, message: error.message || 'Failed to mark as delivered' };
  }
}

/**
 * Seller approves a refund request. Order remains in refund_requested
 * but is flagged for admin to finalize on-chain.
 */
export async function approveRefund(
  orderId: string,
  notes?: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/approve-refund`, {
      method: 'POST',
      headers,
      body: JSON.stringify(notes ? { notes } : {}),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Approve refund error:', error);
    return { success: false, message: error.message || 'Failed to approve refund' };
  }
}

/**
 * Seller rejects a refund request. Order returns to delivered status.
 */
export async function rejectRefund(
  orderId: string,
  notes?: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/reject-refund`, {
      method: 'POST',
      headers,
      body: JSON.stringify(notes ? { notes } : {}),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Reject refund error:', error);
    return { success: false, message: error.message || 'Failed to reject refund' };
  }
}

export async function requestRefund(orderId: string): Promise<ApiResponse<any>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/request-refund`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
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
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Shipping estimate error:', error);
    return { success: false, message: error.message || 'Failed to get shipping estimate' };
  }
}
