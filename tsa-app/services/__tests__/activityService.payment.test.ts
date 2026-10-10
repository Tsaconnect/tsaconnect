/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

// The unified activity feed's TSA Pay source: her payments, listed as payments
// to a merchant — never as the ramp sale that moves them — and opening the
// payment's own screen.
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

jest.mock('../payApi', () => ({
  __esModule: true,
  listMyPayments: jest.fn(),
}));

import { createActivityPager, ALL_TYPES, TYPE_META } from '../activityService';
import { listMyPayments } from '../payApi';

const payment = {
  id: 'p1',
  merchantName: 'Sky Air',
  description: 'LOS-ABV 12 Oct',
  status: 'succeeded',
  customerTotal: 101_005,
  currency: 'NGN',
  paidAt: '2026-09-25T15:07:00Z',
};

describe('activity feed: TSA Pay source', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    (global as any).fetch = jest.fn();
  });

  it('is one of the feed types, with its own filter', () => {
    expect(ALL_TYPES).toContain('payment');
    expect(TYPE_META.payment.label).toBe('Payments');
  });

  it('shows a payment as money to the merchant, opening the payment', async () => {
    (listMyPayments as jest.Mock).mockResolvedValue([payment]);
    const [item] = await createActivityPager(['payment'], 20).next();
    expect(item).toMatchObject({
      id: 'p1',
      type: 'payment',
      title: 'Payment to Sky Air',
      subtitle: 'LOS-ABV 12 Oct',
      amount: '₦1,010.05',
      positive: false,
      timestamp: '2026-09-25T15:07:00Z',
      href: '/pay?id=p1',
      statusVisual: { label: 'Paid', tone: 'success' },
    });
    // Nothing of the plumbing behind it.
    expect(JSON.stringify(item)).not.toMatch(/USDC|Sell|paycrest/i);
  });

  it('labels each state of a payment, and falls back to TSA Pay without a description', async () => {
    (listMyPayments as jest.Mock).mockResolvedValue([
      { ...payment, id: 'a', status: 'processing', description: '' },
      { ...payment, id: 'b', status: 'failed' },
      { ...payment, id: 'c', status: 'refunded', orderId: 'o1' },
    ]);
    const items = await createActivityPager(['payment'], 20).next();
    expect(items.map((i) => i.statusVisual?.label)).toEqual(['Processing', 'Failed', 'Refunded']);
    expect(items[0].subtitle).toBe('TSA Pay');
  });

  it('a refunded attempt is its own row, opening its order — where the refund is', async () => {
    (listMyPayments as jest.Mock).mockResolvedValue([
      { ...payment, status: 'succeeded', paidAt: '2026-09-25T16:00:00Z' },
      { ...payment, status: 'refunded', orderId: 'o1', paidAt: '2026-09-25T15:00:00Z' },
    ]);
    const items = await createActivityPager(['payment'], 20).next();
    // Same payment, two rows: the later try that paid, and the refunded one.
    expect(items.map((i) => [i.id, i.href])).toEqual([
      ['p1', '/pay?id=p1'],
      ['o1', '/wallet/paycrest/order/o1'],
    ]);
  });

  it('an API without the history route just lists no payments', async () => {
    (listMyPayments as jest.Mock).mockRejectedValue(new Error('Payments are not available right now.'));
    const pager = createActivityPager(['payment'], 20);
    expect(await pager.next()).toEqual([]);
    expect(pager.hasMore()).toBe(false);
  });

  it('a payment the merchant refunded says so under it, not as a refunded attempt', async () => {
    (listMyPayments as jest.Mock).mockResolvedValue([
      { ...payment, merchantRefund: { status: 'full', amount: '12.50', tokenSymbol: 'USDT' } },
      { ...payment, id: 'p2', merchantRefund: null },
    ]);
    const items = await createActivityPager(['payment'], 20).next();
    expect(items[0]).toMatchObject({ subtitle: 'Refunded 12.50 USDT', statusVisual: { label: 'Paid', tone: 'success' }, href: '/pay?id=p1' });
    expect(items[1].subtitle).toBe('LOS-ABV 12 Oct');
  });
});
