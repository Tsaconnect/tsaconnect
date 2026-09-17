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
import { getChain, type ChainKey } from '@/constants/chains';
import { p2pTokenDecimals, formatTokenAmount } from '@/services/p2p';
// The multi-hop ramp progress UI's own types — reused verbatim, not
// redeclared. The status→hop MAPPING below is new (Paycrest's converted-order
// statuses have no overlap with the stablecoin sell/buy ones that file maps),
// but the shape it renders through is the same one already wired into
// app/wallet/it/intent/[id].tsx.
import type { Hop } from '@/utils/stableSellQuote';
import type { PaycrestStatus } from './paycrestStatus';

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

  // ── Conversion (Phase 2). Absent/'direct' on every ordinary venue. ──

  /** 'converted' means Paycrest settles on chain/token (below) and a
   *  cross-chain route then delivers requestedChain/requestedToken instead.
   *  This is the ONLY signal that distinguishes a converted route from a
   *  direct one — the app must label it, never fold it into an ordinary
   *  venue silently. */
  kind?: 'direct' | 'converted';
  /** The Paycrest settlement leg a converted venue actually creates the
   *  order against — restates chain/token under their own name so a
   *  converted venue can say what it settles as without being confused with
   *  what the user gets. */
  viaChain?: string;
  viaToken?: string;
  /** What the user actually asked for and will receive on a converted venue. */
  requestedChain?: string;
  requestedToken?: string;
  /** Priced markup for the conversion, in basis points. */
  markupBps?: number;
  /** Guaranteed delivery, in the REQUESTED token's own base units — render
   *  with that token's decimals (see formatPromisedAmount), never as a raw
   *  integer. */
  promisedAmount?: string;
  /** The cross-chain route's estimated execution time, in seconds. */
  etaSeconds?: number;
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

/** Statuses and their pure predicates live in paycrestStatus so that screens
 *  and helpers can reason about one without importing this client. Re-exported
 *  here because everything already imports them from this module. */
export type { PaycrestStatus } from './paycrestStatus';
export { isTerminal, isAwaitingUser } from './paycrestStatus';

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

  // ── Conversion (Phase 2). Empty/'direct' on every pre-Phase-2 order. ──

  /** 'converted' means chain/token above is the SETTLEMENT leg (what
   *  Paycrest actually delivers, to our treasury) — requestedChain/
   *  requestedToken below is what the user gets. Reading chain/token alone
   *  on a converted order is the exact silent substitution this phase exists
   *  to remove; always check this first. */
  routeKind?: 'direct' | 'converted';
  requestedToken?: string;
  requestedChain?: string;
  markupBps?: number;
  /** What we guaranteed the user receives, in the REQUESTED token's base
   *  units. Format with formatPromisedAmount, never render raw. */
  promisedAmount?: string;
  /** Who currently holds the funds. 'paycrest' until the settlement leg
   *  lands, then 'treasury' while converting, then 'user' once delivered or
   *  refunded. */
  holder?: 'user' | 'treasury' | 'paycrest';
  /** What we still owe the user, in destination base units. "0" means
   *  nothing owed; "" means UNDETERMINED (a transfer is in flight) — never
   *  treat "" as zero. */
  owedToUser?: string;
  fault?: 'platform' | 'user';

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
  /** Machine error code, when the server sends one (e.g.
   *  "kyc_verification_required" on a buy). Branch on this, not on message. */
  code?: string;
  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = 'PaycrestError';
    this.status = status;
    this.code = code;
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
    throw new PaycrestError(body?.message ?? fallback, res.status, body?.code);
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
  // The (chain, token) the user has explicitly chosen to receive. Passed
  // through so a server that cannot fill it directly can price a converted
  // route for THIS pair — ConvertedVenues needs to know what to price against,
  // it cannot guess. An older/not-yet-wired server just ignores the extra
  // query params and returns direct venues exactly as before.
  requested?: { chain: string; token: string },
): Promise<PaycrestQuote> {
  const q = new URLSearchParams({ side, amount, currency });
  if (requested?.chain && requested.token) {
    q.set('requestedChain', requested.chain);
    q.set('requestedToken', requested.token);
  }
  return request<PaycrestQuote>(`/quote?${q.toString()}`);
}

