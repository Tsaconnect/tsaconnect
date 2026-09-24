// utils/payCheckout.ts
//
// Pure state machine for app/pay.tsx — one function turning
// (route param, auth, the last fetch) into exactly what the screen should
// show. Kept separate from the screen so every branch (invalid link, signed
// out, waiting on your own payment vs. someone else's, expiring soon,
// terminal) is testable without rendering anything.
import type { PayCheckout, PayStatus } from '@/services/payApi';
import type { SellIntent } from '@/services/paycrestSellSwap';
// From utils/paymentId.ts, not services/payApi.ts: that module's request()
// chain pulls in walletApi.ts's top-level `import AsyncStorage from
// '@react-native-async-storage/async-storage'`, which throws outside a
// mocked test — and this file is a pure function that must stay importable
// (and testable) on its own. utils/paymentId.ts has no such dependency.
import { isPaymentId } from '@/utils/paymentId';

export type PayScreenState =
  | { kind: 'loading' }
  | { kind: 'invalid' }
  | { kind: 'error'; message: string }
  | { kind: 'signin' }
  | { kind: 'payable'; checkout: PayCheckout }
  | { kind: 'waiting'; checkout: PayCheckout }
  | { kind: 'done'; checkout: PayCheckout }
  | { kind: 'blocked'; checkout: PayCheckout; why: string };

export const TERMINAL_STATUSES: PayStatus[] = ['succeeded', 'failed', 'expired'];

// Below this, an approve/decline (or the bank transfer behind a live payment)
// may not finish before the checkout expires server-side — better to block
// and send the customer back for a fresh link than let them start something
// that can't land.
const EXPIRY_WARNING_MS = 5 * 60 * 1000;

export function payScreenState(input: {
  id: unknown;
  authed: boolean;
  checkout?: PayCheckout;
  error?: string;
  now: number;
  /**
   * A payment attempt of hers is moving money right now (swapping, waiting on a
   * payout, asking about a shortfall). The expiry warning must not tear the
   * form down under it: a bridge swap can outlast the five minutes it warns of.
   */
  attemptActive?: boolean;
}): PayScreenState {
  const { id, authed, checkout, error, now, attemptActive } = input;

  if (!isPaymentId(id)) return { kind: 'invalid' };
  if (!authed) return { kind: 'signin' };

  if (!checkout) {
    return error ? { kind: 'error', message: error } : { kind: 'loading' };
  }

  if (TERMINAL_STATUSES.includes(checkout.status)) return { kind: 'done', checkout };

  // Own attempt still settling — keep polling rather than showing Approve/
  // Decline again, which would let the same payer fire a second attempt.
  if (checkout.status === 'processing' && checkout.youArePaying) {
    return { kind: 'waiting', checkout };
  }

  if (!checkout.payable) {
    return {
      kind: 'blocked',
      checkout,
      why: checkout.notPayableWhy ?? 'This payment is not currently payable.',
    };
  }

  const msLeft = new Date(checkout.expiresAt).getTime() - now;
  if (msLeft < EXPIRY_WARNING_MS && !attemptActive) {
    return {
      kind: 'blocked',
      checkout,
      why: 'This payment link expires in under five minutes — ask the merchant for a new one.',
    };
  }

  return { kind: 'payable', checkout };
}

/**
 * Integer kobo as the decimal naira string the rate endpoints take
 * ("5000000" → "50000.00"). Whole-number string maths: a payment's amount is
 * money and never passes through a float on its way to a quote.
 */
export function nairaFromKobo(kobo: number): string {
  if (!Number.isSafeInteger(kobo) || kobo < 0) throw new RangeError(`Not a whole number of kobo: ${kobo}`);
  return `${Math.floor(kobo / 100)}.${String(kobo % 100).padStart(2, '0')}`;
}

const SHOWN_DECIMALS = 6;

