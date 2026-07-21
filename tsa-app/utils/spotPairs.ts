// utils/spotPairs.ts
//
// One asset can be listed several times — different quote assets and different
// chains (ETH/USDC on ethereum, ETH/USDT on arbitrum…). These helpers pick the
// venues for a tapped asset so the picker can offer "as it is listed".
//
// Order is never changed: the public listings arrive ranked by the server
// (pins → volume group → mcap), and the picker shows them in that order.
import type { SpotListing } from '@/services/spotApi';

/** Every listing whose BASE side is `symbol` (case-insensitive). */
export function listingsForSymbol(listings: SpotListing[], symbol: string): SpotListing[] {
  const target = symbol.toUpperCase();
  return listings.filter((l) => l.baseSymbol.toUpperCase() === target);
}

/** Listings bucketed by uppercased base symbol, each bucket in server order. */
export function groupListingsBySymbol(listings: SpotListing[]): Map<string, SpotListing[]> {
  const grouped = new Map<string, SpotListing[]>();
  for (const l of listings) {
    const key = l.baseSymbol.toUpperCase();
    const bucket = grouped.get(key);
    if (bucket) bucket.push(l);
    else grouped.set(key, [l]);
  }
  return grouped;
}
