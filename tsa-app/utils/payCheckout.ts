// utils/payCheckout.ts
//
// Pure state machine for app/pay.tsx — one function turning
// (route param, auth, the last fetch) into exactly what the screen should
// show. Kept separate from the screen so every branch (invalid link, signed
// out, waiting on your own payment vs. someone else's, expiring soon,
// terminal) is testable without rendering anything.
import type { PayCheckout, PayStatus } from '@/services/payApi';
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

const TERMINAL: PayStatus[] = ['succeeded', 'failed', 'expired'];

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
}): PayScreenState {
  const { id, authed, checkout, error, now } = input;

  if (!isPaymentId(id)) return { kind: 'invalid' };
  if (!authed) return { kind: 'signin' };

  if (!checkout) {
    return error ? { kind: 'error', message: error } : { kind: 'loading' };
  }

  if (TERMINAL.includes(checkout.status)) return { kind: 'done', checkout };

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
  if (msLeft < EXPIRY_WARNING_MS) {
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

/** The shortfall question, in USDC — the only unit the server measures it in. */
export function shortfallCopy(missingUsdc: string | undefined): string {
  const by = missingUsdc ? `about ${missingUsdc} USDC short` : 'short';
  return `The rate moved and your swap landed ${by}. Pay the rest from the USDC in your wallet?`;
}

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
