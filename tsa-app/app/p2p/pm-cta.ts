// app/p2p/pm-cta.ts
//
// Pure helpers for the "Become a Payment Merchant" CTA on the PM marketplace
// view (spec A3). Extracted so they can be unit-tested without mounting React.

/** Routing target for the PM onboarding entry point. */
export const PM_MERCHANT_REQUEST_ROUTE =
  '/merchants/merchant-request?type=p2p_merchant';

export interface PmCtaOptions {
  /** True when the marketplace is in PM mode (pm param set). */
  isPmView: boolean;
  /** True when the current user is an active Payment Merchant. */
  isPm: boolean;
  /** True while the PM status is still loading (avoid flash-of-CTA). */
  loading: boolean;
}

/**
 * Returns true iff the "Become a Payment Merchant" CTA should be shown.
 *
 * Conditions (must ALL hold):
 *   1. We are in PM marketplace mode (pm param set).
 *   2. The user is not already an active PM.
 *   3. The PM status is not still loading (prevents flash then hide).
 */
export function shouldShowPmCta({ isPmView, isPm, loading }: PmCtaOptions): boolean {
  return isPmView && !isPm && !loading;
}
