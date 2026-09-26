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

  it('routes deposit confirmed to my-ads (funding context)', () => {
    expect(routeForNotification({ type: 'p2p.deposit.confirmed' })).toBe('/p2p/my-ads');
  });

  it('routes withdraw confirmed to the pool (reclaim screen)', () => {
    expect(routeForNotification({ type: 'p2p.withdraw.confirmed' })).toBe('/p2p/pool');
  });

  it('returns null for unmapped / missing types (tap just opens the app)', () => {
    expect(routeForNotification({ type: 'order.shipped', orderId: '1' } as any)).toBeNull();
    expect(routeForNotification({ type: 'security.account_locked' })).toBeNull();
    expect(routeForNotification({})).toBeNull();
    expect(routeForNotification(null)).toBeNull();
    expect(routeForNotification(undefined)).toBeNull();
  });

  it('routes a TSA Pay payment received through a link to Payment links, any other to Payment Settings', () => {
    expect(routeForNotification({ type: 'pay_payment_received', source: 'link', mode: 'test' })).toBe('/merchants/pay-links?mode=test');
    expect(routeForNotification({ type: 'pay_payment_received', source: 'open', mode: 'live' })).toBe('/merchants/pay-links?mode=live');
    expect(routeForNotification({ type: 'pay_payment_received', source: 'link' })).toBe('/merchants/pay-links');
    expect(routeForNotification({ type: 'pay_payment_received', source: 'api' })).toBe('/merchants/pay-settings');
    expect(routeForNotification({ type: 'pay_payment_received' })).toBe('/merchants/pay-settings');
  });
});
