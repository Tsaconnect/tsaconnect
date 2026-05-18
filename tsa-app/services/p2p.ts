// services/p2p.ts
//
// API client for the P2P trading backend (tsa-api-go internal/handlers/p2p_handler.go).
//
// Pattern note: every state-changing P2P operation is a two-step
// prepare/submit dance shared with the existing escrow flow:
//
//   1. POST  /p2p/.../action       → backend returns { unsignedTx, ... }
//   2. Frontend signs + broadcasts via the user's wallet
//   3. POST  /p2p/.../action/confirm with { txHash } → backend verifies +
//      finalizes DB state
//
// Idempotency: write endpoints accept an `Idempotency-Key` header (sent
// automatically by the helpers below). Retries return the cached response.
//
// All amounts on the wire are base-unit strings (no decimals) so big-integer
// precision is preserved end-to-end — frontend formats for display using
// each token's decimals.

import { API_BASE_URL } from '../constants/api/config';
import { getAuthHeaders } from './walletApi';

// ─── Types ──────────────────────────────────────────────────────────────────

/**
 * P2P ad — a seller's published offer to sell USDT/USDC/MCGP at a fixed
 * fiat rate. Status lifecycle: pending → active → (paused ↔ active) →
 * exhausted | cancelled.
 */
export interface P2PAd {
  id: string;
  sellerId: string;
  side: 'sell' | 'buy';
  token: string;
  chainId: number;
  amount: string;
  remaining: string;
  reserved: string;
  fiatCurrency: string;
  rate: string;
  minLimit?: string;
  maxLimit?: string;
  paymentMethodIds: string[];
  terms?: string;
  onChainAdId?: string;
  escrowTxHash?: string;
  status: 'pending' | 'active' | 'paused' | 'exhausted' | 'cancelled';
  expiresAt?: string;
  createdAt: string;
  updatedAt: string;
}

/**
 * P2P trade — one buyer's claim against an ad. Mirrors the on-chain status
 * enum plus the off-chain `releasing` intermediate.
 */
