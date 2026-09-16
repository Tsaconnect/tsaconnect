// services/__tests__/mcgpValuation.test.ts
//
// Pins the bucket split used to keep the USD total honest: only the
// USDC-bought bucket should ever be priced at the USDC quote (see
// getPremiumMcgpAsset in mcgpSwapApi.ts).

import { boughtBase, creditedBase } from '../mcgpValuation';

describe('boughtBase', () => {
  it('returns premiumBought when the API provides it', () => {
    expect(
      boughtBase({ totalBase: '10000000000000000000', premiumBought: '3000000000000000000' }),
    ).toBe('3000000000000000000');
  });

  it('falls back to the merged total when premiumBought is absent (older API)', () => {
    expect(boughtBase({ totalBase: '10000000000000000000' })).toBe('10000000000000000000');
  });

  it('reports zero bought for a wallet that is entirely naira-bought', () => {
    expect(boughtBase({ totalBase: '10000000000000000000', premiumBought: '0' })).toBe('0');
  });
});

describe('creditedBase', () => {
  it('returns premiumCredited when the API provides it', () => {
    expect(
      creditedBase({ totalBase: '10000000000000000000', premiumCredited: '7000000000000000000' }),
    ).toBe('7000000000000000000');
  });

  it('defaults to zero when the API has not deployed the split yet', () => {
    expect(creditedBase({ totalBase: '10000000000000000000' })).toBe('0');
  });
});
