// The TSA Pay owner routes (tsa-api-go internal/handlers/pay_owner_handler.go).
// Field names match the Go json tags exactly.
import { api } from '@/lib/api';
import type { Mode } from '@/components/shell/mode';

export type { Mode };

export type OwnMerchant = {
  id: string;
  /** The name customers see and payout accounts are matched to. */
  businessName: string;
  /** A rename the owner asked for, waiting for TSA Connect's approval; businessName stays in use until then. */
  pendingBusinessName?: string;
  /** When that rename was asked for. */
  pendingSince?: string;
  /** The owner may ask for a rename now (the feature is on). Absent (an older API) reads as off. */
  businessNameChangeEnabled?: boolean;
  status: 'active' | 'suspended';
  /** A super admin has switched this merchant's live payments on. Until then a live key is refused. */
  liveEnabled: boolean;
  /** Live payments work right now: this merchant's switch and TSA Pay's own. Older APIs don't send it. */
  liveAllowed?: boolean;
  version: number;
  websiteDomain?: string;
  settlementMode: 'fiat' | 'crypto' | '';
  bankCode?: string;
  accountNumber?: string;
  accountName?: string;
  chainId?: number;
  tokenAddress?: string;
  tokenSymbol?: string;
  walletAddress?: string;
  hasLiveKey: boolean;
  hasTestKey: boolean;
  liveKeyPrefix?: string;
  testKeyPrefix?: string;
  webhookUrl: string;
  testWebhookUrl: string;
  returnDomains: string[];
  /** The only addresses the live key is accepted from; empty = anywhere. An API that predates it leaves it out. */
  allowedIps?: string[];
};

export type PaymentStatus = 'pending' | 'processing' | 'succeeded' | 'failed' | 'expired';

/**
 * Who made the payment: the merchant's server (api), the owner as a one-time
 * payment link (link), or a customer through a reusable link (open).
 */
export type PaymentSource = 'api' | 'link' | 'open';

export type Payment = {
  id: string;
  reference: string;
  mode: Mode;
  status: PaymentStatus;
  amount: number;
  customerTotal: number;
  merchantNet: number;
  fee: number;
  currency: 'NGN';
  /** What it was for. On a reusable-link payment (source open) it is the customer's own note, often blank. */
  description: string;
  source: PaymentSource;
  /** Who a payment link is for, when the owner named them. */
  customerName?: string;
  /** Expired because the owner cancelled it, not because time ran out. */
  cancelled?: boolean;
  /** The page a customer pays on — what a payment link shares. */
  checkoutUrl: string;
  /** The reusable link a customer paid through (source open), and its label. */
  openLinkId?: string;
  linkLabel?: string;
  expiresAt: string;
  settlementRef?: string;
  failureReason?: string;
  succeededAt?: string;
  createdAt: string;
};

/** One webhook attempt series for a payment — how it went, never the payload. */
export type Delivery = {
  id: string;
  event: string;
  attempts: number;
  lastStatusCode: number;
  lastError?: string;
  nextAttemptAt?: string;
  deliveredAt?: string;
  failedAt?: string;
  createdAt: string;
};

export type Page<T> = { items: T[]; total: number; page: number; limit: number };
export type PaymentPage = Page<Payment>;
export type PaymentFilter = { status?: string; q?: string; page?: number; limit?: number };

// Reads take TanStack Query's signal, so a query it drops is cancelled on the wire.
export const getMerchant = (signal?: AbortSignal) => api<OwnMerchant>('/pay/merchant', { signal });

const pageQuery = (mode: Mode, p: PaymentFilter) => new URLSearchParams({
  mode,
  ...(p.status ? { status: p.status } : {}),
  ...(p.q ? { q: p.q } : {}),
  page: String(p.page ?? 1),
  ...(p.limit ? { limit: String(p.limit) } : {}),
});

export const listPayments = (mode: Mode, p: PaymentFilter, signal?: AbortSignal) =>
  api<PaymentPage>(`/pay/merchant/payments?${pageQuery(mode, p)}`, { signal });

