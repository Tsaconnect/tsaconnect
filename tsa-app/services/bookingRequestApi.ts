// services/bookingRequestApi.ts
// Booking requests (unified booking engine, Phase 2 — request & approval). A
// non-instant booking service creates an off-chain request the merchant
// approves/declines; on approval the buyer pays, converting it into an escrowed
// order. See the Go booking_request_handler.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';
import type { ApiResponse } from './serviceListingApi';
import type { Order } from './orderApi';

export type BookingRequestStatus =
  | 'requested'
  | 'approved'
  | 'rejected'
  | 'expired'
  | 'converted'
  | 'cancelled';

export interface BookingRequest {
  id: string;
  productId: string;
  buyerId: string;
  sellerId: string;
  token: string;
  serviceTier?: string;
  answers?: Record<string, any>;
  // Seats/tickets asked for in a quantity-mode category; 0 elsewhere.
  quantity?: number;
  status: BookingRequestStatus;
  rejectReason?: string;
  orderId?: string;
  quotedAmountUsd?: number;
  createdAt: string;
  updatedAt: string;
}

// A request row enriched with its service name (as returned by the list endpoint).
export interface BookingRequestRow {
  request: BookingRequest;
  serviceName: string;
  // The order's status once the request has converted (absent before then).
  orderStatus?: string;
}

async function authHeaders(): Promise<Record<string, string>> {
  const token = await AsyncStorage.getItem('authToken');
  return {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
}

async function handle<T>(res: Response): Promise<ApiResponse<T>> {
  let data: any;
  try {
    data = await res.json();
  } catch {
    return { success: false, message: `HTTP ${res.status}` };
  }
  if (!res.ok) return { success: false, message: data.message || `Request failed (${res.status})` };
  return { success: true, ...data };
}

export async function createBookingRequest(
  productId: string,
  body: {
    token: string;
    serviceTier?: string;
    buyerCity?: string;
    buyerState?: string;
    buyerCountry?: string;
    quantity?: number;
    answers?: Record<string, any>;
  }
): Promise<ApiResponse<BookingRequest>> {
  try {
    const res = await fetch(`${API_BASE_URL}/services/${productId}/requests`, {
      method: 'POST',
      headers: await authHeaders(),
      body: JSON.stringify(body),
    });
    return await handle(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to submit booking request' };
  }
}

export async function listBookingRequests(
  role: 'buyer' | 'seller',
  status?: BookingRequestStatus
): Promise<ApiResponse<{ requests: BookingRequestRow[] }>> {
  try {
    const qs = new URLSearchParams({ role });
    if (status) qs.append('status', status);
    const res = await fetch(`${API_BASE_URL}/services/requests?${qs.toString()}`, {
      method: 'GET',
      headers: await authHeaders(),
    });
    return await handle(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to load booking requests' };
  }
}

export async function approveBookingRequest(
  reqId: string,
  amountUsd?: number
): Promise<ApiResponse<BookingRequest>> {
  return mutate(`${API_BASE_URL}/services/requests/${reqId}/approve`, amountUsd != null ? { amountUsd } : undefined);
}

export async function rejectBookingRequest(
  reqId: string,
  reason?: string
): Promise<ApiResponse<BookingRequest>> {
  return mutate(`${API_BASE_URL}/services/requests/${reqId}/reject`, { reason });
}

// Pay an approved request — the server creates the escrowed order and returns it.
export async function payBookingRequest(reqId: string): Promise<ApiResponse<{ orders: Order[] }>> {
  return mutate(`${API_BASE_URL}/services/requests/${reqId}/pay`);
}

async function mutate<T>(url: string, body?: Record<string, any>): Promise<ApiResponse<T>> {
  try {
    const res = await fetch(url, {
      method: 'POST',
      headers: await authHeaders(),
      body: body ? JSON.stringify(body) : undefined,
    });
    return await handle(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Request failed' };
  }
}
