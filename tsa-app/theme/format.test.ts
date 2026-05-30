import { formatFiat, formatNumber } from './format';

describe('formatNumber', () => {
  it('groups thousands (en-US locale)', () => {
    expect(formatNumber(1234567, { locale: 'en-US' })).toBe('1,234,567');
  });
  it('respects max fraction digits', () => {
    expect(formatNumber(1234.5678, { locale: 'en-US', maximumFractionDigits: 2 })).toBe('1,234.57');
  });
  it('handles non-finite input safely', () => {
    expect(formatNumber(NaN)).toBe('0');
  });
});

describe('formatFiat', () => {
  it('prefixes the NGN symbol with no fraction by default', () => {
    expect(formatFiat(1500, 'NGN', { locale: 'en-US' })).toBe('₦1,500');
  });
  it('prefixes the USD symbol', () => {
    expect(formatFiat(1234.5, 'USD', { locale: 'en-US' })).toBe('$1,234.5');
  });
  it('accepts numeric strings', () => {
    expect(formatFiat('1500', 'NGN', { locale: 'en-US' })).toBe('₦1,500');
  });
  it('appends ISO code when withCode is set', () => {
    expect(formatFiat(1500, 'NGN', { locale: 'en-US', withCode: true })).toBe('₦1,500 NGN');
  });
  it('falls back to ISO code for unknown currencies', () => {
    expect(formatFiat(1000, 'XOF', { locale: 'en-US' })).toBe('1,000 XOF');
  });
  it('is case-insensitive on the currency code', () => {
    expect(formatFiat(1500, 'ngn', { locale: 'en-US' })).toBe('₦1,500');
  });
});
