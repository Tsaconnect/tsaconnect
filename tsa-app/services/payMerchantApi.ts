// services/payMerchantApi.ts
//
// A TSA Pay merchant's own account, from the app: where they are paid
// (GET /api/pay/merchant), changing it (PUT /api/pay/merchant/settlement) or
// the business name (PUT /api/pay/merchant/business-name), confirming either
// with the code emailed to them
// (POST /api/pay/merchant/actions/:id/confirm), the payment links she sends
// customers — one-time (/api/pay/merchant/links) and reusable, where the
// customer enters the amount (/api/pay/merchant/open-links). The payer's side of TSA Pay is
// services/payApi.ts; the merchant's server API (/api/pay/v1, key-authenticated)
// is never called from the app.
import { createApiRequest } from '@/services/apiRequest';
import { PayError } from '@/services/payApi';

export type SettlementMode = 'fiat' | 'crypto';

/** The owner's view: the account number arrives masked (last four digits). */
export type OwnMerchant = {
  id: string;
  /** The name customers see and payouts are matched against — until an admin approves a rename. */
  businessName: string;
  /** A rename she asked for, waiting for an admin since pendingSince; businessName stays in use until then. */
  pendingBusinessName?: string;
  pendingSince?: string;
  status: 'active' | 'suspended';
  /** A super admin has switched her live (her own switch only). */
  liveEnabled: boolean;
  /** She can take live payments now: TSA Pay live is on AND she is switched live. */
  liveAllowed: boolean;
  /** What a change from this screen is made against: a stale screen is refused. */
  version: number;
  settlementMode: SettlementMode | '';
  bankCode?: string;
  accountNumber?: string;
  accountName?: string;
  chainId?: number;
  tokenAddress?: string;
  tokenSymbol?: string;
  walletAddress?: string;
};

/** A crypto change names no symbol: the server takes the listed stablecoin's. */
export type SettlementChange =
  | { settlementMode: 'fiat'; bankCode: string; accountNumber: string }
  | { settlementMode: 'crypto'; chainId: number; tokenAddress: string; walletAddress: string };

/** A guarded change (every payout change, a business rename) waits for the code emailed to the owner. */
export type PendingConfirmation = { id: string; kind: string; expiresAt: string; sentTo: string };

/**
 * A change waiting on the emailed code (confirmation), or the merchant as it now
 * is (merchant): after a confirmed code, or when she saved where she is already paid.
 */
export type SettlementAnswer = { merchant?: OwnMerchant; confirmation?: PendingConfirmation };

/** Machine codes the owner routes answer with — keyed on, never the words beside them. */
export const OWNER_CODES = {
  NO_MERCHANT: 'no_merchant',
  NAME_MISMATCH: 'name_mismatch',
  PASSWORD_INCORRECT: 'password_incorrect',
  ACCOUNT_LOCKED: 'account_locked',
  STALE: 'merchant_stale',
  NO_PENDING: 'no_pending_change',
  CODE_INVALID: 'code_invalid',
  SUSPENDED: 'merchant_suspended',
  LIVE_NOT_ENABLED: 'live_not_enabled',
  SETTLEMENT_NOT_SET: 'settlement_not_set',
  REFERENCE_TAKEN: 'reference_taken',
  NOT_CANCELLABLE: 'not_cancellable',
  LINK_NOT_FOUND: 'link_not_found',
} as const;

const request = createApiRequest({
  prefix: '/pay/merchant',
  unavailableMessage: 'Payment settings are not available right now. Please try again later.',
  networkMessage: 'Could not reach the server. Check your connection and try again.',
  makeError: (message, status, code) => new PayError(message, status, code),
});

/** Her merchant, or a PayError coded no_merchant when she has none. */
export function getOwnMerchant(): Promise<OwnMerchant> {
  return request<OwnMerchant>('');
}

