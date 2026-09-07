// itApi.ts imports walletApi.ts -> AsyncStorage. Mocked the same way
// stableSellQuote.test.ts / p2p.test.ts do, so this module can load under
// plain Jest (no native module available) without touching real storage.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
}));

import { rampProviderFor, type RampCurrency } from '@/services/itApi';

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
