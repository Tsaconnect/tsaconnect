// ──────────────────────────────────────────────
// Price format utilities — handles USD → local currency display
// ──────────────────────────────────────────────

import { CurrencyConfig, DEFAULT_CURRENCY } from "../constants/currencies";

export interface FormatPriceOptions {
  /** The exchange rate (1 USD = X local). If omitted or null, returns USD. */
  rate?: number | null;
  /** Target currency config. Defaults to USD. */
  currency?: CurrencyConfig;
  /** Minimum decimal places (default: 2 for most currencies). */
  minDecimals?: number;
  /** Maximum decimal places (default: 2). */
  maxDecimals?: number;
  /** Show the currency code (e.g. "₦1,500.00 NGN"). Default: false */
  showCode?: boolean;
  /** Abbreviate large numbers (e.g. "₦1.5M"). Default: false */
  compact?: boolean;
}

// ── Micro-price (sub-cent) display ──────────────────────────────────────────
//
// Micro-cap tokens (PEPE, BONK, …) trade at $0.000001-scale; rendering them
// with 2–4 decimals shows "$0.00". Exchanges compress the leading zeros into
// a subscript that counts them: 0.0000012 → "0.0₅12".

/** Upper bound (exclusive) of the micro-price band handled by formatMicroPrice. */
export const MICRO_PRICE_MAX = 0.01;

/** Zeros after the decimal point required before subscript compression kicks in. */
const MICRO_COMPRESSION_MIN_ZEROS = 4;

/** Significant digits shown for micro prices (trailing zeros trimmed). */
const MICRO_SIG_DIGITS = 4;

const SUBSCRIPT_DIGITS = ["₀", "₁", "₂", "₃", "₄", "₅", "₆", "₇", "₈", "₉"];

const toSubscript = (n: number): string =>
  String(n)
    .split("")
    .map((d) => SUBSCRIPT_DIGITS[Number(d)])
    .join("");

/** True when `value` is in the micro-price band: finite and 0 < value < $0.01. */
export function isMicroPrice(value: number): boolean {
  return Number.isFinite(value) && value > 0 && value < MICRO_PRICE_MAX;
}

/**
 * Format a sub-cent price with exchange-style leading-zero compression.
 *
 * Returns the bare number — no currency symbol — so callers can prefix "$",
 * "₦", etc.:
 *
 *   0.0000012   → "0.0₅12"     (5 zeros compressed into the subscript)
 *   0.00001234  → "0.0₄1234"
 *   0.00234     → "0.00234"    (< 4 leading zeros: plain, no compression)
 *
 * Contract:
 * - The micro band is 0 < value < 0.01 (MICRO_PRICE_MAX). Within it at most
 *   4 significant digits are shown, trailing zeros trimmed. Compression only
 *   applies when ≥ 4 zeros follow the decimal point — below that the plain
 *   form is just as short and easier to read ("0.00234", not "0.0₂234").
 * - Out-of-band input (0, negatives, NaN, ±Infinity, values ≥ 0.01) is never
 *   compressed; it falls back to the app's plain style (2 decimals, up to 4
 *   below 1). Call sites guard with isMicroPrice() and keep their existing
 *   formatting, so that fallback is only a safety net.
 * - Digits come from Number.prototype.toPrecision (exact decimal rounding),
 *   then the string is rebuilt manually — scientific notation ("1.2e-7") can
 *   never leak into the UI, and binary noise ("0.0000011999…") can't distort
 *   the digits the way walking a raw toString()/toFixed(20) expansion could.
 */
export function formatMicroPrice(value: number): string {
  if (!isMicroPrice(value)) {
    if (!Number.isFinite(value)) return "0.00";
    return value.toLocaleString("en-US", {
      minimumFractionDigits: 2,
      maximumFractionDigits: Math.abs(value) < 1 ? 4 : 2,
    });
  }

  const rounded = value.toPrecision(MICRO_SIG_DIGITS); // "0.000001200" or "1.200e-7"

  let sig: string; // significant digits, e.g. "1200"
  let zeros: number; // zeros between the decimal point and the first sig digit

  const eIdx = rounded.indexOf("e");
  if (eIdx !== -1) {
    // Exponential form — toPrecision switches to it below 1e-6.
    sig = rounded.slice(0, eIdx).replace(".", "");
    zeros = -Number(rounded.slice(eIdx + 1)) - 1; // "1.200e-7" → 6 zeros
  } else {
    // Fixed form "0.000001200" — count the zeros after "0.".
    const frac = rounded.slice(2);
    let i = 0;
    while (i < frac.length && frac[i] === "0") i++;
    zeros = i;
    sig = frac.slice(i);
  }

  sig = sig.replace(/0+$/, "") || "0";

  // Rounding to 4 sig digits can carry out of the compressed range
  // (0.00999999 → "0.01000"): zeros then lands below the threshold and we
  // fall through to the plain form, which is what should be displayed.
  if (zeros >= MICRO_COMPRESSION_MIN_ZEROS) {
    return `0.0${toSubscript(zeros)}${sig}`;
  }
  return `0.${"0".repeat(zeros)}${sig}`;
}

