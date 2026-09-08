// services/__tests__/paycrestApi.cancel.test.ts
//
// cancelOrder — the exit from an order the user has decided against. Its
// reason for existing is the one-open-order lock on the server: without a
// cancel, an abandoned order blocks the user from starting another in the same
// direction until its 30-minute window closes.
//
// Cancelling is local to us (Paycrest has no cancel endpoint), so what matters
// on this side is that the call is made, the refusal is surfaced rather than
// swallowed, and a cancelled order reads as finished everywhere the app asks.
//
// The AsyncStorage mock is required — see paycrestApi.claim.test.ts.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { cancelOrder, isAwaitingUser, isTerminal, statusLabel, PaycrestError } from '../paycrestApi';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
  text: async () => JSON.stringify({ success: true, data }),
});

describe('cancelOrder transport', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('POSTs to /paycrest/orders/:id/cancel', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({ id: 'order-1', status: 'cancelled' }),
    );

    await cancelOrder('order-1');

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe('https://tsa.mcgpchain.com/api/paycrest/orders/order-1/cancel');
    expect(init.method).toBe('POST');
  });

  it('returns the cancelled order', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ id: 'order-1', status: 'cancelled' }));

    const order = await cancelOrder('order-1');

    expect(order.status).toBe('cancelled');
  });

  it('rejects with the server wording when the order is past cancelling', async () => {
    // Unlike claimPayment, this refusal must reach the user: they are about to
    // walk away from an order that is still live.
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({
        success: false,
        message: 'This order can no longer be cancelled. If you have already sent the payment, contact support instead.',
      }),
      text: async () => '',
    });

    await expect(cancelOrder('order-1')).rejects.toThrow(/no longer be cancelled/);
    await expect(cancelOrder('order-1')).rejects.toBeInstanceOf(PaycrestError);
  });
});

describe('cancelled orders read as finished', () => {
  it('is terminal, so the order screen stops polling it', () => {
    expect(isTerminal('cancelled')).toBe(true);
  });

  it('is not awaiting the user, so payment instructions disappear', () => {
    expect(isAwaitingUser('cancelled')).toBe(false);
  });

  it('has its own label rather than falling through as undefined', () => {
    expect(statusLabel({ status: 'cancelled', direction: 'sell' } as any)).toBe('Cancelled');
  });
});
