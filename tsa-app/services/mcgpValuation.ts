// services/mcgpValuation.ts
//
// Pure split of the merged premium balance into its two on-chain buckets, for
// the wallet asset list / totals. `premiumBalance` is bought + credited;
// pricing the whole thing at the premiumSell USDC quote overstates a naira
// buyer's USD total, since the credited (naira-bought) part has no USDC exit
// (see mcgpSellGuard.ts). Only the bought bucket gets a USDC-derived value —
// the credited quantity is still real MCGP and stays in the balance, it just
// contributes no USD value here (no USD-denominated naira rate is available
// to price it against; see the PR description for why that was left out).

export interface PremiumBuckets {
  /** premiumBalance from /mcgp/balance — bought + credited, 18dp base units. */
  totalBase: string;
  /** premiumBought, when the API provides it (additive field). */
  premiumBought?: string;
  /** premiumCredited, when the API provides it (additive field). */
  premiumCredited?: string;
}

/**
 * The USDC-sellable portion. Falls back to the merged total when the API
 * hasn't been deployed with the split yet (fields absent) — matching the
 * pre-existing (unsplit) behaviour rather than inventing a cap from nothing.
 */
export function boughtBase(b: PremiumBuckets): string {
  return b.premiumBought ?? b.totalBase ?? '0';
}

/**
 * The naira-bought, spend-only portion. Defaults to "0" when the API hasn't
 * deployed the split yet — there is nothing to report as naira-only until it
 * has.
 */
export function creditedBase(b: PremiumBuckets): string {
  return b.premiumCredited ?? '0';
}
