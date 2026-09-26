// The TSA Pay owner routes (tsa-api-go internal/handlers/pay_owner_handler.go).
// Field names match the Go json tags exactly.
import { api } from '@/lib/api';
import type { Mode } from '@/components/shell/mode';

export type { Mode };

export type OwnMerchant = {
  id: string;
  businessName: string;
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
};

export type PaymentStatus = 'pending' | 'processing' | 'succeeded' | 'failed' | 'expired';

/** Who made the payment: the merchant's server (api) or the owner, as a payment link (link). */
export type PaymentSource = 'api' | 'link';

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
  description: string;
  source: PaymentSource;
  /** Who a payment link is for, when the owner named them. */
  customerName?: string;
  /** Expired because the owner cancelled it, not because time ran out. */
  cancelled?: boolean;
  /** The page a customer pays on — what a payment link shares. */
  checkoutUrl: string;
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

export type PaymentPage = { items: Payment[]; total: number; page: number; limit: number };
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
export type Pending = { id: string; kind: 'settlement' | 'keys' | 'endpoints' | 'webhook_secret'; expiresAt: string; sentTo: string };
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
export const requestEndpoints = (b: { webhookUrl: string; testWebhookUrl: string; returnDomains: string[]; expectedVersion: number; password: string }) =>
  api<{ confirmation: Pending }>('/pay/merchant/endpoints', { method: 'PUT', body: b });

export const requestWebhookSecret = (action: 'reveal' | 'roll', password: string) =>
  api<{ confirmation: Pending }>('/pay/merchant/webhook-secret', { method: 'POST', body: { action, password } });

export const confirmAction = (id: string, code: string) =>
  api<ActionResult>(`/pay/merchant/actions/${encodeURIComponent(id)}/confirm`, { method: 'POST', body: { code } });

// Payouts.
export type SettlementChange =
  | { settlementMode: 'fiat'; bankCode: string; accountNumber: string }
  | { settlementMode: 'crypto'; chainId: number; tokenAddress: string; walletAddress: string };

/** A bank account or a crypto wallet waits for the emailed code (confirmation); saving where you're already paid changes nothing (merchant). */
export const updateSettlement = (change: SettlementChange, password: string, expectedVersion: number) =>
  api<{ merchant?: OwnMerchant; confirmation?: Pending }>('/pay/merchant/settlement', {
    method: 'PUT',
    body: { ...change, password, expectedVersion },
  });

export type Bank = { code: string; name: string };
export type SettlementOption = { chainId: number; chain: string; symbol: string; tokenAddress: string };

export const listBanks = (signal?: AbortSignal) => api<Bank[]>('/pay/merchant/banks', { signal });

export const resolveAccount = (bankCode: string, accountNumber: string, signal?: AbortSignal) =>
  api<{ accountNumber: string; accountName: string }>(`/pay/merchant/banks/resolve?${new URLSearchParams({ bankCode, accountNumber })}`, { signal });

export const listSettlementOptions = (signal?: AbortSignal) => api<SettlementOption[]>('/pay/merchant/settlement-options', { signal });
