import type { SpotListing } from '@/services/spotApi';
import {
  buildTokenMarkets,
  filterTokensByTab,
  pinMcgpFirst,
  tokensForTab,
} from '../spotTokens';
import { HOT_COUNT } from '../spotTabs';

const NOW = Date.parse('2026-07-26T12:00:00Z');
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

const symbols = (rows: { symbol: string }[]) => rows.map((r) => r.symbol);

describe('buildTokenMarkets', () => {
  it('collapses a token listed on several pairs into one row, in server order', () => {
    const rows = [
      listing({ baseSymbol: 'MCGP', quoteSymbol: 'USDC', chain: 'sonic' }),
      listing({ baseSymbol: 'BTC', quoteSymbol: 'USDT', chain: 'bsc' }),
      listing({ baseSymbol: 'MCGP', quoteSymbol: 'USDT', chain: 'bsc' }),
    ];
    const tokens = buildTokenMarkets(rows);
    expect(symbols(tokens)).toEqual(['MCGP', 'BTC']);
    expect(tokens[0].listings).toHaveLength(2);
  });

  it('takes price, change, mcap, logo and category from the highest-ranked listing', () => {
    const tokens = buildTokenMarkets([
      listing({
        baseSymbol: 'MCGP',
        price: 0.0138,
        change24h: 1.35,
        marketCap: 5_000,
        logoUrl: 'https://x/mcgp.png',
        category: 'tokenized',
      }),
      listing({ baseSymbol: 'MCGP', price: 99, change24h: -50, marketCap: 1, category: 'alpha' }),
    ]);
    expect(tokens[0]).toMatchObject({
      price: 0.0138,
      change24h: 1.35,
      marketCap: 5_000,
      logoUrl: 'https://x/mcgp.png',
      category: 'tokenized',
    });
  });

  it('sums 24h volume across venues, and stays null when no venue reports volume', () => {
    const [withVol] = buildTokenMarkets([
      listing({ baseSymbol: 'ETH', volume24h: 100 }),
      listing({ baseSymbol: 'ETH', volume24h: 25 }),
      listing({ baseSymbol: 'ETH', volume24h: null }),
    ]);
    expect(withVol.volume24h).toBe(125);

    const [noVol] = buildTokenMarkets([
      listing({ baseSymbol: 'ZZZ', volume24h: null }),
      listing({ baseSymbol: 'ZZZ', volume24h: undefined }),
    ]);
    expect(noVol.volume24h).toBeNull();
  });

  it('dates a token from its earliest listing', () => {
    const [tok] = buildTokenMarkets([
      listing({ baseSymbol: 'SOL', createdAt: daysAgo(3) }),
      listing({ baseSymbol: 'SOL', createdAt: daysAgo(40) }),
      listing({ baseSymbol: 'SOL', createdAt: undefined }),
    ]);
    expect(tok.createdAt).toBe(daysAgo(40));
  });

  it('matches base symbols case-insensitively and normalises the display symbol', () => {
    const tokens = buildTokenMarkets([
      listing({ baseSymbol: 'mcgp' }),
      listing({ baseSymbol: 'MCGP' }),
    ]);
    expect(symbols(tokens)).toEqual(['MCGP']);
    expect(tokens[0].listings).toHaveLength(2);
  });

  it('subtitles a single-venue token with its pair, a multi-venue token with a count', () => {
    const [single] = buildTokenMarkets([
      listing({ baseSymbol: 'BTC', quoteSymbol: 'USDT', chain: 'bsc' }),
    ]);
    expect(single.subtitle).toBe('USDT · bsc');

    const [multi] = buildTokenMarkets([
      listing({ baseSymbol: 'ETH', quoteSymbol: 'USDC' }),
      listing({ baseSymbol: 'ETH', quoteSymbol: 'USDT' }),
    ]);
    expect(multi.subtitle).toBe('2 pairs');
  });

  it('returns an empty list for no listings', () => {
    expect(buildTokenMarkets([])).toEqual([]);
  });
});

