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
  // Every other listing opens the Spot Trade desk — the MCGP-style form with
  // the pair's balances and one amount box, LiFi under the hood (see
  // app/wallet/spot-trade). A cross-chain listing (route `allswap`, e.g. any
  // BTC pair: Bitcoin carries one asset) also names where its quote leg lives.
  //
  // A quote chain the admin named is binding. Only an auto listing follows the
  // user's balance (services/quoteChain), and for it the chain sent here is
  // just the fallback. An empty quoteChain reads as auto too, which is how an
  // API older than the quoteChainAuto flag reports it.
  const crossChain = listing.route === 'allswap';
  const auto = crossChain && (listing.quoteChainAuto === true || !listing.quoteChain);
  router.push({
    pathname: '/wallet/spot-trade',
    params: {
      side,
      base: listing.baseSymbol,
      quote: listing.quoteSymbol,
      chain: listing.chain,
      ...(crossChain ? { quoteChain: listing.quoteChain || '' } : {}),
      ...(auto ? { quoteAuto: '1' } : {}),
    },
  } as any);
}
