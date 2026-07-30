// utils/__tests__/tradeEarnCta.test.ts
//
// Regression cover for the three Trade & Earn navigation bugs PM reported:
//
//   1. Both foot CTAs pointed at routes that did not exist ('/marketplace',
//      '/services'), so tapping either dropped the user on expo-router's
//      built-in "Unmatched Route" screen.
//   2. The repaired merchant CTA still went to '/serviceshome' — the Services
//      TAB ROOT, which is neither onboarding nor something you can back out of.
//   3. "Start Trading Now" went to '/products', which renders the SAME screen
//      as the Marketplace tab root, so it stacked a duplicate: back returned to
//      an identical screen, in a tab the user never opened.
//
// These are asserted literally because every one of them fails silently — a
// wrong href here produces a plausible-looking screen, not an error.

import { MARKETPLACE_TAB } from '@/constants/routes';
import {
  MERCHANT_ADS_ROUTE,
  MERCHANT_ONBOARDING_ROUTE,
  merchantCta,
} from '@/utils/tradeEarnCta';

describe('MARKETPLACE_TAB — "Start Trading Now" target', () => {
  it('addresses the marketplace group explicitly', () => {
    expect(MARKETPLACE_TAB).toBe('/(dashboard)/(tabs)/(marketplace)');
  });

  it('is not the bare "/" that app/index.tsx also owns', () => {
    // Group segments are optional when expo-router matches an href, so '/'
    // would be ambiguous between the marketplace index and the app entry.
    expect(MARKETPLACE_TAB).not.toBe('/');
  });

  it('is not /products, which duplicates the tab root when pushed', () => {
    expect(MARKETPLACE_TAB).not.toBe('/products');
  });

  it('is never one of the dead routes that hit Unmatched Route', () => {
    expect(['/marketplace', '/services']).not.toContain(MARKETPLACE_TAB);
  });
});

describe('merchantCta — role-aware merchant button', () => {
  it('sends a non-merchant to P2P merchant onboarding', () => {
    const cta = merchantCta({ isMerchant: false, loading: false });

    expect(cta.label).toBe('Become a P2P Merchant');
    expect(cta.route).toBe(MERCHANT_ONBOARDING_ROUTE);
    expect(cta.disabled).toBe(false);
  });

  it('sends an approved merchant to their ads instead of onboarding', () => {
    const cta = merchantCta({ isMerchant: true, loading: false });

    expect(cta.label).toBe('Manage Your P2P Ads');
    expect(cta.route).toBe(MERCHANT_ADS_ROUTE);
    expect(cta.route).not.toBe(MERCHANT_ONBOARDING_ROUTE);
  });

  it('disables the button until the role resolves', () => {
    // A merchant tapping during the /profile round-trip would otherwise be sent
    // to an application they have already completed.
    expect(merchantCta({ isMerchant: true, loading: true }).disabled).toBe(true);
    expect(merchantCta({ isMerchant: false, loading: true }).disabled).toBe(true);
  });

  it('does not claim merchant status before the role resolves', () => {
    expect(merchantCta({ isMerchant: true, loading: true }).label).toBe('Become a P2P Merchant');
  });

  it('never routes to /serviceshome, the old Services-tab dead end', () => {
    for (const isMerchant of [true, false]) {
      for (const loading of [true, false]) {
        expect(merchantCta({ isMerchant, loading }).route).not.toBe('/serviceshome');
      }
    }
  });

  it('keeps the onboarding route typed as a P2P merchant application', () => {
    expect(MERCHANT_ONBOARDING_ROUTE).toContain('/merchants/merchant-request');
    expect(MERCHANT_ONBOARDING_ROUTE).toContain('type=p2p_merchant');
  });
});
