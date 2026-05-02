// ──────────────────────────────────────────────
// Supported currencies and their metadata
// ──────────────────────────────────────────────

export interface CurrencyConfig {
  code: string;       // ISO 4217 currency code (e.g. "NGN")
  symbol: string;     // Currency symbol (e.g. "₦")
  name: string;       // Full name (e.g. "Nigerian Naira")
  locale: string;     // Intl locale for formatting (e.g. "en-NG")
  flag?: string;      // Optional emoji flag
}

export const SUPPORTED_CURRENCIES: CurrencyConfig[] = [
  { code: "USD", symbol: "$",     name: "US Dollar",          locale: "en-US", flag: "🇺🇸" },
  { code: "NGN", symbol: "₦",     name: "Nigerian Naira",     locale: "en-NG", flag: "🇳🇬" },
  { code: "GHS", symbol: "GH₵",   name: "Ghanaian Cedi",      locale: "en-GH", flag: "🇬🇭" },
  { code: "KES", symbol: "KSh",   name: "Kenyan Shilling",    locale: "en-KE", flag: "🇰🇪" },
  { code: "ZAR", symbol: "R",     name: "South African Rand",  locale: "en-ZA", flag: "🇿🇦" },
  { code: "UGX", symbol: "USh",   name: "Ugandan Shilling",   locale: "en-UG", flag: "🇺🇬" },
  { code: "TZS", symbol: "TSh",   name: "Tanzanian Shilling", locale: "en-TZ", flag: "🇹🇿" },
  { code: "RWF", symbol: "FRw",   name: "Rwandan Franc",      locale: "en-RW", flag: "🇷🇼" },
  { code: "XOF", symbol: "CFA",   name: "West African CFA",   locale: "fr-SN", flag: "🌍" },
  { code: "XAF", symbol: "FCFA",  name: "Central African CFA", locale: "fr-CM", flag: "🌍" },
  { code: "EUR", symbol: "€",     name: "Euro",               locale: "en-DE", flag: "🇪🇺" },
  { code: "GBP", symbol: "£",     name: "British Pound",      locale: "en-GB", flag: "🇬🇧" },
];

// Quick lookup map
export const CURRENCY_MAP: Record<string, CurrencyConfig> = {};
for (const c of SUPPORTED_CURRENCIES) {
  CURRENCY_MAP[c.code] = c;
}

export const DEFAULT_CURRENCY = CURRENCY_MAP.USD;
