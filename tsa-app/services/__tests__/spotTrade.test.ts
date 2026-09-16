/* eslint-disable import/first -- jest hoists the mock above the import it
   replaces; writing it first documents which import it stands in for. */

/**
 * Where a Spot listing's Buy/Sell button lands.
 *
 * The routing rule is the only thing standing between a cross-chain listing
 * and the same-chain desk, which cannot resolve two chains and would fail at
 * quote time — after the user has committed to a trade. So each route is
 * pinned by the destination it produces, not by the branch it takes.
 */
jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn() } }));
// Reached transitively: the Bitcoin check sits in utils/spotChains, beside the
// LiFi chain table, which pulls in the wallet API.
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
  it('sends an allswap listing to the All Swap tab with both legs and both chains', () => {
    openSpotTrade(listing({ quoteChain: 'bsc' }), 'buy');

    expect(push).toHaveBeenCalledWith({
      pathname: '/easyswap',
      params: {
        tab: 'allswap',
        // Buying BTC is paying USDT, so the BSC leg is the one being spent.
        fromSymbol: 'USDT',
        fromChain: 'bsc',
        toSymbol: 'BTC',
        toChain: 'btc',
      },
    });
  });

  it('keeps a quote chain the admin named, rather than following the balance', () => {
    openSpotTrade(listing({ quoteChain: 'bsc' }), 'buy');

    const { params } = push.mock.calls[0][0];
    expect(params).not.toHaveProperty('fundedLeg');
  });

  it('follows the balance when the API flags the listing as auto', () => {
    // The API names a default chain for apps that predate auto; the flag is
    // what marks it as only a fallback.
    openSpotTrade(listing({ quoteChain: 'bsc', quoteChainAuto: true }), 'sell');

    expect(push).toHaveBeenCalledWith(expect.objectContaining({
      params: expect.objectContaining({ fromChain: 'btc', toChain: 'bsc', fundedLeg: 'to' }),
    }));
  });

  it('leaves the quote chain to All Swap when the listing is on auto', () => {
    openSpotTrade(listing({ quoteChain: '' }), 'buy');

    // Bitcoin carries no USDT, so there is no base-chain fallback to offer.
    expect(push).toHaveBeenCalledWith(expect.objectContaining({
      params: expect.objectContaining({ fromSymbol: 'USDT', fromChain: '', toChain: 'btc', fundedLeg: 'from' }),
    }));
  });

  it('flips which leg is paid when the side is sell', () => {
    openSpotTrade(listing({ quoteChain: 'bsc' }), 'sell');

    expect(push).toHaveBeenCalledWith(expect.objectContaining({
      params: expect.objectContaining({
        fromSymbol: 'BTC', fromChain: 'btc', toSymbol: 'USDT', toChain: 'bsc',
      }),
    }));
  });

  it('reads a missing quoteChain as the base leg’s chain', () => {
    openSpotTrade(listing({ chain: 'bsc', baseSymbol: 'CAKE', quoteChain: undefined }), 'buy');

    expect(push).toHaveBeenCalledWith(expect.objectContaining({
      params: expect.objectContaining({ fromChain: 'bsc', toChain: 'bsc' }),
    }));
  });

  it('leaves a lifi listing on the same-chain desk', () => {
    openSpotTrade(listing({ route: 'lifi', baseSymbol: 'CAKE', chain: 'bsc' }), 'sell');

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
