// itApi.ts imports walletApi.ts -> AsyncStorage. Mocked the same way
// stableSellQuote.test.ts / p2p.test.ts do, so this module can load under
// plain Jest (no native module available) without touching real storage.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
}));

import { rampLegsFor, rampProviderFor, type RampCurrency } from '@/services/itApi';

const table: RampCurrency[] = [
  { code: 'NGN', exponent: 2, rampProvider: 'paycrest', depositEnabled: true, payoutEnabled: true, minPayoutMinor: 10000 },
  { code: 'GHS', exponent: 2, rampProvider: 'flutterwave', depositEnabled: true, payoutEnabled: true, minPayoutMinor: 0 },
  { code: 'USD', exponent: 2, rampProvider: '', depositEnabled: false, payoutEnabled: true, minPayoutMinor: 0 },
];

describe('rampProviderFor', () => {
  it('routes each currency to its provider', () => {
    expect(rampProviderFor(table, 'NGN')).toBe('paycrest');
    expect(rampProviderFor(table, 'GHS')).toBe('flutterwave');
  });

  it('reports no provider for a payout-only currency', () => {
    expect(rampProviderFor(table, 'USD')).toBe('');
  });

  // The table arrives over the network. Before it lands, and if it never
  // lands, the screen must not offer a trade it cannot complete.
  it('reports no provider for an unknown code or an empty table', () => {
    expect(rampProviderFor(table, 'XYZ')).toBe('');
    expect(rampProviderFor([], 'NGN')).toBe('');
  });
});

// Direction is a separate question from provider: UGX and TZS are served by
// Paycrest and still cannot be bought, because no provider quotes that side.
const legTable: RampCurrency[] = [
  { code: 'NGN', exponent: 2, rampProvider: 'paycrest', rampBuy: true, rampSell: true, depositEnabled: true, payoutEnabled: true, minPayoutMinor: 10000 },
  { code: 'KES', exponent: 2, rampProvider: 'paycrest', rampBuy: true, rampSell: true, depositEnabled: false, payoutEnabled: true, minPayoutMinor: 0 },
  { code: 'UGX', exponent: 0, rampProvider: 'paycrest', rampBuy: false, rampSell: true, depositEnabled: false, payoutEnabled: true, minPayoutMinor: 0 },
  { code: 'USD', exponent: 2, rampProvider: '', rampBuy: true, rampSell: true, depositEnabled: false, payoutEnabled: true, minPayoutMinor: 0 },
];

describe('rampLegsFor', () => {
  it('reports both directions where both trade', () => {
    expect(rampLegsFor(legTable, 'NGN')).toEqual({ buy: true, sell: true });
    expect(rampLegsFor(legTable, 'KES')).toEqual({ buy: true, sell: true });
  });

  it('reports sell only for an offramp-only currency', () => {
    expect(rampLegsFor(legTable, 'UGX')).toEqual({ buy: false, sell: true });
  });

  // No provider means no trading, whatever the direction fields say — a
  // payout-only currency is not a ramp.
  it('reports no legs without a provider', () => {
    expect(rampLegsFor(legTable, 'USD')).toEqual({ buy: false, sell: false });
  });

  it('reports no legs for an unknown code or an empty table', () => {
    expect(rampLegsFor(legTable, 'XYZ')).toEqual({ buy: false, sell: false });
    expect(rampLegsFor([], 'NGN')).toEqual({ buy: false, sell: false });
  });

  // An older API build sends no direction fields. Reading that as "closed"
  // would blank out the working NGN ramp against any server that has not
  // deployed yet, so absent means both — the order endpoints still refuse a
  // leg no provider can fill.
  it('treats missing direction fields as both directions', () => {
    expect(rampLegsFor(table, 'NGN')).toEqual({ buy: true, sell: true });
  });
});
