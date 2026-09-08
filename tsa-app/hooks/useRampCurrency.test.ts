jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));

import { resolveRampCurrencyCode } from './useRampCurrency';
import type { RampCurrency } from '@/services/itApi';

const list: RampCurrency[] = [
  { code: 'NGN', exponent: 2, rampProvider: 'paycrest', depositEnabled: true, payoutEnabled: true, minPayoutMinor: 10000 },
  { code: 'GHS', exponent: 2, rampProvider: 'flutterwave', depositEnabled: true, payoutEnabled: true, minPayoutMinor: 0 },
  // Payout-enabled but no live ramp provider — must never be selectable.
  { code: 'KES', exponent: 2, rampProvider: '', depositEnabled: false, payoutEnabled: true, minPayoutMinor: 0 },
];

describe('resolveRampCurrencyCode', () => {
  it('keeps the explicit selection when it still has a provider', () => {
    expect(resolveRampCurrencyCode('GHS', list)).toBe('GHS');
  });

  it('falls through when the explicit selection no longer has a provider', () => {
    expect(resolveRampCurrencyCode('KES', list)).toBe('NGN');
  });

  it("defaults to the user's KYC country currency when it has a provider", () => {
    expect(resolveRampCurrencyCode(null, list, { kycCountry: 'GH' })).toBe('GHS');
  });

  it('falls back to device country when there is no KYC country', () => {
    expect(resolveRampCurrencyCode(null, list, { deviceCountry: 'GH' })).toBe('GHS');
  });

  it('prefers KYC country over device country', () => {
    expect(resolveRampCurrencyCode(null, list, { kycCountry: 'GH', deviceCountry: 'NG' })).toBe('GHS');
  });

  it("never defaults to the country's currency when that currency has no provider", () => {
    // Kenya maps to KES, which is payout-only (no ramp provider) in this table.
    expect(resolveRampCurrencyCode(null, list, { kycCountry: 'KE' })).toBe('NGN');
  });

  it('falls back to NGN when neither country resolves to a ramped currency', () => {
    expect(resolveRampCurrencyCode(null, list, { kycCountry: 'FR' })).toBe('NGN');
  });

  it('falls back to the first ramped currency when NGN has no provider', () => {
    const noNgn = list.filter((c) => c.code !== 'NGN');
    expect(resolveRampCurrencyCode(null, noNgn)).toBe('GHS');
  });

  it('falls back to NGN for an empty list rather than crashing', () => {
    expect(resolveRampCurrencyCode(null, [])).toBe('NGN');
  });

  it('is case-insensitive on the explicit selection', () => {
    expect(resolveRampCurrencyCode('ghs', list)).toBe('GHS');
  });
});
