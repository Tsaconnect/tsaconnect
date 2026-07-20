import type { SpotListing } from '@/services/spotApi';
import {
  assetCategoryOf,
  categoryBySymbol,
  filterListingsByTab,
  HOT_COUNT,
  SPOT_TABS,
  WALLET_CATEGORY_FILTERS,
} from '../spotTabs';

const NOW = Date.parse('2026-07-20T12:00:00Z');
const daysAgo = (d: number) => new Date(NOW - d * 24 * 60 * 60 * 1000).toISOString();

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

const symbols = (rows: SpotListing[]) => rows.map((r) => r.baseSymbol);

describe('filterListingsByTab', () => {
  it('all: returns the server-ranked list untouched', () => {
    const rows = [listing({ baseSymbol: 'MCGP' }), listing({ baseSymbol: 'BTC' })];
    expect(filterListingsByTab(rows, 'all', new Set(), NOW)).toBe(rows);
  });

  it('favorites: keeps only favorited ids in server order', () => {
    const rows = [
      listing({ id: 'a', baseSymbol: 'MCGP' }),
      listing({ id: 'b', baseSymbol: 'BTC' }),
      listing({ id: 'c', baseSymbol: 'ETH' }),
    ];
    expect(symbols(filterListingsByTab(rows, 'favorites', new Set(['c', 'a']), NOW))).toEqual([
      'MCGP',
      'ETH',
    ]);
  });

  it('hot: top by 24h volume, capped', () => {
    const rows = [
      listing({ baseSymbol: 'LOW', volume24h: 10 }),
      listing({ baseSymbol: 'HIGH', volume24h: 900 }),
      listing({ baseSymbol: 'MID', volume24h: 50 }),
      listing({ baseSymbol: 'NONE', volume24h: null }),
    ];
    expect(symbols(filterListingsByTab(rows, 'hot', new Set(), NOW))).toEqual([
      'HIGH',
      'MID',
      'LOW',
      'NONE',
    ]);
    const many = Array.from({ length: HOT_COUNT + 5 }, (_, i) =>
      listing({ baseSymbol: `T${i}`, volume24h: i }),
    );
    expect(filterListingsByTab(many, 'hot', new Set(), NOW)).toHaveLength(HOT_COUNT);
  });

  it('category tabs: match admin-assigned category only', () => {
    const rows = [
      listing({ baseSymbol: 'AAPL', category: 'tstocks' }),
      listing({ baseSymbol: 'MCGP', category: 'spot' }),
      listing({ baseSymbol: 'DEGEN', category: 'alpha' }),
      listing({ baseSymbol: 'GOLD', category: 'tokenized' }),
      listing({ baseSymbol: 'BOND', category: 'tradefi' }),
    ];
    expect(symbols(filterListingsByTab(rows, 'tstocks', new Set(), NOW))).toEqual(['AAPL']);
    expect(symbols(filterListingsByTab(rows, 'alpha', new Set(), NOW))).toEqual(['DEGEN']);
    expect(symbols(filterListingsByTab(rows, 'tokenized', new Set(), NOW))).toEqual(['GOLD']);
    expect(symbols(filterListingsByTab(rows, 'tradefi', new Set(), NOW))).toEqual(['BOND']);
  });

  it('new: recent listings only, newest first; missing createdAt excluded', () => {
    const rows = [
      listing({ baseSymbol: 'OLD', createdAt: daysAgo(30) }),
      listing({ baseSymbol: 'WEEK', createdAt: daysAgo(7) }),
      listing({ baseSymbol: 'TODAY', createdAt: daysAgo(0) }),
      listing({ baseSymbol: 'UNDATED', createdAt: undefined }),
    ];
    expect(symbols(filterListingsByTab(rows, 'new', new Set(), NOW))).toEqual(['TODAY', 'WEEK']);
  });

  it('gainers/losers: split by 24h change sign, sorted by magnitude', () => {
    const rows = [
      listing({ baseSymbol: 'UP2', change24h: 2 }),
      listing({ baseSymbol: 'DOWN9', change24h: -9 }),
      listing({ baseSymbol: 'UP8', change24h: 8 }),
      listing({ baseSymbol: 'FLAT', change24h: 0 }),
      listing({ baseSymbol: 'DOWN1', change24h: -1 }),
      listing({ baseSymbol: 'NOPRICE', change24h: null }),
    ];
    expect(symbols(filterListingsByTab(rows, 'gainers', new Set(), NOW))).toEqual(['UP8', 'UP2']);
    expect(symbols(filterListingsByTab(rows, 'losers', new Set(), NOW))).toEqual(['DOWN9', 'DOWN1']);
  });

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
});
