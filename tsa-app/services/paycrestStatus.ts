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
  | 'cancelled'
  // ── Converted-order states (Phase 2). None of these are terminal. ──
  /** Paycrest delivered to our treasury; the cross-chain conversion has not
   *  finished. Holder is 'treasury' throughout. */
  | 'converting'
  /** A refund leg back to the user's own wallet is in flight. */
  | 'refunding'
  /** Funds are stranded at Paycrest in a way no retry can fix; an operator
   *  is working it through a dispute — same shape of problem as `unmatched`. */
  | 'recovery_pending';

/** Statuses that will never change on their own. */
export function isTerminal(status: PaycrestStatus): boolean {
  return ['settled', 'refunded', 'expired', 'unmatched', 'failed', 'cancelled'].includes(status);
}

/**
 * Whether a problem with this order can be reported. Mirrors the server's
 * paycrestPreMoneyStatuses (dispute_subjects.go): before any money has moved
 * there is nothing to dispute — cancelling is the way out — so the button
 * would only ever be refused.
 */
export function isDisputableOrder(status: PaycrestStatus): boolean {
  return !isAwaitingUser(status) && status !== 'cancelled';
}

/** Whether the order is still waiting on the user to do something. */
export function isAwaitingUser(status: PaycrestStatus): boolean {
  return status === 'awaiting_payment' || status === 'awaiting_deposit';
}
