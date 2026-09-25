// hooks/payAttempt.ts
//
// One state machine for a live TSA Pay attempt, shared by the naira and the
// crypto hooks. Before it, usePayNaira composed three stage vocabularies and
// eight loose pieces of state, and read the public stage / retryable /
// canContinue / error out of nested ternaries; this is the single place those
// are decided.
//
//   idle → paying → waiting | shortfall | failed | deposit
//   idle | failed | waiting | deposit → begin → paying
//   idle → continue (a payment of hers under way, needing her) → paying
//   idle → deposit (a direct order waiting on a deposit that never left her wallet) → paying
//
// Pure (reducePayAttempt, payAttemptView, isUnresolved) so every transition and
// every guard is a table test. `useAttempt` is the thin React shell: it keeps
// the state in a ref updated SYNCHRONOUSLY beside the useState mirror, so a
// double biometric success sees the attempt already begun and the guards read
// the latest state, not the last render's.
import { useCallback, useRef, useState } from 'react';
import type { SellStage } from '@/hooks/useSwappedSell';
import type { PaycrestOrder } from '@/services/paycrestApi';
import type { SellIntent } from '@/services/paycrestSellSwap';

export type PayLiveStage = 'idle' | 'paying' | 'continue' | 'deposit' | 'shortfall' | 'waiting' | 'failed';

/** Detail every non-idle attempt may carry. */
type Detail = {
  /** The swapped-sell pipeline's own stage, for progress copy. */
  swapStage?: SellStage;
  order?: PaycrestOrder;
};

export type PayAttempt =
  | { kind: 'idle' }
  | ({ kind: 'paying'; walletTried: boolean } & Detail)
  | ({ kind: 'continue'; intent: SellIntent } & Detail)
  /** A direct order waits for her deposit, and none left her wallet: the same order is sent, never a new one. */
  | { kind: 'deposit'; order: PaycrestOrder; error?: string }
  | ({ kind: 'shortfall'; intent?: SellIntent; missingUsdc?: string; walletTried: boolean } & Detail)
  | ({ kind: 'waiting' } & Detail)
  | ({
      kind: 'failed';
      error: string;
      code?: string;
      /** False when starting the payment over is not safe (a swap may be under way): Pay is hidden. */
      retryable: boolean;
      /** The server paused it: never followed, retried or reset by us. */
      paused: boolean;
      /** A swap may have reached the merchant and the server may not know: never downgraded to a failure that offers Pay. */
      held: boolean;
      /** Nothing to continue: the failure is final on this phone. */
      noContinue: boolean;
      intent?: SellIntent;
    } & Detail);

export type PayAttemptEvent =
  /** She authorised: a new attempt starts. */
  | { type: 'begin' }
  /** The pipeline moved to a new stage. */
  | ({ type: 'progress'; swapStage: SellStage } & Pick<Detail, 'order'>)
  /** Handed to the server (the pipeline finished, or a direct order was funded). */
  | ({ type: 'waiting' } & Detail)
  /** The pipeline stopped with a sentence for her. */
  | ({ type: 'stop'; message: string; retryable: boolean; intent?: SellIntent } & Pick<Detail, 'order'>)
  /** The swap landed short of what the payout needs. */
  | { type: 'shortfall'; missingUsdc?: string; intent?: SellIntent }
  /** She offered her wallet's USDC for the rest. */
  | { type: 'walletOffered' }
  /** A payment of hers is under way and needs her. */
  | { type: 'continue'; intent: SellIntent }
  /** Her direct order still waits for its deposit — with the reason the last send failed, if one did. */
  | { type: 'deposit'; order: PaycrestOrder; error?: string }
  /** A failure the hook itself detected. */
  | ({
      type: 'fail'; error: string; errorCode?: string; retryable: boolean; paused?: boolean; held?: boolean; noContinue?: boolean;
      intent?: SellIntent;
    } & Pick<Detail, 'order'>)
  /** The server let go of the payment. */
  | { type: 'reset' };

const walletTried = (a: PayAttempt): boolean => (a.kind === 'paying' || a.kind === 'shortfall') && a.walletTried;
const isPaused = (a: PayAttempt): boolean => a.kind === 'failed' && a.paused;
const isHeld = (a: PayAttempt): boolean => a.kind === 'failed' && a.held;

/** An attempt in flight is never pulled out from under itself, and a payment the server paused is never reset by us. */
export const canReset = (a: PayAttempt): boolean => a.kind !== 'paying' && !isPaused(a);

