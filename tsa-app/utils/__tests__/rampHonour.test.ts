import { honourOf } from '../rampHonour';

const now = Date.parse('2026-10-03T12:00:00Z');
const routed = { direction: 'buy', asset: 'USDC', status: 'pending_payment', rateExpiresAt: '2026-10-03T12:15:00Z' };

describe('honourOf', () => {
  it('holds a routed buy inside its window', () => {
    expect(honourOf(routed, now)).toEqual({ honourUntil: routed.rateExpiresAt, holding: true, rateUpdated: false });
  });

  it('stops holding once the window ends, or once the money arrived', () => {
    expect(honourOf(routed, now + 16 * 60_000).holding).toBe(false);
    expect(honourOf({ ...routed, status: 'payment_received' }, now).holding).toBe(false);
  });

  it('says the rate was updated only for a routed buy priced at a live rate', () => {
    expect(honourOf({ ...routed, status: 'payment_received', repriced: true }, now).rateUpdated).toBe(true);
    // On a buy opened outside the router, repriced means "priced on arrival".
    expect(honourOf({ direction: 'buy', asset: 'USDC', status: 'payment_received', repriced: true }, now))
      .toEqual({ holding: false, rateUpdated: false });
  });

  it('ignores MCGP, sells and nothing at all', () => {
    expect(honourOf({ ...routed, asset: 'MCGP' }, now).holding).toBe(false);
    expect(honourOf({ ...routed, direction: 'sell' }, now).holding).toBe(false);
    expect(honourOf(null, now)).toEqual({ holding: false, rateUpdated: false });
  });
});
