// services/payMerchantApi.ts
//
// A TSA Pay merchant's own account, from the app: where they are paid
// (GET /api/pay/merchant), changing it (PUT /api/pay/merchant/settlement),
// confirming the change with the code emailed to them
// (POST /api/pay/merchant/actions/:id/confirm), and the payment links she sends
// customers (/api/pay/merchant/links). The payer's side of TSA Pay is
// services/payApi.ts; the merchant's server API (/api/pay/v1, key-authenticated)
// is never called from the app.
import { createApiRequest } from '@/services/apiRequest';
import { PayError } from '@/services/payApi';

export type SettlementMode = 'fiat' | 'crypto';

/** The owner's view: the account number arrives masked (last four digits). */
export type OwnMerchant = {
  id: string;
  businessName: string;
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

/** A guarded change (every payout change) waits for the code emailed to the owner. */
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
  /** 'link' for one she made here; 'api' for one her own server made. */
  source: 'api' | 'link';
  amount: number;
  customerTotal: number;
  merchantNet: number;
  fee: number;
  currency: 'NGN';
  description: string;
  customerName?: string;
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