/** The payment links the owner made; `status: 'cancelled'` narrows to the ones they cancelled. */
export const listLinks = (mode: Mode, p: PaymentFilter, signal?: AbortSignal) =>
  api<PaymentPage>(`/pay/merchant/links?${pageQuery(mode, p)}`, { signal });

export type LinkDays = 1 | 3 | 7;

/** A payment link: `amount` in kobo; a blank reference is made by the server. */
export type NewLink = { amount: number; description: string; customerName: string; reference: string; expiresInDays: LinkDays };

/** One Idempotency-Key per open form, so a double submit or a retry makes one link. */
export const createLink = (mode: Mode, link: NewLink, idempotencyKey: string) =>
  api<Payment>(`/pay/merchant/links?mode=${mode}`, { method: 'POST', body: link, headers: { 'Idempotency-Key': idempotencyKey } });

export const cancelLink = (mode: Mode, id: string) =>
  api<Payment>(`/pay/merchant/links/${encodeURIComponent(id)}/cancel?mode=${mode}`, { method: 'POST' });

/** Who paid: a name (as on their ID when verified; "Deleted account" once deleted). Never contact details. */
export type Payer = { name: string };

/** One payment as its detail page reads it. */
export type PaymentDetail = { payment: Payment; payer: Payer | null; deliveries: Delivery[] };

export const getPayment = (mode: Mode, id: string, signal?: AbortSignal) =>
  api<PaymentDetail>(`/pay/merchant/payments/${encodeURIComponent(id)}?mode=${mode}`, { signal });

// Guarded actions: password → emailed code → confirm.
export type Pending = { id: string; kind: 'settlement' | 'keys' | 'endpoints' | 'webhook_secret' | 'business_name'; expiresAt: string; sentTo: string };
/** A guarded request's answer: a code to confirm, or — when nothing needs confirming — the merchant as it is. */
export type GuardedAnswer = { confirmation?: Pending; merchant?: OwnMerchant };

export type ActionResult = {
  merchant?: OwnMerchant;
  mode?: Mode;
  key?: string;
  secretAction?: 'reveal' | 'roll';
  webhookSecretLive?: string;
  webhookSecretTest?: string;
};

export const requestKeyRoll = (mode: Mode, password: string) =>
  api<{ confirmation: Pending }>('/pay/merchant/keys', { method: 'POST', body: { mode, password } });

/** PUT /endpoints replaces all three settings, so every call sends all three. */
export const requestEndpoints = (b: { webhookUrl: string; testWebhookUrl: string; returnDomains: string[]; allowedIps: string[]; expectedVersion: number; password: string }) =>
  api<{ confirmation: Pending }>('/pay/merchant/endpoints', { method: 'PUT', body: b });

export const requestWebhookSecret = (action: 'reveal' | 'roll', password: string) =>
  api<{ confirmation: Pending }>('/pay/merchant/webhook-secret', { method: 'POST', body: { action, password } });

/**
 * Asks for the business to be renamed: the emailed code files the request, and the name
 * changes only when TSA Connect approves it. Asking for the current name needs no code:
 * it answers the merchant (withdrawing any rename still waiting).
 */
export const requestBusinessName = (name: string, password: string, expectedVersion: number) =>
  api<GuardedAnswer>('/pay/merchant/business-name', { method: 'PUT', body: { name, password, expectedVersion } });

export const confirmAction = (id: string, code: string) =>
  api<ActionResult>(`/pay/merchant/actions/${encodeURIComponent(id)}/confirm`, { method: 'POST', body: { code } });

// Payouts.
export type SettlementChange =
  | { settlementMode: 'fiat'; bankCode: string; accountNumber: string }
  | { settlementMode: 'crypto'; chainId: number; tokenAddress: string; walletAddress: string };

/** A bank account or a crypto wallet waits for the emailed code (confirmation); saving where you're already paid changes nothing (merchant). */
export const updateSettlement = (change: SettlementChange, password: string, expectedVersion: number) =>
  api<GuardedAnswer>('/pay/merchant/settlement', {
    method: 'PUT',
    body: { ...change, password, expectedVersion },
  });

