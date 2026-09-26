// utils/progressSteps.ts
//
// The data behind every progress list the app draws with <Stepper>: ramp
// orders, conversion hops, stablecoin intents, TSA Pay attempts, merchant
// order timelines. The step builders (paycrestOrderSteps, payProgress,
// statusToHops, …) produce these; components/ui/Stepper renders them.
//
// The rule every builder keeps: a step is done only on evidence.

export type StepState = 'done' | 'active' | 'pending' | 'failed';

export interface Step {
  key: string;
  label: string;
  state: StepState;
  /** A line under the label: when it happened, or what it is waiting on. */
  detail?: string;
  /** Emphasise this step as where things stand — for a history whose latest step is already done. */
  current?: boolean;
}

type StepDef = Pick<Step, 'key' | 'label' | 'detail'>;

/** Steps before `reached` are done, the one at it is active (with `activeDetail`), the rest pending. */
export function sequenceSteps(defs: StepDef[], reached: number, activeDetail?: string): Step[] {
  return defs.map((d, i) => {
    if (i < reached) return { ...d, state: 'done' };
    if (i === reached) return { ...d, state: 'active', detail: activeDetail ?? d.detail };
    // A pending step has not happened, so it has no time to show.
    return { key: d.key, label: d.label, state: 'pending' };
  });
}
