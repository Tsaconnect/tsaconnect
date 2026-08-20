// services/billsApi.ts
//
// Client for utility bill payments (/api/bills/*): airtime, data, electricity,
// cable TV, betting and exam e-PINs, vended via ClubKonnect. Payment is the
// user's SPENDABLE MCGP via the operator debit path: the backend debits
// on-chain with the operator key, so paying is a single authenticated API call
// — no signing, no gas. Amounts to/from the backend are WHOLE NAIRA integers;
// mcgpAmount is 18dp base units and rateKobo is kobo per 1 MCGP.

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

export type BillCategory = 'airtime' | 'data' | 'electricity' | 'cabletv' | 'betting' | 'epin';

export interface BillProvider {
  code: string;
  name: string;
  minAmount?: number; // whole ₦ bounds for user-priced categories
  maxAmount?: number;
}

export interface BillPlan {
  code: string;
  name: string;
  amount: number; // whole ₦ the USER pays (data plans already include markup)
}

export interface BillOrder {
  id: string;
  category: BillCategory;
  provider: string;
  providerName?: string;
  customerId?: string;
  customerName?: string;
  phoneNumber?: string;
  planRef?: string;
  planName?: string;
  meterType?: string;
  ngnAmount: number; // whole ₦
  mcgpAmount: string; // 18dp base units
  rateKobo: string; // kobo per MCGP at quote time
  vendorOrderId?: string;
  deliveryPayload?: string; // prepaid meter token / exam PIN
  failureReason?: string;
  status:
    | 'quoted'
    | 'paying'
    | 'paid'
    | 'vending'
    | 'delivered'
    | 'vend_failed'
    | 'refunded'
    | 'expired'
    | 'failed';
  quoteExpiresAt: string;
  createdAt: string;
}

export interface BillQuote {
  order: BillOrder;
  mcgpAmount: string;
  rateKobo: string;
  ngnAmount: number;
  expiresAt: string;
}

export interface BillQuoteInput {
  category: BillCategory;
  provider: string;
  customerId?: string;
  customerName?: string;
  phoneNumber?: string;
  planRef?: string;
  meterType?: string; // 'prepaid' | 'postpaid' (electricity)
  ngnAmount?: number; // user-priced categories; plan-priced are server-priced
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

// Operator catalogs per category (telcos, discos, cable operators, betting
// companies, waec/jamb).
export const getBillProviders = (category: BillCategory) =>
  req<BillProvider[]>('GET', `/bills/catalog/${category}`);

// Fixed-price products for one operator (data bundles, cable packages, exam PINs).
export const getBillPlans = (category: BillCategory, provider: string) =>
  req<BillPlan[]>('GET', `/bills/catalog/${category}/${encodeURIComponent(provider)}/plans`);

// Validate/resolve the customer before quoting. For electricity/cable,
// data.customerName is the resolved account holder.
export const validateCustomer = (input: { category: BillCategory; provider: string; customerId: string }) =>
  req<{ customerName: string }>('POST', '/bills/validate', input);

// Price a bill in MCGP and stage a quoted order.
export const quoteBill = (input: BillQuoteInput) => req<BillQuote>('POST', '/bills/quote', input);

// Pay a quoted order with spendable MCGP (operator-signed server-side) and vend.
export const payBillOrder = (orderId: string) =>
  req<BillOrder>('POST', `/bills/orders/${orderId}/pay`);

export const getBillOrder = (id: string) => req<BillOrder>('GET', `/bills/orders/${id}`);

export const listBillOrders = () => req<BillOrder[]>('GET', '/bills/orders');

export interface BillReport {
  id: string;
  orderId: string;
  userId: string;
  reason: string;
  status: 'open' | 'resolved' | 'rejected';
  resolution?: string;
  resolvedAt?: string;
  createdAt: string;
}

// File a complaint against one of your own paid orders. One report per order —
// a second attempt returns a 409 with a clear message.
export const reportBillOrder = (orderId: string, reason: string) =>
  req<BillReport>('POST', `/bills/orders/${orderId}/report`, { reason });

// The caller's report on this order; 404s (success:false) when none exists.
export const getBillOrderReport = (orderId: string) =>
  req<BillReport>('GET', `/bills/orders/${orderId}/report`);
