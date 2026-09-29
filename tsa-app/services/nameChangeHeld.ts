// services/nameChangeHeld.ts
//
// What the name-change screen does with the stablecoin payment this phone
// holds for a change (services/nameChangePay records it) each time it reads
// the change: confirm it again, keep it, or let it go.
//
// Confirming is paced. The server stores a hash the first time it is confirmed
// and its sweep proves it from then on, so the screen's steady poll reads the
// change (GET) and only asks the server again about the hash every
// NAME_CHANGE_CONFIRM_EVERY_MS — the confirm route allows 60 calls in 15
// minutes per user, and asking again is only how an answer of "never"
// (refuted, or no change it can pay) reaches this phone. Kept in its own
// module so the screen's tests can stand in for nameChangePay underneath it.
import {
  confirmRecordedPayment, forgetNameChangePayment, needsSupport, paymentPending, type PaymentPending,
} from '@/services/nameChangePay';
import type { NameChange } from '@/services/nameChangeApi';

/** How often a held payment is confirmed again while the screen polls: 30 per 15 minutes, half the route's limit. */
export const NAME_CHANGE_CONFIRM_EVERY_MS = 30_000;

/**
 * A pacer for confirming: `claim()` grants a confirm at most once every
 * NAME_CHANGE_CONFIRM_EVERY_MS, `mark()` records one made another way (a send
 * confirms its own hash; "Check now" is asked for).
 */
export function confirmPacer(now: () => number = Date.now) {
  let last = -Infinity;
  return {
    claim: (): boolean => {
      if (now() - last < NAME_CHANGE_CONFIRM_EVERY_MS) return false;
      last = now();
      return true;
    },
    mark: (): void => { last = now(); },
  };
}

/**
 * A stablecoin payment this phone sent that the server has not proven yet: it
 * is confirmed again, never resent — while the change is paying, or after it
 * closed with no fee taken (the server then takes it late and refunds it).
 */
export const stillConfirming = (c: NameChange | null, held: PaymentPending): boolean =>
  !!c && c.payMethod === 'stablecoin' && held?.kind === 'sent'
  && (c.status === 'paying' || (c.status === 'cancelled' && c.feeStatus === 'none'));

export type HeldPayment = {
  held: PaymentPending;
  /** The server took the payment: the change is worth reading again. */
  taken: boolean;
};

/**
 * Reconcile what this phone holds for change `c`. A payment still confirming is
 * confirmed again when `claimConfirm()` grants it — a synchronous claim, so two
 * reads racing each other cannot both ask — and an answer of "never" moves it
 * to a support case (nameChangePay records that). Only a change that used the
 * payment lets its hash go: a paying or cancelled one keeps it (for its refund,
 * or for support), and so does one the server refused it for.
 */
export async function reconcileHeldPayment(c: NameChange, claimConfirm: () => boolean): Promise<HeldPayment> {
  const held = await paymentPending(c.id);
  if (stillConfirming(c, held)) {
    if (!claimConfirm()) return { held, taken: false };
    let taken = false;
    try {
      taken = !!(await confirmRecordedPayment(c.id));
    } catch {
      // A refusal is read back from paymentPending; anything else is asked again next time.
    }
    return { held: await paymentPending(c.id), taken };
  }
  if (c.status === 'paying' || c.status === 'cancelled' || needsSupport(held)) return { held, taken: false };
  await forgetNameChangePayment(c.id);
  return { held: null, taken: false };
}