/** Change where she is paid. The password is checked again on the server. */
export function updateSettlement(change: SettlementChange, password: string, expectedVersion: number): Promise<SettlementAnswer> {
  return request<SettlementAnswer>('/settlement', {
    method: 'PUT',
    body: JSON.stringify({ ...change, password, expectedVersion }),
  });
}

/**
 * Ask for her business to be renamed. Guarded like a payout change (password,
 * then the emailed code files the request); the new name takes effect only when
 * an admin approves it. Asking for the current name files nothing and withdraws
 * a request still waiting — answered with the merchant, no confirmation.
 */
export function updateBusinessName(name: string, password: string, expectedVersion: number): Promise<SettlementAnswer> {
  return request<SettlementAnswer>('/business-name', {
    method: 'PUT',
    body: JSON.stringify({ name: name.trim(), password, expectedVersion }),
  });
}

/** Apply a waiting guarded change with the code from her email. */
export function confirmOwnerAction(actionId: string, code: string): Promise<SettlementAnswer> {
  return request<SettlementAnswer>(`/actions/${encodeURIComponent(actionId)}/confirm`, {
    method: 'POST',
    body: JSON.stringify({ code: code.trim() }),
  });
}

export type PayLinkMode = 'live' | 'test';

/** Her links are live once she can take live payments; until then every link is a test one. */
export const payLinkModeOf = (m: Pick<OwnMerchant, 'liveAllowed'>): PayLinkMode => (m.liveAllowed ? 'live' : 'test');

/** A mode named in a route (`?mode=`), or undefined when it names none. */
export const payLinkModeParam = (v: unknown): PayLinkMode | undefined => (v === 'live' || v === 'test' ? v : undefined);

/** One payment as its merchant sees it (the server's PayPaymentView). Money is integer kobo. */
export type PayLink = {
  id: string;
  reference: string;
  mode: PayLinkMode;
  status: 'pending' | 'processing' | 'succeeded' | 'failed' | 'expired';
  /** 'link' for a one-time link she made; 'open' for a payment through a reusable link; 'api' for one her own server made. */
  source: 'api' | 'link' | 'open';
  amount: number;
  customerTotal: number;
  merchantNet: number;
  fee: number;
  currency: 'NGN';
  /** A one-time link's "what it's for"; for an open payment, only the customer's own note (often empty). */
  description: string;
  customerName?: string;
  /** An open payment: the reusable link it was paid through. */
  linkLabel?: string;
  /** She cancelled it: the server keeps it as expired, with this set. */
  cancelled?: boolean;
  expiresAt: string;
  succeededAt?: string;
  /** What the customer opens — the link she shares. */
  checkoutUrl: string;
  createdAt: string;
};

export type CreatePayLinkInput = {
  /** Integer kobo. */
  amount: number;
  /** What it's for — the customer sees it. */
  description: string;
  customerName?: string;
  /** Blank and the server makes one. */
  reference?: string;
  expiresInDays: 1 | 3 | 7;
};

export type PayLinkPage = { items: PayLink[]; total: number; page: number; limit: number };

/**
 * Make a link. `idempotencyKey` is held by the form across retries of the same
 * request, so a timed-out create that did land is answered with that link, not a second one.
 */
export function createPayLink(mode: PayLinkMode, input: CreatePayLinkInput, idempotencyKey: string): Promise<PayLink> {
  return request<PayLink>(`/links?mode=${mode}`, {
    method: 'POST',
    body: JSON.stringify(input),
    headers: { 'Idempotency-Key': idempotencyKey },
  });
}

/** Her links in one mode, newest first. */
export function listPayLinks(mode: PayLinkMode, page = 1, limit = 50): Promise<PayLinkPage> {
  return request<PayLinkPage>(`/links?mode=${mode}&page=${page}&limit=${limit}`);
}

/** One of her payments (a link is one), from the owner's payment detail. */
export async function getPayLink(mode: PayLinkMode, id: string): Promise<PayLink> {
  const data = await request<{ payment: PayLink }>(`/payments/${encodeURIComponent(id)}?mode=${mode}`);
  return data.payment;
}

