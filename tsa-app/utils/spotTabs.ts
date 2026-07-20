// utils/spotTabs.ts
//
// Category-tab filtering for the Spot Trade list (PM taxonomy spec). Admin
// categories come from listing.category; Favorites/Hot/New/Gainers/Losers are
// computed here from the server-enriched fields. The server list is already
// ranked (pins → volume group → mcap), so filters preserve incoming order
// unless the tab itself defines a sort (Hot/New/Gainers/Losers).
import type { SpotListing } from '@/services/spotApi';

export type SpotTabKey =
  | 'all'
  | 'favorites'
  | 'hot'
  | 'alpha'
  | 'tstocks'
  | 'tokenized'
  | 'tradefi'
  | 'new'
  | 'gainers'
  | 'losers';

export const SPOT_TABS: { key: SpotTabKey; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'favorites', label: 'Favorites' },
  { key: 'hot', label: 'Hot' },
  { key: 'alpha', label: 'Alpha' },
  { key: 'tstocks', label: 'tStocks' },
  { key: 'tokenized', label: 'Tokenized' },
  { key: 'tradefi', label: 'Tradefi' },
  { key: 'new', label: 'New' },
  { key: 'gainers', label: 'Gainers' },
  { key: 'losers', label: 'Losers' },
];

export const HOT_COUNT = 10;
export const NEW_WINDOW_DAYS = 14;
const DAY_MS = 24 * 60 * 60 * 1000;

export function filterListingsByTab(
  listings: SpotListing[],
  tab: SpotTabKey,
  favoriteIds: ReadonlySet<string>,
  nowMs: number = Date.now(),
): SpotListing[] {
  switch (tab) {
    case 'all':
      return listings;
    case 'favorites':
      return listings.filter((l) => favoriteIds.has(l.id));
    case 'hot':
      // Top movers by 24h volume. With no volume data at all this degrades to
      // the server's ranked order (nulls sort last, stably).
      return [...listings]
        .sort((a, b) => (b.volume24h ?? -1) - (a.volume24h ?? -1))
        .slice(0, HOT_COUNT);
    case 'alpha':
    case 'tstocks':
    case 'tokenized':
    case 'tradefi':
      return listings.filter((l) => l.category === tab);
    case 'new': {
      const cutoff = nowMs - NEW_WINDOW_DAYS * DAY_MS;
      return listings
        .filter((l) => {
          const t = l.createdAt ? Date.parse(l.createdAt) : NaN;
          return isFinite(t) && t >= cutoff;
        })
        .sort((a, b) => Date.parse(b.createdAt!) - Date.parse(a.createdAt!));
    }
    case 'gainers':
      return listings
        .filter((l) => (l.change24h ?? 0) > 0)
        .sort((a, b) => (b.change24h ?? 0) - (a.change24h ?? 0));
    case 'losers':
      return listings
        .filter((l) => (l.change24h ?? 0) < 0)
        .sort((a, b) => (a.change24h ?? 0) - (b.change24h ?? 0));
    default:
      return listings;
  }
}
