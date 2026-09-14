// services/spotApi.ts
//
// Client for admin-curated Spot Trade listings (/api/spot-listings + admin CRUD).
// Reads are enriched server-side with price + 24h change. Buy/Sell reuse existing
// app flows (see SpotList) chosen by `route` — no new swap logic here.

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

// 'allswap' is for pairs whose two legs do not share a chain — Bitcoin above
// all, where LiFi lists exactly one token and so no same-chain pair can exist.
// Such a pair is only tradeable as a cross-chain route, which the same-chain
// Spot desk cannot build.
export type SpotRoute = 'mcgp' | 'lifi' | 'allswap';

// Admin-assigned taxonomy bucket. Computed tabs (Hot/New/Gainers/Losers/
// Favorites) are derived client-side — see utils/spotTabs.
export type SpotCategory = 'spot' | 'alpha' | 'tstocks' | 'tokenized' | 'tradefi' | 'rwa';

export interface SpotListing {
  id: string;
  baseSymbol: string;
  quoteSymbol: string;
  chain: string;
  // The chain the QUOTE leg lives on, when it is not `chain`. Absent on every
  // same-chain listing, which is why it stays optional rather than being
  // backfilled: a missing value means "same chain as the base leg", and that
  // is the only reading that leaves existing listings meaning what they meant.
  quoteChain?: string;
  // True when the admin left the quote chain on auto. The API then still sends
  // a default `quoteChain` (for apps that predate auto), so this flag — not an
  // empty quoteChain — is what says the leg should follow the user's balance.
  quoteChainAuto?: boolean;
  route: SpotRoute;
  logoUrl?: string;
  enabled: boolean;
  displayOrder: number;
  category: SpotCategory;
  pinnedPosition?: number | null;
  createdAt?: string;
  // Enriched on the public read only (null when no price is available; absent on
  // the admin list). The public list arrives pre-ranked by the server (pins →
  // volume group → mcap) — render in order, don't re-sort.
  price?: number | null;
  change24h?: number | null;
  marketCap?: number | null;
  volume24h?: number | null;
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
