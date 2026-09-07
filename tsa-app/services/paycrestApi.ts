// services/paycrestApi.ts
//
// Client for the Paycrest ramp (/api/paycrest/*) — buy and sell stablecoins
// for Naira through a network of liquidity providers.
//
// Two things make this different from itApi:
//
//   1. It is non-custodial on both legs. A buy delivers to the user's own
//      connected wallet; a sell is signed and sent FROM that wallet. TSA never
//      holds the asset, so there is no balance to read here.
//   2. Liquidity is not a fixed property of a pair. Providers join and leave
//      the book, and price by order size — measured swings of 4x overnight.
//      So never cache a quote, and never render a network picker from a
//      hardcoded list: ask for a quote at the amount the user actually typed
//      and show only what comes back.

import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';
import { makeIdempotencyKey } from '@/services/itApi';

/** One fillable (token, network) with its live price. */
export interface PaycrestVenue {
  /** Our chain key — 'base', 'polygon', 'bsc'. */
  chain: string;
  token: string;
  /** Naira per token, as quoted. String to avoid float drift on money. */
  rate: string;
  /** Cost against the protocol's market mid. Positive on a buy = the user
   *  pays above mid. Show this; it is the real cost of the route. */
  spreadPercent: number;
  /** What the user receives (buy) or must send (sell). */
  tokenAmount: number;
}

export interface PaycrestQuote {
  currency: string;
  amount: string;
  side: 'buy' | 'sell';
  marketRate: number;
  venues: PaycrestVenue[];
  /** Cheapest for the user. Null only if venues is empty, which the API
   *  reports as an error rather than an empty quote. */
  best: PaycrestVenue | null;
  quotedAt: string;
}

/** Server-side order statuses. `unmatched` is ours, not Paycrest's: the user
 *  paid, the bank confirmed, and the protocol never registered it. Nothing
 *  automated can resolve that, so the UI must not present it as an expiry. */
export type PaycrestStatus =
  | 'awaiting_payment'
  | 'awaiting_deposit'
  | 'pending'
  | 'settling'
  | 'settled'
  | 'refunded'
  | 'expired'
  | 'unmatched'
  | 'failed';

export interface PaycrestOrder {
  id: string;
  direction: 'buy' | 'sell';
  status: PaycrestStatus;
  reference: string;
  currency: string;
  amount: string;
  token: string;
  chain: string;
  rate?: string;
  tokenAmount?: string;
  senderFee?: string;
  walletAddress: string;

  /** Buy leg — the provider account the user must pay. */
  payInstitution?: string;
  payAccountNumber?: string;
  payAccountName?: string;
  /** Includes our sender fee, so it is MORE than `amount`. Pay this exactly. */
  payAmount?: string;
  validUntil?: string;

  /** Sell leg — where the user sends stablecoin from their own wallet. */
  depositAddress?: string;
  payoutBankCode?: string;
  payoutAccount?: string;
  payoutAccountName?: string;

  amountPaid?: string;
  txHash?: string;
  failureReason?: string;
  /** Set once claimPayment() lands — see that function for why it exists. */
  paymentClaimedAt?: string;
  settledAt?: string;
  createdAt: string;
}

/** Thrown with the server's own wording — these messages are written to be
 *  shown to users, so screens can surface `message` directly. */
