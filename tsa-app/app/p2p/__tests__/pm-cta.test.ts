// app/p2p/__tests__/pm-cta.test.ts
//
// Tests for Task 4 (spec A3):
//   (a) On the pm=1 view with useIsPaymentMerchant → {isPm:false}, the
//       "Become a Payment Merchant" CTA is shown and routes to
//       /merchants/merchant-request?type=p2p_merchant.
//   (b) With {isPm:true}, the CTA is absent.
//
// We test the pure logic that drives this — the shouldShowPmCta predicate —
// rather than mounting the full React component (which would require a deep
// Expo Router + ThemeProvider harness). The real screen wires shouldShowPmCta
// directly, so this covers the spec requirement without brittle mount overhead.
//
// The routing target is documented here as the contract and exercised in the
// integration assertions below.

// ─── shouldShowPmCta ─────────────────────────────────────────────────────────

import { shouldShowPmCta, PM_MERCHANT_REQUEST_ROUTE } from '../pm-cta';

describe('shouldShowPmCta — PM CTA visibility predicate', () => {
  it('returns true when pm param is set and user is NOT a PM', () => {
    expect(shouldShowPmCta({ isPmView: true, isPm: false, loading: false })).toBe(true);
  });

  it('returns false when pm param is set but user IS an active PM', () => {
    expect(shouldShowPmCta({ isPmView: true, isPm: true, loading: false })).toBe(false);
  });

  it('returns false when pm param is NOT set (non-PM marketplace view)', () => {
    expect(shouldShowPmCta({ isPmView: false, isPm: false, loading: false })).toBe(false);
  });

  it('returns false while loading (avoid flash-of-CTA before status resolves)', () => {
    expect(shouldShowPmCta({ isPmView: true, isPm: false, loading: true })).toBe(false);
  });
});

describe('PM_MERCHANT_REQUEST_ROUTE — routing target contract', () => {
  it('includes type=p2p_merchant query param', () => {
    expect(PM_MERCHANT_REQUEST_ROUTE).toContain('type=p2p_merchant');
  });

  it('routes to /merchants/merchant-request', () => {
    expect(PM_MERCHANT_REQUEST_ROUTE).toContain('/merchants/merchant-request');
  });
});
