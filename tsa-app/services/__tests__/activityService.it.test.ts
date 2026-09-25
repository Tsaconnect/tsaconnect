/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

// The unified activity feed's Instant Transfer source: IT intents + Paycrest
// orders, rendered through the same buildITActivity the IT hub uses, and
// opening the same detail screens.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

jest.mock('../itApi', () => ({
  __esModule: true,
  listITIntents: jest.fn(),
}));

jest.mock('../paycrestApi', () => ({
  __esModule: true,
  listOrders: jest.fn(),
}));

import { createActivityPager, ALL_TYPES } from '../activityService';
import { listITIntents } from '../itApi';
import { listOrders } from '../paycrestApi';

const intent = {
  id: 'i1',
  direction: 'buy',
  status: 'pending_payment',
  currency: 'NGN',
  ngnKobo: '500000',
  asset: 'MCGP',
  createdAt: '2026-09-20T10:00:00Z',
};

const order = {
  id: 'o1',
  direction: 'sell',
  status: 'settled',
  token: 'USDC',
  tokenAmount: '12.5',
  amount: '12.5',
  currency: 'NGN',
  createdAt: '2026-09-21T10:00:00Z',
};

describe('activity feed: Instant Transfer source', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    (global as any).fetch = jest.fn();
  });

  it('is one of the feed types', () => {
    expect(ALL_TYPES).toContain('it');
  });

  it('merges IT intents and Paycrest orders, newest first, with in-app links', async () => {
    (listITIntents as jest.Mock).mockResolvedValue({ success: true, data: { intents: [intent] } });
    (listOrders as jest.Mock).mockResolvedValue([order]);

    const items = await createActivityPager(['it'], 20).next();

    expect(items.map((i) => i.id)).toEqual(['pc-o1', 'it-i1']);
    expect(items[0]).toMatchObject({
      type: 'it',
      title: 'Sell USDC',
      amount: '12.5 USDC',
      href: '/wallet/paycrest/order/o1',
      statusVisual: { label: 'Complete', tone: 'success' },
    });
    expect(items[1]).toMatchObject({
      type: 'it',
      title: 'Buy MCGP',
      amount: '₦5,000',
      href: '/wallet/it/intent/i1',
      statusVisual: { label: 'Awaiting transfer', tone: 'warning' },
    });
  });

  it('keeps the other source when one fails', async () => {
    (listITIntents as jest.Mock).mockRejectedValue(new Error('network'));
    (listOrders as jest.Mock).mockResolvedValue([order]);

    const items = await createActivityPager(['it'], 20).next();

    expect(items.map((i) => i.id)).toEqual(['pc-o1']);
  });

  it('treats an unsuccessful intents reply as empty', async () => {
    (listITIntents as jest.Mock).mockResolvedValue({ success: false, message: 'nope' });
    (listOrders as jest.Mock).mockResolvedValue([]);

    expect(await createActivityPager(['it'], 20).next()).toEqual([]);
  });

  it("uses the caller's currency symbols", async () => {
    (listITIntents as jest.Mock).mockResolvedValue({
      success: true,
      data: { intents: [{ ...intent, currency: 'UGX' }] },
    });
    (listOrders as jest.Mock).mockResolvedValue([]);

    const [item] = await createActivityPager(['it'], 20, { symbolFor: (c) => (c === 'UGX' ? 'USh' : c) }).next();

    expect(item.amount).toBe('USh5,000');
  });
});
