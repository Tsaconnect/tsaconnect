// utils/merchantOrderTimeline.ts
//
// The lifecycle steps of a marketplace order, as the merchant's order screen
// draws them with <Stepper>. Moved out of app/merchants/orders/[orderId].tsx so
// every branch is a table test.
import type { Order } from '@/services/orderApi';
import type { Step } from '@/utils/progressSteps';

type Row = { key: string; label: string; timestamp?: string; complete: boolean; current?: boolean; ended?: boolean };

export function merchantOrderTimeline(order: Order, when: (iso: string) => string): Step[] {
  const toStep = (r: Row): Step => ({
    key: r.key,
    label: r.label,
    // A cancellation is where the order ended, not a success.
    state: r.ended ? 'failed' : r.complete ? 'done' : 'pending',
    current: r.current,
    detail: r.timestamp ? when(r.timestamp) : undefined,
  });

  // Refund / cancel branches replace later steps
  if (order.status === 'cancelled') {
    return [
      { key: 'created', label: 'Order Placed', timestamp: order.createdAt, complete: true },
      { key: 'cancelled', label: 'Cancelled', timestamp: order.updatedAt, complete: true, current: true, ended: true },
    ].map(toStep);
  }

  // Services skip the "Shipped" step and read "Delivered" as "Rendered".
  const isService = order.kind === 'service';
  const reached = (...statuses: string[]) => statuses.includes(order.status);

  const rows: Row[] = [
    { key: 'created', label: 'Order Placed', timestamp: order.createdAt, complete: true },
    // The backend stores no escrow timestamp.
    { key: 'escrowed', label: 'Payment Escrowed', complete: reached('escrowed', 'shipped', 'delivered', 'completed', 'refund_requested', 'refunded') },
    ...(isService
      ? []
      : [{ key: 'shipped', label: 'Shipped', timestamp: order.sellerShippedAt, complete: reached('shipped', 'delivered', 'completed', 'refund_requested', 'refunded') }]),
    {
      key: 'delivered',
      label: isService ? 'Rendered' : 'Delivered',
      timestamp: order.sellerDeliveredAt,
      complete: reached('delivered', 'completed', 'refund_requested', 'refunded'),
    },
    { key: 'completed', label: 'Completed', timestamp: order.buyerConfirmedAt, complete: reached('completed') },
  ];

  if (reached('refund_requested', 'refunded')) {
    rows.push({ key: 'refund_requested', label: 'Refund Requested', complete: true });
    if (order.status === 'refunded') {
      rows.push({ key: 'refunded', label: 'Refunded', timestamp: order.updatedAt, complete: true, current: true });
    }
  }

  // The step named by the order's status is where things stand.
  const current = rows.find((r) => r.key === order.status);
  if (current) current.current = true;

  return rows.map(toStep);
}