/** Stop a link that nobody has started paying. Refused (not_cancellable) once someone has. */
export function cancelPayLink(mode: PayLinkMode, id: string): Promise<PayLink> {
  return request<PayLink>(`/links/${encodeURIComponent(id)}/cancel?mode=${mode}`, { method: 'POST' });
}

/**
 * A reusable ("open") payment link: the customer enters the amount, and any
 * number of customers can pay through it — a shop's counter QR. Money is
 * integer kobo; a min or max of 0 is the merchant's own bound.
 */
export type OpenLink = {
  id: string;
  mode: PayLinkMode;
  slug: string;
  /** What the QR encodes and she shares. */
  url: string;
  /** What the payer sees, e.g. "Mama Ngozi Store — Shop 12". */
  label: string;
  minAmount: number;
  maxAmount: number;
  /** The merchant's own bounds: what a 0 min or max above stands for. */
  merchantMinAmount: number;
  merchantMaxAmount: number;
  /** Her own switch: false is paused. */
  active: boolean;
  /** Whether a customer can pay through it right now — paused is only one reason it can't. */
  takingPayments: boolean;
  notTakingReason?: OpenLinkNotTakingReason;
  /** What a change from the detail screen is made against: a stale screen is refused. */
  version: number;
  createdAt: string;
  /** Payments that succeeded through it; their prices summed (paidTotal) and what she received (receivedTotal). */
  paidCount: number;
  paidTotal: number;
  receivedTotal: number;
};

/** range_outside_limits: her business's limits moved, so no amount fits the link's range — she must edit it. */
export type OpenLinkNotTakingReason = 'paused' | 'suspended' | 'live_off' | 'no_payout' | 'range_outside_limits';

export type OpenLinkRange = { minAmount: number; maxAmount: number };
export type CreateOpenLinkInput = OpenLinkRange & { label: string };
export type OpenLinkChange = CreateOpenLinkInput & { active: boolean };
export type OpenLinkPage = { items: OpenLink[]; total: number; page: number; limit: number };

/** Make a reusable link; `idempotencyKey` as for createPayLink. */
export function createOpenLink(mode: PayLinkMode, input: CreateOpenLinkInput, idempotencyKey: string): Promise<OpenLink> {
  return request<OpenLink>(`/open-links?mode=${mode}`, {
    method: 'POST',
    body: JSON.stringify(input),
    headers: { 'Idempotency-Key': idempotencyKey },
  });
}

/** Her reusable links in one mode, newest first, each with what has been paid through it. */
export function listOpenLinks(mode: PayLinkMode, page = 1, limit = 50): Promise<OpenLinkPage> {
  return request<OpenLinkPage>(`/open-links?mode=${mode}&page=${page}&limit=${limit}`);
}

/** One reusable link and the latest payments made through it. */
export function getOpenLink(mode: PayLinkMode, id: string): Promise<{ openLink: OpenLink; payments: PayLink[] }> {
  return request<{ openLink: OpenLink; payments: PayLink[] }>(`/open-links/${encodeURIComponent(id)}?mode=${mode}`);
}

/** Change its label, range, or pause/resume it. */
export function updateOpenLink(mode: PayLinkMode, id: string, change: OpenLinkChange, expectedVersion: number): Promise<OpenLink> {
  return request<OpenLink>(`/open-links/${encodeURIComponent(id)}?mode=${mode}`, {
    method: 'PUT',
    body: JSON.stringify({ ...change, expectedVersion }),
  });
}

/** A new address and QR for the same link (same id); the old address stops working, and posters printed with it. */
export function replaceOpenLink(mode: PayLinkMode, id: string): Promise<OpenLink> {
  return request<OpenLink>(`/open-links/${encodeURIComponent(id)}/replace?mode=${mode}`, { method: 'POST' });
}
