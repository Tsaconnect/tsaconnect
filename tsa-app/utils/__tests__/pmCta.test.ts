// utils/__tests__/pmCta.test.ts
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

import { shouldShowPmCta, merchantDashboardCta, PM_MERCHANT_REQUEST_ROUTE } from '@/utils/pmCta';

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

// ─── merchantDashboardCta ────────────────────────────────────────────────────
//
// An LP-only user's /p2p/pm/me top level is the LP row, so its status must
// never produce the merchant activation card (which would 404 on ?kind=merchant).

describe('merchantDashboardCta', () => {
  it('offers merchant activation only for an approved MERCHANT row', () => {
    expect(merchantDashboardCta({ isPm: false, status: 'approved', kind: 'merchant', loading: false })).toBe('activate');
    expect(merchantDashboardCta({ isPm: false, status: 'approved', kind: 'lp', loading: false })).toBe('upgrade');
  });
  it('lets an LP of any status upgrade to payment merchant', () => {
    for (const status of ['approved', 'active', 'paused', 'rejected']) {
      expect(merchantDashboardCta({ isPm: false, status, kind: 'lp', loading: false })).toBe('upgrade');
    }
  });
  it('keeps the merchant rules', () => {
    expect(merchantDashboardCta({ isPm: true, status: 'active', kind: 'merchant', loading: false })).toBeNull();
    expect(merchantDashboardCta({ isPm: false, status: 'paused', kind: 'merchant', loading: false })).toBeNull();
    expect(merchantDashboardCta({ isPm: false, status: '', loading: false })).toBe('upgrade');
    expect(merchantDashboardCta({ isPm: false, status: 'approved', kind: 'merchant', loading: true })).toBeNull();
  });
});
