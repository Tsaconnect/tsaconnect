// Refund amounts in the merchant's words. Money is micro-USD in BigInt — never a float —
// mirroring the API's ParseMicroUSD/FormatMicroUSD (pay_money.go).
import type { Refund, RefundStatus } from './api';

const USD = /^(\d+)(?:\.(\d{1,6}))?$/;

/** "12.50" → 12_500_000n; anything else (including blank) → 0n. For the API's own strings. */
export function microOf(s: string): bigint {
  const m = USD.exec(s.trim());
  if (!m) return 0n;
  return BigInt(m[1]) * 1_000_000n + BigInt((m[2] ?? '').padEnd(6, '0'));
}

/** What a merchant typed → micro-USD; not a positive amount with at most 6 decimals → null. */
export function parseUsd(text: string): bigint | null {
  if (!USD.test(text.trim())) return null;
  const micro = microOf(text);
  return micro > 0n ? micro : null;
}

/** 12_500_000n → "12.50": two decimals at least, up to six. */
export function formatUsd(micro: bigint): string {
  const whole = micro / 1_000_000n;
  let frac = (micro % 1_000_000n).toString().padStart(6, '0').replace(/0+$/, '');
  if (frac.length < 2) frac = frac.padEnd(2, '0');
  return `${whole}.${frac}`;
}

/** The naira a refund stands for at the payment's own rate (customer total / cap), in kobo. */
export function nairaForRefund(micro: bigint, customerTotalKobo: number, capMicro: bigint): number | null {
  if (capMicro <= 0n) return null;
  return Number((micro * BigInt(customerTotalKobo)) / capMicro);
}

export function refundAmountError(text: string, remaining: bigint): string | null {
  if (!text.trim()) return 'Enter an amount.';
  const micro = parseUsd(text);
  if (micro === null) return 'Enter an amount in US dollars, like 12.50 (up to 6 decimal places).';
  if (micro > remaining) return `You can refund up to ${formatUsd(remaining)} USD.`;
  return null;
}

/** "12.50 USD" until a token is chosen at send time, then "12.50 USDT" / "12.50 USDC". */
export const refundAmountLabel = (r: Pick<Refund, 'amount' | 'tokenSymbol'>) => `${r.amount} ${r.tokenSymbol || 'USD'}`;

const OPEN: readonly RefundStatus[] = ['requested', 'submitted', 'needs_review'];

/** Still moving: at most one per payment, and nothing else can be asked for meanwhile. */
export const isOpenRefund = (s: RefundStatus) => OPEN.includes(s);

/**
 * Who pays a refund, from where, to where, and how much — said once, in the docs and in the
 * refund dialog alike. The cap is what left the customer, fee included (spec D16).
 */
export const REFUND_TERMS =
  "You, the merchant, pay it from your own TSA wallet in the TSA Connect app, in USDT or USDC, to your customer's TSA Connect wallet. " +
  'You can refund in part or in full, up to what your customer actually paid (which includes any fee they paid); TSA does not return its own fee.';

/** The API's owner-safe reason for a broadcast refused for gas (payRefundBroadcastReason) — the one topping up fixes. */
export const NO_GAS_REASON = 'Not enough gas in your wallet';

/** A refused broadcast, in the words every client uses (plans review I6). */
export const broadcastNote = (reason: string) =>
  `Couldn't broadcast: ${reason} — ${reason === NO_GAS_REASON ? 'top up gas; ' : ''}we retry automatically.`;

/** What an open refund is waiting for, in one line. */
export function waitingNote(r: Refund): string {
  const what = `A refund of ${refundAmountLabel(r)}`;
  if (r.status === 'submitted') {
    return r.lastBroadcastError
      ? `${what} hasn't reached the network yet. ${broadcastNote(r.lastBroadcastError)}`
      : `${what} is on its way — waiting for the network to confirm it.`;
  }
  if (r.status === 'needs_review') return `${what} is being checked by TSA Connect.`;
  return `${what} is waiting for you to approve and send it in the TSA Connect app.`;
}
