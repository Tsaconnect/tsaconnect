// utils/spotTokens.ts
//
// TOKEN-level view of the spot catalogue (PM: "represent a token, not a pair").
// The API lists PAIRS — MCGP/USDC on sonic and MCGP/USDT on bsc are two rows —
// but Home and Spot Trade show one row per token; tapping it opens the pair
// picker. These helpers do that collapse, then apply the category tabs.
//
// Tabs filter TOKENS, not listings: "Hot" means the ten hottest tokens, so a
// token listed on five venues can't eat five of the ten slots.
import type { SpotCategory, SpotListing } from '@/services/spotApi';
import { groupListingsBySymbol } from '@/utils/spotPairs';
import { HOT_COUNT, NEW_WINDOW_DAYS, type SpotTabKey } from '@/utils/spotTabs';

/** The token whose #1 slot the PM spec guarantees on All and Hot. */
export const PINNED_SYMBOL = 'MCGP';

const DAY_MS = 24 * 60 * 60 * 1000;

export interface TokenMarket {
  /** Uppercased base symbol — the row's identity. */
  symbol: string;
  /** "USDT · bsc" for a single venue, "3 pairs" when listed several times. */
  subtitle: string;
  logoUrl?: string;
  category: SpotCategory;
  /** Quote-side metrics come from the highest-ranked listing… */
  price: number | null;
  change24h: number | null;
  marketCap: number | null;
  /** …except volume, which is the token's liquidity across every venue. */
  volume24h: number | null;
  /** Earliest listing: a token is "new" from when it first appeared. */
  createdAt?: string;
  /** Every venue, in server order — handed straight to AssetPairSheet. */
  listings: SpotListing[];
}

const parseTime = (iso?: string): number => (iso ? Date.parse(iso) : NaN);

function subtitleFor(listings: SpotListing[]): string {
  if (listings.length === 1) return `${listings[0].quoteSymbol} · ${listings[0].chain}`;
  return `${listings.length} pairs`;
}

/**
 * Collapses ranked pair listings into ranked token rows. Order follows each
 * token's first (best-ranked) listing, so the server's ranking — pins, then
 * volume group, then mcap — survives the collapse untouched.
 */
export function buildTokenMarkets(listings: SpotListing[]): TokenMarket[] {
  const tokens: TokenMarket[] = [];

  for (const [symbol, venues] of groupListingsBySymbol(listings)) {
    const primary = venues[0];

    // Null and undefined both mean "no data" here; summing must not turn a
    // fully-unknown volume into 0, or such tokens would outrank real zeroes.
    let volume24h: number | null = null;
    for (const v of venues) {
      if (v.volume24h != null) volume24h = (volume24h ?? 0) + v.volume24h;
    }

    let createdAt: string | undefined;
    let earliest = Infinity;
    for (const v of venues) {
      const t = parseTime(v.createdAt);
      if (isFinite(t) && t < earliest) {
        earliest = t;
        createdAt = v.createdAt;
      }
    }

    tokens.push({
      symbol,
      subtitle: subtitleFor(venues),
      logoUrl: primary.logoUrl,
      category: primary.category,
      price: primary.price ?? null,
      change24h: primary.change24h ?? null,
      marketCap: primary.marketCap ?? null,
      volume24h,
      createdAt,
      listings: venues,
    });
  }

  return tokens;
}

/**
 * Token-level twin of filterListingsByTab. `favoriteSymbols` holds base symbols
 * (favorites moved off listing ids when rows became tokens).
 */
export function filterTokensByTab(
  tokens: TokenMarket[],
  tab: SpotTabKey,
  favoriteSymbols: ReadonlySet<string>,
  nowMs: number = Date.now(),
): TokenMarket[] {
  switch (tab) {
    case 'all':
      return tokens;
    case 'favorites': {
      const wanted = new Set([...favoriteSymbols].map((s) => s.toUpperCase()));
      return tokens.filter((t) => wanted.has(t.symbol));
    }
    case 'hot':
      // Nulls sort last, stably, so a volume-less catalogue degrades to the
      // server's ranked order rather than to noise.
      return [...tokens]
        .sort((a, b) => (b.volume24h ?? -1) - (a.volume24h ?? -1))
        .slice(0, HOT_COUNT);
    case 'alpha':
    case 'tstocks':
    case 'tokenized':
    case 'tradefi':
      return tokens.filter((t) => t.category === tab);
    case 'new': {
      const cutoff = nowMs - NEW_WINDOW_DAYS * DAY_MS;
      return tokens
        .filter((t) => {
          const at = parseTime(t.createdAt);
          return isFinite(at) && at >= cutoff;
        })
        .sort((a, b) => parseTime(b.createdAt) - parseTime(a.createdAt));
    }
    case 'gainers':
      return tokens
        .filter((t) => (t.change24h ?? 0) > 0)
        .sort((a, b) => (b.change24h ?? 0) - (a.change24h ?? 0));
    case 'losers':
      return tokens
        .filter((t) => (t.change24h ?? 0) < 0)
        .sort((a, b) => (a.change24h ?? 0) - (b.change24h ?? 0));
    default:
      return tokens;
  }
}

/** Lifts MCGP to the top, leaving every other row in place. */
export function pinMcgpFirst(tokens: TokenMarket[]): TokenMarket[] {
  const at = tokens.findIndex((t) => t.symbol === PINNED_SYMBOL);
  if (at <= 0) return tokens;
  const pinned = tokens[at];
  return [pinned, ...tokens.slice(0, at), ...tokens.slice(at + 1)];
}

/**
 * Tabs where MCGP holds position #1 per the PM spec. Deliberately excludes the
 * measured tabs — pinning MCGP into Gainers or Losers would misreport its
 * performance, and into Favorites would override the user's own choice.
 */
const PINNED_TABS: ReadonlySet<SpotTabKey> = new Set<SpotTabKey>(['all', 'hot']);

/** Filter for the tab, then apply the MCGP pin where the spec calls for it. */
export function tokensForTab(
  tokens: TokenMarket[],
  tab: SpotTabKey,
  favoriteSymbols: ReadonlySet<string>,
  nowMs: number = Date.now(),
): TokenMarket[] {
  const filtered = filterTokensByTab(tokens, tab, favoriteSymbols, nowMs);
  return PINNED_TABS.has(tab) ? pinMcgpFirst(filtered) : filtered;
}