export interface P2PTrade {
  id: string;
  adId: string;
  buyerId: string;
  sellerId: string;
  token: string;
  chainId: number;
  tokenAmount: string;
  fiatAmount: string;
  fiatCurrency: string;
  rate: string;
  paymentMethodId: string;
  onChainTradeId?: string;
  openTxHash?: string;
  releaseTxHash?: string;
  status:
    | 'pending_payment'
    | 'paid'
    | 'releasing'
    | 'released'
    | 'cancelled'
    | 'disputed'
    | 'resolved';
  proofImageUrl?: string;
  cancelReason?: string;
  resolvedByAdmin?: string;
  markedPaidAt?: string;
  releasedAt?: string;
  expiresAt: string;
  expiredNotifiedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface P2PPaymentMethod {
  id: string;
  userId: string;
  type: 'bank' | 'mobile_money';
  country: string;
  currency: string;
  bankName: string;
  bankCode?: string;
  accountNumber: string;
  accountName: string;
  verified: boolean;
  isDefault: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface P2PMessage {
  id: string;
  tradeId: string;
  senderId?: string; // null for system / bot messages
  type: 'text' | 'image' | 'system' | 'bot';
  body?: string;
  imageUrl?: string;
  createdAt: string;
}

export interface P2PDispute {
  id: string;
  tradeId: string;
  raisedBy: string;
  raisedRole: 'buyer' | 'seller';
  reason: string;
  status: 'open' | 'resolved' | 'rejected';
  resolvedBy?: string;
  resolution?: string;
  refundedToBuyer?: boolean;
  resolveTxHash?: string;
  createdAt: string;
  resolvedAt?: string;
}

/**
 * Unsigned transaction shape produced by the backend's escrow adapter.
 * Mirrors `blockchain.UnsignedTx` in Go. Frontend wallets consume this
 * directly to sign — gasLimit/gasPrice are pre-estimated server-side.
 */
export interface UnsignedTx {
  nonce?: number;
  to: string;
  value: string;
  data: string;
  gasLimit?: number;
  gasPrice?: string;
  chainId?: string;
}

export interface ApiResponse<T = unknown> {
  success: boolean;
  message?: string;
  data?: T;
}

// ─── Helpers ────────────────────────────────────────────────────────────────

/**
 * Generate a fresh idempotency key per logical write. Re-broadcasting the
 * same key returns the cached response server-side — safe for retries.
 */
function makeIdempotencyKey(): string {
  // RFC 4122 v4-ish: timestamp + random; collision odds are negligible for
  // per-user single-request scope. Falls back to crypto.randomUUID when
  // available (Hermes / web).
  const g: any = globalThis as any;
  if (g.crypto?.randomUUID) {
    return g.crypto.randomUUID();
  }
  return `p2p-${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
}

async function safeJson<T = unknown>(response: Response): Promise<ApiResponse<T>> {
  const text = await response.text();
  if (!text) {
    return { success: false, message: `Empty response (HTTP ${response.status})` };
  }
  try {
    return JSON.parse(text) as ApiResponse<T>;
  } catch {
    const preview = text.length > 200 ? text.slice(0, 200) + '...' : text;
    return {
      success: false,
      message: response.ok
        ? `Unexpected server response: ${preview}`
        : `Server error (HTTP ${response.status}): ${preview}`,
    };
  }
}

interface RequestOptions {
  /** Send Idempotency-Key header; defaults to false for GETs, true for writes. */
  idempotent?: boolean;
  /** Override the auto-generated key (useful for retrying a known request). */
  idempotencyKey?: string;
  /** Extra query-string params. Values are URL-encoded automatically. */
  query?: Record<string, string | number | undefined>;
}

async function request<T>(
  method: 'GET' | 'POST' | 'PATCH' | 'DELETE',
  path: string,
  body?: unknown,
  opts: RequestOptions = {}
): Promise<ApiResponse<T>> {
  try {
    const headers = await getAuthHeaders();
    if (opts.idempotent ?? method !== 'GET') {
      headers['Idempotency-Key'] = opts.idempotencyKey ?? makeIdempotencyKey();
    }
    const qs = opts.query
      ? '?' +
        Object.entries(opts.query)
          .filter(([, v]) => v !== undefined && v !== '')
          .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
          .join('&')
      : '';
    const response = await fetch(`${API_BASE_URL}${path}${qs}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
    return await safeJson<T>(response);
  } catch (err: any) {
    return { success: false, message: err?.message || 'Network error' };
  }
}

// ─── Payment methods ────────────────────────────────────────────────────────

export const paymentMethodsApi = {
  list: () => request<P2PPaymentMethod[]>('GET', '/p2p/payment-methods'),

  create: (input: {
    type: 'bank' | 'mobile_money';
    country: string;
    currency: string;
    bankName: string;
    bankCode?: string;
    accountNumber: string;
    accountName: string;
    isDefault?: boolean;
  }) => request<P2PPaymentMethod>('POST', '/p2p/payment-methods', input),

  remove: (id: string) =>
    request<void>('DELETE', `/p2p/payment-methods/${encodeURIComponent(id)}`),
};

// ─── Ads ────────────────────────────────────────────────────────────────────

export interface CreateAdInput {
  token: string;
  tokenAmount: string;
  fiatCurrency: string;
  rate: string;
  minLimit?: string;
  maxLimit?: string;
  paymentMethodIds: string[];
  terms?: string;
  sellerAddress: string;
}

export interface ListAdsFilter {
  side?: 'sell' | 'buy';
  token?: string;
  fiatCurrency?: string;
  status?: P2PAd['status'];
  sellerId?: string;
  limit?: number;
  offset?: number;
}

export const adsApi = {
  list: (filter: ListAdsFilter = {}) =>
    request<P2PAd[]>('GET', '/p2p/ads', undefined, { query: filter as any }),

  listMine: (filter: Pick<ListAdsFilter, 'status' | 'limit' | 'offset'> = {}) =>
    request<P2PAd[]>('GET', '/p2p/ads/my', undefined, { query: filter as any }),

  get: (id: string) => request<P2PAd>('GET', `/p2p/ads/${encodeURIComponent(id)}`),

  create: (input: CreateAdInput) =>
    request<{ ad: P2PAd; unsignedTx: UnsignedTx }>('POST', '/p2p/ads', input),

  confirm: (id: string, txHash: string, sellerAddress?: string) =>
    request<P2PAd>('POST', `/p2p/ads/${encodeURIComponent(id)}/confirm`, {
      txHash,
      sellerAddress,
    }),

  pause: (id: string) =>
    request<void>('POST', `/p2p/ads/${encodeURIComponent(id)}/pause`),

  resume: (id: string) =>
    request<void>('POST', `/p2p/ads/${encodeURIComponent(id)}/resume`),

  prepareClose: (id: string, sellerAddress: string) =>
    request<{ unsignedTx: UnsignedTx }>(
      'POST',
      `/p2p/ads/${encodeURIComponent(id)}/close`,
      { sellerAddress }
    ),

  confirmClose: (id: string, txHash: string) =>
    request<P2PAd>('POST', `/p2p/ads/${encodeURIComponent(id)}/close/confirm`, {
      txHash,
    }),
};

// ─── Trades ─────────────────────────────────────────────────────────────────

export interface OpenTradeInput {
  fiatAmount: string;
  paymentMethodId: string;
  buyerAddress: string;
}

export interface ListTradesFilter {
  role?: 'buyer' | 'seller';
  status?: P2PTrade['status'];
  limit?: number;
  offset?: number;
}

export const tradesApi = {
  list: (filter: ListTradesFilter = {}) =>
    request<P2PTrade[]>('GET', '/p2p/trades', undefined, { query: filter as any }),

  get: (id: string) =>
    request<P2PTrade>('GET', `/p2p/trades/${encodeURIComponent(id)}`),

  open: (adId: string, input: OpenTradeInput) =>
    request<{ trade: P2PTrade; unsignedTx: UnsignedTx }>(
      'POST',
      `/p2p/ads/${encodeURIComponent(adId)}/trades`,
      input
    ),

  confirmOpen: (tradeId: string, txHash: string, callerAddress?: string) =>
    request<P2PTrade>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/open/confirm`,
      { txHash, callerAddress }
    ),

  markPaid: (tradeId: string, proofImageUrl: string) =>
    request<P2PTrade>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/mark-paid`,
      { proofImageUrl }
    ),

