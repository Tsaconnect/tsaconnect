// app/wallet/it/sell-quote.ts
//
// Pure helpers for the IT sell screen's currency/rail picker + live FX quote
// (Task 2.6). Extracted so the money-affecting decisions — which destination
// the picker defaults to, whether a quote is needed for the selected
// currency, how the "you'll receive" line reads, and whether Confirm should
// be blocked — are unit-testable without mounting the screen (this repo has
// no @testing-library/react-native).

import { parseUnits } from 'ethers';

/** One entry from GET /api/it/withdraw/destinations. */
export interface WithdrawDestination {
  currency: string;
  rail: string; // "bank" | "mobile_money"
  /** mobile_money only, e.g. "wave". */
  network?: string;
  label: string;
}

/** GET /api/it/withdraw/quote response. */
export interface WithdrawQuote {
  destCurrency: string;
  destAmount: number;
  rate: number;
  stale: boolean;
}

/**
 * NGN is IT's source currency (the sell rate already converts MCGP → NGN),
 * so it never needs — and the backend never returns — an FX quote. Every
 * other catalog currency does.
 */
export function needsQuote(currencyCode: string): boolean {
  return currencyCode.trim().toUpperCase() !== 'NGN';
}

/**
 * Chooses which catalog entry the destination picker should default to:
 *   1. the entry matching the user's display currency, if the catalog offers it
 *   2. else the NGN entry, if present
 *   3. else the first catalog entry
 * Returns null only when the catalog itself is empty (still loading / outage) —
 * callers should keep the picker in a loading/disabled state in that case.
 */
export function pickDefaultDestination(
  displayCurrencyCode: string,
  catalog: WithdrawDestination[],
): WithdrawDestination | null {
  if (catalog.length === 0) return null;
  const upper = displayCurrencyCode.trim().toUpperCase();
  return (
    catalog.find((d) => d.currency.toUpperCase() === upper) ??
    catalog.find((d) => d.currency.toUpperCase() === 'NGN') ??
    catalog[0]
  );
}

export interface QuoteDisplayState {
  /** The currently selected destination currency (ISO 4217). */
  currency: string;
  quote: WithdrawQuote | null;
  loading: boolean;
  /** User-facing message when the last fetch failed (e.g. 503/400), else null. */
  error: string | null;
}

/**
 * The "you'll receive ≈ X CCY" row text, or a status string while
 * loading/errored. Empty string for NGN — no quote is fetched or shown for
 * the source currency.
 */
export function formatReceiveEstimate(state: QuoteDisplayState): string {
  if (!needsQuote(state.currency)) return '';
  if (state.loading) return 'Fetching live rate…';
  if (state.error) return state.error;
  if (!state.quote) return '';
  const amount = state.quote.destAmount.toLocaleString(undefined, {
    maximumFractionDigits: 2,
  });
  return `≈ ${amount} ${state.quote.destCurrency}`;
}

/**
 * True when Confirm should be disabled purely because of the quote's state:
 * no quote loaded yet, mid-fetch, errored (400/503), or the quote came back
 * flagged stale. NGN never blocks on this — no quote is fetched for it.
 */
export function isSellBlockedByQuote(state: QuoteDisplayState): boolean {
  if (!needsQuote(state.currency)) return false;
  if (state.loading || state.error || !state.quote) return true;
  return state.quote.stale;
}

export interface MobileMoneyFields {
  mobileCountry: string;
  mobileMsisdn: string;
}

/**
 * Mirrors the backend's validateSellBank requirement for the mobile_money
 * rail (network + country + phone all non-empty). Network always comes from
 * the selected catalog entry, so only country + phone are user input here.
 */
export function isMobileMoneyDestinationComplete(fields: MobileMoneyFields): boolean {
  return fields.mobileCountry.trim().length > 0 && fields.mobileMsisdn.trim().length > 0;
}

/**
 * MCGP amount (decimal string, as typed in the amount field) converted to
 * NGN via the sell rate (kobo per 1 MCGP) — this is both what the "you'll
 * receive" figure was already computed from on the legacy NGN-only screen,
 * and the `ngn` parameter the withdraw-quote endpoint expects. Returns 0 for
 * an invalid/empty amount or rate instead of throwing, so callers can gate a
 * quote fetch on `> 0`.
 */
export function mcgpAmountToNgn(mcgpAmount: string, sellRateKobo: string): number {
  const amt = Number(mcgpAmount);
  const rateKobo = Number(sellRateKobo);
  if (!Number.isFinite(amt) || amt <= 0 || !Number.isFinite(rateKobo) || rateKobo <= 0) return 0;
  return amt * (rateKobo / 100);
}

/**
 * True when the currently selected destination has everything the sell
 * submission needs to be fulfillable: a destination must be selected, and
 * for mobile_money the contact fields must be filled (the server rejects an
 * incomplete mobile_money destination outright). Bank-rail fields stay
 * optional client-side — the server falls back to the manual payout queue
 * when they're blank, same as the pre-existing legacy NGN flow.
 */