/**
 * Convert a USD price to local currency and format it.
 *
 * @param usdAmount   - Price in USD
 * @param options     - Formatting options
 * @returns Formatted price string (e.g. "₦161,775.00")
 */
export function formatPrice(usdAmount: number, options: FormatPriceOptions = {}): string {
  const {
    currency = DEFAULT_CURRENCY,
    rate,
    minDecimals,
    maxDecimals,
    showCode = false,
    compact = false,
  } = options;

  // If currency is USD or no rate available, return USD formatted (don't
  // silently apply a 1:1 fallback — that would 100x mis-render NGN etc.).
  if (currency.code === "USD" || rate == null || rate === 0) {
    return formatUSD(usdAmount, { compact, showCode });
  }

  // Convert to local currency
  const localAmount = usdAmount * rate;

  // Micro-cap prices can be sub-cent in local currency too (₦0.00192 would
  // render "₦0.00"). Only auto-compress when the caller didn't pin decimals,
  // and prefix the symbol manually — Intl currency-symbol placement is
  // inconsistent across Hermes ICU builds (same approach as theme/format.ts).
  if (!compact && minDecimals == null && maxDecimals == null && isMicroPrice(localAmount)) {
    const sym = currency.symbol || currency.code;
    const formatted = `${sym}${formatMicroPrice(localAmount)}`;
    return showCode ? `${formatted} ${currency.code}` : formatted;
  }

  // Format with Intl
  try {
    const formatted = new Intl.NumberFormat(currency.locale, {
      style: compact ? "decimal" : "currency",
      currency: compact ? undefined : currency.code,
      minimumFractionDigits: minDecimals ?? (localAmount >= 1 ? 2 : 4),
      maximumFractionDigits: maxDecimals ?? (localAmount >= 1 ? 2 : 4),
      notation: compact ? "compact" : "standard",
      compactDisplay: "short",
    }).format(localAmount);

    if (showCode) {
      return `${formatted} ${currency.code}`;
    }
    return formatted;
  } catch {
    // Fallback for environments without Intl support
    const sym = currency.symbol || currency.code;
    const formatted = localAmount.toLocaleString("en-US", {
      minimumFractionDigits: minDecimals ?? 2,
      maximumFractionDigits: maxDecimals ?? 2,
    });
    return showCode ? `${sym}${formatted} ${currency.code}` : `${sym}${formatted}`;
  }
}

/**
 * Format a USD price (always shows $). `maxDecimals` (default 2) lets price
 * displays show a stablecoin's 0.999851 instead of rounding it to 1.00.
 */
export function formatUSD(
  usdAmount: number,
  options: { compact?: boolean; showCode?: boolean; maxDecimals?: number } = {},
): string {
  const { compact = false, showCode = false, maxDecimals = 2 } = options;

  // Sub-cent prices round to "$0.00" at 2 decimals — use leading-zero
  // compression instead. Compact mode keeps its abbreviation contract.
  if (!compact && isMicroPrice(usdAmount)) {
    const formatted = `$${formatMicroPrice(usdAmount)}`;
    return showCode ? `${formatted} USD` : formatted;
  }

  try {
    const formatted = new Intl.NumberFormat("en-US", {
      style: compact ? "decimal" : "currency",
      currency: compact ? undefined : "USD",
      minimumFractionDigits: compact ? 0 : 2,
      maximumFractionDigits: compact ? 0 : Math.max(2, maxDecimals),
      notation: compact ? "compact" : "standard",
      compactDisplay: "short",
    }).format(usdAmount);

    return showCode ? `${formatted} USD` : formatted;
  } catch {
    return showCode ? `$${usdAmount.toFixed(2)} USD` : `$${usdAmount.toFixed(2)}`;
  }
}

/**
 * Display both USD and local price — useful in product detail / cart.
 * e.g. "₦161,775 ($100.00)"
 */
export function formatPriceDual(
  usdAmount: number,
  rate: number | null | undefined,
  currency: CurrencyConfig,
): { local: string; usd: string; full: string } {
  const local = formatPrice(usdAmount, { rate, currency });
  const usd = formatUSD(usdAmount);
  return {
    local,
    usd,
    full: currency.code === "USD" || rate == null ? usd : `${local} (${usd})`,
  };
}
