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

  it('opens the conversation for a chat message', () => {
    const id = '3f2a9c1e-5b7d-4e8a-9c21-7d4e5f6a8b90';
    expect(routeForNotification({ type: 'chat.message.new', threadId: id })).toBe(`/chat/${id}`);
    expect(routeForNotification({ type: 'chat.message.new' })).toBe('/messages');
    expect(routeForNotification({ type: 'chat.message.new', threadId: '../pay' })).toBe('/messages');
  });

  it('falls back to the trades list when tradeId is missing', () => {
    expect(routeForNotification({ type: 'p2p.trade.paid' })).toBe('/p2p/my-trades');
  });

  it('routes deposit confirmed to my-ads (funding context)', () => {
    expect(routeForNotification({ type: 'p2p.deposit.confirmed' })).toBe('/p2p/my-ads');
  });

  it('routes the moved-to-BNB-Chain notice to my-ads, where her Sonic ads are', () => {
    expect(routeForNotification({ type: 'p2p.direct_moved' })).toBe('/p2p/my-ads');
  });

  it('routes withdraw confirmed to the pool (reclaim screen)', () => {
    expect(routeForNotification({ type: 'p2p.withdraw.confirmed' })).toBe('/p2p/pool');
  });

  it('routes a liquidity voucher reminder to the liquidity dashboard', () => {
    expect(routeForNotification({ type: 'liquidity.voucher_expiring', adId: 'a' })).toBe('/liquidity');
  });

  it('returns null for unmapped / missing types (tap just opens the app)', () => {
    expect(routeForNotification({ type: 'order.shipped', orderId: '1' } as any)).toBeNull();
    expect(routeForNotification({ type: 'security.account_locked' })).toBeNull();
    expect(routeForNotification({})).toBeNull();
    expect(routeForNotification(null)).toBeNull();
    expect(routeForNotification(undefined)).toBeNull();
  });

  it('routes a TSA Pay payment received: a one-time link to its detail, a reusable one to Payment links, any other to Payment Settings', () => {
    const PID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
    expect(routeForNotification({ type: 'pay_payment_received', source: 'link', mode: 'test', paymentId: PID }))
      .toBe(`/merchants/pay-links/${PID}?mode=test`);
    // Not a payment id: never built into a route.
    expect(routeForNotification({ type: 'pay_payment_received', source: 'link', mode: 'test', paymentId: '../wallet/send' }))
      .toBe('/merchants/pay-links?mode=test');
    expect(routeForNotification({ type: 'pay_payment_received', source: 'open', mode: 'live' })).toBe('/merchants/pay-links?mode=live');
    expect(routeForNotification({ type: 'pay_payment_received', source: 'link' })).toBe('/merchants/pay-links');
    expect(routeForNotification({ type: 'pay_payment_received', source: 'api' })).toBe('/merchants/pay-settings');
    expect(routeForNotification({ type: 'pay_payment_received' })).toBe('/merchants/pay-settings');
  });

  it('routes every name-change outcome to the name-change screen', () => {
    for (const type of ['name_change_started', 'name_change_approved', 'name_change_held', 'name_change_rejected', 'name_change_lapsed']) {
      expect(routeForNotification({ type })).toBe('/profile/name-change');
    }
    expect(routeForNotification({ type: 'name_change_other' })).toBeNull();
  });

  it('routes a TSA Pay business rename (filed, approved, rejected) to Payment Settings', () => {
    for (const type of ['pay_business_name_requested', 'pay_business_name_approved', 'pay_business_name_rejected']) {
      expect(routeForNotification({ type })).toBe('/merchants/pay-settings');
    }
  });

  it('routes the owner\'s refund events to the refund, or to her refunds without a refund id', () => {
    const id = '3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f';
    for (const type of ['pay_refund_requested', 'pay_refund_confirmed', 'pay_refund_failed', 'pay_refund_needs_review']) {
      expect(routeForNotification({ type, refundId: id, paymentId: 'x' })).toBe(`/merchants/refunds/${id}`);
      expect(routeForNotification({ type, refundId: '../evil' })).toBe('/merchants/refunds');
    }
  });

  it('routes the payer\'s refund to the payment\'s summary', () => {
    const id = '8e4d2c1b-6a5f-4e3d-9c2b-7a1f0e9d8c7b';
    expect(routeForNotification({ type: 'pay_refund_received', paymentId: id, refundId: 'r' })).toBe(`/pay?id=${id}`);
    expect(routeForNotification({ type: 'pay_refund_received', paymentId: 'nope' })).toBeNull();
  });
});