export function isDestinationReadyToSubmit(
  destination: WithdrawDestination | null,
  mobileFields: MobileMoneyFields,
): boolean {
  if (!destination) return false;
  if (destination.rail === 'mobile_money') {
    return isMobileMoneyDestinationComplete(mobileFields);
  }
  return true;
}

// ─── Two-step MCGP sell (amount → payout) ─────────────────────────────────
//
// Step 1 prices the sale through GET /api/it/sell/quote in either direction;
// these helpers turn what the user typed into that request and render what
// comes back. Nothing here computes a fee or inverts one — the server owns the
// pricing, so the app can never show a figure the sell would not charge.

/** Which unit the amount field is in. `ngn` means what the user RECEIVES in
 *  their bank, after fees (the owner's call, same as the Paycrest sell). */
export type SellAmountMode = 'mcgp' | 'ngn';

export type SellQuoteRequest =
  | { ok: true; mcgpAmount: string }
  | { ok: true; receiveKobo: string }
  /** error null ⇒ nothing to price yet (empty/zero) — say nothing. */
  | { ok: false; error: string | null };

/**
 * The quote request for what the user typed: exact wei for MCGP, exact kobo
 * for naira. Parsed as decimal strings (never via floats) so ₦1,000.30 is
 * 100030 kobo, not 100029.
 */
export function sellQuoteRequest(mode: SellAmountMode, input: string): SellQuoteRequest {
  const raw = input.trim();
  if (!raw || !(Number(raw) > 0)) return { ok: false, error: null };
  try {
    if (mode === 'mcgp') {
      const wei = parseUnits(raw, 18);
      return wei > 0n ? { ok: true, mcgpAmount: wei.toString() } : { ok: false, error: null };
    }
    const kobo = parseUnits(raw, 2);
    return kobo > 0n ? { ok: true, receiveKobo: kobo.toString() } : { ok: false, error: null };
  } catch {
    return {
      ok: false,
      error: mode === 'ngn' ? 'Use at most 2 decimal places' : 'Invalid amount',
    };
  }
}

/** True when the sale needs more MCGP than the user can spend. Fails closed:
 *  an unreadable figure is treated as over the balance, never as fine. */
export function exceedsSpendable(mcgpWei: string, spendableWei: string): boolean {
  try {
    if (!spendableWei) return true;
    return BigInt(mcgpWei) > BigInt(spendableWei);
  } catch {
    return true;
  }
}

function groupThousands(whole: string): string {
  return whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

/** Kobo (integer string) as "₦12,345.67". Integer math — no float rounding. */
export function formatKobo(kobo: string): string {
  let v: bigint;
  try {
    if (!kobo) return '—';
    v = BigInt(kobo);
  } catch {
    return '—';
  }
  const neg = v < 0n;
  if (neg) v = -v;
  const whole = (v / 100n).toString();
  const frac = (v % 100n).toString().padStart(2, '0');
  return `${neg ? '−' : ''}₦${groupThousands(whole)}.${frac}`;
}

/**
 * MCGP wei for display, at most `maxDp` decimals, rounded UP — the figure is
 * what leaves the user's balance, so it may overstate by a hair but never
 * understate. Trailing zeros trimmed, thousands grouped.
 */
export function formatMcgp(wei: string, maxDp = 6): string {
  let v: bigint;
  try {
    v = BigInt(wei);
  } catch {
    return '—';
  }
  const unit = 10n ** BigInt(18 - maxDp);
  const scaled = (v + unit - 1n) / unit; // ceil to maxDp decimals
  const base = 10n ** BigInt(maxDp);
  const whole = (scaled / base).toString();
  const frac = (scaled % base).toString().padStart(maxDp, '0').replace(/0+$/, '');
  return frac ? `${groupThousands(whole)}.${frac}` : groupThousands(whole);
}

/** The two figures a sell quote commits to. */
export interface SellQuoteFigures {
  mcgpAmount: string; // wei
  netKobo: string;
}

/**
 * What changed between the quote the user agreed to and a fresh one, as the
 * note to show them — or null when nothing did. The sell re-prices at submit,
 * so a move is surfaced for a second look rather than applied silently.
 */
export function sellQuoteMoved(agreed: SellQuoteFigures, fresh: SellQuoteFigures): string | null {
  if (agreed.netKobo !== fresh.netKobo) {
    return `The rate moved: you now receive ${formatKobo(fresh.netKobo)} (was ${formatKobo(agreed.netKobo)}).`;
  }
  if (agreed.mcgpAmount !== fresh.mcgpAmount) {
    return (
      `The rate moved: you now sell ${formatMcgp(fresh.mcgpAmount)} MCGP ` +
      `(was ${formatMcgp(agreed.mcgpAmount)} MCGP) to receive ${formatKobo(fresh.netKobo)}.`
    );
  }
  return null;
}
