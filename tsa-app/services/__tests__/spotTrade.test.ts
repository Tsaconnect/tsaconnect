/* eslint-disable import/first -- jest hoists the mock above the import it
   replaces; writing it first documents which import it stands in for. */

/**
 * Where a Spot listing's Buy/Sell button lands.
 *
 * A cross-chain listing has to reach the desk WITH its quote chain: without it
 * the desk reads the pair as same-chain, which a BTC pair can never be, and
 * fails at quote time — after the user has committed to a trade. So each route
 * is pinned by the destination it produces, not by the branch it takes.
 */
jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn() } }));
// Reached transitively through the Spot API types' module graph.
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(), setItem: jest.fn(), removeItem: jest.fn() },
}));

import { router } from 'expo-router';
import { openSpotTrade } from '../spotTrade';
import type { SpotListing } from '../spotApi';

const push = router.push as jest.Mock;

const listing = (over: Partial<SpotListing>): SpotListing => ({
  id: 'l1',
  baseSymbol: 'BTC',
  quoteSymbol: 'USDT',
  chain: 'btc',
  route: 'allswap',
  enabled: true,
  displayOrder: 0,
  category: 'spot',
  ...over,
});

beforeEach(() => push.mockClear());

describe('openSpotTrade', () => {
  it('sends a cross-chain listing to the Spot desk with both chains, not to All Swap', () => {
    openSpotTrade(listing({ quoteChain: 'bsc' }), 'buy');

    expect(push).toHaveBeenCalledWith({
      pathname: '/wallet/spot-trade',
      params: { side: 'buy', base: 'BTC', quote: 'USDT', chain: 'btc', quoteChain: 'bsc' },
    });
  });

  it('keeps a quote chain the admin named, rather than following the balance', () => {
    openSpotTrade(listing({ quoteChain: 'bsc' }), 'buy');

    const { params } = push.mock.calls[0][0];
    expect(params).not.toHaveProperty('quoteAuto');
  });

  it('follows the balance when the API flags the listing as auto, keeping its chain as the fallback', () => {
    // The API names a default chain for apps that predate auto; the flag is
    // what marks it as only a fallback.
    openSpotTrade(listing({ quoteChain: 'bsc', quoteChainAuto: true }), 'sell');

    expect(push).toHaveBeenCalledWith(expect.objectContaining({
      params: expect.objectContaining({ side: 'sell', chain: 'btc', quoteChain: 'bsc', quoteAuto: '1' }),
    }));
  });

  it('reads an empty quote chain as auto', () => {
    openSpotTrade(listing({ quoteChain: '' }), 'buy');

    expect(push).toHaveBeenCalledWith(expect.objectContaining({
      params: expect.objectContaining({ quoteChain: '', quoteAuto: '1' }),
    }));
  });

  it('leaves a lifi listing on the same-chain desk with no quote chain', () => {
    openSpotTrade(listing({ route: 'lifi', baseSymbol: 'CAKE', chain: 'bsc', quoteChain: 'eth' }), 'sell');

    expect(push).toHaveBeenCalledWith({
      pathname: '/wallet/spot-trade',
      params: { side: 'sell', base: 'CAKE', quote: 'USDT', chain: 'bsc' },
    });
  });

  it('leaves an mcgp listing on the OTC desk', () => {
    openSpotTrade(listing({ route: 'mcgp' }), 'buy');

    expect(push).toHaveBeenCalledWith('/wallet/mcgp/buy');
  });
});
