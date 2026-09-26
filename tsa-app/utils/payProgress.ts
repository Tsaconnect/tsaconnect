// utils/payProgress.ts
//
// The progress steps a payer watches while a TSA Pay payment to a naira
// merchant moves: her swap (when she paid with another token), her payment
// leaving her wallet, the provider receiving it, the merchant being paid.
//
// Pure, like paycrestOrderSteps, and held to the same rule: a step is done only
// on evidence. Nothing is drawn until the attempt's shape is known (a swap, or
// an order to fund), so the list never reshapes under her; and without an
// order to read there is nothing to show progress from.
import type { PayLiveStage } from '@/hooks/payAttempt';
import type { SellStage } from '@/hooks/useSwappedSell';
import type { PaycrestStatus } from '@/services/paycrestStatus';
import { sequenceSteps, type Step } from '@/utils/progressSteps';

export type PayProgressInput = {
  stage: PayLiveStage;
  swapStage?: SellStage;
  /** This attempt swapped her token first (carried by the attempt, see payAttempt.ts). */
  swapped: boolean;
  /** An order exists for this payment — the only thing whose status can move the later steps. */
  hasOrder: boolean;
  /** The linked order's status, as the server last recorded it. */
  orderStatus?: PaycrestStatus;
  /** With `waiting`: the send's outcome is not known yet (hooks/payAttempt.ts). */
  notice?: string;
  merchantName: string;
};

/** Undefined when there is nothing to show progress from: the panel keeps its one-line status instead. */
export function payProgressSteps(input: PayProgressInput): Step[] | undefined {
  const { stage, swapStage, swapped, hasOrder, orderStatus, notice, merchantName } = input;
  const defs = [
    ...(swapped ? [{ key: 'swap', label: 'Swap to USDC' }] : []),
    { key: 'sent', label: 'Payment sent' },
    { key: 'received', label: 'Received by the provider' },
    { key: 'paying', label: `Paying ${merchantName}` },
    { key: 'paid', label: 'Paid' },
  ];
  const at = (key: string) => defs.findIndex((d) => d.key === key);

  if (stage === 'paying') {
    if (swapped && (swapStage === 'swapping' || swapStage === 'waiting')) {
      return sequenceSteps(defs, at('swap'), swapStage === 'waiting' ? 'This can take a few minutes' : 'Swapping your coins');
    }
    if (swapped) return sequenceSteps(defs, at('sent'), swapStage === 'ordering' ? 'Opening the payout' : 'Sending for payout');
    // A direct payment is sending once its order exists; before that, nothing has started.
    return hasOrder ? sequenceSteps(defs, at('sent'), 'Sending from your wallet') : undefined;
  }
  if (stage !== 'waiting' || !hasOrder) return undefined;
  // It may have left her wallet, but nothing proves it yet.
  if (notice) return sequenceSteps(defs, at('sent'), 'Checking whether it left your wallet');

  switch (orderStatus) {
    case undefined:
    case 'awaiting_deposit':
    case 'awaiting_payment':
      return sequenceSteps(defs, at('received'), 'Usually under a minute');
    case 'pending':
      return sequenceSteps(defs, at('paying'), 'Matching a provider');
    case 'settling':
      return sequenceSteps(defs, at('paying'));
    case 'settled':
      return sequenceSteps(defs, defs.length);
    default:
      // Refunded, expired, stuck: the checkout says how it ended, not this list.
      return undefined;
  }
}
