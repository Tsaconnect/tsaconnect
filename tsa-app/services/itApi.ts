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

// One entry from GET /it/withdraw/destinations — product catalog of
// currency/rail (and, for mobile_money, network) combinations IT withdrawal
// supports. Currently NGN/bank only until multi-currency sell is re-enabled.
export interface WithdrawDestination {
  currency: string;
  rail: 'bank' | 'mobile_money' | string;
  network?: string; // mobile_money only, e.g. "wave"
  label: string;
}

// GET /api/it/withdraw/destinations — product catalog (NGN/bank only until
// multi-currency sell is re-enabled). Single source of truth also used to
// validate the sell submission server-side.
export const getWithdrawDestinations = () =>
  req<{ destinations: WithdrawDestination[] }>('GET', '/it/withdraw/destinations');

export interface WithdrawQuote {
  destCurrency: string;
  destAmount: number;
  rate: number;
  stale: boolean;
}

// GET /api/it/withdraw/quote?ngn=<amt>&currency=<ISO> — live FX preview for a
// non-NGN destination. With the NGN-only product catalog this is unused by
// the app; kept for when foreign corridors reopen.
export const quoteWithdrawal = (ngn: number, currency: string) =>
  req<WithdrawQuote>('GET', `/it/withdraw/quote?ngn=${encodeURIComponent(ngn)}&currency=${encodeURIComponent(currency)}`);

// Fee line from GET /api/it/quote — minor units + display string.
export interface ITFeeLine {
  minor: string;
  display: string;
}

// Itemized deposit/withdrawal fee quote (MCGP ↔ NGN). Quote-only; never moves money.
export interface ITFeeQuote {
  flow: 'deposit' | 'withdraw' | string;
  asset: string;
  currency: string;
  amount: ITFeeLine;
  platformFee: { usdCents: number; usd: string; inCurrency: ITFeeLine };
  providerFee: { fee: ITFeeLine; source: string; fallbackBps?: number };
  tax: {
    basis: string;
    base: ITFeeLine;
    vatBps: number;
    vat: ITFeeLine;
    platformTaxBps: number;
    platformTax: ITFeeLine;
    total: ITFeeLine;
  };
  swapFee: { fee: ITFeeLine; bps: number; legs?: string[] };
  totalFees: ITFeeLine;
  netAmount: ITFeeLine;
  viable: boolean;
  assetOutEstimate?: string | null;
}

// GET /api/it/quote?flow=&asset=&amount=&currency= — itemized fee breakdown
// shown on sell Review (and available for buy). Amount is a positive decimal
// in `currency` (NGN for IT), max 2dp.
export const getITFeeQuote = (params: {
  flow: 'deposit' | 'withdraw';
  asset: 'MCGP' | 'USDC' | 'USDT';
  amount: string;
  currency?: string;
  chain?: string;
}) => {
  const q = new URLSearchParams({
    flow: params.flow,
    asset: params.asset,
    amount: params.amount,
    currency: params.currency || 'NGN',
  });
  if (params.chain) q.set('chain', params.chain);
  return req<{ quote: ITFeeQuote }>('GET', `/it/quote?${q.toString()}`);
};

// Optional payout destination for /it/sell. Omit entirely (or leave every
// field empty) for the legacy NGN manual-payout-queue path — unchanged
// behavior. When destinationCurrency/railType are set they're validated
// against the withdraw-destinations catalog server-side before the intent is
// created.
export interface ITSellOptions {
  bankCode?: string;
  accountNumber?: string;
  accountName?: string;
  destinationCurrency?: string;
  railType?: 'bank' | 'mobile_money';
  mobileNetwork?: string; // mobile_money only, from the selected catalog entry
  mobileCountry?: string; // mobile_money only, ISO-2 e.g. "SN"
  mobileMsisdn?: string; // mobile_money only
}

// mcgpAmount is base-unit wei (string) — semantics unchanged. `opts` carries
// the optional payout destination; omitted, this is exactly the legacy
// NGN-bank call.
export const itSell = (mcgpAmount: string, opts: ITSellOptions = {}) =>
  req<{ intent: ITIntent }>('POST', '/it/sell', { mcgpAmount, ...opts });
