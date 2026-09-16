// services/paycrestStatus.ts
//
// The order status vocabulary and the pure predicates over it, split out of
// paycrestApi so code that only needs to reason about a status does not have
// to import the API client — which pulls in AsyncStorage and, with it, a
// native module that cannot load outside the app.
//
// paycrestApi re-exports all three, so existing importers are unaffected.

/** Server-side order statuses. `unmatched` is ours, not Paycrest's: the user
 *  paid, the bank confirmed, and the protocol never registered it. Nothing
 *  automated can resolve that, so the UI must not present it as an expiry. */
export type PaycrestStatus =
  | 'awaiting_payment'
  | 'awaiting_deposit'
  | 'pending'
  | 'settling'
  | 'settled'
  | 'refunded'
  | 'expired'
  | 'unmatched'
  | 'failed'
  /** Ours: the user abandoned the order before sending anything. Paycrest's
   *  own copy keeps running to its expiry — see the cancel endpoint. */
  | 'cancelled';

/** Statuses that will never change on their own. */
export function isTerminal(status: PaycrestStatus): boolean {
  return ['settled', 'refunded', 'expired', 'unmatched', 'failed', 'cancelled'].includes(status);
}

/** Whether the order is still waiting on the user to do something. */
export function isAwaitingUser(status: PaycrestStatus): boolean {
  return status === 'awaiting_payment' || status === 'awaiting_deposit';
}
