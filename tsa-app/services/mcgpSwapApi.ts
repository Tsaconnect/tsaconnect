// services/mcgpSwapApi.ts
//
// Client for the MCGP Swap Contract B endpoints (/api/mcgp/*). Mirrors the
// prepare→sign→confirm contract used by walletApi/p2p: prepare endpoints return
// an unsigned tx the app signs + broadcasts; confirm endpoints verify the
// on-chain event and write the audit ledger row.
//
// Two buckets:
//   • PREMIUM — bought (buyPremium, paid in USDC), spendable (pay) + sellable.
//   • LEGACY  — pre-launch snapshot entitlement, REDEEM-ONLY (sellLegacy).

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

export type McgpUnsignedTx = {
  nonce?: number;
  gasPrice?: string;
  gasLimit?: string;
  to: string;
  value?: string;
  data: string;
  chainId?: string;
};

export interface McgpBalance {
  walletAddress: string;
  premiumBalance: string; // MCGP base units (18dp)
  legacyEntitlement: string;
  legacyRemaining: string;
}

export interface McgpQuote {
  kind: string;
  mcgpAmount: string;
  usdcAmount: string; // USDC base units (6dp)
}

export interface McgpPreparePay {
  ref: string;
  recipient: string;
  recipientName: string;
  mcgpAmount: string;
  tx: McgpUnsignedTx;
}

export interface McgpLedgerEntry {
  id: string;
  direction: string;
  mcgpAmount: string;
  usdcAmount?: string;
  txHash?: string;
  status: string;
  createdAt: string;
}

export type ApiResult<T> = { success: boolean; data?: T; message?: string };

export type SwapDirection =
  | 'buy_premium'
  | 'sell_premium'
  | 'sell_legacy'
  | 'withdraw';

export type QuoteKind = 'premiumBuy' | 'premiumSell' | 'legacySell';

async function safeJson(res: Response): Promise<any> {
  try {
    return await res.json();
  } catch {
    return null;
  }
}

// GET helper for wrapped {success,data} responses.
async function getJson<T>(path: string): Promise<ApiResult<T>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}${path}`, { method: 'GET', headers });
    const body = await safeJson(res);
    if (body?.success) return { success: true, data: body.data as T };
    return { success: false, message: body?.message || `Request failed (${res.status})` };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

// POST helper for wrapped {success,data} responses.
async function postJson<T>(path: string, body: unknown): Promise<ApiResult<T>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}${path}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
    });
    const json = await safeJson(res);
    if (json?.success) return { success: true, data: json.data as T };
    return { success: false, message: json?.message || `Request failed (${res.status})` };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

// POST helper for the prepare endpoints that return the RAW unsigned tx
// ({to,data,...}) on success, or the {success:false,message} envelope on error.
async function postRawTx(path: string, body: unknown): Promise<ApiResult<McgpUnsignedTx>> {
  try {
    const headers = await getAuthHeaders();
    const res = await fetch(`${API_BASE_URL}${path}`, {
      method: 'POST',
      headers,
      body: JSON.stringify(body),
    });
    const json = await safeJson(res);
    if (json && json.success === false) {
      return { success: false, message: json.message || `Request failed (${res.status})` };
    }
    if (json && typeof json.to === 'string' && typeof json.data === 'string') {
      return { success: true, data: json as McgpUnsignedTx };
    }
    return { success: false, message: `Unexpected response (${res.status})` };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}

// ─────────────────────────────── reads ────────────────────────────────────
export const getMcgpBalance = () => getJson<McgpBalance>('/mcgp/balance');

export const getMcgpQuote = (kind: QuoteKind, amount: string) =>
  getJson<McgpQuote>(`/mcgp/quote?kind=${kind}&amount=${encodeURIComponent(amount)}`);

export const getMcgpLedger = () =>
  getJson<{ entries: McgpLedgerEntry[] }>('/mcgp/ledger');

// ────────────────────────────── prepares ──────────────────────────────────
export const prepareMcgpApprove = (token: 'usdc' | 'mcgp', amount: string) =>
  postJson<{ currentAllowance: string; tx: McgpUnsignedTx }>('/mcgp/approve/prepare', {
    token,
    amount,
  });

export const prepareMcgpBuy = (mcgpAmount: string, maxUsdc: string) =>
  postRawTx('/mcgp/buy/prepare', { mcgpAmount, maxUsdc });

export const prepareMcgpSell = (mcgpAmount: string, minUsdc: string) =>
  postRawTx('/mcgp/sell/prepare', { mcgpAmount, minUsdc });

export const prepareMcgpLegacySell = (mcgpAmount: string, minUsdc: string) =>
  postRawTx('/mcgp/legacy/sell/prepare', { mcgpAmount, minUsdc });

export const prepareMcgpWithdraw = (mcgpAmount: string) =>
  postRawTx('/mcgp/withdraw/prepare', { mcgpAmount });

export const prepareMcgpPay = (username: string, mcgpAmount: string) =>
  postJson<McgpPreparePay>('/mcgp/pay/prepare', { username, mcgpAmount });

// ────────────────────────────── confirms ──────────────────────────────────
export const confirmMcgpSwap = (txHash: string, direction: SwapDirection) =>
  postJson<{ ledgerId?: string; mcgpAmount?: string; usdcAmount?: string }>('/mcgp/confirm', {
    txHash,
    direction,
  });

export const confirmMcgpPay = (txHash: string, ref: string, username: string) =>
  postJson<{ ledgerId?: string }>('/mcgp/pay/confirm', { txHash, ref, username });
