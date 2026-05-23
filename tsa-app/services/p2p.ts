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
 * P2P ad — a seller's published offer to sell USDT/USDC/MCGP at either a
 * fixed fiat rate (`rateType: 'fixed'`) or a floating rate computed at trade
 * time as `referenceRate * (1 + floatOffsetBps/10000)` (`rateType: 'float'`).
 *
 * For float ads, `rate` is a server-cached snapshot of the most recently
 * computed effective rate — used for browse display only. The authoritative
 * price at trade open is recomputed live from the feed and snapshotted onto
 * the trade row.
 *
 * `rateStale` is set by the list endpoint when the reference feed is
 * unavailable for a float ad. Buyers must not be allowed to open a trade in
 * that state (the server will reject with 503 anyway).
 *
 * Status lifecycle: pending → active → (paused ↔ active) → exhausted | cancelled.
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
  rateType: 'fixed' | 'float';
  floatOffsetBps?: number | null;
  floatAnchor?: string;
  rateStale?: boolean;
  minLimit?: string;
  maxLimit?: string;
  paymentMethodIds: string[];
  terms?: string;
  // ── Pool/voucher fields (post-Phase-2B). Populated by the backend after
  //    CreateAd; SellerAddress / VoucherSignature are the seller wallet that
  //    signed the AdVoucher EIP-712 payload returned alongside the ad.
  sellerAddress?: string;
  voucherNonce?: string;
  voucherDeadline?: string;
  voucherHash?: string;
  voucherSignature?: string;
  // ── Legacy lock-on-publish fields (kept on the wire for any rows that
  //    pre-date the migration; new ads do not populate these).
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
  /** HTTP status code of the underlying response (when reachable). Useful
   *  for status-specific UX, e.g. distinguishing 503 STALE_FEED from generic
   *  failure. Absent on network errors. */
  status?: number;
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
    return { success: false, message: `Empty response (HTTP ${response.status})`, status: response.status };
  }
  try {
    const parsed = JSON.parse(text) as ApiResponse<T>;
    return { ...parsed, status: response.status };
  } catch {
    const preview = text.length > 200 ? text.slice(0, 200) + '...' : text;
    return {
      success: false,
      message: response.ok
        ? `Unexpected server response: ${preview}`
        : `Server error (HTTP ${response.status}): ${preview}`,
      status: response.status,
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
  // qs is computed up front so the catch block can include it in the log
  // breadcrumb without re-running the (potentially expensive) Object.entries.
  const qs = opts.query
    ? '?' +
      Object.entries(opts.query)
        .filter(([, v]) => v !== undefined && v !== '')
        .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(String(v))}`)
        .join('&')
    : '';
  try {
    const headers = await getAuthHeaders();
    if (opts.idempotent ?? method !== 'GET') {
      headers['Idempotency-Key'] = opts.idempotencyKey ?? makeIdempotencyKey();
    }
    const response = await fetch(`${API_BASE_URL}${path}${qs}`, {
      method,
      headers,
      body: body ? JSON.stringify(body) : undefined,
    });
    const parsed = await safeJson<T>(response);
    // Non-success responses (>= 400 OR success:false) get a single console
    // line so debugging a "why did the UI show nothing" report has a
    // breadcrumb. Sensitive bodies are not logged — the helper only
    // surfaces method + path + status + server-provided message.
    if (!parsed.success) {
      console.warn(`[p2p] ${method} ${path}${qs} → ${parsed.status ?? 'no-status'}: ${parsed.message ?? '<empty>'}`);
    }
    return parsed;
  } catch (err: any) {
    console.warn(`[p2p] ${method} ${path}${qs} threw: ${err?.message || err}`);
    return { success: false, message: err?.message || 'Network error' };
  }
}

// ─── Nigerian banks (Nomba-backed) ──────────────────────────────────────────

export interface NgBank {
  code: string;
  name: string;
}

export interface AccountResolveResult {
  accountNumber: string;
  accountName: string;
}

export const banksApi = {
  /** Fetch the Nomba-supported Nigerian bank directory. Server caches 24h. */
  list: () => request<NgBank[]>('GET', '/p2p/banks'),

  /**
   * NUBAN name enquiry. Resolves the account holder name for a 10-digit
   * account number at the given bank. Returns a 404-shaped error
   * (`status === 404`) when the account does not exist at that bank — UI
   * should distinguish this from network/outage errors.
   */
  resolveAccount: (bankCode: string, accountNumber: string) =>
    request<AccountResolveResult>('GET', '/p2p/banks/resolve', undefined, {
      query: { bankCode, accountNumber },
    }),
};

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
  /** Required when rateType === 'fixed' (the default). Must be omitted for 'float' ads. */
  rate?: string;
  rateType?: 'fixed' | 'float';
  /** Required when rateType === 'float'. Basis points, e.g. 50 = +0.5%. */
  floatOffsetBps?: number;
  /** Defaults to 'bybit_sell' server-side. */
  floatAnchor?: string;
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

// VoucherTypedData mirrors the EIP-712 payload returned by POST /p2p/ads.
// The seller's wallet signs it via signTypedData; the signature goes back
// via adsApi.sign(). No on-chain transaction at publish time.
export interface VoucherTypedData {
  types: Record<string, Array<{ name: string; type: string }>>;
  primaryType: string;
  domain: {
    name: string;
    version: string;
    chainId: number;
    verifyingContract: string;
  };
  message: {
    seller: string;
    token: string;
    maxAmount: string;
    minPerTrade: string;
    maxPerTrade: string;
    deadline: string;
    nonce: string;
  };
}

export const adsApi = {
  list: (filter: ListAdsFilter = {}) =>
    request<P2PAd[]>('GET', '/p2p/ads', undefined, { query: filter as any }),

  listMine: (filter: Pick<ListAdsFilter, 'status' | 'limit' | 'offset'> = {}) =>
    request<P2PAd[]>('GET', '/p2p/ads/my', undefined, { query: filter as any }),

  get: (id: string) => request<P2PAd>('GET', `/p2p/ads/${encodeURIComponent(id)}`),

  /**
   * Publish a new ad. Returns the persisted ad (status `pending`) plus the
   * EIP-712 typed-data payload the seller's wallet must sign. Call
   * `adsApi.sign(ad.id, signature)` next to flip the ad to `active`.
   */
  create: (input: CreateAdInput) =>
    request<{ ad: P2PAd; typedData: VoucherTypedData }>('POST', '/p2p/ads', input),

  /**
   * Submit the seller's EIP-712 signature over the voucher returned by
   * `create()`. Backend verifies the signature recovers to `sellerAddress`
   * and flips the ad to `active`.
   */
  sign: (id: string, signature: string, sellerAddress: string) =>
    request<P2PAd>('POST', `/p2p/ads/${encodeURIComponent(id)}/sign`, {
      signature,
      sellerAddress,
    }),

  pause: (id: string) =>
    request<void>('POST', `/p2p/ads/${encodeURIComponent(id)}/pause`),

  resume: (id: string) =>
    request<void>('POST', `/p2p/ads/${encodeURIComponent(id)}/resume`),

  /**
   * Prepare the on-chain cancelVoucher tx. Cancellation is on-chain because
   * the contract is the source of truth for voucher revocation — a DB-only
   * flag would still leave the signature presentable by buyers.
   */
  prepareCancel: (id: string, sellerAddress: string) =>
    request<{ unsignedTx: UnsignedTx }>(
      'POST',
      `/p2p/ads/${encodeURIComponent(id)}/cancel`,
      { sellerAddress }
    ),

  confirmCancel: (id: string, txHash: string) =>
    request<P2PAd>('POST', `/p2p/ads/${encodeURIComponent(id)}/cancel/confirm`, {
      txHash,
    }),
};

// ─── Pool capital (deposit / withdraw / balance) ────────────────────────────

export interface PoolBalance {
  available: string;
  locked: string;
}

export const poolApi = {
  /** Read the authenticated seller's on-chain (available, locked) balance. */
  balance: (token: string) =>
    request<PoolBalance>('GET', '/p2p/balance', undefined, { query: { token } }),

  prepareDeposit: (token: string, amount: string) =>
    request<{ unsignedTx: UnsignedTx }>('POST', '/p2p/deposits', { token, amount }),

  /** Confirm a deposit tx. The backend cross-checks that the on-chain event
   *  amount matches `amount` — submit the same number you prepared with. */
  confirmDeposit: (token: string, amount: string, txHash: string) =>
    request<{ token: string; amount: string }>('POST', '/p2p/deposits/confirm', {
      token,
      amount,
      txHash,
    }),

  prepareWithdraw: (token: string, amount: string) =>
    request<{ unsignedTx: UnsignedTx }>('POST', '/p2p/withdrawals', { token, amount }),

  confirmWithdraw: (token: string, amount: string, txHash: string) =>
    request<{ token: string; amount: string }>('POST', '/p2p/withdrawals/confirm', {
      token,
      amount,
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
 *
 * Throws on inputs the contract would reject: scientific notation,
 * multiple decimal points, non-numeric junk. An empty string returns
 * "0" (callers test for > 0 before submitting anyway).
 */
export function parseTokenAmount(input: string, decimals: number): string {
  const trimmed = input.trim();
  if (!trimmed) return '0';
  const negative = trimmed.startsWith('-');
  const cleaned = negative ? trimmed.slice(1) : trimmed;
  // Strict: digits, optionally followed by a single dot + digits. Rejects
  // "1e2", "1.2.3", "abc", "0x10", and similar malformed shapes early so
  // the backend doesn't have to reject them after a round-trip.
  if (!/^\d+(\.\d+)?$/.test(cleaned)) {
    throw new Error(`Invalid decimal amount: ${JSON.stringify(input)}`);
  }
  const [intPart, fracPart = ''] = cleaned.split('.');
  const frac = (fracPart + '0'.repeat(decimals)).slice(0, decimals);
  const combined = `${intPart}${frac}`.replace(/^0+(?=\d)/, '');
  const base = combined || '0';
  return negative ? `-${base}` : base;
}

// ─── Shared FE helpers (extracted to deduplicate across P2P screens) ────────

/**
 * Token decimals lookup for the small whitelist of P2P-supported tokens.
 * Falls back to 18 (ERC-20 default) for anything unknown.
 */
export const TOKEN_DECIMALS: Record<string, number> = {
  USDT: 6,
  USDC: 6,
  MCGP: 18,
};

/**
 * Adapt the backend's UnsignedTx (hex without 0x prefix; chainId as string)
 * to the shape ethers' Transaction.from() expects. Centralised so every
 * P2P screen's broadcast path produces the same wire format.
 */
export function unsignedTxToTxLike(tx: UnsignedTx) {
  const ensure0x = (v?: string) => (!v ? undefined : v.startsWith('0x') ? v : `0x${v}`);
  return {
    to: tx.to,
    value: tx.value || '0',
    data: ensure0x(tx.data),
    nonce: tx.nonce,
    gasLimit: tx.gasLimit,
    gasPrice: tx.gasPrice,
    chainId: tx.chainId ? Number(tx.chainId) : undefined,
  };
}
