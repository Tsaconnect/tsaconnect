// services/rampApi.ts
//
// Client for the provider-neutral ramp (tsa-api-go handlers/ramp_handler.go;
// docs/specs/2026-10-02-ramp-router-design.md). Same shapes as paycrestApi —
// orders keep their bare UUID ids — plus the venue handle and the quoted
// rate an order is held to.
//
// Until every server runs the router, a call whose ROUTE is missing (Gin's
// plain-text 404, so no JSON body) is retried once on /api/paycrest/*, minus
// the ramp-only fields. A JSON 404 ("Order not found") is a real answer and
// is never retried. Remove the fallback one release after the API ships.
import { createApiRequest } from '@/services/apiRequest';
import {
  PaycrestError,
  cancelOrder,
  claimPayment,
  createBuy,
  createSell,
  getOrder,
  getQuote,
  listOrders,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
} from '@/services/paycrestApi';

export type RampVenue = PaycrestVenue & { handle?: string };
export type RampQuote = Omit<PaycrestQuote, 'venues' | 'best'> & {
  venues: RampVenue[];
  best: RampVenue | null;
};

/** A PaycrestError, so every screen's existing `instanceof PaycrestError`
 *  message handling keeps working; carries the raw body for price_moved. */
export class RampError extends PaycrestError {
  body: unknown;
  constructor(message: string, status: number, code: string | undefined, body: unknown) {
    super(message, status, code);
    this.name = 'RampError';
    this.body = body;
  }
}

const request = createApiRequest({
  prefix: '/ramp',
  unavailableMessage: 'Naira transfers are not available right now. Please try again later.',
  networkMessage: 'Could not reach the transfer provider. Check your connection and try again.',
  makeError: (message, status, code, body) => new RampError(message, status, code, body),
});

/** The server has no /api/ramp route at all — not "this order is not yours". */
export function isRouteMissing(e: unknown): boolean {
  return e instanceof RampError && e.status === 404 && e.body == null;
}

/** The fresh quote from a price_moved refusal, or null for any other error. */
export function priceMovedQuote(e: unknown): RampQuote | null {
  if (!(e instanceof RampError) || e.code !== 'price_moved') return null;
  const data = (e.body as { data?: RampQuote } | null)?.data;
  return data && Array.isArray(data.venues) ? data : null;
}

/** The request as an older server's /paycrest route takes it: no ramp-only fields. */
function withoutQuote<T extends { quotedRate: string; handle?: string }>(input: T): Omit<T, 'quotedRate' | 'handle'> {
  const legacy: Partial<T> = { ...input };
  delete legacy.quotedRate;
  delete legacy.handle;
  return legacy as Omit<T, 'quotedRate' | 'handle'>;
}

async function withFallback<T>(primary: () => Promise<T>, fallback: () => Promise<T>): Promise<T> {
  try {
    return await primary();
  } catch (e) {
    if (isRouteMissing(e)) return fallback();
    throw e;
  }
}

export function getRampQuote(side: 'buy' | 'sell', amount: string, currency = 'NGN'): Promise<RampQuote> {
  const q = new URLSearchParams({ side, amount, currency });
  return withFallback(
    () => request<RampQuote>(`/quote?${q.toString()}`),
    () => getQuote(side, amount, currency) as Promise<RampQuote>,
  );
}

/** A DIRECT buy: the converted path (requested asset ≠ venue) stays on createBuy. */
export interface CreateRampBuyInput {
  amount: string;
  chain: string;
  token: string;
  walletAddress: string;
  refundBankCode: string;
  refundAccount: string;
  refundAccountName?: string;
  currency?: string;
  /** The venue rate she confirmed; the order is held to it (server tolerance). */
  quotedRate: string;
  handle?: string;
}

export function createRampBuy(input: CreateRampBuyInput): Promise<PaycrestOrder> {
  return withFallback(
    () => request<PaycrestOrder>('/buy', { method: 'POST', body: JSON.stringify(input) }),
    () => createBuy(withoutQuote(input)),
  );
}

export interface CreateRampSellInput {
  amount: string;
  chain: string;
  token: string;
  walletAddress: string;
  bankCode: string;
  accountNumber: string;
  accountName?: string;
  currency?: string;
  quotedRate: string;
  handle?: string;
}

export function createRampSell(input: CreateRampSellInput): Promise<PaycrestOrder> {
  return withFallback(
    () => request<PaycrestOrder>('/sell', { method: 'POST', body: JSON.stringify(input) }),
    () => createSell(withoutQuote(input)),
  );
}

export function getRampOrder(id: string): Promise<PaycrestOrder> {
  return withFallback(() => request<PaycrestOrder>(`/orders/${encodeURIComponent(id)}`), () => getOrder(id));
}

export function listRampOrders(limit = 25): Promise<PaycrestOrder[]> {
  return withFallback(() => request<PaycrestOrder[]>(`/orders?limit=${limit}`), () => listOrders(limit));
}

export function claimRampPayment(id: string): Promise<PaycrestOrder> {
  return withFallback(
    () => request<PaycrestOrder>(`/orders/${encodeURIComponent(id)}/claim-payment`, { method: 'POST' }),
    () => claimPayment(id),
  );
}

export function cancelRampOrder(id: string): Promise<PaycrestOrder> {
  return withFallback(
    () => request<PaycrestOrder>(`/orders/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
    () => cancelOrder(id),
  );
}