export interface PayoutInstitution {
  code: string;
  name: string;
  /** What the destination is, so the account field can ask for the right
   *  thing: a bank wants an account number, mobile money wants a phone
   *  number. Optional because an older API build does not send it; absent is
   *  treated as a bank, which is every currency's majority destination. */
  type?: 'bank' | 'mobile_money';
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
  /** What the user actually wants delivered. Omitted (or equal to
   *  chain/token) means a direct buy — the server defaults both to
   *  chain/token, so leaving these unset behaves exactly as before. */
  requestedChain?: string;
  requestedToken?: string;
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

/**
 * Abandon an order the user has not paid or funded.
 *
 * The server holds one open order per user per direction, so an order left
 * hanging blocks the next one until its window closes — this is the way out of
 * that. It is refused once the payment has been claimed as sent, and unlike
 * claimPayment that refusal must reach the user: they are about to walk away
 * from an order that is still live at the provider.
 */
export async function cancelOrder(id: string): Promise<PaycrestOrder> {
  return request<PaycrestOrder>(`/orders/${id}/cancel`, { method: 'POST' });
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
    case 'cancelled':
      return 'Cancelled';
    case 'converting':
      return 'Converting to what you asked for';
    case 'refunding':
      return 'Returning to your wallet';
    case 'recovery_pending':
      return 'We are recovering your funds';
  }
}

// ─── Conversion (Phase 2) ──────────────────────────────────────────────────

export function isConvertedVenue(v: Pick<PaycrestVenue, 'kind'>): boolean {
  return v.kind === 'converted';
}

export function isConvertedOrder(o: Pick<PaycrestOrder, 'routeKind'>): boolean {
  return o.routeKind === 'converted';
}

/**
 * The single rule this whole phase exists to enforce: pick the venue that
 * fills the user's exact requested (chainKey, symbol) — or nothing.
 *
 * An exact DIRECT match always wins over a converted one for the same pair
 * (mirrors the backend's own Decision 3: never convert what Paycrest can
 * already deliver). A converted match is used only when no direct match
 * exists. When NEITHER exists this returns `null` — never `venues[0]`,
 * never "best", never anything the user did not explicitly ask for. A
 * caller with nothing to show must render "no route" and offer an
 * alternative as a choice, not silently substitute one here.
 */
export function selectVenue(
  venues: PaycrestVenue[],
  wantChainKey: string,
  wantSymbol: string,
): PaycrestVenue | null {
  const matchesWanted = (v: PaycrestVenue) =>
    v.chain.toLowerCase() === wantChainKey.toLowerCase() &&
    v.token.toUpperCase() === wantSymbol.toUpperCase();
  const direct = venues.find((v) => matchesWanted(v) && !isConvertedVenue(v));
  if (direct) return direct;
  const converted = venues.find(
    (v) =>
      isConvertedVenue(v) &&
      v.requestedChain?.toLowerCase() === wantChainKey.toLowerCase() &&
      v.requestedToken?.toUpperCase() === wantSymbol.toUpperCase(),
  );
  return converted ?? null;
}

/**
 * On-chain decimals for a (chainKey, symbol) pair, resolved through the
 * app's own chain registry — the same source `p2pTokenDecimals` already
 * gets right for BSC's 18-decimal stables vs everywhere else's 6. Falls back
 * to whatever `p2pTokenDecimals` does for an unresolved chain (its own
 * symbol-only default), rather than inventing a second guess here.
 */
export function requestedAssetDecimals(chainKey: string, token: string): number {
  const chain = getChain(chainKey as ChainKey);
  return p2pTokenDecimals(chain?.chainId, token);
}

/**
 * promisedAmount is a raw base-units integer string in the REQUESTED token's
 * OWN decimals (never the settlement leg's — see the field's doc comment).
 * Rendering it without rescaling is off by 10^decimals and would show a user
 * "9950000" instead of "9.95". Null when any of the three fields required to
 * price it are missing, which is every direct venue/order — callers gate
 * display on `kind`/`routeKind` first.
 */
export function formatPromisedAmount(
  v: Pick<PaycrestVenue, 'promisedAmount' | 'requestedChain' | 'requestedToken'>,
): string | null {
  if (!v.promisedAmount || !v.requestedChain || !v.requestedToken) return null;
  const decimals = requestedAssetDecimals(v.requestedChain, v.requestedToken);
  return formatTokenAmount(v.promisedAmount, decimals, decimals);
}

/** "~30s" under 90 seconds (matches how a user actually reads a short wait),
 *  "~2 min" above it. A missing/zero estimate reads as a vague-but-honest
 *  "a few minutes" rather than "~0s", which would be a promise we cannot
 *  keep. */
