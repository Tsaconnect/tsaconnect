// hooks/usePayCrypto.ts
//
// Paying a TSA Pay merchant that settles in crypto: one LiFi swap, signed in
// her own wallet, that delivers the merchant's token straight to their wallet.
// There is no payout leg — after the swap is sent the SERVER confirms delivery
// and settles the payment, and this hook only waits.
//
// Opening the screen on a payment already under way only FOLLOWS it: the one
// server write is reporting a hash already on her phone (never signing, never
// sending). Only a sale that pays THIS payment is followed.
//
// What must never be lost is the swap's hash: it is what lets the server follow
// a payment that has already left her wallet. So it goes to AsyncStorage before
// the broadcast (services/sellSwapRun, the same code the swapped sell uses), the
// server is told with retries, and a payment that is already under way is
// followed rather than started again (409 `pay_in_progress`).
//
// And because the swap goes straight to the MERCHANT, a second payment would pay
// them twice. So beside the hash a hold is persisted (services/payHold) the
// moment the swap is signed. It survives an app kill: on opening, a live hold is
// looked at before Pay is offered again, and it lifts only when the server has
// the swap, the payment is terminal, or 24 h have passed. A server that refuses
// a late hash (the intent was reaped) does NOT lift it: the swap may still mine.
import { useCallback, useEffect, useRef, useState } from 'react';
import { canReset, payAttemptView, useAttempt, type PayLiveStage } from '@/hooks/payAttempt';
import { usePayAuthorization, type AuthorizationProps } from '@/hooks/usePayAuthorization';
import type { PayEstimate } from '@/hooks/usePayLive';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { SwapBroadcastUnconfirmedError } from '@/services/lifiFlow';
import { isNonEVMLiFiChain, type LiFiToken } from '@/services/lifi';
import { getOpenSellIntent, recordSellSwap, type SellIntent } from '@/services/paycrestSellSwap';
import { clearPayHold, holdActive, readPayHold, writePayHold } from '@/services/payHold';
import { getCryptoQuote, payCrypto, type PayCheckout } from '@/services/payApi';
import { clearPendingSwapHash, executeSellSwap, readPendingSwapHash } from '@/services/sellSwapRun';
import {
  classifyStartError, errorMessage, estimateError, intentForPayment, NOTHING_TO_CONTINUE, payAmountLabel,
  receiveAmountLabel, startFailure, unconfirmedCopy, unsupportedNetworkMessage,
} from '@/utils/payCheckout';

export type UsePayCrypto = {
  /** A crypto payment has no `continue` or `shortfall`: one swap, delivered straight to the merchant. */
  stage: PayLiveStage;
  error?: string;
  /** The server's machine code behind `error`, when it sent one (e.g. KYC). */
  errorCode?: string;
  /** False when starting the payment over is not safe (her swap may be under way): hide Pay. */
  retryable: boolean;
  /** Continue payment (check on it again) is the way forward. */
  canContinue: boolean;
  authorization: AuthorizationProps;
  walletAddress: string | null;
  /** `priced` is what she was shown: the authorization says so, and the signature is held to it. */
  payWith(token: LiFiToken, chainId: number, priced?: PayEstimate): void;
  /** What she will send, e.g. "0.084 BNB", and what the store receives — before she taps Pay. */
  estimate(token: LiFiToken, chainId: number): Promise<PayEstimate>;
  /** Follow her own attempt at this payment, e.g. after the app was closed mid-swap. */
  resume(): Promise<void>;
  /** Check on a payment that could not be confirmed. Reads and re-reports; signs nothing. */
  continuePayment(): Promise<void>;
  /**
   * Back to idle — the server released the payment. Refuses while an attempt is
   * in flight, while the server has paused it, and while a swap that may have
   * reached the merchant is not yet known to the server (a re-pay would pay them
   * twice): it looks at the hold again first and only then lets go.
   */
  reset(): Promise<void>;
};

const REPORT_TRIES = 3;
const REPORT_DELAY_MS = 2_000;
const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

const NOT_SENT = ' Nothing was sent. If you cannot try again right away, the payment frees up again within about ten minutes.';
const NOT_CONFIRMED =
  "Your payment was sent, but we couldn't confirm it with our server yet. Don't pay again — reopen this payment in a few minutes and it will be picked up.";
const BROADCAST_UNCONFIRMED =
  "We couldn't confirm your payment was sent. Don't pay again — give it a few minutes and reopen this payment to check on it.";

type Pick = { token: LiFiToken; chainId: number; priced?: PayEstimate };
/** How the server took (or did not take) the swap's hash. */
type Reported = 'reported' | 'rejected' | 'unreachable';

/** The intent states in which the server has the swap's hash (or has settled what became of it). */
const SERVER_HAS_SWAP = new Set<SellIntent['status']>(['swapped', 'ordering', 'ordered', 'delivered']);
const serverHasSwap = (i: SellIntent) => !!i.swapTxHash || SERVER_HAS_SWAP.has(i.status);

