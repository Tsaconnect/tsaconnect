jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
}));

import {
  distinctAdProviders,
  exceedsCapacity,
  hasSufficientAllowance,
  previewFiatFromToken,
  withinFiatLimits,
  type P2PAd,
} from './p2p';

function ad(summary: { provider: string; label: string }[]): P2PAd {
  return { paymentMethodSummary: summary } as unknown as P2PAd;
}

describe('distinctAdProviders', () => {
  it('returns deduped providers across ads, order-stable', () => {
    const ads = [
      ad([{ provider: 'bank_transfer', label: 'Bank Transfer' }, { provider: 'opay', label: 'OPay' }]),
      ad([{ provider: 'opay', label: 'OPay' }, { provider: 'paypal', label: 'PayPal' }]),
    ];
    expect(distinctAdProviders(ads)).toEqual([
      { provider: 'bank_transfer', label: 'Bank Transfer' },
      { provider: 'opay', label: 'OPay' },
      { provider: 'paypal', label: 'PayPal' },
    ]);
  });

  it('ignores ads with no summary', () => {
    expect(distinctAdProviders([ad([]), {} as P2PAd])).toEqual([]);
  });

  it('handles a buy-ad fixture (side: buy)', () => {
    const buyAd: P2PAd = {
      id: 'ad-buy-1',
      sellerId: 'merchant-1',
      side: 'buy',
      token: 'USDT',
      chainId: 146,
      amount: '1000000000',
      remaining: '1000000000',
      reserved: '0',
      fiatCurrency: 'NGN',
      rate: '1600',
      rateType: 'fixed',
      paymentMethodIds: ['pm-1'],
      paymentMethodSummary: [{ provider: 'bank_transfer', label: 'Bank Transfer' }],
      status: 'active',
      createdAt: '2026-01-01T00:00:00Z',
      updatedAt: '2026-01-01T00:00:00Z',
    };
    expect(distinctAdProviders([buyAd])).toEqual([
      { provider: 'bank_transfer', label: 'Bank Transfer' },
    ]);
  });
});

describe('hasSufficientAllowance', () => {
  it('returns true when allowance equals needed', () => {
    expect(hasSufficientAllowance('1000000', '1000000')).toBe(true);
  });

  it('returns true when allowance exceeds needed', () => {
    expect(hasSufficientAllowance('2000000', '1000000')).toBe(true);
  });

  it('returns false when allowance is less than needed', () => {
    expect(hasSufficientAllowance('500000', '1000000')).toBe(false);
  });

  it('returns false when allowance is zero', () => {
    expect(hasSufficientAllowance('0', '1000000')).toBe(false);
  });

  it('returns true when both are zero', () => {
    expect(hasSufficientAllowance('0', '0')).toBe(true);
  });

  it('handles large values beyond Number precision correctly (18-decimal token)', () => {
    // 1 MCGP = 1e18. If we need 1.5 MCGP = 1500000000000000000
    const oneAndHalf = '1500000000000000000';
    const two = '2000000000000000000';
    const one = '1000000000000000000';
    expect(hasSufficientAllowance(two, oneAndHalf)).toBe(true);
    expect(hasSufficientAllowance(one, oneAndHalf)).toBe(false);
  });

  it('handles empty strings gracefully', () => {
    expect(hasSufficientAllowance('', '0')).toBe(true);
    expect(hasSufficientAllowance('', '1')).toBe(false);
    expect(hasSufficientAllowance('100', '')).toBe(true);
  });
});

