// utils/__tests__/currencyDisplay.test.ts
//
// Tests for the pure helpers that wire the IT (Instant Transfer) screens to
// CurrencyContext (task 1.1). IT is priced and settled in Naira on the
// backend — bank transfers via Nomba only move NGN — so these helpers never
// replace the authoritative Naira amount; they only compute a secondary,
// informational USD-anchored conversion (ngnToUsd) and decide when it's
// worth showing (shouldShowNgnSecondary).

import { ngnToUsd, shouldShowNgnSecondary } from '@/utils/currencyDisplay';

describe('ngnToUsd', () => {
  it('converts a Naira amount to USD using the NGN rate (1 USD = X NGN)', () => {
    // 1 USD = 1600 NGN -> 16,000 NGN = $10
    expect(ngnToUsd(16000, 1600)).toBeCloseTo(10, 6);
  });

  it('returns null when the NGN rate is missing (rates not loaded yet)', () => {
    expect(ngnToUsd(16000, null)).toBeNull();
    expect(ngnToUsd(16000, undefined)).toBeNull();
  });

  it('returns null when the NGN rate is zero or negative (guards divide-by-zero)', () => {
    expect(ngnToUsd(16000, 0)).toBeNull();
    expect(ngnToUsd(16000, -1)).toBeNull();
  });

  it('returns null for a non-finite Naira amount', () => {
    expect(ngnToUsd(NaN, 1600)).toBeNull();
    expect(ngnToUsd(Infinity, 1600)).toBeNull();
  });
});

describe('shouldShowNgnSecondary', () => {
  it('returns false when the selected currency is NGN (would just repeat the primary line)', () => {
    expect(shouldShowNgnSecondary('NGN')).toBe(false);
    expect(shouldShowNgnSecondary('ngn')).toBe(false); // case-insensitive
  });

  it('returns true for any non-NGN selected currency', () => {
    expect(shouldShowNgnSecondary('USD')).toBe(true);
    expect(shouldShowNgnSecondary('GHS')).toBe(true);
  });
});
