import { routeForNotification } from './notificationRouting';

describe('routeForNotification', () => {
  it('routes p2p trade lifecycle events to the cockpit', () => {
    for (const type of [
      'p2p.trade.opened',
      'p2p.trade.paid',
      'p2p.trade.released',
      'p2p.trade.cancelled',
      'p2p.trade.disputed',
      'p2p.trade.resolved',
      'p2p.trade.expired',
    ]) {
      expect(routeForNotification({ type, tradeId: 'abc' })).toBe('/p2p/trade/abc');
    }
  });

  it('routes a chat message to the cockpit', () => {
    expect(routeForNotification({ type: 'p2p.message.new', tradeId: 'xyz' })).toBe('/p2p/trade/xyz');
  });

  it('falls back to the trades list when tradeId is missing', () => {
    expect(routeForNotification({ type: 'p2p.trade.paid' })).toBe('/p2p/my-trades');
  });

  it('routes capital movement to the pool', () => {
    expect(routeForNotification({ type: 'p2p.deposit.confirmed' })).toBe('/p2p/pool');
    expect(routeForNotification({ type: 'p2p.withdraw.confirmed' })).toBe('/p2p/pool');
  });

  it('returns null for unmapped / missing types (tap just opens the app)', () => {
    expect(routeForNotification({ type: 'order.shipped', orderId: '1' } as any)).toBeNull();
    expect(routeForNotification({ type: 'security.account_locked' })).toBeNull();
    expect(routeForNotification({})).toBeNull();
    expect(routeForNotification(null)).toBeNull();
    expect(routeForNotification(undefined)).toBeNull();
  });
});
