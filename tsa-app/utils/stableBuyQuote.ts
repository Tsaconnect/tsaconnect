/**
 * Pure money math and status mapping for the stablecoin buy flow.
 *
 * Lives outside the screen for the reason utils/sellQuote.ts and
 * utils/stableSellQuote.ts do: the repo has no React Native test harness, so
 * anything that must be TESTED has to be free of React. Keeping it here from
 * the first commit is also what stops buy-stable.tsx growing the way
 * sell-stable.tsx did.
 */

/** Stablecoin base units are 6 decimals on every chain Flutterwave supports. */
import type { StepState } from '@/utils/progressSteps';
const ASSET_DECIMALS = 6;

/** Every status a stablecoin BUY intent can hold, per the backend model.
 *  The mapping test enumerates this, so adding one there without a hop here
 *  fails CI rather than shipping a blank screen. */
export const STABLE_BUY_STATUSES = [
  'pending_payment',
  'payment_received',
  'awaiting_funds',
  'converting',
  'delivering',
  'settled',
  'buy_awaiting_rate',
  'cancelled',
  'failed',
  'refunded',
] as const;

/** Finished for good (refunded: an operator returned her naira). `buy_awaiting_rate` is deliberately absent: the money
 *  landed but could not be priced, and an operator can resume it back to
 *  payment_received — so the screen must keep polling. */
const TERMINAL = new Set(['settled', 'cancelled', 'failed', 'refunded']);

export function isTerminalBuyStatus(status: string): boolean {
  return TERMINAL.has((status || '').trim());
}

function digitsOnly(v: string): string {
  const t = (v ?? '').trim();
  return /^\d+$/.test(t) ? t : '';
}

/** 6-decimal base units → a display string with trailing zeros trimmed. */
export function formatAssetUnits(units: string): string {
  const d = digitsOnly(units);
  if (!d) return '0';
  const padded = d.padStart(ASSET_DECIMALS + 1, '0');
  const whole = padded.slice(0, -ASSET_DECIMALS);
  const frac = padded.slice(-ASSET_DECIMALS).replace(/0+$/, '');
  return frac ? `${whole}.${frac}` : whole;
}

/** Fiat minor units → grouped display with exactly two decimals. */
export function formatMinorAsNaira(minor: string): string {
  const d = digitsOnly(minor);
  if (!d) return '0.00';
  const padded = d.padStart(3, '0');
  const whole = padded.slice(0, -2);
  const frac = padded.slice(-2);
  return `${whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',')}.${frac}`;
}

export type HopState = StepState;
export interface Hop {
  label: string;
  state: HopState;
}

const ORDER = ['payment_received', 'awaiting_funds', 'converting', 'delivering', 'settled'];
const LABELS: Record<string, string> = {
  pending_payment: 'Waiting for your transfer',
  payment_received: 'Payment received',
  awaiting_funds: 'Confirming your payment',
  converting: 'Buying',
  delivering: 'Sending to your wallet',
  settled: 'Delivered',
  buy_awaiting_rate: 'Waiting for us to review',
  cancelled: 'Cancelled',
  failed: 'Failed',
  refunded: 'Refunded',
};

export function buyStatusLabel(status: string): string {
  return LABELS[(status || '').trim()] ?? 'In progress';
}

/** The four visible hops, with the current one active. */
export function buyStatusToHops(status: string): Hop[] {
  const s = (status || '').trim();
  const shown = ['awaiting_funds', 'converting', 'delivering', 'settled'];

  if (s === 'cancelled' || s === 'failed' || s === 'refunded') {
    return shown.map((k) => ({ label: LABELS[k], state: 'pending' as HopState }));
  }
  if (s === 'buy_awaiting_rate') {
    // Money landed, unpriced, parked for an operator. Flagged rather than
    // merely stalled so it does not read as ordinary progress.
    return [
      { label: LABELS.payment_received, state: 'done' },
      { label: LABELS.buy_awaiting_rate, state: 'failed' },
    ];
  }

  const idx = ORDER.indexOf(s);
  return shown.map((k) => {
    const ki = ORDER.indexOf(k);
    if (idx < 0) return { label: LABELS[k], state: 'pending' as HopState };
    if (ki < idx) return { label: LABELS[k], state: 'done' as HopState };
    if (ki === idx) {
      return { label: LABELS[k], state: (s === 'settled' ? 'done' : 'active') as HopState };
    }
    return { label: LABELS[k], state: 'pending' as HopState };
  });
}
