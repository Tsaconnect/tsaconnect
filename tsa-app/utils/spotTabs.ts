// utils/spotTabs.ts
//
// The spot taxonomy registry (PM taxonomy spec): the tab list shown above the
// token market list, the wallet's category filter row, and the symbol →
// category map that ties a wallet asset back to its listing.
//
// The filtering itself lives in utils/spotTokens.ts, which operates on tokens
// rather than pairs.
import type { SpotCategory, SpotListing } from '@/services/spotApi';

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

// Wallet asset-category filter row (second dimension next to the chain chips).
// RWA is an OTC-side class, not a wallet filter — rwa-categorized symbols only
// appear under All here.
export const WALLET_CATEGORY_FILTERS: { key: 'all' | SpotCategory; label: string }[] = [
  { key: 'all', label: 'All' },
  { key: 'spot', label: 'Spot' },
  { key: 'alpha', label: 'Alpha' },
  { key: 'tstocks', label: 'tStocks' },
  { key: 'tokenized', label: 'Tokenized' },
  { key: 'tradefi', label: 'Tradefi' },
];

// Builds the SYMBOL → category map from the spot listings (the single source of
// asset taxonomy). First listing per base symbol wins (server order is ranked).
export function categoryBySymbol(listings: SpotListing[]): Map<string, SpotCategory> {
  const map = new Map<string, SpotCategory>();
  for (const l of listings) {
    const sym = l.baseSymbol.toUpperCase();
    if (!map.has(sym) && l.category) map.set(sym, l.category);
  }
  return map;
}

// Category for a wallet asset. Symbols with no spot listing count as plain
// "spot" assets so they never vanish from every tab.
export function assetCategoryOf(symbol: string, map: Map<string, SpotCategory>): SpotCategory {
  return map.get(symbol.toUpperCase()) ?? 'spot';
}

/** Rows the Hot tab keeps. */
export const HOT_COUNT = 10;
/** How long a token counts as newly listed. */
export const NEW_WINDOW_DAYS = 14;