export type Bank = { code: string; name: string };
export type SettlementOption = { chainId: number; chain: string; symbol: string; tokenAddress: string };

export const listBanks = (signal?: AbortSignal) => api<Bank[]>('/pay/merchant/banks', { signal });

export const resolveAccount = (bankCode: string, accountNumber: string, signal?: AbortSignal) =>
  api<{ accountNumber: string; accountName: string }>(`/pay/merchant/banks/resolve?${new URLSearchParams({ bankCode, accountNumber })}`, { signal });

export const listSettlementOptions = (signal?: AbortSignal) => api<SettlementOption[]>('/pay/merchant/settlement-options', { signal });

/** range_outside_limits: the business's limits moved, so no amount fits the link's own range. */
export type NotTakingReason = 'paused' | 'suspended' | 'live_off' | 'no_payout' | 'range_outside_limits';

// Reusable ("open") links: one address many customers pay, each entering the
// amount — a shop's QR on the counter, say. Many per mode; each shows its own takings.
export type OpenLink = {
  id: string;
  mode: Mode;
  slug: string;
  /** The page a customer opens: {checkout}/l/{slug}. */
  url: string;
  /** What the payer sees ("Mama Ngozi Store — Shop 12"). */
  label: string;
  /** Kobo, as set; 0 means the business's own limit (merchantMin/MaxAmount). */
  minAmount: number;
  maxAmount: number;
  merchantMinAmount: number;
  merchantMaxAmount: number;
  /** The owner's own switch; Paused: a customer who opens it is told it isn't taking payments. */
  active: boolean;
  /** Whether a customer can pay it right now, and if not, why. Older APIs send only `active`. */
  takingPayments?: boolean;
  notTakingReason?: NotTakingReason;
  version: number;
  /** Succeeded payments made through it; their price (paidTotal) and what the business received (receivedTotal), in kobo. */
  paidCount: number;
  paidTotal: number;
  receivedTotal: number;
  createdAt: string;
};

export type NewOpenLink = { label: string; minAmount: number; maxAmount: number };

/** PUT /open-links/:id replaces every setting, so each change sends them all. */
export type OpenLinkChange = NewOpenLink & { active: boolean; expectedVersion: number };

/** The settings a change starts from: what the link has now. */
export const openLinkChange = (l: OpenLink): OpenLinkChange => ({
  label: l.label, minAmount: l.minAmount, maxAmount: l.maxAmount, active: l.active, expectedVersion: l.version,
});

/** Like a payment link, one Idempotency-Key per request body. */
export const createOpenLink = (mode: Mode, link: NewOpenLink, idempotencyKey: string) =>
  api<OpenLink>(`/pay/merchant/open-links?mode=${mode}`, { method: 'POST', body: link, headers: { 'Idempotency-Key': idempotencyKey } });

export const listOpenLinks = (mode: Mode, p: PaymentFilter, signal?: AbortSignal) =>
  api<Page<OpenLink>>(`/pay/merchant/open-links?${pageQuery(mode, p)}`, { signal });

/** One reusable link and the latest payments made through it. */
export type OpenLinkDetail = { openLink: OpenLink; payments: Payment[] };

export const getOpenLink = (mode: Mode, id: string, signal?: AbortSignal) =>
  api<OpenLinkDetail>(`/pay/merchant/open-links/${encodeURIComponent(id)}?mode=${mode}`, { signal });

export const updateOpenLink = (mode: Mode, id: string, change: OpenLinkChange) =>
  api<OpenLink>(`/pay/merchant/open-links/${encodeURIComponent(id)}?mode=${mode}`, { method: 'PUT', body: change });

/** A new address for the same link (same id); the old one, and every poster of it, stops taking payments. */
export const replaceOpenLink = (mode: Mode, id: string) =>
  api<OpenLink>(`/pay/merchant/open-links/${encodeURIComponent(id)}/replace?mode=${mode}`, { method: 'POST' });
