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
import { getChainByChainId, type ChainKey } from '../constants/chains';

// ─── Types ──────────────────────────────────────────────────────────────────

export interface SellerStats {
  completionRate?: number;
  avgReleaseSecs?: number;
  totalOrders: number;
  positivePct?: number;
  online: boolean;
  verified: boolean;
  trusted: boolean;
  isNew: boolean;
}

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
  /** Username of the seller, decorated by the list/detail endpoints so the
   *  FE can render "@ojay" instead of a truncated UUID. Optional — missing
   *  when the user lookup failed transiently. */
  sellerUsername?: string;
  minLimit?: string;
  maxLimit?: string;
  paymentMethodIds: string[];
  /** Privacy-safe payment descriptor for the marketplace list — provider +
   *  label only, never account numbers. Populated by the list endpoint. */
  paymentMethodSummary?: { provider: string; label: string }[];
  /** Seller reputation snapshot decorated by the list/detail endpoints. */
  sellerStats?: SellerStats;
  /** Seller's payment-method rows the buyer should send NGN to. Populated
   *  by the ad-detail endpoint only — the marketplace list omits it so
   *  bank account numbers aren't broadcast in the feed. */
  paymentMethods?: P2PPaymentMethod[];
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
  /** True when this ad is posted by a Payment Merchant (PM). PM buy-ads
   *  trigger Nomba bank-transfer payout — the app must collect the seller's
   *  destination bank account at open time. */
  isPm?: boolean;
  /** ID of the PM that owns this ad. Populated server-side alongside isPm. */
  pmId?: string;
  /** For a PM ad, the merchant's Nomba VA the buyer pays into (auto-settled).
   *  Surfaced on the ad detail so a buyer sees the pay-to before opening. */
  pmPayTo?: { accountNumber: string; bank: string; accountName: string } | null;
  status: 'pending' | 'active' | 'paused' | 'exhausted' | 'cancelled';
  expiresAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface P2PRating {
  id: string;
  tradeId: string;
  raterId: string;
  rateeId: string;
  value: 1 | -1;
  createdAt: string;
}

export interface MerchantProfile {
  userId: string;
  stats: SellerStats;
  recentRatings: P2PRating[];
  activeAds: P2PAd[];
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
  // PM (Payment Merchant) automation: when isPm, the backend bot drives
  // markPaid + release — the user never does so manually. pmPayTo is the
  // Nomba inbound account a buyer pays into; payerName is the buyer's KYC name.
  isPm?: boolean;
  pmPayTo?: { accountNumber: string; bank: string; accountName: string } | null;
  payerName?: string;
}

export interface P2PPaymentMethod {
  id: string;
  userId: string;
  type: 'bank' | 'mobile_money' | 'wallet';
  provider: string;
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
  /** Cloudinary image URLs submitted with the dispute as evidence. Decoded
   *  server-side from the stored JSON (P2PDispute.AfterFind); omitted when
   *  none were attached. */
  evidenceUrls?: string[];
  status: 'open' | 'resolved' | 'rejected';
  resolvedBy?: string;
  resolution?: string;
  refundedToBuyer?: boolean;
  resolveTxHash?: string;
  createdAt: string;
  resolvedAt?: string;
}

export interface PaymentProvider {
  slug: string;
  label: string;
  type: 'bank' | 'mobile_money' | 'wallet';
  identifierLabel: string;
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

// ─── Chains ─────────────────────────────────────────────────────────────────

export interface P2PChainInfo {
  chainId: number;
  assets: string[];
}

export const chainsApi = {
  list: () => request<P2PChainInfo[]>('GET', '/p2p/chains'),
};

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

  providers: () => request<PaymentProvider[]>('GET', '/p2p/payment-providers'),