  prepareRelease: (tradeId: string, sellerAddress: string) =>
    request<{ unsignedTx: UnsignedTx }>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/release`,
      { sellerAddress }
    ),

  confirmRelease: (tradeId: string, txHash: string) =>
    request<P2PTrade>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/release/confirm`,
      { txHash }
    ),

  prepareCancel: (tradeId: string, callerAddress?: string) =>
    request<{ unsignedTx: UnsignedTx }>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/cancel`,
      callerAddress ? { callerAddress } : {}
    ),

  confirmCancel: (tradeId: string, txHash: string) =>
    request<P2PTrade>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/cancel/confirm`,
      { txHash }
    ),

  raiseDispute: (tradeId: string, reason: string, callerAddress?: string) =>
    request<{ unsignedTx: UnsignedTx }>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/dispute`,
      { reason, callerAddress }
    ),

  confirmRaiseDispute: (tradeId: string, txHash: string) =>
    request<P2PTrade>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/dispute/confirm`,
      { txHash }
    ),

  getDispute: (tradeId: string) =>
    request<P2PDispute>('GET', `/p2p/trades/${encodeURIComponent(tradeId)}/dispute`),
};

// ─── Chat ───────────────────────────────────────────────────────────────────

export interface ListMessagesOptions {
  /** RFC3339 timestamp — only return messages created after this time. */
  since?: string;
  limit?: number;
}

