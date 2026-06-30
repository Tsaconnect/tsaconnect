// services/bookingFeeApi.ts
// API calls for a booking-mode service category's merged call-out+booking fare
// schedule. The admin sets a per-area-tier fare; the buyer checkout reads it to
// price a booking. See the Go backend category booking-fee handlers.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';

// The six area tiers, near → far. Mirrors models.BookingFeeTiers on the backend.
export const BOOKING_TIERS = [
  'residential',
  'same_city',
  'same_lga',
  'same_state',
  'same_country',
  'outside_country',
] as const;

export type BookingTier = (typeof BOOKING_TIERS)[number];

// Human labels for the tiers (UI).
export const BOOKING_TIER_LABELS: Record<BookingTier, string> = {
  residential: 'Same residential area',
  same_city: 'Same city',
  same_lga: 'Same LGA',
  same_state: 'Same state',
  same_country: 'Same country',
  outside_country: 'Outside the country',
};

export type BookingFees = Partial<Record<BookingTier, number>>;

export interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  data?: T;
}

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

// Public read of a category's full six-tier fare schedule (absent tiers → 0).
export async function getCategoryBookingFees(
  categoryId: string
): Promise<ApiResponse<{ categoryId: string; fees: Record<BookingTier, number> }>> {
  try {
    const response = await fetch(`${API_BASE_URL}/products/category/${categoryId}/booking-fees`, {
      method: 'GET',
      headers: { 'Content-Type': 'application/json' },
    });
    return await handleResponse(response);
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to load booking fees' };
  }
}

// Admin upsert of the full schedule. Omitted tiers are stored as 0.
export async function upsertCategoryBookingFees(
  categoryId: string,
  fees: BookingFees
): Promise<ApiResponse<{ categoryId: string; fees: Record<BookingTier, number> }>> {
  try {
    const headers = await authHeaders();
    const response = await fetch(`${API_BASE_URL}/products/category/${categoryId}/booking-fees`, {
      method: 'PUT',
      headers,
      body: JSON.stringify({ fees }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to save booking fees' };
  }
}
