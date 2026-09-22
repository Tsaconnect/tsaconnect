// services/deliveryAddressApi.ts
// The buyer's saved delivery addresses, picked from at checkout. An order
// snapshots the address it was sent to, so editing or deleting one here never
// changes a past order.
import { API_BASE_URL } from '../constants/api/config';
import { getAuthHeaders, handleResponse, type ApiResponse } from './orderApi';

export interface DeliveryAddressInput {
  label?: string;
  recipientName: string;
  phone: string;
  street: string;
  city: string;
  state: string;
  country: string;
  isDefault?: boolean;
}

export interface DeliveryAddress extends DeliveryAddressInput {
  id: string;
  isDefault: boolean;
  createdAt: string;
  updatedAt: string;
}

const BASE = `${API_BASE_URL}/delivery-addresses`;

async function request<T>(url: string, method: string, body?: unknown): Promise<ApiResponse<T>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(url, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    return await handleResponse<T>(response);
  } catch (error: any) {
    return { success: false, message: error.message || 'Network error' };
  }
}

/** Default first, then most recently updated. */
export const listDeliveryAddresses = () => request<DeliveryAddress[]>(BASE, 'GET');

export const createDeliveryAddress = (input: DeliveryAddressInput) =>
  request<DeliveryAddress>(BASE, 'POST', input);

export const updateDeliveryAddress = (id: string, input: DeliveryAddressInput) =>
  request<DeliveryAddress>(`${BASE}/${id}`, 'PUT', input);

export const deleteDeliveryAddress = (id: string) => request<null>(`${BASE}/${id}`, 'DELETE');

export const formatDeliveryAddress = (a: Pick<DeliveryAddressInput, 'street' | 'city' | 'state' | 'country'>) =>
  [a.street, a.city, a.state, a.country].filter(Boolean).join(', ');