export const messagesApi = {
  list: (tradeId: string, opts: ListMessagesOptions = {}) =>
    request<P2PMessage[]>(
      'GET',
      `/p2p/trades/${encodeURIComponent(tradeId)}/messages`,
      undefined,
      { query: opts as any }
    ),

  sendText: (tradeId: string, body: string) =>
    request<P2PMessage>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/messages`,
      { type: 'text', body }
    ),

  sendImage: (tradeId: string, imageUrl: string) =>
    request<P2PMessage>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/messages`,
      { type: 'image', imageUrl }
    ),
};

// ─── Admin (dispute resolution) ─────────────────────────────────────────────

export interface AdminResolveInput {
  refundBuyer: boolean;
  note?: string;
  adminAddress?: string;
}

export const adminP2pApi = {
  listDisputes: (status?: P2PDispute['status']) =>
    request<P2PDispute[]>('GET', '/admin/p2p/disputes', undefined, {
      query: { status },
    }),

  getDispute: (tradeId: string) =>
    request<P2PDispute>(
      'GET',
      `/admin/p2p/disputes/${encodeURIComponent(tradeId)}`
    ),

  prepareResolve: (tradeId: string, input: AdminResolveInput) =>
    request<{ unsignedTx: UnsignedTx }>(
      'POST',
      `/admin/p2p/disputes/${encodeURIComponent(tradeId)}/resolve`,
      input
    ),

  confirmResolve: (tradeId: string, txHash: string) =>
    request<P2PTrade>(
      'POST',
      `/admin/p2p/disputes/${encodeURIComponent(tradeId)}/resolve/confirm`,
      { txHash }
    ),
};

// ─── Convenience: stateful flow helpers ────────────────────────────────────

/**
 * Format a token base-unit amount for display. USDT/USDC use 6 decimals;
 * MCGP and most native tokens use 18.
 *
 * Returns a string with at most `displayDecimals` fractional digits,
 * trailing zeros trimmed. Use this for chat / receipts; for input fields
 * keep the raw base-unit string to avoid round-trip precision loss.
 */
export function formatTokenAmount(
  baseUnits: string,
  decimals: number,
  displayDecimals = 4
): string {
  if (!baseUnits) return '0';
  const negative = baseUnits.startsWith('-');
  const s = (negative ? baseUnits.slice(1) : baseUnits).padStart(decimals + 1, '0');
  const intPart = s.slice(0, s.length - decimals) || '0';
  let frac = s.slice(s.length - decimals);
  if (displayDecimals < frac.length) {
    frac = frac.slice(0, displayDecimals);
  }
  // Trim trailing zeros but keep at least one digit so we can format
  // sensibly downstream.
  frac = frac.replace(/0+$/, '');
  const out = frac ? `${intPart}.${frac}` : intPart;
  return negative ? `-${out}` : out;
}

/**
 * Inverse of formatTokenAmount: parse a user-typed decimal string into
 * base units. Truncates anything beyond `decimals` fractional digits —
 * matches the backend's rate-scaling truncation policy so display and
 * server math agree to the last unit.
 */
export function parseTokenAmount(input: string, decimals: number): string {
  const trimmed = input.trim();
  if (!trimmed) return '0';
  const negative = trimmed.startsWith('-');
  const cleaned = negative ? trimmed.slice(1) : trimmed;
  const [intPart = '0', fracPart = ''] = cleaned.split('.');
  const frac = (fracPart + '0'.repeat(decimals)).slice(0, decimals);
  const combined = `${intPart}${frac}`.replace(/^0+(?=\d)/, '');
  const base = combined || '0';
  return negative ? `-${base}` : base;
}
