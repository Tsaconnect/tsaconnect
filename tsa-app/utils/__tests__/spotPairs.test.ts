import type { SpotListing } from '@/services/spotApi';
import { groupListingsBySymbol, listingsForSymbol } from '../spotPairs';

let seq = 0;
function listing(over: Partial<SpotListing>): SpotListing {
  seq += 1;
  return {
    id: over.id ?? `id-${seq}`,
    baseSymbol: over.baseSymbol ?? `TOK${seq}`,
    quoteSymbol: over.quoteSymbol ?? 'USDC',
    chain: over.chain ?? 'sonic',
    route: over.route ?? 'lifi',
    enabled: true,
    displayOrder: seq,
    category: 'spot',
    ...over,
  };
}

const idsOf = (rows: SpotListing[]) => rows.map((r) => r.id);

describe('listingsForSymbol', () => {
  it('returns every listing for the symbol, preserving server order', () => {
    const rows = [
      listing({ id: 'eth-usdc-eth', baseSymbol: 'ETH', chain: 'ethereum' }),
      listing({ id: 'mcgp', baseSymbol: 'MCGP' }),
      listing({ id: 'eth-usdt-arb', baseSymbol: 'ETH', quoteSymbol: 'USDT', chain: 'arbitrum' }),
    ];
    expect(idsOf(listingsForSymbol(rows, 'ETH'))).toEqual(['eth-usdc-eth', 'eth-usdt-arb']);
  });

  it('matches case-insensitively on the base symbol only', () => {
    const rows = [
      listing({ id: 'a', baseSymbol: 'ETH' }),
      // ETH as the QUOTE side must not be treated as an ETH listing.
      listing({ id: 'b', baseSymbol: 'MCGP', quoteSymbol: 'ETH' }),
    ];
    expect(idsOf(listingsForSymbol(rows, 'eth'))).toEqual(['a']);
  });

  it('returns an empty array for an unlisted symbol', () => {
    expect(listingsForSymbol([listing({ baseSymbol: 'ETH' })], 'DOGE')).toEqual([]);
  });

  it('tolerates an empty listing set', () => {
    expect(listingsForSymbol([], 'ETH')).toEqual([]);
  });
});

describe('groupListingsBySymbol', () => {
  it('buckets listings under their uppercased base symbol', () => {
    const rows = [
      listing({ id: 'a', baseSymbol: 'ETH', chain: 'ethereum' }),
      listing({ id: 'b', baseSymbol: 'eth', chain: 'arbitrum' }),
      listing({ id: 'c', baseSymbol: 'MCGP' }),
    ];
    const grouped = groupListingsBySymbol(rows);
    expect([...grouped.keys()].sort()).toEqual(['ETH', 'MCGP']);
    expect(idsOf(grouped.get('ETH')!)).toEqual(['a', 'b']);
    expect(idsOf(grouped.get('MCGP')!)).toEqual(['c']);
  });
});
