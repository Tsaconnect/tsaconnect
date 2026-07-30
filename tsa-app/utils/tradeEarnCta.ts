// utils/tradeEarnCta.ts
//
// The two CTAs at the foot of Trade & Earn. Both used to point at routes that
// were wrong in a way nothing on screen could reveal:
//
//   "Start Trading Now"     -> /marketplace  (not a route at all — the tab is a
//                              GROUP, so this hit the Unmatched Route screen)
//   "Become a P2P Merchant" -> /services     (same), later /serviceshome, which
//                              is the Services TAB ROOT: not onboarding, and
//                              nothing there to press back on.
//
// The merchant CTA is now role-aware, so its label and target have to move
// together — a merchant seeing "Become a P2P Merchant" and landing on an
// application they already completed is the bug this prevents. Kept pure so the
// pairing is tested without mounting the screen (mirrors utils/pmCta.ts).

// `satisfies Href` on both routes, never `as`: tsc checks each literal against
// the generated route union while keeping its narrow type, so a typo is a build
// error rather than a screen that silently fails to exist. A cast would defeat
// exactly the check this change is about.
import type { Href } from 'expo-router';

/** Where a user who has NOT been approved as a merchant goes to apply. */
export const MERCHANT_ONBOARDING_ROUTE =
  '/merchants/merchant-request?type=p2p_merchant' satisfies Href;

/** Where an existing merchant goes instead — their own ad book. */
export const MERCHANT_ADS_ROUTE = '/p2p/my-ads' satisfies Href;

export interface MerchantCta {
  label: string;
  route: Href;
  /** True until the role resolves; the button must not act on a guess. */
  disabled: boolean;
}

/**
 * Label + destination for the merchant CTA.
 *
 * While the role is still loading the CTA renders in its non-merchant form but
 * is disabled: an approved merchant who taps early would otherwise be sent to
 * the application form.
 */
export function merchantCta(opts: { isMerchant: boolean; loading: boolean }): MerchantCta {
  const isMerchant = !opts.loading && opts.isMerchant;

  return {
    label: isMerchant ? 'Manage Your P2P Ads' : 'Become a P2P Merchant',
    route: isMerchant ? MERCHANT_ADS_ROUTE : MERCHANT_ONBOARDING_ROUTE,
    disabled: opts.loading,
  };
}
