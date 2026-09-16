// services/spotTrade.ts
//
// The single Buy/Sell routing rule for a spot listing. Extracted from SpotList
// so the pair/chain picker routes identically — two copies would drift the
// moment a new route is added.
//
// No swap logic lives here: this only chooses which EXISTING flow to open.
import { router } from 'expo-router';
import type { SpotListing } from '@/services/spotApi';
import { isBitcoinSpotChain } from '@/utils/spotChains';

export function openSpotTrade(listing: SpotListing, side: 'buy' | 'sell') {
  if (listing.route === 'mcgp') {
    // Contract B USDC OTC desk.
    router.push(side === 'buy' ? '/wallet/mcgp/buy' : '/wallet/mcgp/sell');
    return;
  }
  if (listing.route === 'allswap') {
    // A cross-chain pair has no same-chain desk to open: with the legs on two
    // chains there is no single chain to resolve them both against, and a BTC
    // pair is always in that shape because Bitcoin carries one asset. All Swap
    // already quotes and signs across chains, so the listing arrives there with
    // both legs chosen rather than at a form that could only fail at quote time.
    //
    // A quote chain the admin named is binding. Only an auto listing follows
    // the user's balance (see utils/quoteChain), and for it the chain sent here
    // is just the fallback. An empty quoteChain reads as auto too, which is how
    // an API older than the quoteChainAuto flag reports it.
    const auto = listing.quoteChainAuto === true || !listing.quoteChain;
    // Bitcoin carries no stablecoin, so it is never a fallback for the quote leg;
    // All Swap then uses the asset's default.
    const quoteChain = listing.quoteChain || (isBitcoinSpotChain(listing.chain) ? '' : listing.chain);
    router.push({
      pathname: '/easyswap',
      params: {
        tab: 'allswap',
        // Buying the base is paying with the quote leg, which is the same
        // reading of `side` the same-chain desk uses; All Swap's from/to is
        // that pay/receive axis and nothing else.
        fromSymbol: side === 'buy' ? listing.quoteSymbol : listing.baseSymbol,
        fromChain: side === 'buy' ? quoteChain : listing.chain,
        toSymbol: side === 'buy' ? listing.baseSymbol : listing.quoteSymbol,
        toChain: side === 'buy' ? listing.chain : quoteChain,
        ...(auto ? { fundedLeg: side === 'buy' ? 'from' : 'to' } : {}),
      },
    } as any);
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
