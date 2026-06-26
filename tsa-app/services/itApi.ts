// services/itApi.ts
//
// Client for Instant Transfer (/api/it/*): buy/sell MCGP with Naira via the
// company PM. No client-side signing — the backend operator signs the on-chain
// credit/debit; the app just creates intents and polls status.

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

export interface ITRate {
  buyRateKobo: string; // kobo per 1 MCGP
  sellRateKobo: string;
}

export interface ITBalance {
  premiumBought: string; // MCGP wei (USDC-bought, self-exitable)
  premiumCredited: string; // MCGP wei (IT/NGN, spendable)
  pendingSettle: string; // MCGP wei (in-flight NGN sell)
  legacyRemaining: string;
}

export interface ITIntent {
  id: string;
  direction: 'buy' | 'sell';
  status: string;
  mcgpAmount: string;
  ngnKobo: string;
  rateKobo: string;
  paymentRef?: string;
  txHash?: string;
  failureReason?: string;
  createdAt: string;
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

export const getITRate = () => req<ITRate>('GET', '/it/rate');
export const getITBalance = () => req<ITBalance>('GET', '/it/balance');
export const listITIntents = () => req<{ intents: ITIntent[] }>('GET', '/it/intents');
export const getITIntent = (id: string) => req<{ intent: ITIntent }>('GET', `/it/intents/${id}`);

export interface ITPayTo {
  accountNumber: string;
  bank: string;
  accountName: string;
  reference: string;
}

// ngnKobo = naira * 100 (string). Returns the intent + paymentRef + the company
// pay-into account (payTo is null when no payment account is configured).
export const itBuy = (ngnKobo: string) =>
  req<{ intent: ITIntent; paymentRef: string; payTo: ITPayTo | null }>('POST', '/it/buy', { ngnKobo });

// mcgpAmount is base-unit wei (string).
export const itSell = (mcgpAmount: string) =>
  req<{ intent: ITIntent }>('POST', '/it/sell', { mcgpAmount });
