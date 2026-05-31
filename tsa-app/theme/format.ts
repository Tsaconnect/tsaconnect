// Locale-aware number / money formatting for the design system. Replaces the
// hardcoded en-US formatting scattered across P2P screens. Uses Intl.NumberFormat
// (available in Hermes on Expo SDK 54) with a manual fallback if Intl is unavailable.

// Common fiat symbols. Intl currency-display is inconsistent across Hermes ICU
// builds, so we group the number locale-aware and prepend a known symbol, falling
// back to the ISO code. Extend as new currencies are supported.
const CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$',
  NGN: '₦',
  GHS: '₵',
  KES: 'KSh',
  ZAR: 'R',
  GBP: '£',
  EUR: '€',
  CAD: 'C$',
  USDT: '',
  USDC: '',
};

function fallbackGroup(value: number, maxFrac: number): string {
  const fixed = value.toFixed(maxFrac);
  const [intPart, frac] = fixed.split('.');
  const grouped = intPart.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  return frac && Number(frac) !== 0 ? `${grouped}.${frac.replace(/0+$/, '')}` : grouped;
}

export interface FormatNumberOptions {
  locale?: string; // undefined => device locale
  minimumFractionDigits?: number;
  maximumFractionDigits?: number;
}

/** Locale-aware grouped number. */
export function formatNumber(value: number, opts: FormatNumberOptions = {}): string {
  const { locale, minimumFractionDigits = 0, maximumFractionDigits = 2 } = opts;
  if (!isFinite(value)) return '0';
  try {
    return new Intl.NumberFormat(locale, {
      minimumFractionDigits,
      maximumFractionDigits,
    }).format(value);
  } catch {
    return fallbackGroup(value, maximumFractionDigits);
  }
}

export interface FormatFiatOptions {
  locale?: string;
  /** Append the ISO code (e.g. "₦1,500 NGN"). */
  withCode?: boolean;
  /** Force a minimum number of fraction digits (e.g. 2 to always show cents). */
  minimumFractionDigits?: number;
  /** Override fraction digits (defaults: 0 for whole-unit fiat like NGN, 2 otherwise). */
  maximumFractionDigits?: number;
}

/**
 * Format a fiat amount with a leading currency symbol (or ISO code fallback),
 * locale-aware grouping. `amount` may be a number or numeric string.
 */
export function formatFiat(
  amount: number | string,
  currency: string,
  opts: FormatFiatOptions = {}
): string {
  const value = typeof amount === 'string' ? Number(amount) : amount;
  const code = (currency || '').toUpperCase();
  const symbol = CURRENCY_SYMBOLS[code];
  const maxFrac = opts.maximumFractionDigits ?? (code === 'NGN' ? 0 : 2);
  const minFrac = Math.min(opts.minimumFractionDigits ?? 0, maxFrac);
  const num = formatNumber(isFinite(value) ? value : 0, {
    locale: opts.locale,
    minimumFractionDigits: minFrac,
    maximumFractionDigits: maxFrac,
  });
  const hasSymbol = symbol !== undefined && symbol !== '';
  const prefix = hasSymbol ? symbol : '';
  // No symbol (unknown currency or symbol-less codes like USDT/USDC) -> suffix the code.
  const codeSuffix = opts.withCode || !hasSymbol ? ` ${code}` : '';
  return `${prefix}${num}${codeSuffix}`.trim();
}
