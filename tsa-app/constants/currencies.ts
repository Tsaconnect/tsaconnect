// ──────────────────────────────────────────────
// Currency display config
//
// The full list of supported currencies (and most of their metadata) is now
// driven by the backend exchange-rate response. Only USD is hardcoded here as
// an offline/initial-render fallback.
// ──────────────────────────────────────────────

export interface CurrencyConfig {
  code: string; // ISO 4217 currency code (e.g. "NGN")
  symbol: string;
  name: string;
  // Falls back to device locale when absent.
  locale?: string;
  flag?: string;
}

export const DEFAULT_CURRENCY: CurrencyConfig = {
  code: "USD",
  symbol: "$",
  name: "US Dollar",
  locale: "en-US",
  flag: "🇺🇸",
};