export function formatEta(seconds?: number): string {
  if (!seconds || seconds <= 0) return 'a few minutes';
  if (seconds < 90) return `~${Math.round(seconds)}s`;
  return `~${Math.round(seconds / 60)} min`;
}

/**
 * The one sentence a converted route must say, per the no-silent-substitution
 * rule this phase exists to enforce: what the user receives, that it is a
 * conversion (not the thing Paycrest itself fills), and roughly how long it
 * takes. `chainLabel` is caller-supplied so this stays free of any one
 * screen's display-name table.
 */
export function describeConvertedVenue(
  v: Pick<PaycrestVenue, 'requestedToken' | 'requestedChain' | 'viaToken' | 'viaChain' | 'etaSeconds'>,
  chainLabel: (chainKey: string) => string,
): string {
  const to = `${v.requestedToken ?? '?'} on ${chainLabel(v.requestedChain ?? '')}`;
  const via = `${v.viaToken ?? '?'} on ${chainLabel(v.viaChain ?? '')}`;
  return `${to} — includes a conversion from ${via}, arrives in ${formatEta(v.etaSeconds)}`;
}

/**
 * The order-screen timeline for a CONVERTED order: Received → Converting →
 * Delivering. Callers gate this on `isConvertedOrder` — a direct order never
 * reaches `converting`/`refunding`/`recovery_pending`, so it needs no hops.
 *
 * `holder` backstops `status` rather than being read alone: the model only
 * ever moves it to 'treasury' in the same write that moves status to
 * 'converting' (see conversion_orchestrator.go), so the two should always
 * agree — but a stale/racy read that still shows holder 'paycrest' under a
 * 'converting' status must not claim the funds were received when our own
 * ledger hasn't caught up to say so.
 *
 * A refund replaces "Delivering" outright rather than failing it: the money
 * is going BACK to the user's wallet, not forward to what they originally
 * asked for, and reporting it as a failed delivery would understate that it
 * is still landing in their own hands.
 */
export function paycrestConversionHops(status: PaycrestStatus, holder?: string): Hop[] {
  if (status === 'refunding' || status === 'refunded') {
    const done = status === 'refunded';
    return [
      { key: 'received', label: 'Received', state: 'done' },
      { key: 'converting', label: 'Converting', state: 'done' },
      {
        key: 'delivering',
        label: done ? 'Returned to your wallet' : 'Returning to your wallet',
        state: done ? 'done' : 'active',
      },
    ];
  }
  if (status === 'recovery_pending') {
    return [
      { key: 'received', label: 'Received', state: 'done' },
      { key: 'converting', label: 'Converting', state: 'failed' },
      { key: 'delivering', label: 'Delivering', state: 'pending' },
    ];
  }
  if (status === 'settled') {
    return [
      { key: 'received', label: 'Received', state: 'done' },
      { key: 'converting', label: 'Converting', state: 'done' },
      { key: 'delivering', label: 'Delivering', state: 'done' },
    ];
  }
  if (status === 'converting') {
    const receivedDone = holder !== 'paycrest';
    return [
      { key: 'received', label: 'Received', state: receivedDone ? 'done' : 'active' },
      { key: 'converting', label: 'Converting', state: 'active' },
      { key: 'delivering', label: 'Delivering', state: 'pending' },
    ];
  }
  if (status === 'pending' || status === 'settling') {
    return [
      { key: 'received', label: 'Received', state: 'active' },
      { key: 'converting', label: 'Converting', state: 'pending' },
      { key: 'delivering', label: 'Delivering', state: 'pending' },
    ];
  }
  if (status === 'awaiting_payment' || status === 'awaiting_deposit') {
    return [
      { key: 'received', label: 'Received', state: 'pending' },
      { key: 'converting', label: 'Converting', state: 'pending' },
      { key: 'delivering', label: 'Delivering', state: 'pending' },
    ];
  }
  // expired/unmatched/failed/cancelled: never claim progress the order never
  // made.
  return [
    { key: 'received', label: 'Received', state: 'failed' },
    { key: 'converting', label: 'Converting', state: 'pending' },
    { key: 'delivering', label: 'Delivering', state: 'pending' },
  ];
}

/** Shared with gasSponsorship.ts, which lives under the same /paycrest routes. */
export { request as paycrestRequest };