export class PaycrestError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = 'PaycrestError';
    this.status = status;
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const headers = await getAuthHeaders();
  // Order creation is the one thing here that must not double-fire. Two orders
  // means two provider accounts and two countdowns, and the user cannot tell
  // which to pay. The server's open-order guard catches the sequential case;
  // this closes the concurrent one.
  if (init?.method === 'POST') {
    (headers as Record<string, string>)['Idempotency-Key'] = makeIdempotencyKey();
  }
  const res = await fetch(`${API_BASE_URL}/paycrest${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...headers, ...(init?.headers ?? {}) },
  });
  const body = await res.json().catch(() => null);
  if (!res.ok || body?.success === false) {
    // A 404 here is not a bug in the request — it is the route not being
    // registered because the server has no Paycrest credentials. Saying
    // "something went wrong" sends the user to retry forever against a
    // feature that is switched off.
    const fallback =
      res.status === 404 || res.status === 503
        ? 'Naira transfers are not available right now. Please try again later.'
        : 'Could not reach the transfer provider. Check your connection and try again.';
    throw new PaycrestError(body?.message ?? fallback, res.status);
  }
  return body?.data as T;
}

/**
 * Ask what is actually fillable at this amount, right now.
 *
 * Call this on every amount change (debounced) rather than once — a quote is
 * only true for the size it was asked about, and only for a moment.
 */
export async function getQuote(
  side: 'buy' | 'sell',
  amount: string,
  currency = 'NGN',
): Promise<PaycrestQuote> {
  const q = new URLSearchParams({ side, amount, currency });
  return request<PaycrestQuote>(`/quote?${q.toString()}`);
}

export interface PayoutInstitution {
  code: string;
  name: string;
}

/** Payout destinations for a currency, already in the vocabulary the order
 *  expects: NIBSS codes for NGN, Paycrest's own codes elsewhere. The caller
 *  sends the code back unchanged. */
export async function getPayoutInstitutions(currency = 'NGN'): Promise<PayoutInstitution[]> {
  const data = await request<{ banks: PayoutInstitution[] }>(`/banks?currency=${currency}`);
  return data.banks ?? [];
}

export interface CreateBuyInput {
  amount: string;
  chain: string;
  token: string;
  /** The connected wallet. Must match the account's registered address or the
   *  server rejects with 409 — a device holding a different key cannot point
   *  a purchase at it. */
  walletAddress: string;
  /** Where Naira returns if the order fails. Prefill from the user's saved
   *  payout account: refunds should reach whoever paid. */
  refundBankCode: string;
  refundAccount: string;
  refundAccountName?: string;
  currency?: string;
}

/** Opens an onramp. The returned order carries the provider account to pay. */
export async function createBuy(input: CreateBuyInput): Promise<PaycrestOrder> {
  return request<PaycrestOrder>('/buy', { method: 'POST', body: JSON.stringify(input) });
}

export interface CreateSellInput {
  /** Naira the user wants to receive; the token amount is derived. */
  amount: string;
  chain: string;
  token: string;
  walletAddress: string;
  bankCode: string;
  accountNumber: string;
  accountName?: string;
  currency?: string;
}

/** Opens an offramp. The returned order carries the deposit address the user
 *  must send to from their own wallet. */
export async function createSell(input: CreateSellInput): Promise<PaycrestOrder> {
  return request<PaycrestOrder>('/sell', { method: 'POST', body: JSON.stringify(input) });
}

/** Fetches one order, refreshed against the provider server-side. */
export async function getOrder(id: string): Promise<PaycrestOrder> {
  return request<PaycrestOrder>(`/orders/${id}`);
}

export async function listOrders(limit = 25): Promise<PaycrestOrder[]> {
  return request<PaycrestOrder[]>(`/orders?limit=${limit}`);
}

/**
 * Tells the server the user believes they completed their side — the bank
 * transfer on a buy ("I've sent the money"), the on-chain send on a sell
 * ("I've sent it"). This is the ONLY way the server can distinguish a
 * genuine unmatched deposit (paid, bank confirmed, Paycrest never registered
 * it — a real operator alarm) from an order the user simply abandoned before
 * paying, which looks identical once it expires with amountPaid=0.
 *
 * Does not move the order's status — it just records the claim for the next
 * sync to read. Callers should fire this and swallow failures: the user has
 * already sent (or believes they sent) the money, so a lost claim must
 * degrade to the ordinary "expired" outcome, not block navigation or retry
 * loops on this screen.
 */
export async function claimPayment(id: string): Promise<PaycrestOrder> {
  return request<PaycrestOrder>(`/orders/${id}/claim-payment`, { method: 'POST' });
}

/** Statuses that will never change on their own. */
export function isTerminal(status: PaycrestStatus): boolean {
  return ['settled', 'refunded', 'expired', 'unmatched', 'failed'].includes(status);
}

/** Whether the order is still waiting on the user to do something. */
export function isAwaitingUser(status: PaycrestStatus): boolean {
  return status === 'awaiting_payment' || status === 'awaiting_deposit';
}

/**
 * Seconds left before the provider quote expires, floored at 0.
 *
 * Worth showing prominently: an order that expires while the user is still
 * transferring is the failure mode we hit in testing, and the money can end up
 * at the provider's bank with no automatic way back.
 */
export function secondsRemaining(validUntil?: string): number {
  if (!validUntil) return 0;
  const ms = new Date(validUntil).getTime() - Date.now();
  return ms > 0 ? Math.floor(ms / 1000) : 0;
}

/** User-facing status text. Deliberately avoids "failed" for `unmatched`:
 *  the user's money is real and recoverable, just not automatically. */
export function statusLabel(order: PaycrestOrder): string {
  switch (order.status) {
    case 'awaiting_payment':
      return 'Waiting for your transfer';
    case 'awaiting_deposit':
      return 'Waiting for your deposit';
    case 'pending':
      return 'Payment received — matching a provider';
    case 'settling':
      return order.direction === 'buy' ? 'Sending your crypto' : 'Paying your bank';
    case 'settled':
      return 'Complete';
    case 'refunded':
      return 'Refunded';
    case 'expired':
      return 'Expired — no payment received';
    case 'unmatched':
      return 'We are recovering your payment';
    case 'failed':
      return 'Could not be completed';
  }
}
