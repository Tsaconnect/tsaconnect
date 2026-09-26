import type { Order } from '@/services/orderApi';
import { merchantOrderTimeline } from '../merchantOrderTimeline';

const when = (iso: string) => `at ${iso}`;
const order = (status: string, over: Partial<Order> = {}) =>
  ({ status, kind: 'product', createdAt: 'T0', updatedAt: 'T9', ...over }) as unknown as Order;
const states = (o: Order) => merchantOrderTimeline(o, when).map((s) => `${s.key}:${s.state}${s.current ? '*' : ''}`);

describe('merchantOrderTimeline', () => {
  it('a cancelled order ends in a failure, never a success check', () => {
    expect(states(order('cancelled'))).toEqual(['created:done', 'cancelled:failed*']);
    expect(merchantOrderTimeline(order('cancelled'), when)[1].detail).toBe('at T9');
  });

  it('a shipped product: done up to shipped, which is current, the rest pending', () => {
    expect(states(order('shipped', { sellerShippedAt: 'T2' } as Partial<Order>))).toEqual([
      'created:done', 'escrowed:done', 'shipped:done*', 'delivered:pending', 'completed:pending',
    ]);
  });

  it('a service has no shipping and is "Rendered"', () => {
    const steps = merchantOrderTimeline(order('delivered', { kind: 'service' } as Partial<Order>), when);
    expect(steps.map((s) => s.label)).toEqual(['Order Placed', 'Payment Escrowed', 'Rendered', 'Completed']);
  });

  it('a refund adds its steps after the lifecycle', () => {
    expect(states(order('refunded')).slice(-2)).toEqual(['refund_requested:done', 'refunded:done*']);
  });

  it('a pending step shows no time', () => {
    const steps = merchantOrderTimeline(order('escrowed', { buyerConfirmedAt: 'T5' } as Partial<Order>), when);
    expect(steps.find((s) => s.key === 'created')?.detail).toBe('at T0');
    expect(steps.find((s) => s.key === 'completed')?.state).toBe('pending');
  });
});
