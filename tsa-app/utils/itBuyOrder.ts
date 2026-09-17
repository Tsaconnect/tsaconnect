// utils/itBuyOrder.ts
//
// One open MCGP buy order per user, and the user's own way out of one.
//
// The API refuses a second order with `it_buy_order_open` and names the order
// that is already open, so the buy screen can send the user there instead of to
// an error. It refuses a cancel with `it_buy_payment_arrived` when a payment
// that looks like the order's is already in the queue — that message is written
// for the user and is shown as-is.
//
// Branch on the code, never the message.
import { Alert } from 'react-native';

export const IT_BUY_ORDER_OPEN = 'it_buy_order_open';
export const IT_BUY_PAYMENT_ARRIVED = 'it_buy_payment_arrived';
/** The order is no longer awaiting payment (settling, settled or already
 *  cancelled). Shared by every ramp endpoint, hence the stable_ prefix. */
export const IT_BAD_STATE = 'stable_bad_state';

export interface OpenBuyOrder {
  intentId: string;
  /** The open order's naira, in kobo. '' when the server did not say. */
  ngnKobo: string;
}

/** The order an `it_buy_order_open` refusal names, or null for anything else. */
export function openBuyOrderOf(r: { code?: string; errorData?: unknown }): OpenBuyOrder | null {
  if (r.code !== IT_BUY_ORDER_OPEN) return null;
  const data = r.errorData;
  if (!data || typeof data !== 'object') return null;
  const { intentId, ngnKobo } = data as { intentId?: unknown; ngnKobo?: unknown };
  if (typeof intentId !== 'string' || !intentId) return null;
  return { intentId, ngnKobo: typeof ngnKobo === 'string' ? ngnKobo : '' };
}

/** The Instant Transfer order detail screen. */
export function itIntentRoute(id: string): string {
  return `/wallet/it/intent/${id}`;
}

/** What to tell the user when a cancel is refused. */
export function cancelBuyErrorMessage(r: { code?: string; message?: string }): string {
  if (r.code === IT_BUY_PAYMENT_ARRIVED && r.message) return r.message;
  // The server's text here is an internal one, and "try again" would be wrong:
  // the order has moved on and retrying can never succeed.
  if (r.code === IT_BAD_STATE) return 'This order can no longer be cancelled — its status has changed.';
  return 'Could not cancel. Try again.';
}

/**
 * Confirms before cancelling an MCGP buy order. The one thing the user must
 * understand: money sent to an order that is then cancelled matches nothing,
 * so it is held for an operator and refunded — never credited.
 */
export function confirmCancelITBuy(onConfirm: () => void): void {
  Alert.alert(
    'Cancel this order?',
    'Only cancel if you have NOT sent the money yet. Money sent to a cancelled order is held for review and refunded, not credited.',
    [
      { text: 'Keep order', style: 'cancel' },
      { text: 'Cancel order', style: 'destructive', onPress: onConfirm },
    ],
  );
}
