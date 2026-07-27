// utils/__tests__/railRoutes.test.ts
//
// The deep links behind the wallet's Deposit and Withdraw sheets. Routes are
// asserted literally because a wrong query param fails SILENTLY: the P2P feed
// inverts `side` internally, so ?side=buy browses SELL ads. A typo lands the
// user on the opposite market with nothing on screen to signal it.
//
// (Replaces the fiatRails test that came in with store-readiness; the rail
// table it covered was consolidated into constants/rails.ts, which the wallet,
// marketplace, services grid and cashback screens all read from.)

import { RAILS, RAIL_ROUTES, railLabel } from '@/constants/rails';

describe('rail routes — deposit (buying crypto)', () => {
  it('routes Individual Payment to the buy-side marketplace', () => {
    expect(RAIL_ROUTES.p2pBuy).toBe('/p2p?side=buy');
  });

  it('routes Payment Merchant to the buy side in merchant mode', () => {
    expect(RAIL_ROUTES.pmBuy).toBe('/p2p?pm=1&side=buy');
  });

  it('routes Instant Transfer to its buy screen', () => {
    expect(RAIL_ROUTES.itBuy).toBe('/wallet/it/buy');
  });
});

describe('rail routes — withdraw (selling crypto)', () => {
  it('routes Individual Payment to the sell-side marketplace', () => {
    expect(RAIL_ROUTES.p2pSell).toBe('/p2p?side=sell');
  });

  it('routes Payment Merchant to the sell side in merchant mode', () => {
    expect(RAIL_ROUTES.pmSell).toBe('/p2p?pm=1&side=sell');
  });

  it('routes Instant Transfer to its sell screen', () => {
    expect(RAIL_ROUTES.itSell).toBe('/wallet/it/sell');
  });
});

describe('rail routes — shape', () => {
  it('sends deposit and withdraw to different routes on every rail', () => {
    expect(RAIL_ROUTES.p2pBuy).not.toBe(RAIL_ROUTES.p2pSell);
    expect(RAIL_ROUTES.pmBuy).not.toBe(RAIL_ROUTES.pmSell);
    expect(RAIL_ROUTES.itBuy).not.toBe(RAIL_ROUTES.itSell);
  });

  it('flags pm=1 on the Payment Merchant routes only', () => {
    const withPmFlag = Object.entries(RAIL_ROUTES)
      .filter(([, route]) => route.includes('pm=1'))
      .map(([key]) => key)
      .sort();
    expect(withPmFlag).toEqual(['pm', 'pmBuy', 'pmSell']);
  });

  it('keeps the IT deep links below /wallet/it so the stack anchor applies', () => {
    // Paired with the anchor asserted in itAnchor.test.ts: these links skip the
    // hub, and only the anchor gives the pushed screen a route to go back to.
    expect(RAIL_ROUTES.itBuy.startsWith('/wallet/it/')).toBe(true);
    expect(RAIL_ROUTES.itSell.startsWith('/wallet/it/')).toBe(true);
  });
});

describe('rail names', () => {
  it('spells the rail out and keeps the acronym for continuity', () => {
    expect(railLabel('p2p')).toBe('Individual Payment (P2P)');
    expect(railLabel('pm')).toBe('Payment Merchant (PM)');
    expect(railLabel('it')).toBe('Instant Transfer (IT)');
  });

  it('gives every rail copy for both directions', () => {
    (['p2p', 'pm', 'it'] as const).forEach((key) => {
      expect(RAILS[key].depositDesc.length).toBeGreaterThan(0);
      expect(RAILS[key].withdrawDesc.length).toBeGreaterThan(0);
    });
  });
});