  create: (input: {
    provider: string;
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
  /** 'sell' (default) = user sells crypto; 'buy' = merchant posts a buy-ad and will pay fiat. */
  side?: 'sell' | 'buy';
  /** Chain the ad is published on. Omitting defaults to Sonic server-side. */
  chainId?: number;
}

export interface ListAdsFilter {
  side?: 'sell' | 'buy';
  token?: string;
  fiatCurrency?: string;
  /** Filter ads by chain. Omitting returns ads from all chains. */
  chainId?: number;
  status?: P2PAd['status'];
  sellerId?: string;
  limit?: number;
  offset?: number;
  /** PM filter: true = PM-only ads; false = exclude PM ads; omit = all.
   *  Serialised as "true"/"false" in the query string (?pm=true / ?pm=false). */
  pm?: boolean;
}

// VoucherTypedData mirrors the EIP-712 payload returned by POST /p2p/ads.
// The seller's wallet signs it via signTypedData; the signature goes back
// via adsApi.sign(). No on-chain transaction at publish time.
export interface VoucherTypedData {
  types: Record<string, { name: string; type: string }[]>;
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

// Every pool op requires the active wallet address — the in-app HD wallet
// lets users hold multiple derived accounts, and the backend's gas
// estimation must run against the wallet actually doing the deposit, not
// the user's registered primary. On confirm endpoints the backend cross-
// checks the address against the on-chain event's `seller` so a caller
// can't credit themselves with another wallet's deposit.
//
// Every pool op also takes an optional `chainId`. Omitting it (or passing
// undefined) defaults to Sonic server-side — back-compat with the
// single-chain behavior. Pass the pool screen's selected chain to fund/
// withdraw a pool on a specific chain (Plan 7 Task 8).
export const poolApi = {
  /** Read (available, locked) pool balance for a wallet on a chain. */
  balance: (token: string, walletAddress: string, chainId?: number) =>
    request<PoolBalance>('GET', '/p2p/balance', undefined, {
      query: { token, address: walletAddress, ...(chainId ? { chainId } : {}) },
    }),

  /** Read the wallet's current ERC-20 allowance to the escrow contract.
   *  Used to skip the approve step when an existing allowance covers the
   *  intended deposit. */
  allowance: (token: string, walletAddress: string, chainId?: number) =>
    request<{ allowance: string }>('GET', '/p2p/allowance', undefined, {
      query: { token, address: walletAddress, ...(chainId ? { chainId } : {}) },
    }),

  /** Prepare the unsigned ERC-20 approve(escrow, amount) tx. Returns the
   *  current allowance alongside so the frontend can decide whether to
   *  broadcast the approve or skip straight to deposit. */
  prepareApprove: (token: string, amount: string, walletAddress: string, chainId?: number) =>
    request<{ unsignedTx: UnsignedTx; currentAllowance: string }>(
      'POST',
      '/p2p/deposits/approve',
      { token, amount, walletAddress, ...(chainId ? { chainId } : {}) },
    ),

  prepareDeposit: (token: string, amount: string, walletAddress: string, chainId?: number) =>
    request<{ unsignedTx: UnsignedTx }>('POST', '/p2p/deposits', {
      token,
      amount,
      walletAddress,
      ...(chainId ? { chainId } : {}),
    }),

  /** Confirm a deposit tx. The backend cross-checks that the on-chain event
   *  amount matches `amount` and seller matches `walletAddress` — submit the
   *  same values you prepared with. */
  confirmDeposit: (
    token: string,
    amount: string,
    txHash: string,
    walletAddress: string,
    chainId?: number,
  ) =>
    request<{ token: string; amount: string }>('POST', '/p2p/deposits/confirm', {
      token,
      amount,
      txHash,
      walletAddress,
      ...(chainId ? { chainId } : {}),
    }),

  prepareWithdraw: (token: string, amount: string, walletAddress: string, chainId?: number) =>
    request<{ unsignedTx: UnsignedTx }>('POST', '/p2p/withdrawals', {
      token,
      amount,
      walletAddress,
      ...(chainId ? { chainId } : {}),
    }),

  confirmWithdraw: (
    token: string,
    amount: string,
    txHash: string,
    walletAddress: string,
    chainId?: number,
  ) =>
    request<{ token: string; amount: string }>('POST', '/p2p/withdrawals/confirm', {
      token,
      amount,
      txHash,
      walletAddress,
      ...(chainId ? { chainId } : {}),
    }),
};

// ─── Trades ─────────────────────────────────────────────────────────────────

export interface OpenTradeInput {
  fiatAmount: string;
  paymentMethodId: string;
  /** Present when the user is the buyer (sell-ad path). */
  buyerAddress?: string;
  /** Present when the user is the seller responding to a buy-ad.
   *  The backend treats tokenAmount as authoritative and derives fiat from
   *  tokenAmount × rate. sellerAddress is used for on-chain escrow. */
  sellerAddress?: string;
  /** Base-unit crypto amount the user will sell (buy-ad path only). */
  tokenAmount?: string;
  /** Payout bank details — required when selling to a PM buy-ad so the
   *  backend knows where to send the Nomba bank transfer. */
  payoutAccountNumber?: string;
  payoutBank?: string;
  payoutAccountName?: string;
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

  raiseDispute: (
    tradeId: string,
    reason: string,
    callerAddress?: string,
    evidenceUrls?: string[],
  ) =>
    request<{ unsignedTx: UnsignedTx }>(
      'POST',
      `/p2p/trades/${encodeURIComponent(tradeId)}/dispute`,
      // evidenceUrls is omitted from the body when empty/absent so older
      // backends ignore it and the wire shape stays identical to before.
      {
        reason,
        callerAddress,
        ...(evidenceUrls && evidenceUrls.length > 0 ? { evidenceUrls } : {}),
      }
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

// PM manual-release queue item returned by GET /admin/pm/manual-queue.
// Trades land here when the PM automation can't auto-match an inbound payment
// (name or amount mismatch) and a human operator must verify before release.
export interface PMManualQueueItem {
  tradeId: string;
  adId: string;
  buyerId: string;
  buyerName: string;
  token: string;
  tokenAmount: string;
  fiatAmount: string;
  createdAt: string;
}

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

// PM (Payment Merchant) admin operations.
// These endpoints are gated with RequireP2PAdmin on the backend — generic
// admins that don't have the PM role get a 403. The queue lists trades that
// the PM automation couldn't auto-match (name/amount mismatch); a human
// operator verifies the bank transfer and then calls release or decline.
//
// decline is NOT an immediate refund — it marks the trade so the timeout
// watchdog will cancel it and return the escrowed funds on its next tick.

// ─── MerchantRequest types (corporate PM application) ───────────────────────

/**
 * A merchant-request row as returned by GET /admin/merchant-requests.
 * Only the fields relevant to the PM application screen are typed here;
 * the full model has address/city/state/country/phone which are omitted.
 */
export interface MerchantRequest {
  id: string;
  userId: string;
  businessType: string;
  businessName: string;
  businessDescription?: string;
  status: 'pending' | 'approved' | 'rejected';
  adminNote?: string;
  reviewedAt?: string;
  createdAt: string;
  updatedAt: string;
  // Corporate PM fields (p2p_merchant applications only)
  settlementBankCode?: string;
  settlementAccountNumber?: string;
  makerWalletAddress?: string;
  requestedPerTradeCapUsd?: number;
  requestedDailyCapUsd?: number;
  // Decorated by the backend on Preload("User")
  user?: { id: string; username?: string; email?: string };
}

/**
 * Paginated list response from GET /admin/merchant-requests.
 */
export interface MerchantRequestListResponse {
  requests: MerchantRequest[];
  total: number;
  page: number;
  limit: number;
}

/**
 * PaymentMerchant row as returned by the PM manual-queue endpoint context.
 * Used for the PM status view — note there is no standalone PM-list endpoint
 * on the backend; status data is surfaced from what the manual-queue + approve
 * flow provides. See task-8-report.md for the gap note.
 */
export interface PaymentMerchant {
  id: string;
  userId: string;
  name: string;
  status: 'pending' | 'active' | 'paused';
  nombaAccountId?: string;
  nombaWalletId?: string;
  inboundAccountNumber?: string;
  inboundBank?: string;
  operatorWalletAddress?: string;
  nombaSubAccountId?: string;
  settlementBankCode?: string;
  settlementAccountNumber?: string;
  settlementAccountName?: string;
  availableNgn: string;
  reservedNgn: string;
  provisioningStatus: 'pending' | 'provisioned' | 'active' | 'failed';
  perTradeCapUsd: number;
  dailyCapUsd: number;
  createdAt: string;
  updatedAt: string;
}

export const pmAdminApi = {
  manualQueue: () =>
    request<PMManualQueueItem[]>('GET', '/admin/pm/manual-queue'),

  release: (tradeId: string) =>
    request<void>('POST', `/admin/pm/trades/${encodeURIComponent(tradeId)}/release`, {}),

  decline: (tradeId: string) =>
    request<void>('POST', `/admin/pm/trades/${encodeURIComponent(tradeId)}/decline`, {}),

  payout: (tradeId: string) =>
    request<void>('POST', `/admin/pm/trades/${encodeURIComponent(tradeId)}/payout`, {}),

  /**
   * List pending p2p_merchant MerchantRequests.
   *
   * Hits: GET /admin/merchant-requests?status=pending
   * The backend does not filter by businessType server-side, so the result is
   * filtered to businessType === 'p2p_merchant' in this helper.
   */
  listApplications: () =>
    request<MerchantRequestListResponse>('GET', '/admin/merchant-requests', undefined, {
      query: { status: 'pending', limit: 100 },
    }).then((res) => {
      if (!res.success || !res.data) return res;
      return {
        ...res,
        data: {
          ...res.data,
          requests: res.data.requests.filter(
            (r) => r.businessType === 'p2p_merchant',
          ),
        },
      };
    }),

  /**
   * Approve a p2p_merchant MerchantRequest. Triggers backend provisioning of a
   * Nomba sub-account + VA. The backend may return 5xx on transient Nomba
   * failure — callers should surface the message and allow retry.
   *
   * Hits: POST /admin/merchant-requests/:id/approve
   */
  approveApplication: (requestId: string) =>
    request<MerchantRequest>(
      'POST',
      `/admin/merchant-requests/${encodeURIComponent(requestId)}/approve`,
      {},
    ),

  /**
   * List approved p2p_merchant MerchantRequests.
   * Used by the PM status screen to show activated PMs.
   *
   * Hits: GET /admin/merchant-requests?status=approved&limit=100
   * Filtered to businessType === 'p2p_merchant' client-side.
   */
  listApprovedApplications: () =>
    request<MerchantRequestListResponse>('GET', '/admin/merchant-requests', undefined, {
      query: { status: 'approved', limit: 100 },
    }).then((res) => {
      if (!res.success || !res.data) return res;
      return {
        ...res,
        data: {
          ...res.data,
          requests: res.data.requests.filter(
            (r) => r.businessType === 'p2p_merchant',
          ),
        },
      };
    }),

  /**
   * List all PaymentMerchant rows (admin only).
   *
   * Hits: GET /admin/pm
   * Returns the safe projection (no NombaCredsRef or secret fields).
   */
  listPMs: () =>
    request<PaymentMerchant[]>('GET', '/admin/pm'),

  /**
   * Withdraw NGN from a PM's available balance to its settlement bank.
   *
   * Hits: POST /admin/pm/:id/withdraw
   */
  withdrawPM: (pmId: string, amount: string) =>
    request<{ payoutId: string }>(
      'POST',
      `/admin/pm/${encodeURIComponent(pmId)}/withdraw`,
      { amount },
    ),
};

// ─── PM user-facing read ─────────────────────────────────────────────────────

export interface MyPmStatus {
  isPm: boolean;
  status: string;
}

/**
 * Read the current user's PM status from GET /p2p/pm/me.
 *
 * Returns { isPm, status } on success. On ANY error (network, 401, non-200)
 * returns { isPm: false, status: '' } — never throws to the UI. The route is
 * dormant-safe: if the PM/Nomba stack is unconfigured the backend returns
 * 200 { isPm: false } rather than 503, so this function always resolves.
 */
export async function getMyPmStatus(): Promise<MyPmStatus> {
  try {
    const res = await request<{ isPm: boolean; status: string }>('GET', '/p2p/pm/me');
    if (res.success && res.data != null) {
      return { isPm: !!res.data.isPm, status: res.data.status ?? '' };
    }
    return { isPm: false, status: '' };
  } catch {
    return { isPm: false, status: '' };
  }
}

// ─── Sellers ────────────────────────────────────────────────────────────────

export const sellersApi = {
  profile: (id: string) => request<MerchantProfile>('GET', `/p2p/sellers/${encodeURIComponent(id)}`),
};

// ─── Ratings ────────────────────────────────────────────────────────────────

export const ratingsApi = {
  rate: (tradeId: string, value: 1 | -1) =>
    request<P2PRating>('POST', `/p2p/trades/${encodeURIComponent(tradeId)}/rate`, { value }),
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
 * Format a fiat-side numeric string for display. The server stores rates at
 * up to 10 fractional digits ("1378.9900000000") for headroom; humans want
 * grouped thousands and at most two decimals with no trailing zeros
 * ("1,378.99"). Use this anywhere a `rate`, `minLimit`, or `maxLimit` is
 * rendered to the user; the raw string stays on the wire.
 *
 * `maxFractionDigits` defaults to 2 (NGN-like). Pass 0 for whole-fiat
 * limits where decimals just add noise.
 */
export function formatFiat(value: string | undefined, maxFractionDigits = 2): string {
  if (!value) return '0';
  const n = Number(value);
  if (!isFinite(n)) return value;
  return n.toLocaleString('en-US', {
    minimumFractionDigits: 0,
    maximumFractionDigits: maxFractionDigits,
  });
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

// ─── Sell-side pure helpers ──────────────────────────────────────────────────

/**
 * Return true iff the current ERC-20 allowance is enough to cover `needed`.
 * Both values are base-unit strings (no decimals). Uses BigInt so amounts
 * beyond Number precision (e.g. 18-decimal tokens at large face values) are
 * compared correctly.
 */
export function hasSufficientAllowance(allowance: string, needed: string): boolean {
  try {
    return BigInt(allowance || '0') >= BigInt(needed || '0');
  } catch {
    return false;
  }
}

/**
 * Preview the fiat the user will receive when selling `tokenAmount` base units
 * at `rate` fiat-per-token. `decimals` is the token's decimal precision (e.g.
 * 6 for USDT). Returns a human-readable string rounded to 2 decimal places.
 *
 * All arithmetic is done in floating-point — this is a display-only preview,
 * not a binding quote. The backend recomputes the authoritative fiat amount at
 * trade-open time from the same formula.
 */
export function previewFiatFromToken(
  tokenAmount: string,
  rate: string,
  decimals: number,
): string {
  const humanAmount = Number(tokenAmount || '0') / Math.pow(10, decimals);
  const rateNum = Number(rate || '0');
  if (!isFinite(humanAmount) || !isFinite(rateNum) || rateNum <= 0) return '0.00';
  return (humanAmount * rateNum).toFixed(2);
}

// ─── Shared FE helpers (extracted to deduplicate across P2P screens) ────────

/**
 * Token decimals lookup for the small whitelist of P2P-supported tokens.
 * Falls back to 18 (ERC-20 default) for anything unknown.
 *
 * @deprecated Use `p2pTokenDecimals(chainId, symbol)` for per-chain accuracy.
 *   This map is kept for the tokenSupported guard in ad/[adId].tsx (key-existence
 *   check) and for legacy code paths that have no chainId context.
 */
export const TOKEN_DECIMALS: Record<string, number> = {
  USDT: 6,
  USDC: 6,
  MCGP: 18,
};

/** On-chain decimals for a token on a chain. BSC (56/97) USDT/USDC are 18;
 *  Sonic (146/14601) USDT/USDC are 6; MCGP is 18. */
export function p2pTokenDecimals(chainId: number | undefined, symbol: string): number {
  const sym = (symbol || '').toUpperCase();
  if (chainId === 56 || chainId === 97) {
    if (sym === 'USDT' || sym === 'USDC') return 18;
  }
  if (sym === 'USDT' || sym === 'USDC') return 6;
  return 18; // MCGP + default
}

/** ChainKey for a P2P entity's chainId; falls back to 'sonic'. */
export function p2pChainKey(chainId: number | undefined): ChainKey {
  return (chainId && getChainByChainId(chainId)?.key) || 'sonic';
}

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

/**
 * Return true iff `tokenBaseUnits` exceeds the ad's remaining capacity.
 * Both values are base-unit strings (no decimals). Uses BigInt so amounts
 * beyond Number precision are compared correctly.
 *
 * "Exceeds" means strictly greater than — equal-to-remaining is still valid
 * (the buyer takes the last of what the merchant has available).
 */
export function exceedsCapacity(tokenBaseUnits: string, remaining: string): boolean {
  try {
    return BigInt(tokenBaseUnits || '0') > BigInt(remaining || '0');
  } catch {
    // If either value is not a valid integer string, fail safe by blocking.
    return true;
  }
}

/**
 * Return true iff `fiat` is within the ad's optional min/max fiat limits.
 * `fiat`, `min`, and `max` are decimal strings (human-readable, e.g. "500").
 * Missing min/max are treated as unbounded on that side.
 *
 * Uses numeric (float) comparison — the limits are fiat whole-unit values so
 * floating-point is sufficient and consistent with the display layer.
 */
export function withinFiatLimits(fiat: string, min?: string, max?: string): boolean {
  const fiatNum = parseFloat(fiat || '0');
  if (!isFinite(fiatNum)) return false;
  if (min !== undefined && min !== '') {
    const minNum = parseFloat(min);
    if (isFinite(minNum) && fiatNum < minNum) return false;
  }
  if (max !== undefined && max !== '') {
    const maxNum = parseFloat(max);
    if (isFinite(maxNum) && fiatNum > maxNum) return false;
  }
  return true;
}

/**
 * Distinct, order-stable list of payment providers present across the given
 * ads' privacy-safe summaries. Used to populate the "All Payment Methods"
 * filter from already-loaded ads (no extra request).
 */
export function distinctAdProviders(
  ads: P2PAd[],
): { provider: string; label: string }[] {
  const seen = new Set<string>();
  const out: { provider: string; label: string }[] = [];
  for (const ad of ads) {
    for (const s of ad.paymentMethodSummary ?? []) {
      if (!s || seen.has(s.provider)) continue;
      seen.add(s.provider);
      out.push({ provider: s.provider, label: s.label });
    }
  }
  return out;
}