/**
 * "0.084 BNB": what the payer sends, from base units. At most six decimals, and
 * a sliver past the last one rounds UP — an "about" figure must never
 * understate what leaves her wallet. Empty for anything that is not a whole
 * number of base units, rather than a wrong figure.
 */
export function payAmountLabel(baseUnits: string, decimals: number, symbol: string): string {
  if (!/^\d+$/.test(baseUnits)) return '';
  const unit = 10n ** BigInt(decimals);
  const keep = 10n ** BigInt(Math.max(decimals - SHOWN_DECIMALS, 0));
  const total = BigInt(baseUnits);
  const rounded = ((total + keep - 1n) / keep) * keep;
  const whole = rounded / unit;
  const frac = (rounded % unit).toString().padStart(decimals, '0').replace(/0+$/, '');
  return `${whole}${frac ? `.${frac}` : ''} ${symbol}`;
}

/**
 * What the merchant receives, from base units: cut down (never rounded up) to
 * the cent, or to six decimals under one. Empty for a figure it cannot read.
 */
export function receiveAmountLabel(baseUnits: string, decimals: number, symbol: string): string {
  if (!/^\d+$/.test(baseUnits)) return '';
  const digits = baseUnits.padStart(decimals + 1, '0');
  const whole = digits.slice(0, digits.length - decimals).replace(/^0+(?=\d)/, '');
  const frac = digits.slice(digits.length - decimals);
  if (whole !== '0') return `${whole}.${frac.padEnd(2, '0').slice(0, 2)} ${symbol}`;
  const cut = frac.padEnd(SHOWN_DECIMALS, '0').slice(0, SHOWN_DECIMALS).replace(/0+$/, '');
  return `0${cut ? `.${cut}` : ''} ${symbol}`;
}

/**
 * The shortfall question, in USDC — the only unit the server measures it in.
 * `walletTried`: she already offered her wallet's USDC and it did not cover the
 * rest, so asking again would just repeat itself.
 */
export function shortfallCopy(missingUsdc: string | undefined, walletTried = false): string {
  if (walletTried) {
    const more = missingUsdc ? `about ${missingUsdc} USDC more is needed` : 'more USDC is needed';
    return `Your USDC on Base isn't enough to cover the rest — ${more}. Add USDC to your wallet, then tap Use my USDC again.`;
  }
  const by = missingUsdc ? `about ${missingUsdc} USDC short` : 'short';
  return `The rate moved and your swap landed ${by}. Pay the rest from the USDC in your wallet?`;
}

/**
 * Her open sale, but only when it is the one that pays THIS payment. An
 * unrelated open sale of hers is not this payment's attempt and must never be
 * followed, resumed or reported on as if it were.
 */
export function intentForPayment(open: SellIntent | null | undefined, paymentId: string): SellIntent | null {
  return open && open.payPaymentId === paymentId ? open : null;
}

/** Said in place of the swapped sell's own words when a payment cannot safely be started over. */
export const PAYMENT_STILL_GOING =
  "Your payment isn't finished yet, but your money is safe. Give it a few minutes, then tap Continue payment — don't pay again.";

/** 409 from starting a payment: an attempt of hers already exists — follow it, never start another. */
export const PAY_IN_PROGRESS = 'pay_in_progress';

/** The server's machine code on an error, if it sent one. Keyed on, never the words beside it. */
export function errorCode(e: unknown): string | undefined {
  const code = (e as { code?: unknown } | null)?.code;
  return typeof code === 'string' ? code : undefined;
}

export const errorMessage = (e: unknown, fallback: string): string =>
  e instanceof Error && e.message ? e.message : fallback;

/** Nothing can be sent from Solana, Bitcoin or Sui for a payment yet. */
export const unsupportedNetworkMessage = (symbol: string): string =>
  `Paying with ${symbol} from that network isn't supported yet. Pick a coin on another network.`;

/** Continue payment found no sale of hers that pays this payment. */
export const NOTHING_TO_CONTINUE =
  "We couldn't find a payment of yours to continue. If it doesn't finish within a few minutes it will be released, and you can try again.";

