// utils/rampHonour.ts
//
// A stablecoin buy opened through the ramp router has its quote held for a
// window (spec 2026-10-03 §5.2): pay in time and exactly, and you get exactly
// what was shown; pay late, or a different amount, and it is priced at the live rate. The API marks
// such a buy with `rateExpiresAt` — no other buy carries it — and `repriced`
// then means "priced at a live rate instead". On any other buy `repriced`
// only means "priced when the money landed", so it is never read alone.
import { secondsLeft } from '@/hooks/useCountdown';

export interface HonourIntent {
  direction?: string;
  asset?: string;
  status?: string;
  rateExpiresAt?: string;
  repriced?: boolean;
}

export interface HonourState {
  /** When the quote stops being held; absent when it never was. */
  honourUntil?: string;
  /** Still waiting for the transfer, inside the window: the amount is firm. */
  holding: boolean;
  /** Priced at the live rate instead of the quote: show "Rate updated". */
  rateUpdated: boolean;
}

const STABLE = new Set(['USDC', 'USDT']);

export function honourOf(intent: HonourIntent | null | undefined, now: number = Date.now()): HonourState {
  if (
    !intent ||
    intent.direction !== 'buy' ||
    !STABLE.has((intent.asset ?? '').toUpperCase()) ||
    !intent.rateExpiresAt
  ) {
    return { holding: false, rateUpdated: false };
  }
  return {
    honourUntil: intent.rateExpiresAt,
    holding: intent.status === 'pending_payment' && secondsLeft(intent.rateExpiresAt, now) > 0,
    rateUpdated: !!intent.repriced,
  };
}
