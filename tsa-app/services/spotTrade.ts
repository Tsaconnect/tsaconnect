// services/spotTrade.ts
//
// The single Buy/Sell routing rule for a spot listing. Extracted from SpotList
// so the pair/chain picker routes identically — two copies would drift the
// moment a new route is added.
//
// No swap logic lives here: this only chooses which EXISTING flow to open.
import { router } from 'expo-router';
import type { SpotListing } from '@/services/spotApi';

export function openSpotTrade(listing: SpotListing, side: 'buy' | 'sell') {
  if (listing.route === 'mcgp') {
    // Contract B USDC OTC desk.
    router.push(side === 'buy' ? '/wallet/mcgp/buy' : '/wallet/mcgp/sell');
    return;
  }
  // Generic tokens open the Spot Trade desk — MCGP-style form with both legs'
  // balances, LiFi under the hood (see app/wallet/spot-trade).
  router.push({
    pathname: '/wallet/spot-trade',
    params: {
      side,
      base: listing.baseSymbol,
      quote: listing.quoteSymbol,
      chain: listing.chain,
    },
  } as any);
}
