// The TSA Pay owner routes (tsa-api-go internal/handlers/pay_owner_handler.go).
// Field names match the Go json tags exactly.
import { api } from '@/lib/api';
import type { Mode } from '@/components/shell/mode';

export type { Mode };

export type OwnMerchant = {
  id: string;
  businessName: string;
  status: 'active' | 'suspended';
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

export const listPayments = (mode: Mode, p: PaymentFilter, signal?: AbortSignal) =>
  api<PaymentPage>(`/pay/merchant/payments?${new URLSearchParams({
    mode,
    ...(p.status ? { status: p.status } : {}),
    ...(p.q ? { q: p.q } : {}),
    page: String(p.page ?? 1),
    ...(p.limit ? { limit: String(p.limit) } : {}),
  })}`, { signal });

/** Who paid: a name, and whether it is the name on a verified ID. Never contact details. */
export type Payer = { name: string; verified: boolean };

export const getPayment = (mode: Mode, id: string, signal?: AbortSignal) =>
  api<{ payment: Payment; payer: Payer | null; deliveries: Delivery[] }>(`/pay/merchant/payments/${encodeURIComponent(id)}?mode=${mode}`, { signal });

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

/** A bank account applies at once (merchant); a crypto wallet waits for the emailed code (confirmation). */
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