export function reducePayAttempt(a: PayAttempt, e: PayAttemptEvent): PayAttempt {
  // A payment the server paused is final on this phone: nothing rewrites it, not even a later failure.
  if (isPaused(a)) return a;
  // A held failure is never downgraded to one that offers Pay (only lifted, by the hold lifting or a reset).
  if (isHeld(a) && (e.type === 'fail' || e.type === 'stop') && e.retryable) return a;
  switch (e.type) {
    case 'begin':
      // An attempt in flight is never begun twice.
      return a.kind === 'paying' ? a : { kind: 'paying', walletTried: false };
    case 'progress':
      return { kind: 'paying', walletTried: walletTried(a), swapStage: e.swapStage, order: e.order };
    case 'waiting':
      return { kind: 'waiting', swapStage: e.swapStage, order: e.order };
    case 'stop':
      return {
        kind: 'failed', error: e.message, retryable: e.retryable, paused: false, held: false, noContinue: !e.intent,
        intent: e.intent, order: e.order,
      };
    case 'shortfall':
      return {
        kind: 'shortfall', intent: e.intent, missingUsdc: e.missingUsdc, walletTried: walletTried(a), swapStage: 'shortfall',
      };
    case 'walletOffered':
      return { kind: 'paying', walletTried: true, swapStage: 'ordering' };
    case 'continue':
      return { kind: 'continue', intent: e.intent };
    case 'deposit':
      return { kind: 'deposit', order: e.order, error: e.error };
    case 'fail':
      return {
        kind: 'failed', error: e.error, code: e.errorCode, retryable: e.retryable, paused: !!e.paused, held: !!e.held,
        noContinue: e.noContinue ?? !e.intent, intent: e.intent, order: e.order,
      };
    case 'reset':
      return canReset(a) ? { kind: 'idle' } : a;
  }
}

/** Everything the panel and the screen read of an attempt. */
export type PayAttemptView = {
  stage: PayLiveStage;
  error?: string;
  errorCode?: string;
  retryable: boolean;
  canContinue: boolean;
  missingUsdc?: string;
  walletShort: boolean;
  swapStage: SellStage;
  order?: PaycrestOrder;
};

export function payAttemptView(a: PayAttempt): PayAttemptView {
  const base = { walletShort: false, swapStage: 'idle' as SellStage, retryable: true, canContinue: false };
  switch (a.kind) {
    case 'idle':
      return { stage: 'idle', ...base };
    case 'paying':
      return { stage: 'paying', ...base, swapStage: a.swapStage ?? 'idle', order: a.order };
    case 'continue':
      return { stage: 'continue', ...base, canContinue: true, swapStage: a.swapStage ?? 'idle', order: a.order };
    case 'deposit':
      // Not retryable: the order exists, so Pay (a new order) is never the way forward.
      return { stage: 'deposit', ...base, retryable: false, error: a.error, order: a.order };
    case 'shortfall':
      return {
        stage: 'shortfall', ...base, missingUsdc: a.missingUsdc, walletShort: a.walletTried,
        swapStage: a.swapStage ?? 'shortfall', order: a.order,
      };
    case 'waiting':
      return { stage: 'waiting', ...base, swapStage: a.swapStage ?? 'idle', order: a.order };
    case 'failed':
      return {
        stage: 'failed', ...base, error: a.error, errorCode: a.code, retryable: a.retryable,
        canContinue: !a.retryable && !a.paused && !a.noContinue, swapStage: a.swapStage ?? 'idle', order: a.order,
      };
  }
}

/**
 * Whether an attempt is unresolved — the live form must stay up under the
 * expiry warning. Anything but idle is, except a failure that is safe to retry
 * (nothing was started, or it was closed).
 */
export const isUnresolved = (stage: PayLiveStage, retryable: boolean): boolean =>
  stage !== 'idle' && !(stage === 'failed' && retryable);

/**
 * The React shell. `ref` is always the latest state (updated synchronously in
 * `dispatch`); `attempt` is the render-time mirror.
 */
export function useAttempt() {
  const ref = useRef<PayAttempt>({ kind: 'idle' });
  const [attempt, setAttempt] = useState<PayAttempt>(ref.current);
  const dispatch = useCallback((e: PayAttemptEvent) => {
    const next = reducePayAttempt(ref.current, e);
    if (next !== ref.current) {
      ref.current = next;
      setAttempt(next);
    }
  }, []);
  return { attempt, ref, dispatch };
}
