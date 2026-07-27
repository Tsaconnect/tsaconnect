import type { SpotListing } from '@/services/spotApi';
import {
  assetCategoryOf,
  categoryBySymbol,
  HOT_COUNT,
  NEW_WINDOW_DAYS,
  SPOT_TABS,
  WALLET_CATEGORY_FILTERS,
} from '../spotTabs';

let seq = 0;
function listing(over: Partial<SpotListing>): SpotListing {
  seq += 1;
  return {
    id: over.id ?? `id-${seq}`,
    baseSymbol: over.baseSymbol ?? `TOK${seq}`,
    quoteSymbol: 'USDC',
    chain: 'sonic',
    route: 'lifi',
    enabled: true,
    displayOrder: seq,
    category: 'spot',
    ...over,
  };
}

describe('spot taxonomy registry', () => {
  it('wallet categories: symbol map from listings, unlisted symbols default to spot', () => {
    const rows = [
      listing({ baseSymbol: 'AAPL', category: 'tstocks' }),
      listing({ baseSymbol: 'aapl', category: 'alpha' }), // dup symbol — first (ranked) wins
      listing({ baseSymbol: 'BOND', category: 'tradefi' }),
    ];
    const map = categoryBySymbol(rows);
    expect(assetCategoryOf('aapl', map)).toBe('tstocks');
    expect(assetCategoryOf('BOND', map)).toBe('tradefi');
    expect(assetCategoryOf('MCGP', map)).toBe('spot');
    expect(WALLET_CATEGORY_FILTERS[0]).toEqual({ key: 'all', label: 'All' });
    expect(WALLET_CATEGORY_FILTERS.map((f) => f.label)).toEqual([
      'All',
      'Spot',
      'Alpha',
      'tStocks',
      'Tokenized',
      'Tradefi',
    ]);
  });

  it('tab registry: All first, labels match the PM spec', () => {
    expect(SPOT_TABS[0]).toEqual({ key: 'all', label: 'All' });
    expect(SPOT_TABS.map((t) => t.label)).toEqual([
      'All',
      'Favorites',
      'Hot',
      'Alpha',
      'tStocks',
      'Tokenized',
      'Tradefi',
      'New',
      'Gainers',
      'Losers',
    ]);
  });

  it('tab sizing constants match the PM spec', () => {
    expect(HOT_COUNT).toBe(10);
    expect(NEW_WINDOW_DAYS).toBe(14);
  });
});