/**
 * Tell the server which swap this payment is. A 4xx is the server's final word
 * (it takes a hash only for an intent still `swapping`, so a reaped one is
 * refused for good): no retries. Anything else is retried, then given up as
 * unreachable. The hash and the hold stay on the phone until the server has it.
 */
async function reportSwap(paymentId: string, intentId: string, hash: string): Promise<Reported> {
  for (let i = 0; i < REPORT_TRIES; i++) {
    if (i > 0) await sleep(REPORT_DELAY_MS);
    try {
      await recordSellSwap(intentId, hash);
      await clearPendingSwapHash(intentId);
      await clearPayHold(paymentId);
      return 'reported';
    } catch (e) {
      console.warn('[pay] recordSellSwap failed', e);
      const status = (e as { status?: unknown } | null)?.status;
      if (typeof status === 'number' && status >= 400 && status < 500 && status !== 408 && status !== 429) return 'rejected';
    }
  }
  return 'unreachable';
}

export function usePayCrypto(checkout: PayCheckout): UsePayCrypto {
  const { address } = useSignableWallet();
  // One state machine (hooks/payAttempt): every stage, guard and public flag is decided there.
  const { attempt, ref, dispatch } = useAttempt();
  // Pay is withheld until the persisted hold has been looked at: a kill may have left one.
  const [holdChecked, setHoldChecked] = useState(false);
  const checking = useRef(false);

  /** The swap is known to the server, or the hold has run out: let go of the phone's copy. */
  const liftHold = useCallback(async (intentId?: string) => {
    await clearPayHold(checkout.id);
    if (intentId) await clearPendingSwapHash(intentId);
  }, [checkout.id]);

  /** A swap that may have reached the merchant and is not known to the server: hold, and say so. */
  const failHeld = useCallback(async (intentId: string, why: Reported | 'nohash') => {
    await writePayHold(checkout.id, intentId);
    dispatch(why === 'unreachable'
      ? { type: 'fail', error: NOT_CONFIRMED, retryable: false, noContinue: false }
      : { type: 'fail', error: unconfirmedCopy(checkout.id), retryable: false, noContinue: true });
  }, [checkout.id, dispatch]);

  /**
   * Look at where this payment stands, and act only on what is safe: make sure
   * the server has the swap's hash (a report is not a signature), or hold. Never
   * signs, never sends. `explicit`: she asked to continue, so finding nothing
   * is said, not silently waited on.
   */
  const check = useCallback(async (explicit: boolean) => {
    if (checking.current) return;
    checking.current = true;
    try {
      const stored = await readPayHold(checkout.id);
      const hold = holdActive(stored) ? stored : null;
      if (stored && !hold) await liftHold(stored.intentId); // 24 h on: the server has given up on it too

      let open: SellIntent | null;
      try {
        // Only the sale that pays THIS payment; an unrelated open sale of hers is not ours to touch.
        open = intentForPayment(await getOpenSellIntent(), checkout.id);
      } catch (e) {
        if (hold) await failHeld(hold.intentId, 'unreachable');
        else dispatch({ type: 'fail', error: errorMessage(e, 'Could not check on your payment. Please try again.'), retryable: true });
        return;
      }
      if (open && serverHasSwap(open)) {
        await liftHold(open.id);
        dispatch({ type: 'waiting' });
        return;
      }
      // A sale that is not waiting for a hash (already closed, say) has nothing to report — unless a hold says the swap is out there.
      if (open && open.status !== 'swapping' && !hold) {
        dispatch({ type: 'waiting' });
        return;
      }
      const intentId = open?.id ?? hold?.intentId;
      if (!intentId) {
        // She asked to continue and there is nothing of hers: say so, and offer no dead button.
        dispatch(explicit
          ? { type: 'fail', error: NOTHING_TO_CONTINUE, retryable: false, noContinue: true }
          : { type: 'waiting' });
        return;
      }
      const hash = await readPendingSwapHash(intentId);
      if (!hash) {
        // Nothing to report. With a hold the swap may still be out there; without one there was never a swap.
        if (hold) await failHeld(intentId, 'nohash');
        else dispatch({ type: 'waiting' });
        return;
      }
      const reported = await reportSwap(checkout.id, intentId, hash);
      if (reported === 'reported') dispatch({ type: 'waiting' });
      else await failHeld(intentId, reported);
    } finally {
      checking.current = false;
    }
  }, [checkout.id, dispatch, failHeld, liftHold]);

  // Opening the screen: a live hold (a kill left one) is looked at before Pay is offered.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const stored = await readPayHold(checkout.id);
      if (holdActive(stored)) await check(false);
      else if (stored) await liftHold(stored.intentId);
      if (!cancelled) setHoldChecked(true);
    })();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once per payment.
  }, [checkout.id]);

  const resumeOpen = useCallback(() => check(false), [check]);
  const continuePayment = useCallback(() => check(true), [check]);

  const run = async ({ token, chainId, priced }: Pick) => {
    // One attempt at a time: `begin` is refused (and the ref says so at once) while one is in flight.
    if (!address || ref.current.kind === 'paying') return;
    dispatch({ type: 'begin' });
    await startPayment(token, chainId, address, priced);
  };

  const startPayment = async (token: LiFiToken, chainId: number, address: string, priced?: PayEstimate) => {
    if (isNonEVMLiFiChain(chainId)) {
      dispatch({ type: 'fail', error: unsupportedNetworkMessage(token.symbol), retryable: true });
      return;
    }

    let started;
    try {
      started = await payCrypto(checkout.id, {
        sourceChainId: chainId,
        sourceToken: token.address,
        sourceAddress: address,
      });
    } catch (e) {
      if (classifyStartError(e) === 'follow') return resumeOpen();
      // Paused: never followed, retried or reset by us. Anything else: nothing started.
      dispatch({ type: 'fail', ...startFailure(e), paused: classifyStartError(e) === 'paused' });
      return;
    }

    const { intent, quote, gasTopUp } = started;
    let hash: string;
    try {
      hash = await executeSellSwap({
        intentId: intent.id,
        quote,
        gasTopUp,
        token: { ...token, chainId },
        sourceChainId: chainId,
        sourceAddress: address,
        pricedFromAmount: priced?.fromAmount,
        paymentId: checkout.id,
      });
    } catch (e) {
      if (e instanceof SwapBroadcastUnconfirmedError) {
        // It may be on its way: never a retry — the server decides from the hash.
        const known = e.txHash ?? (await readPendingSwapHash(intent.id));
        const reported = known ? await reportSwap(checkout.id, intent.id, known) : 'unreachable';
        if (reported === 'reported') dispatch({ type: 'waiting' });
        else {
          await writePayHold(checkout.id, intent.id);
          dispatch(reported === 'rejected'
            ? { type: 'fail', error: unconfirmedCopy(checkout.id), retryable: false, noContinue: true }
            : { type: 'fail', error: BROADCAST_UNCONFIRMED, retryable: false, noContinue: false });
        }
        return;
      }
      dispatch({ type: 'fail', error: `${errorMessage(e, 'The swap did not go through.')}${NOT_SENT}`, retryable: true });
      return;
    }

    const reported = await reportSwap(checkout.id, intent.id, hash);
    if (reported === 'reported') dispatch({ type: 'waiting' });
    else await failHeld(intent.id, reported);
  };

  const { request, authorization } = usePayAuthorization<Pick>({
    title: (pending) => (pending?.priced ? `Authorize payment of about ${pending.priced.pay}` : 'Authorize payment'),
    onAuthorized: (pick) => { void run(pick); },
  });

  const payWith = useCallback(
    (token: LiFiToken, chainId: number, priced?: PayEstimate) => request({ token, chainId, priced }),
    [request],
  );

  const estimate = async (token: LiFiToken, chainId: number): Promise<PayEstimate> => {
    if (!address) throw new Error('Connect a wallet on this device first — you pay from it yourself.');
    let q;
    try {
      q = await getCryptoQuote(checkout.id, {
        sourceChainId: chainId,
        sourceToken: token.address,
        sourceAddress: address,
      });
    } catch (e) {
      throw estimateError(e);
    }
    const pay = payAmountLabel(q.fromAmount, token.decimals, token.symbol);
    const symbol = checkout.settleTokenSymbol;
    const receives = q.destAmount && q.destDecimals != null && symbol
      ? receiveAmountLabel(q.destAmount, q.destDecimals, symbol)
      : '';
    return receives ? { pay, fromAmount: q.fromAmount, receives } : { pay, fromAmount: q.fromAmount };
  };

  const reset = useCallback(async () => {
    if (!canReset(ref.current)) return; // in flight, or paused by the server
    const stored = await readPayHold(checkout.id);
    if (holdActive(stored)) {
      // A swap that may have reached the merchant: the payment being freed must
      // not become a second payment. Look again; never let go into idle.
      await check(false);
      return;
    }
    if (stored) await liftHold(stored.intentId);
    dispatch({ type: 'reset' });
  }, [check, checkout.id, dispatch, liftHold, ref]);
  const view = payAttemptView(attempt);

  return {
    stage: view.stage,
    error: view.error,
    errorCode: view.errorCode,
    retryable: view.retryable && holdChecked,
    canContinue: view.canContinue,
    authorization,
    walletAddress: address,
    payWith,
    estimate,
    resume: resumeOpen,
    continuePayment,
    reset,
  };
}
