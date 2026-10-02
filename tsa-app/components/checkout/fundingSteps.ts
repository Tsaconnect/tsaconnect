// components/checkout/fundingSteps.ts
//
// Where a checkout funded with another token stands, as Stepper rows: create
// the orders → swap in her wallet → the funds arrive on Sonic (with the route's
// own ETA — some bridges take ~20 minutes) → the Sonic gas top-up, when one was
// needed → pay each seller's escrow. Pure, so every stage is a table test.
import type { Step, StepState } from '@/utils/progressSteps';
import { etaLabel, type FundingRunState } from '@/services/checkoutFundingRun';

const ORDER = ['creating_orders', 'swap', 'bridging', 'sonic_gas', 'paying', 'done'] as const;
type Position = (typeof ORDER)[number];

/** Which step the run is on (for a failure: the step it failed at). */
function position(s: FundingRunState): Position {
  switch (s.stage) {
    case 'idle':
    case 'creating_orders':
      return 'creating_orders';
    case 'starting':
    case 'signing':
      return 'swap';
    case 'bridging':
    case 'sonic_gas':
    case 'paying':
    case 'done':
      return s.stage;
    case 'failed': {
      const rec = s.record;
      if (!rec) return 'creating_orders';
      if (rec.delivered) return 'paying';
      if (rec.swapHash) return 'bridging';
      return 'swap';
    }
  }
}

export function fundingSteps(
  s: FundingRunState,
  opts: { sourceSymbol: string; orderToken: string; orderNames: string[] },
): Step[] {
  const at = ORDER.indexOf(position(s));
  const failed = s.stage === 'failed';
  const state = (i: number): StepState => (i < at ? 'done' : i === at ? (failed ? 'failed' : s.stage === 'done' ? 'done' : 'active') : 'pending');
  const rec = s.record;
  const paid = new Set(rec?.paid ?? []);

  const steps: Step[] = [
    { key: 'orders', label: 'Place your orders', state: state(0) },
    {
      key: 'swap',
      label: `Swap ${opts.sourceSymbol} into ${opts.orderToken}`,
      state: state(1),
      // The in-app wallet signs as soon as she taps Pay: nothing for her to do here.
      detail: s.stage === 'signing' ? 'Sending from your wallet — this takes a few seconds' : undefined,
    },
    {
      key: 'bridge',
      label: `${opts.orderToken} arrives in your wallet on Sonic`,
      state: state(2),
      detail: at <= 2 ? `Usually ${etaLabel(rec?.etaSeconds)} · you can leave this screen` : undefined,
    },
  ];
  if (s.stage === 'sonic_gas' || rec?.gasDone) {
    steps.push({ key: 'gas', label: 'Network fee on Sonic covered', state: state(3) });
  }
  const payState = state(4);
  opts.orderNames.forEach((name, i) => {
    const order = rec?.orders[i];
    let st: StepState = payState;
    if (order && paid.has(order.id)) st = 'done';
    else if (payState === 'active' && s.orderIndex !== undefined && i !== s.orderIndex) st = 'pending';
    else if (payState === 'failed' && s.orderIndex !== undefined && i !== s.orderIndex) st = 'pending';
    steps.push({ key: `pay-${i}`, label: `Pay ${name}`, state: st });
  });
  return steps;
}
