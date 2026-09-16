// services/__tests__/paycrestApi.claim.test.ts
//
// Transport-level test for claimPayment — the call behind "I've sent the
// money" (buy) and "I've sent it" (sell). See app/wallet/paycrest/buy.tsx and
// sell.tsx: both fire this and swallow its error before navigating, so it is
// the request shape that matters here, not error handling (there is none by
// design — a lost claim degrades to the old "expired" outcome rather than
// trapping the user on the screen).
//
// The AsyncStorage mock is required — see itStableApi.test.ts for why:
// paycrestApi's request() goes through getAuthHeaders (walletApi.ts), which
// reads AsyncStorage, and without the mock every call silently fails before
// fetch is ever invoked.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { claimPayment } from '../paycrestApi';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
  text: async () => JSON.stringify({ success: true, data }),
});

describe('claimPayment transport', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('POSTs to /paycrest/orders/:id/claim-payment', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({ id: 'order-1', status: 'awaiting_payment', paymentClaimedAt: '2026-09-07T00:00:00Z' })
    );

    await claimPayment('order-1');

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe('https://tsa.mcgpchain.com/api/paycrest/orders/order-1/claim-payment');
    expect(init.method).toBe('POST');
  });

  it('returns the updated order on success', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({ id: 'order-1', status: 'awaiting_payment', paymentClaimedAt: '2026-09-07T00:00:00Z' })
    );

    const order = await claimPayment('order-1');

    expect(order.paymentClaimedAt).toBe('2026-09-07T00:00:00Z');
    // The claim must never itself appear to move the order's status — that
    // is Sync's job on the next poll, not this call's.
    expect(order.status).toBe('awaiting_payment');
  });

  it('rejects on a server error, leaving the caller to swallow it', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 404,
      json: async () => ({ success: false, message: 'Order not found' }),
      text: async () => JSON.stringify({ success: false, message: 'Order not found' }),
    });

    await expect(claimPayment('order-1')).rejects.toThrow('Order not found');
  });
});
