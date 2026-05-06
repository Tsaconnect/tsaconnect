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
 * Format a USD price (always shows $).
 */
export function formatUSD(usdAmount: number, options: { compact?: boolean; showCode?: boolean } = {}): string {
  const { compact = false, showCode = false } = options;

  try {
    const formatted = new Intl.NumberFormat("en-US", {
      style: compact ? "decimal" : "currency",
      currency: compact ? undefined : "USD",
      minimumFractionDigits: compact ? 0 : 2,
      maximumFractionDigits: compact ? 0 : 2,
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