describe('filterTokensByTab', () => {
  const tokens = () =>
    buildTokenMarkets([
      listing({ baseSymbol: 'MCGP', volume24h: 10, change24h: 1, createdAt: daysAgo(2) }),
      listing({ baseSymbol: 'BTC', volume24h: 900, change24h: -3, createdAt: daysAgo(90) }),
      listing({ baseSymbol: 'AAPL', category: 'tstocks', volume24h: 50, change24h: 8 }),
    ]);

  it('all: returns the server-ranked tokens untouched', () => {
    const rows = tokens();
    expect(filterTokensByTab(rows, 'all', new Set(), NOW)).toBe(rows);
  });

  it('favorites: keeps favorited SYMBOLS in server order', () => {
    expect(symbols(filterTokensByTab(tokens(), 'favorites', new Set(['AAPL', 'MCGP']), NOW))).toEqual([
      'MCGP',
      'AAPL',
    ]);
  });

  it('favorites: matches case-insensitively', () => {
    expect(symbols(filterTokensByTab(tokens(), 'favorites', new Set(['btc']), NOW))).toEqual(['BTC']);
  });

  it('hot: ranks TOKENS by summed volume and caps the list', () => {
    expect(symbols(filterTokensByTab(tokens(), 'hot', new Set(), NOW))).toEqual([
      'BTC',
      'AAPL',
      'MCGP',
    ]);

    // A token listed on many venues must still occupy exactly one of the slots.
    const many = buildTokenMarkets(
      Array.from({ length: HOT_COUNT + 5 }, (_, i) =>
        listing({ baseSymbol: `T${i}`, volume24h: i }),
      ).concat([listing({ baseSymbol: 'T0', volume24h: 5 })]),
    );
    expect(filterTokensByTab(many, 'hot', new Set(), NOW)).toHaveLength(HOT_COUNT);
  });

  it('category tabs: match the admin-assigned category', () => {
    expect(symbols(filterTokensByTab(tokens(), 'tstocks', new Set(), NOW))).toEqual(['AAPL']);
    expect(filterTokensByTab(tokens(), 'alpha', new Set(), NOW)).toEqual([]);
  });

  it('new: tokens first listed inside the window, newest first', () => {
    expect(symbols(filterTokensByTab(tokens(), 'new', new Set(), NOW))).toEqual(['MCGP']);
  });

  it('gainers/losers: split by 24h change sign, sorted by magnitude', () => {
    expect(symbols(filterTokensByTab(tokens(), 'gainers', new Set(), NOW))).toEqual(['AAPL', 'MCGP']);
    expect(symbols(filterTokensByTab(tokens(), 'losers', new Set(), NOW))).toEqual(['BTC']);
  });
});

describe('pinMcgpFirst', () => {
  it('moves MCGP to the top without disturbing the rest of the order', () => {
    const rows = buildTokenMarkets([
      listing({ baseSymbol: 'BTC' }),
      listing({ baseSymbol: 'ETH' }),
      listing({ baseSymbol: 'MCGP' }),
    ]);
    expect(symbols(pinMcgpFirst(rows))).toEqual(['MCGP', 'BTC', 'ETH']);
  });

  it('is a no-op when MCGP is absent or already first', () => {
    const noMcgp = buildTokenMarkets([listing({ baseSymbol: 'BTC' })]);
    expect(pinMcgpFirst(noMcgp)).toBe(noMcgp);

    const already = buildTokenMarkets([listing({ baseSymbol: 'MCGP' }), listing({ baseSymbol: 'BTC' })]);
    expect(pinMcgpFirst(already)).toBe(already);
  });
});

describe('tokensForTab', () => {
  const rows = () =>
    buildTokenMarkets([
      listing({ baseSymbol: 'BTC', volume24h: 900, change24h: 5, createdAt: daysAgo(1) }),
      listing({ baseSymbol: 'MCGP', volume24h: 10, change24h: -2, createdAt: daysAgo(1) }),
    ]);

  it('pins MCGP first on All and Hot', () => {
    expect(symbols(tokensForTab(rows(), 'all', new Set(), NOW))[0]).toBe('MCGP');
    expect(symbols(tokensForTab(rows(), 'hot', new Set(), NOW))[0]).toBe('MCGP');
  });

  it('leaves the honest sort alone on Losers, Gainers and New', () => {
    // MCGP is the only loser here, so pinning is indistinguishable — use a case
    // where pinning would actually reorder: BTC gains more than MCGP.
    expect(symbols(tokensForTab(rows(), 'gainers', new Set(), NOW))).toEqual(['BTC']);
    expect(symbols(tokensForTab(rows(), 'losers', new Set(), NOW))).toEqual(['MCGP']);
    expect(symbols(tokensForTab(rows(), 'new', new Set(), NOW))).toEqual(['BTC', 'MCGP']);
  });

  it('does not pin MCGP into Favorites when it was not favorited', () => {
    expect(symbols(tokensForTab(rows(), 'favorites', new Set(['BTC']), NOW))).toEqual(['BTC']);
  });
});
