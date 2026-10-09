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
  isConvertedVenue,
  listOrders,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
  type RequestedAsset,
} from '@/services/paycrestApi';
import type { ITConversion } from '@/services/itApi';

/** One provider fee inside a venue or an order (spec 2026-10-03 §14): the fee
 *  in tokens and its naira value at the display rate. */
export interface RampFee {
  kind: string; // 'conversion' | 'network'
  label: string;
  /** Display name the line is tagged with ("Flutterwave"). */
  provider: string;
  token: string;
  ngn: string;
}

/** `rate` is the display rate (margin in, provider fees out); `effectiveRate`
 *  the all-in rate the server ranks and holds the order to; `tokenAmount` is
 *  what she receives after every fee. */
export type RampVenue = PaycrestVenue & { handle?: string; effectiveRate?: string; fees?: RampFee[] };
export type RampQuote = Omit<PaycrestQuote, 'venues' | 'best'> & {
  venues: RampVenue[];
  best: RampVenue | null;
  /** Set only on a pool-only quote (pool=1): false = TSA Connect cannot fill this, these are other offers. */
  poolAvailable?: boolean;
};

/** Where to pay for a company-wallet or Flutterwave buy. */
export interface RampPayInto {
  bank: string;
  accountName: string;
  accountNumber: string;
  /** Central mode only: the exact naira to send. */
  amountNgn?: string;
  payerInstruction?: string;
}

export type RampProviderName = 'paycrest' | 'treasury' | 'flutterwave' | 'tsa_pool';

/** An order from any provider. A Paycrest body is unchanged (no `provider`);
 *  an IT-backed buy says which provider fills it and carries its pay-into
 *  card, its rate hold, its quoted breakdown and, once sent, the tx. */
export type RampOrder = PaycrestOrder & {
  provider?: RampProviderName;
  payInto?: RampPayInto;
  honourUntil?: string;
  repriced?: boolean;
  effectiveRate?: string;
  fees?: RampFee[];
  /** A converted Instant Transfer buy: the conversion it feeds (spec §15.8). */
  conversion?: ITConversion;
};

/** The rate an order is held to: the all-in one when the server sent it (§14). */
export function quotedRateOf(v: { rate: string; effectiveRate?: string }): string {
  return v.effectiveRate || v.rate;
}

/** The Instant Transfer purchase behind an order the company wallet,
 *  Flutterwave or the TSA Connect pool fills — its screen. Null for Paycrest. */
export function itIntentIdOf(o: unknown): string | null {
  if (!o || typeof o !== 'object') return null;
  const { provider, id } = o as { provider?: unknown; id?: unknown };
  const it = provider === 'treasury' || provider === 'flutterwave' || provider === 'tsa_pool';
  return it && typeof id === 'string' && id ? id : null;
}

export const RAMP_ORDER_OPEN = 'ramp_order_open';

/** The purchase a `ramp_order_open` refusal names, or null for anything else. */
export function openOrderOf(e: unknown): { orderId: string } | null {
  if (!(e instanceof RampError) || e.code !== RAMP_ORDER_OPEN) return null;
  const data = (e.body as { data?: { orderId?: unknown } | null } | null)?.data;
  if (!data || typeof data.orderId !== 'string' || !data.orderId) return null;
  return { orderId: data.orderId };
}

/** One venue list for the buy screen: the router's direct venues (every
 *  provider, best per pair, in the server's order) plus the converted venues
 *  only /paycrest prices. Either may be missing and the other still stands. */
export function mergeBuyQuotes(
  direct: RampQuote | null | undefined,
  converted: PaycrestQuote | null | undefined,
): RampQuote | null {
  const d = direct && Array.isArray(direct.venues) ? direct : null;
  const c = converted && Array.isArray(converted.venues) ? converted : null;
  if (!d && !c) return null;
  if (!d) return c as RampQuote;
  const conv = (c?.venues ?? []).filter((v) => isConvertedVenue(v));
  const venues = [...d.venues, ...conv];
  // `best` must be one of the venues shown, never a /paycrest direct venue the merge dropped.
  return { ...d, venues, best: d.best ?? venues[0] ?? null };
}

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

/** The router's venues. With `want` (a buy of a named asset) the authenticated
 *  converted quote answers: every direct venue plus converted ones, company
 *  wallet first (spec §15.8). Older servers fall back to /ramp/quote, then
 *  /paycrest/quote. `pool` asks for the TSA Connect pool's venues only
 *  (pool=1); the answer's `poolAvailable` says whether it could fill. */
export function getRampQuote(
  side: 'buy' | 'sell',
  amount: string,
  currency = 'NGN',
  want?: RequestedAsset,
  opts: { pool?: boolean } = {},
): Promise<RampQuote> {
  const q = new URLSearchParams({ side, amount, currency });
  if (opts.pool) q.set('pool', '1');
  const direct = () =>
    withFallback(
      () => request<RampQuote>(`/quote?${q.toString()}`),
      () => getQuote(side, amount, currency) as Promise<RampQuote>,
    );
  if (side !== 'buy' || !want) return direct();
  const c = new URLSearchParams({
    amount,
    currency,
    requestedChainId: String(want.chainId),
    requestedTokenAddress: want.address,
    requestedToken: want.token,
  });
  if (want.chain) c.set('requestedChain', want.chain);
  if (want.destinationAddress) c.set('destinationAddress', want.destinationAddress);
  if (opts.pool) c.set('pool', '1');
  return withFallback(() => request<RampQuote>(`/converted-quote?${c.toString()}`), direct);
}

/** A direct buy, or a converted one the router priced (requested* set). */
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
  /** A converted venue the router priced (spec §15): what she asked for. */
  requestedChainId?: number;
  requestedTokenAddress?: string;
  requestedToken?: string;
  requestedChain?: string;
  destinationAddress?: string;
}

export function createRampBuy(input: CreateRampBuyInput): Promise<RampOrder> {
  return withFallback(
    () => request<RampOrder>('/buy', { method: 'POST', body: JSON.stringify(input) }),
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

export function createRampSell(input: CreateRampSellInput): Promise<RampOrder> {
  return withFallback(
    () => request<RampOrder>('/sell', { method: 'POST', body: JSON.stringify(input) }),
    () => createSell(withoutQuote(input)),
  );
}

export function getRampOrder(id: string): Promise<RampOrder> {
  return withFallback(() => request<RampOrder>(`/orders/${encodeURIComponent(id)}`), () => getOrder(id));
}

export function listRampOrders(limit = 25): Promise<RampOrder[]> {
  return withFallback(() => request<RampOrder[]>(`/orders?limit=${limit}`), () => listOrders(limit));
}

export function claimRampPayment(id: string): Promise<RampOrder> {
  return withFallback(
    () => request<RampOrder>(`/orders/${encodeURIComponent(id)}/claim-payment`, { method: 'POST' }),
    () => claimPayment(id),
  );
}

export function cancelRampOrder(id: string): Promise<RampOrder> {
  return withFallback(
    () => request<RampOrder>(`/orders/${encodeURIComponent(id)}/cancel`, { method: 'POST' }),
    () => cancelOrder(id),
  );
}
