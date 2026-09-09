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

// Direction is a separate question from provider, and a one-direction screen
// must never be handed a currency that cannot go that way.
//
// The concrete failure: a Ugandan user opens Buy, the resolver picks UGX from
// their KYC country because UGX has a provider, and Paycrest has no UGX onramp
// — so every amount they type fails the quote with a generic "could not fetch a
// rate". Reachable with no currency param at all from the MCGP asset picker.
const legList: RampCurrency[] = [
  { code: 'NGN', exponent: 2, rampProvider: 'paycrest', rampBuy: true, rampSell: true, depositEnabled: true, payoutEnabled: true, minPayoutMinor: 10000 },
  { code: 'KES', exponent: 2, rampProvider: 'paycrest', rampBuy: true, rampSell: true, depositEnabled: false, payoutEnabled: true, minPayoutMinor: 0 },
  { code: 'UGX', exponent: 0, rampProvider: 'paycrest', rampBuy: false, rampSell: true, depositEnabled: false, payoutEnabled: true, minPayoutMinor: 0 },
  { code: 'TZS', exponent: 2, rampProvider: 'paycrest', rampBuy: false, rampSell: true, depositEnabled: false, payoutEnabled: true, minPayoutMinor: 0 },
];

describe('resolveRampCurrencyCode with a direction', () => {
  it('does not default a Ugandan user into a currency that cannot be bought', () => {
    expect(resolveRampCurrencyCode(null, legList, { kycCountry: 'UG', leg: 'buy' })).not.toBe('UGX');
    // Their own currency is still right for the direction that does work.
    expect(resolveRampCurrencyCode(null, legList, { kycCountry: 'UG', leg: 'sell' })).toBe('UGX');
  });

  it('ignores an explicit pick that cannot trade this way', () => {
    // Saved from the hub or a sell screen, then carried onto Buy.
    expect(resolveRampCurrencyCode('UGX', legList, { leg: 'buy' })).not.toBe('UGX');
    expect(resolveRampCurrencyCode('UGX', legList, { leg: 'sell' })).toBe('UGX');
  });

  it('falls back to a currency that does trade this way', () => {
    expect(resolveRampCurrencyCode(null, legList, { kycCountry: 'TZ', leg: 'buy' })).toBe('NGN');
  });

  it('still honours a currency that trades both ways', () => {
    expect(resolveRampCurrencyCode(null, legList, { kycCountry: 'KE', leg: 'buy' })).toBe('KES');
    expect(resolveRampCurrencyCode(null, legList, { kycCountry: 'KE', leg: 'sell' })).toBe('KES');
  });

  // Without a leg the hub gets every tradable currency, because it renders the
  // live direction per row rather than picking one for the whole screen.
  it('is unrestricted when no direction is given', () => {
    expect(resolveRampCurrencyCode('UGX', legList)).toBe('UGX');
  });

  // Against an API build that predates the direction fields, absent means both
  // — the same convention rampLegsFor uses — so a leg filter must not blank out
  // a working ramp.
  it('treats missing direction fields as tradable either way', () => {
    expect(resolveRampCurrencyCode(null, list, { leg: 'buy' })).toBe('NGN');
    expect(resolveRampCurrencyCode('GHS', list, { leg: 'sell' })).toBe('GHS');
  });
});
