// utils/__tests__/otcListings.test.ts
//
// The rules that decide what the OTC tab lists and which buttons are live.
// These are the admin controls that used to be fetched and dropped on the
// floor — logo and category had no effect in the app at all — so the mapping
// from settings to row is worth pinning down.
import {
  otcListingsFrom, listingsForCategory, canTrade, type OtcListing,
} from '../otcListings';
import type { OtcSettings } from '@/services/walletApi';

const settings = (over: Partial<OtcSettings> = {}): OtcSettings => ({
  buyEnabled: true,
  sellEnabled: true,
  logoUrl: '',
  assetClass: '',
  category: 'direct',
  ...over,
});

const only = (s: OtcSettings | null): OtcListing => {
  const list = otcListingsFrom(s);
  expect(list).toHaveLength(1);
  return list[0];
};

describe('otcListingsFrom', () => {
  it('lists the MCGP/USDC desk', () => {
    const l = only(settings());
    expect(l.baseSymbol).toBe('MCGP');
    expect(l.quoteSymbol).toBe('USDC');
    expect(l.pair).toBe('MCGP/USDC');
  });

  it('carries the admin logo and asset class onto the row', () => {
    // Both were fetched and never rendered before — this is the wiring.
    const l = only(settings({ logoUrl: 'https://cdn/x.png', assetClass: 'RWA' }));
    expect(l.logoUrl).toBe('https://cdn/x.png');
    expect(l.assetClass).toBe('RWA');
  });

  it('still lists the desk, open, when settings have not loaded', () => {
    // getOtcSettings fails OPEN; a blank marketplace would read as an outage.
    const l = only(null);
    expect(l.buyEnabled).toBe(true);
    expect(l.sellEnabled).toBe(true);
    expect(l.category).toBe('direct');
    expect(l.logoUrl).toBe('');
  });

  it('closes only the direction the admin paused', () => {
    expect(only(settings({ buyEnabled: false })).buyEnabled).toBe(false);
    expect(only(settings({ buyEnabled: false })).sellEnabled).toBe(true);
    expect(only(settings({ sellEnabled: false })).sellEnabled).toBe(false);
    expect(only(settings({ sellEnabled: false })).buyEnabled).toBe(true);
  });

  it('treats an unknown category as direct rather than hiding the desk', () => {
    // A category the app does not know would otherwise match no sub-tab and
    // make the whole marketplace look empty.
    expect(only(settings({ category: 'something-new' })).category).toBe('direct');
    expect(only(settings({ category: undefined as never })).category).toBe('direct');
  });

  it('honours a presale category', () => {
    expect(only(settings({ category: 'presale' })).category).toBe('presale');
  });
});

describe('listingsForCategory', () => {
  it('places the desk under the sub-tab the admin chose', () => {
    const presale = otcListingsFrom(settings({ category: 'presale' }));
    expect(listingsForCategory(presale, 'presale')).toHaveLength(1);
    expect(listingsForCategory(presale, 'direct')).toHaveLength(0);

    const direct = otcListingsFrom(settings({ category: 'direct' }));
    expect(listingsForCategory(direct, 'direct')).toHaveLength(1);
    expect(listingsForCategory(direct, 'presale')).toHaveLength(0);
  });
});

describe('canTrade', () => {
  it('gates each side on its own switch', () => {
    const l = only(settings({ buyEnabled: false }));
    expect(canTrade(l, 'buy')).toBe(false);
    expect(canTrade(l, 'sell')).toBe(true);
  });
});
