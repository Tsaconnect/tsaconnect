jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
}));

import {
  distinctAdProviders,
  hasSufficientAllowance,
  previewFiatFromToken,
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