/** 409 on every start route: the server could not prove whether an attempt exists, so it paused the payment for support. */
export const PAY_ATTEMPT_UNCERTAIN = 'pay_attempt_uncertain';

/** Said for that answer — never offers Pay or Retry: a second payment could double-pay. */
export const PAYMENT_PAUSED =
  "This payment is paused while we check it — please don't pay again. We'll update you shortly.";

/** The route refused this token for now (fail-closed): nothing was started and the payment is still payable. */
export const CANT_PAY_WITH_TOKEN =
  "You can't pay this merchant with that token right now — try another token, or try again later.";

/** A 422/503 from a start or price route. By status only: the words differ per route and reason. */
export function isRouteRefused(e: unknown): boolean {
  const status = (e as { status?: unknown } | null)?.status;
  return status === 422 || status === 503;
}

/** How far the fresh quote may exceed the price she was shown before we refuse to sign it. */
export const PRICE_TOLERANCE_BPS = 200;
/** A price older than this is looked up again (and never trusted at the Pay tap). */
export const PRICE_STALE_MS = 30_000;

/** Whether `fresh` costs more than `priced` by over the tolerance. Base units, exact. */
export function exceedsPriced(fresh: bigint, priced: bigint, toleranceBps: number = PRICE_TOLERANCE_BPS): boolean {
  return fresh * 10_000n > priced * BigInt(10_000 + toleranceBps);
}

/** The refusal when the price moved past what she agreed to: nothing was sent, and what to do. */
export function priceMovedMessage(freshBase: string, pricedBase: string, decimals: number, symbol: string): string {
  return `The price moved while you were deciding: this would now cost about ${payAmountLabel(freshBase, decimals, symbol)}, `
    + `not the ${payAmountLabel(pricedBase, decimals, symbol)} you were shown. Nothing was sent. Check the new price and try again.`;
}

export type StartErrorKind = 'follow' | 'paused' | 'refused' | 'other';

/**
 * One reading of a start route's answer, for both hooks: an attempt of hers
 * already exists (follow it), the server paused the payment, the route refused
 * this token for now, or anything else. By code and status — never by words.
 */
export function classifyStartError(e: unknown): StartErrorKind {
  const code = errorCode(e);
  if (code === PAY_IN_PROGRESS) return 'follow';
  if (code === PAY_ATTEMPT_UNCERTAIN) return 'paused';
  return isRouteRefused(e) ? 'refused' : 'other';
}

type Failure = { error: string; errorCode?: string; retryable: boolean };
/** A payment the server paused: never retried, never followed. */
export const PAUSED_FAILURE: Failure = { error: PAYMENT_PAUSED, errorCode: PAY_ATTEMPT_UNCERTAIN, retryable: false };
/** A route that refused this token for now: nothing started, so try another. */
export const REFUSED_FAILURE: Failure = { error: CANT_PAY_WITH_TOKEN, retryable: true };

/** What a failed start looks like to the payer, per kind. `follow` is the caller's own. */
export function startFailure(e: unknown): Failure {
  switch (classifyStartError(e)) {
    case 'paused':
      return PAUSED_FAILURE;
    case 'refused':
      return REFUSED_FAILURE;
    default:
      return { error: errorMessage(e, 'Something went wrong. Please try again.'), errorCode: errorCode(e), retryable: true };
  }
}

/** A price lookup that failed: a refusal becomes the shared copy, anything else passes through. */
export const estimateError = (e: unknown): Error => (isRouteRefused(e) ? new Error(CANT_PAY_WITH_TOKEN) : (e as Error));

/**
 * The terminal state for a swap the server will not take a hash for: it may
 * still be mining and reach the merchant, so she must not pay again, and only
 * support can settle it — with the payment's reference.
 */
export const unconfirmedCopy = (paymentId: string): string =>
  `We couldn't confirm this payment with our server. Your payment may still go through, so please don't pay again. `
  + `Contact support and quote payment reference ${paymentId}.`;
