// app/wallet/it/currency-display.ts
//
// Pure helpers for showing IT (Instant Transfer) fiat amounts in the user's
// selected display currency (task 1.1, CurrencyContext wiring).
//
// IT is priced and settled in Naira on the backend — the buy/sell rate is a
// live NGN-per-MCGP figure, and payment instructions/payouts move real Naira
// via a Nigerian bank transfer (Nomba). Those authoritative amounts must stay
// in Naira regardless of the user's selected display currency, or the app
// would risk instructing a wrong-currency bank transfer. These helpers only
// compute an *informational* USD-anchored conversion (for use with
// CurrencyContext's `formatPrice`) so the app can additionally show fiat
// amounts in the selected currency without ever changing the authoritative
// Naira figure or any amount sent to the backend.

/**
 * Converts a Naira amount to its USD-anchored equivalent using the NGN
 * exchange rate (1 USD = X NGN, i.e. CurrencyContext's
 * `allRates['NGN']?.midRate`). Returns null when the rate isn't
 * loaded/invalid so callers can hide the secondary line instead of showing a
 * bogus or divide-by-zero value.
 */
export function ngnToUsd(
  ngnAmount: number,
  ngnRatePerUsd: number | null | undefined,
): number | null {
  if (!Number.isFinite(ngnAmount)) return null;
  if (ngnRatePerUsd == null || !Number.isFinite(ngnRatePerUsd) || ngnRatePerUsd <= 0) return null;
  return ngnAmount / ngnRatePerUsd;
}

/**
 * True when a secondary "≈ {selected currency}" hint should render next to
 * an authoritative Naira amount — i.e. whenever the user's selected display
 * currency isn't NGN itself (showing "≈ ₦X" next to "₦X" would just be
 * noise).
 */
export function shouldShowNgnSecondary(selectedCurrencyCode: string): boolean {
  return selectedCurrencyCode.toUpperCase() !== 'NGN';
}
