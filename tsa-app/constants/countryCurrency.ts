// constants/countryCurrency.ts
//
// Country -> currency code, shared by anything that defaults a currency from
// a user's country (KYC profile or device locale). Pulled out of
// CurrencyContext so hooks/services can reuse the same table instead of
// hardcoding their own — a second copy is how these silently drift apart.
export const COUNTRY_CURRENCY_MAP: Record<string, string> = {
  NG: "NGN", GH: "GHS", KE: "KES", ZA: "ZAR", UG: "UGX", TZ: "TZS", RW: "RWF",
  SN: "XOF", CI: "XOF", ML: "XOF", BF: "XOF", BJ: "XOF", TG: "XOF", NE: "XOF",
  CM: "XAF", CF: "XAF", TD: "XAF", GQ: "XAF", GA: "XAF", CG: "XAF",
  GB: "GBP", US: "USD",
  DE: "EUR", FR: "EUR", IT: "EUR", ES: "EUR", NL: "EUR", BE: "EUR",
};

/** Currency for a country code, or null when the country isn't mapped. */
export function currencyForCountry(country?: string | null): string | null {
  if (!country) return null;
  return COUNTRY_CURRENCY_MAP[country.toUpperCase()] ?? null;
}
