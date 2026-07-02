// services/serviceListingApi.ts
// API calls for the three service types' listing config: per-service profile
// (fees, slot config, policies, coverage), the order-type menu, and booking-type
// availability windows + computed slots. Reads are public; writes require the
// owning merchant's auth token. See the Go backend service_* handlers.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';

export interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  data?: T;
}

export interface ServiceProfile {
  id?: string;
  productId: string;
  accessFeeUsd: number;
  calloutFeeUsd: number;
  slotDurationMinutes: number;
  maxCapacityPerSlot: number;
  serviceAddress?: string;
  coverageArea?: string;
  operatingCompany?: string;
  cancellationPolicy?: string;
  reschedulePolicy?: string;
  refundPolicy?: string;
  bookingRules?: string;
}

export interface ServiceMenuItem {
  id: string;
  productId: string;
  name: string;
  description?: string;
  priceUsd: number;
  imageUrl?: string;
  isAvailable: boolean;
  sortOrder: number;
}

export interface AvailabilityWindow {
  dayOfWeek: number; // 0=Sun … 6=Sat
  startTime: string; // "HH:MM"
  endTime: string;
}

export interface OpenSlot {
  start: string; // RFC3339
  end: string;
  capacity: number; // 0 = unlimited
  booked: number;
  remaining: number; // -1 = unlimited
}

export const WEEKDAY_LABELS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

async function authHeaders(): Promise<Record<string, string>> {
  const token = await AsyncStorage.getItem('authToken');
  return {
    'Content-Type': 'application/json',
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
  };
}

async function handleResponse<T>(response: Response): Promise<ApiResponse<T>> {
  let data: any;
  try {
    data = await response.json();
  } catch {
    return { success: false, message: `HTTP ${response.status}` };
  }
  if (!response.ok) {
    return { success: false, message: data.message || `Request failed (${response.status})` };
  }
  return { success: true, ...data };
}

// ---------- profile ----------

export async function getServiceProfile(productId: string): Promise<ApiResponse<ServiceProfile>> {
  try {
    const res = await fetch(`${API_BASE_URL}/services/${productId}/profile`, { method: 'GET' });
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to load service profile' };
  }
}

export async function upsertServiceProfile(
  productId: string,
  profile: Partial<ServiceProfile>
): Promise<ApiResponse<ServiceProfile>> {
  try {
    const headers = await authHeaders();
    const res = await fetch(`${API_BASE_URL}/services/${productId}/profile`, {
      method: 'PUT',
      headers,
      body: JSON.stringify(profile),
    });
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to save service profile' };
  }
}

// ---------- menu (order type) ----------

export async function listServiceMenu(
  productId: string
): Promise<ApiResponse<{ items: ServiceMenuItem[] }>> {
  try {
    const res = await fetch(`${API_BASE_URL}/services/${productId}/menu`, { method: 'GET' });
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to load menu' };
  }
}

export async function createServiceMenuItem(
  productId: string,
  item: Partial<ServiceMenuItem>
): Promise<ApiResponse<ServiceMenuItem>> {
  try {
    const headers = await authHeaders();
    const res = await fetch(`${API_BASE_URL}/services/${productId}/menu`, {
      method: 'POST',
      headers,
      body: JSON.stringify(item),
    });
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to add menu item' };
  }
}

export async function updateServiceMenuItem(
  productId: string,
  itemId: string,
  item: Partial<ServiceMenuItem>
): Promise<ApiResponse<ServiceMenuItem>> {
  try {
    const headers = await authHeaders();
    const res = await fetch(`${API_BASE_URL}/services/${productId}/menu/${itemId}`, {
      method: 'PUT',
      headers,
      body: JSON.stringify(item),
    });
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to update menu item' };
  }
}

export async function deleteServiceMenuItem(
  productId: string,
  itemId: string
): Promise<ApiResponse<{ id: string }>> {
  try {
    const headers = await authHeaders();
    const res = await fetch(`${API_BASE_URL}/services/${productId}/menu/${itemId}`, {
      method: 'DELETE',
      headers,
    });
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to delete menu item' };
  }
}

// ---------- availability + slots (booking type) ----------

export async function getServiceAvailability(
  productId: string
): Promise<ApiResponse<{ windows: AvailabilityWindow[] }>> {
  try {
    const res = await fetch(`${API_BASE_URL}/services/${productId}/availability`, { method: 'GET' });
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to load availability' };
  }
}

export async function replaceServiceAvailability(
  productId: string,
  windows: AvailabilityWindow[]
): Promise<ApiResponse<{ windows: AvailabilityWindow[] }>> {
  try {
    const headers = await authHeaders();
    const res = await fetch(`${API_BASE_URL}/services/${productId}/availability`, {
      method: 'PUT',
      headers,
      body: JSON.stringify({ windows }),
    });
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to save availability' };
  }
}

// Computed bookable slots for a date range (YYYY-MM-DD), each annotated with
// remaining capacity.
export async function getServiceSlots(
  productId: string,
  from: string,
  to: string
): Promise<ApiResponse<{ slots: OpenSlot[]; slotDurationMinutes: number; maxCapacityPerSlot: number }>> {
  try {
    const res = await fetch(
      `${API_BASE_URL}/services/${productId}/slots?from=${from}&to=${to}`,
      { method: 'GET' }
    );
    return await handleResponse(res);
  } catch (e: any) {
    return { success: false, message: e.message || 'Failed to load slots' };
  }
}
