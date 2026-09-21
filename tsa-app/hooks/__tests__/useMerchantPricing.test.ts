jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

import {
  localToUsd,
  usdToLocal,
  formatUsdBadge,
  resolveMerchantDefaultCurrency,
  convertCurrencyValue,
  rateFor,
} from '../useMerchantPricing';

describe('Merchant pricing conversion functions', () => {
  describe('localToUsd', () => {
    it('converts NGN local amount to USD using midRate', () => {
      // 1 USD = 1,500 NGN
      expect(localToUsd('15000', 1500)).toBe(10);
      expect(localToUsd('45000', 1500)).toBe(30);
      expect(localToUsd('50000', 1500)).toBe(33.33);
    });

    it('converts UGX local amount to USD using midRate', () => {
      // 1 USD = 3,700 UGX
      expect(localToUsd('37000', 3700)).toBe(10);
      expect(localToUsd('100000', 3700)).toBe(27.03);
    });

    it('handles commas in input string gracefully', () => {
      expect(localToUsd('15,000', 1500)).toBe(10);
      expect(localToUsd('1,000,000', 1500)).toBe(666.67);
    });

    it('returns 0 for invalid, 0, or negative inputs', () => {
      expect(localToUsd('', 1500)).toBe(0);
      expect(localToUsd('0', 1500)).toBe(0);
      expect(localToUsd('-500', 1500)).toBe(0);
      expect(localToUsd('abc', 1500)).toBe(0);
      expect(localToUsd('100', 0)).toBe(0);
      expect(localToUsd('100', null)).toBe(0);
    });

    it('clamps tiny positive fractions to 0.01 minimum USD rather than 0', () => {
      expect(localToUsd('1', 1500)).toBe(0.01);
    });
  });

  describe('usdToLocal', () => {
    it('converts USD amount to local currency string', () => {
      expect(usdToLocal(10, 1500)).toBe('15000');
      expect(usdToLocal('30', 1500)).toBe('45000');
      expect(usdToLocal(33.33, 1500)).toBe('49995');
    });

    it('formats fractional local amounts cleanly without trailing zeros', () => {
      expect(usdToLocal(1.25, 10)).toBe('12.5');
      expect(usdToLocal(1.234, 10)).toBe('12.34');
    });

    it('returns empty string for invalid or zero inputs', () => {
      expect(usdToLocal(0, 1500)).toBe('');
      expect(usdToLocal('', 1500)).toBe('');
      expect(usdToLocal(-10, 1500)).toBe('');
      expect(usdToLocal(10, 0)).toBe('');
      expect(usdToLocal(10, null)).toBe('');
    });
  });

  describe('formatUsdBadge', () => {
    it('returns formatted USD badge when not in USD', () => {
      expect(formatUsdBadge('15000', 1500, false)).toBe('≈ $10.00 USD');
      expect(formatUsdBadge('50000', 1500, false)).toBe('≈ $33.33 USD');
    });

    it('returns empty string when currency is USD', () => {
      expect(formatUsdBadge('35', 1, true)).toBe('');
    });

    it('returns empty string when local amount is empty or 0', () => {
      expect(formatUsdBadge('', 1500, false)).toBe('');
      expect(formatUsdBadge('0', 1500, false)).toBe('');
    });
  });

  describe('resolveMerchantDefaultCurrency', () => {
    const supported = ['USD', 'NGN', 'UGX', 'KES', 'GHS'];

    it("resolves merchant's KYC country when supported", () => {
      expect(resolveMerchantDefaultCurrency('NG', 'USD', supported)).toBe('NGN');
      expect(resolveMerchantDefaultCurrency('UG', 'USD', supported)).toBe('UGX');
      expect(resolveMerchantDefaultCurrency('KE', 'USD', supported)).toBe('KES');
      expect(resolveMerchantDefaultCurrency('GH', 'USD', supported)).toBe('GHS');
    });

    it('falls back to active app currency when KYC country is unmapped or absent', () => {
      expect(resolveMerchantDefaultCurrency(null, 'UGX', supported)).toBe('UGX');
      expect(resolveMerchantDefaultCurrency('XX', 'KES', supported)).toBe('KES');
    });

    it('falls back to NGN when both KYC country and app currency are unavailable', () => {
      expect(resolveMerchantDefaultCurrency(null, null, supported)).toBe('NGN');
    });

    it('never defaults to a currency it cannot price', () => {
      // Rates not loaded yet: only USD is priceable, so NG must not get NGN.
      expect(resolveMerchantDefaultCurrency('NG', 'NGN', ['USD'])).toBe('USD');
    });
  });

  describe('convertCurrencyValue', () => {
    const rates: Record<string, { midRate: number }> = {
      NGN: { midRate: 1500 },
      UGX: { midRate: 3700 },
    };

    it('converts between NGN and USD accurately', () => {
      // 45,000 NGN -> 30 USD
      expect(convertCurrencyValue('45000', 'NGN', 'USD', rates)).toBe('30');
      // 30 USD -> 45,000 NGN
      expect(convertCurrencyValue('30', 'USD', 'NGN', rates)).toBe('45000');
    });

    it('converts cross-currency between NGN and UGX via USD equivalence', () => {
      // 15,000 NGN = $10 USD -> 37,000 UGX
      expect(convertCurrencyValue('15000', 'NGN', 'UGX', rates)).toBe('37000');
    });

    it('clears the value instead of converting at 1:1 when a rate is missing', () => {
      expect(convertCurrencyValue('45000', 'NGN', 'KES', rates)).toBe('');
      expect(convertCurrencyValue('45000', 'KES', 'USD', rates)).toBe('');
    });

    it('preserves empty or invalid inputs', () => {
      expect(convertCurrencyValue('', 'NGN', 'USD', rates)).toBe('');
      expect(convertCurrencyValue('abc', 'NGN', 'USD', rates)).toBe('abc');
    });
  });

  describe('rateFor', () => {
    it('is 1 for USD and null for an unknown or non-positive rate', () => {
      expect(rateFor('USD', {})).toBe(1);
      expect(rateFor('NGN', {})).toBeNull();
      expect(rateFor('NGN', { NGN: { midRate: 0 } })).toBeNull();
      expect(rateFor('NGN', { NGN: { midRate: 1500 } })).toBe(1500);
    });
  });
});