describe('previewFiatFromToken', () => {
  it('computes fiat for USDT (6 decimals)', () => {
    // 1 USDT in base units = 1_000_000; rate = 1600 NGN/USDT
    // expected: 1 × 1600 = 1600.00
    expect(previewFiatFromToken('1000000', '1600', 6)).toBe('1600.00');
  });

  it('computes fractional token amounts', () => {
    // 0.5 USDT = 500_000 base units; rate = 1500
    // expected: 0.5 × 1500 = 750.00
    expect(previewFiatFromToken('500000', '1500', 6)).toBe('750.00');
  });

  it('rounds to 2 decimal places', () => {
    // 1/3 USDT at rate 3: should give ~1.00 but with rounding
    // 333333 base units / 1e6 × 3 = 0.999999
    expect(previewFiatFromToken('333333', '3', 6)).toBe('1.00');
  });

  it('returns 0.00 for zero amount', () => {
    expect(previewFiatFromToken('0', '1600', 6)).toBe('0.00');
  });

  it('returns 0.00 for zero rate', () => {
    expect(previewFiatFromToken('1000000', '0', 6)).toBe('0.00');
  });

  it('returns 0.00 for empty strings', () => {
    expect(previewFiatFromToken('', '', 6)).toBe('0.00');
  });

  it('works for 18-decimal tokens (MCGP)', () => {
    // 1 MCGP = 1e18; rate = 0.001 (very cheap token)
    // 1 × 0.001 = 0.00100 → '0.00'  (rounds to 2dp)
    expect(previewFiatFromToken('1000000000000000000', '0.001', 18)).toBe('0.00');
    // 1000 MCGP at rate 2 = 2000.00
    const thousandMcgp = (BigInt(1000) * BigInt(10) ** BigInt(18)).toString();
    expect(previewFiatFromToken(thousandMcgp, '2', 18)).toBe('2000.00');
  });
});

describe('exceedsCapacity', () => {
  it('returns false when amount equals remaining (exactly fills capacity)', () => {
    expect(exceedsCapacity('1000000', '1000000')).toBe(false);
  });

  it('returns false when amount is less than remaining', () => {
    expect(exceedsCapacity('500000', '1000000')).toBe(false);
  });

  it('returns true when amount is greater than remaining', () => {
    expect(exceedsCapacity('1000001', '1000000')).toBe(true);
  });

  it('returns true when remaining is zero and amount is non-zero', () => {
    expect(exceedsCapacity('1', '0')).toBe(true);
  });

  it('returns false when both are zero', () => {
    expect(exceedsCapacity('0', '0')).toBe(false);
  });

  it('handles large 18-decimal values correctly (BigInt)', () => {
    const oneAndHalfMcgp = '1500000000000000000';
    const oneMcgp = '1000000000000000000';
    const twoMcgp = '2000000000000000000';
    expect(exceedsCapacity(oneAndHalfMcgp, twoMcgp)).toBe(false);
    expect(exceedsCapacity(oneAndHalfMcgp, oneMcgp)).toBe(true);
  });

  it('fails safe (returns true) when values are malformed', () => {
    expect(exceedsCapacity('abc', '1000000')).toBe(true);
    expect(exceedsCapacity('1000000', 'xyz')).toBe(true);
  });
});

describe('withinFiatLimits', () => {
  it('returns true when no limits are set', () => {
    expect(withinFiatLimits('500')).toBe(true);
    expect(withinFiatLimits('500', undefined, undefined)).toBe(true);
  });

  it('returns true when fiat equals the min bound (inclusive)', () => {
    expect(withinFiatLimits('100', '100', '1000')).toBe(true);
  });

  it('returns true when fiat equals the max bound (inclusive)', () => {
    expect(withinFiatLimits('1000', '100', '1000')).toBe(true);
  });

  it('returns false when fiat is below min', () => {
    expect(withinFiatLimits('99', '100', '1000')).toBe(false);
  });

  it('returns false when fiat is above max', () => {
    expect(withinFiatLimits('1001', '100', '1000')).toBe(false);
  });

  it('returns true with only min set and fiat is above it', () => {
    expect(withinFiatLimits('200', '100')).toBe(true);
  });

  it('returns false with only min set and fiat is below it', () => {
    expect(withinFiatLimits('50', '100')).toBe(false);
  });

  it('returns true with only max set and fiat is below it', () => {
    expect(withinFiatLimits('800', undefined, '1000')).toBe(true);
  });

  it('returns false with only max set and fiat is above it', () => {
    expect(withinFiatLimits('1200', undefined, '1000')).toBe(false);
  });

  it('handles empty-string limits as unbounded', () => {
    expect(withinFiatLimits('500', '', '')).toBe(true);
  });

  it('handles large fiat values', () => {
    expect(withinFiatLimits('999999999', '1', '1000000000')).toBe(true);
    expect(withinFiatLimits('1000000001', '1', '1000000000')).toBe(false);
  });

  it('returns false for non-finite fiat input', () => {
    expect(withinFiatLimits('', '100', '1000')).toBe(false);
    expect(withinFiatLimits('abc', '100', '1000')).toBe(false);
  });
});
