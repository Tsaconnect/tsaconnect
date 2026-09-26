// utils/paycrestOrderSteps.ts
//
// The progress steps of a direct Paycrest ramp order, for the Stepper on the
// order screen. (A converted order has its own three hops —
// paycrestConversionHops — because its money takes a different road.)
//
// The rule every branch keeps: never claim progress the order has not made. A
// step is done only on a status that proves it, a failure ends the list at the
// step it happened on, and nothing after a failure is drawn as merely pending.
//
// Pure: the time formatter is passed in, so every branch is a table test.
import { sequenceSteps, type Step } from '@/utils/progressSteps';
import type { PaycrestOrder } from '@/services/paycrestApi';

type StepDef = Pick<Step, 'key' | 'label' | 'detail'>;

export type OrderStepsInput = {
  order: Pick<PaycrestOrder, 'direction' | 'status' | 'createdAt' | 'paymentClaimedAt' | 'settledAt'>;
  /**
   * What this phone knows of the deposit: `proven` — it broadcast one and has
   * the hash; `unconfirmed` — it may have (a hold, a lost answer), with nothing
   * to prove it. Unconfirmed is never drawn as sent.
   */
  sent?: 'proven' | 'unconfirmed';
  /** An ISO time as the user reads it. */
  when: (iso: string) => string;
};

export function paycrestOrderSteps({ order, sent, when }: OrderStepsInput): Step[] {
  const sell = order.direction === 'sell';
  const at = (iso?: string) => (iso ? when(iso) : undefined);
  const defs: StepDef[] = [
    { key: 'opened', label: 'Order opened', detail: at(order.createdAt) },
    { key: 'sent', label: sell ? 'Crypto sent' : 'Transfer sent', detail: at(order.paymentClaimedAt) },
    { key: 'received', label: 'Received by the provider' },
    { key: 'paying', label: sell ? 'Paying your bank' : 'Sending your crypto' },
    { key: 'paid', label: sell ? 'Paid to your bank' : 'Delivered to your wallet', detail: at(order.settledAt) },
  ];
  // Her side is done once she says it is, or this phone proves it — the provider seeing it is the next step.
  const userDone = !!order.paymentClaimedAt || sent === 'proven';
  const upTo = (n: number) => defs.slice(0, n).map((d): Step => ({ ...d, state: 'done' }));
  const ending = (key: string, label: string, state: Step['state'], detail?: string): Step => ({ key, label, state, detail });

  switch (order.status) {
    case 'awaiting_payment':
    case 'awaiting_deposit':
      if (userDone) return sequenceSteps(defs, 2, 'Usually under a minute');
      if (sent === 'unconfirmed') return sequenceSteps(defs, 1, 'Confirming it left your wallet');
      return sequenceSteps(defs, 1, sell ? 'Waiting for your deposit' : 'Waiting for your transfer');
    case 'pending':
      return sequenceSteps(defs, 3, 'Matching a provider');
    case 'settling':
    case 'converting':
      return sequenceSteps(defs, 3);
    case 'settled':
      return sequenceSteps(defs, defs.length);
    case 'refunding':
      return [...upTo(3), ending('refund', 'Returning to you', 'active')];
    case 'refunded':
      return [...upTo(3), ending('refund', 'Returned to you', 'done')];
    case 'unmatched':
    case 'recovery_pending':
      // Her money arrived; a person is recovering it. In progress, not failed.
      return [...upTo(2), ending('recovery', 'Recovering your payment', 'active', 'Our team is on it')];
    case 'cancelled':
      return [...upTo(userDone ? 2 : 1), ending('cancelled', 'Cancelled', 'failed')];
    case 'expired':
      return [...upTo(1), ending('expired', sell ? 'Expired — no deposit arrived' : 'Expired — no transfer arrived', 'failed')];
    case 'failed':
      return [...upTo(userDone ? 2 : 1), ending('failed', 'Could not be completed', 'failed')];
  }
  // A status this build does not know yet: say only what is certain.
  return upTo(1);
}
