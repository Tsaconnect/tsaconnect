// services/spotApi.ts
//
// Client for admin-curated Spot Trade listings (/api/spot-listings + admin CRUD).
// Reads are enriched server-side with price + 24h change. Buy/Sell reuse existing
// app flows (see SpotList) chosen by `route` — no new swap logic here.

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

export type SpotRoute = 'mcgp' | 'lifi';

export interface SpotListing {
  id: string;
  baseSymbol: string;
  quoteSymbol: string;
  chain: string;
  route: SpotRoute;
  logoUrl?: string;
  enabled: boolean;
  displayOrder: number;
  // Enriched on the public read only (null when no price is available; absent on
  // the admin list).
  price?: number | null;
  change24h?: number | null;
}

export interface SpotListingInput {
  baseSymbol: string;
  quoteSymbol: string;
  chain: string;
  route: SpotRoute;
  logoUrl?: string;
  enabled?: boolean;
  displayOrder?: number;
}

export type ApiResult<T> = { success: boolean; data?: T; message?: string };

async function req<T>(method: string, path: string, body?: unknown): Promise<ApiResult<T>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}${path}`, {
      method,
      headers,
      body: body == null ? undefined : JSON.stringify(body),
    });
    let json: any = null;
    try {
      json = await res.json();
    } catch {
      /* empty */
    }
    if (json?.success) return { success: true, data: json.data as T };
    return { success: false, message: json?.message || `Request failed (${res.status})` };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

// Public (auth) — the list users see on the Spot tab, with price + 24h change.
export const getSpotListings = () =>
  req<{ listings: SpotListing[] }>('GET', '/spot-listings');

// Admin CRUD.
export const listSpotListingsAdmin = () =>
  req<{ listings: SpotListing[] }>('GET', '/admin/spot-listings');

export const createSpotListing = (body: SpotListingInput) =>
  req<SpotListing>('POST', '/admin/spot-listings', body);

export const updateSpotListing = (id: string, body: Partial<SpotListingInput>) =>
  req<SpotListing>('PUT', `/admin/spot-listings/${id}`, body);

export const deleteSpotListing = (id: string) =>
  req<null>('DELETE', `/admin/spot-listings/${id}`);

export const reorderSpotListings = (items: { id: string; displayOrder: number }[]) =>
  req<null>('POST', '/admin/spot-listings/reorder', { items });
