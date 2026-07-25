// utils/__tests__/fiatRails.test.ts
//
// The wallet's Fund and Send sheets route the three fiat rails (P2P, PM, IT)
// through fiatRails(). The routes are asserted literally because a wrong query
// param fails silently: the P2P feed inverts `side` internally, so ?side=buy
// browses SELL ads. A typo lands the user on the opposite market with nothing
// on screen to signal it.

import { fiatRails } from '@/utils/fiatRails';

const routeFor = (direction: 'buy' | 'sell', key: 'p2p' | 'pm' | 'it') =>
  fiatRails(direction).find((r) => r.key === key)?.route;

describe('fiatRails — buy (Fund with fiat)', () => {
  it('routes P2P to the buy-side marketplace', () => {
    expect(routeFor('buy', 'p2p')).toBe('/p2p?side=buy');
  });

  it('routes PM to the buy-side marketplace in Payment Merchant mode', () => {
    expect(routeFor('buy', 'pm')).toBe('/p2p?side=buy&pm=1');
  });

  it('routes IT to the Instant Transfer buy screen', () => {
    expect(routeFor('buy', 'it')).toBe('/wallet/it/buy');
  });
});

describe('fiatRails — sell (Send as fiat)', () => {
  it('routes P2P to the sell-side marketplace', () => {
    expect(routeFor('sell', 'p2p')).toBe('/p2p?side=sell');
  });

  it('routes PM to the sell-side marketplace in Payment Merchant mode', () => {
    expect(routeFor('sell', 'pm')).toBe('/p2p?side=sell&pm=1');
  });

  it('routes IT to the Instant Transfer sell screen', () => {
    expect(routeFor('sell', 'it')).toBe('/wallet/it/sell');
  });
});

describe('fiatRails — shape', () => {
  it('offers exactly P2P, PM and IT, in that order, both directions', () => {
    expect(fiatRails('buy').map((r) => r.key)).toEqual(['p2p', 'pm', 'it']);
    expect(fiatRails('sell').map((r) => r.key)).toEqual(['p2p', 'pm', 'it']);
  });

  it('sends buy and sell to different routes on every rail', () => {
    const buy = fiatRails('buy');
    const sell = fiatRails('sell');
    buy.forEach((rail, i) => {
      expect(rail.route).not.toBe(sell[i].route);
    });
  });

  it('flags pm=1 on the PM rail only', () => {
    (['buy', 'sell'] as const).forEach((direction) => {
      const withPmFlag = fiatRails(direction).filter((r) => r.route.includes('pm=1'));
      expect(withPmFlag.map((r) => r.key)).toEqual(['pm']);
    });
  });

  it('gives every rail a title and a description for the sheet row', () => {
    (['buy', 'sell'] as const).forEach((direction) => {
      fiatRails(direction).forEach((rail) => {
        expect(rail.title).toBeTruthy();
        expect(rail.desc).toBeTruthy();
      });
    });
  });
});
