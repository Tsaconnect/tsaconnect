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
import { useCallback, useRef, useState } from 'react';
import { usePayAuthorization, type AuthorizationProps } from '@/hooks/usePayAuthorization';
import type { PayEstimate, PayLiveStage } from '@/hooks/usePayLive';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { SwapBroadcastUnconfirmedError } from '@/services/lifiFlow';
import { isNonEVMLiFiChain, type LiFiToken } from '@/services/lifi';
import { getOpenSellIntent, recordSellSwap, type SellIntent } from '@/services/paycrestSellSwap';
import { getCryptoQuote, payCrypto, type PayCheckout } from '@/services/payApi';
import { clearPendingSwapHash, executeSellSwap, readPendingSwapHash } from '@/services/sellSwapRun';
import {
  CANT_PAY_WITH_TOKEN, errorCode, errorMessage, intentForPayment, isRouteRefused, NOTHING_TO_CONTINUE,
  PAY_ATTEMPT_UNCERTAIN, PAY_IN_PROGRESS, PAYMENT_PAUSED, payAmountLabel, receiveAmountLabel,
  unsupportedNetworkMessage,
} from '@/utils/payCheckout';

export type PayCryptoStage = Exclude<PayLiveStage, 'continue' | 'shortfall'>;

export type UsePayCrypto = {
  stage: PayCryptoStage;
  error?: string;
  /** The server's machine code behind `error`, when it sent one (e.g. KYC). */
  errorCode?: string;
  /** False when starting the payment over is not safe (her swap may be under way): hide Pay. */
  retryable: boolean;
  /** Continue payment (check on it again) is the way forward. */
  canContinue: boolean;
  authorization: AuthorizationProps;
  payWith(token: LiFiToken, chainId: number): void;
  /** What she will send, e.g. "0.084 BNB", and what the store receives — before she taps Pay. */
  estimate(token: LiFiToken, chainId: number): Promise<PayEstimate>;
  /** Follow her own attempt at this payment, e.g. after the app was closed mid-swap. */
  resume(): Promise<void>;
  /** Check on a payment that could not be confirmed. Reads and re-reports; signs nothing. */
  continuePayment(): Promise<void>;
  /**
   * Back to idle — the server released the payment. Refuses while an attempt is
   * in flight, and while a swap that may have reached the merchant is not yet
   * known to the server (a re-pay would pay them twice): it retries the report
   * first and only then lets go.
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

type Pick = { token: LiFiToken; chainId: number };
type Outcome = {
  stage: PayCryptoStage; error?: string; errorCode?: string; retryable?: boolean;
  /** The server paused the payment: nothing to continue, nothing to retry. */
  paused?: boolean;
};

/** Tell the server which swap this payment is, retrying; the hash stays on the phone until it has heard. */
async function reportSwap(intentId: string, hash: string): Promise<boolean> {
  for (let i = 0; i < REPORT_TRIES; i++) {
    if (i > 0) await sleep(REPORT_DELAY_MS);
    try {
      await recordSellSwap(intentId, hash);
      await clearPendingSwapHash(intentId);
      return true;
    } catch (e) {
      console.warn('[pay] recordSellSwap failed', e);
    }
  }
  return false;
}

/** A hash on this phone the server never got: report it. Returns false only when reporting failed. */
async function reportStoredSwap(intent: SellIntent): Promise<boolean> {
  // Only a payment still waiting for its hash has anything to report.
  if (intent.swapTxHash || intent.status !== 'swapping') return true;
  const stored = await readPendingSwapHash(intent.id);
  return stored ? reportSwap(intent.id, stored) : true;
}

export function usePayCrypto(checkout: PayCheckout): UsePayCrypto {
  const { address } = useSignableWallet();
  const [outcome, setOutcome] = useState<Outcome>({ stage: 'idle' });
  const running = useRef(false);
  const outcomeRef = useRef(outcome);
  outcomeRef.current = outcome;
  // The intent whose swap may have left her wallet without the server hearing of it.
  const held = useRef<string | null>(null);

  /** An attempt of hers already exists: make sure the server has its hash, then wait. */
  const follow = useCallback(async (explicit: boolean) => {
    if (!address) return;
    try {
      // Only the sale that pays THIS payment; an unrelated open sale of hers is not ours to touch.
      const open = intentForPayment(await getOpenSellIntent(), checkout.id);
      if (!open && explicit) {
        // She asked to continue: silently waiting on nothing would be a lie.
        setOutcome({ stage: 'failed', error: NOTHING_TO_CONTINUE, retryable: false });
        return;
      }
      if (open && !(await reportStoredSwap(open))) {
        held.current = open.id;
        setOutcome({ stage: 'failed', error: NOT_CONFIRMED, retryable: false });
        return;
      }
      held.current = null;
      setOutcome({ stage: 'waiting' });
    } catch (e) {
      setOutcome({ stage: 'failed', error: errorMessage(e, 'Could not check on your payment. Please try again.') });
    }
  }, [address, checkout.id]);
  const resumeOpen = useCallback(() => follow(false), [follow]);
  const continuePayment = useCallback(() => follow(true), [follow]);

  const run = async ({ token, chainId }: Pick) => {
    if (!address || running.current) return;
    running.current = true;
    try {
      await startPayment(token, chainId, address);
    } finally {
      running.current = false;
    }
  };

  const startPayment = async (token: LiFiToken, chainId: number, address: string) => {
    if (isNonEVMLiFiChain(chainId)) {
      setOutcome({ stage: 'failed', error: unsupportedNetworkMessage(token.symbol) });
      return;
    }
    setOutcome({ stage: 'paying' });

    let started;
    try {
      started = await payCrypto(checkout.id, {
        sourceChainId: chainId,
        sourceToken: token.address,
        sourceAddress: address,
      });
    } catch (e) {
      if (errorCode(e) === PAY_IN_PROGRESS) return resumeOpen();
      if (errorCode(e) === PAY_ATTEMPT_UNCERTAIN) {
        // The server could not prove whether an attempt exists and paused the
        // payment for support. Never followed, never retried, never reset by us.
        setOutcome({ stage: 'failed', error: PAYMENT_PAUSED, errorCode: PAY_ATTEMPT_UNCERTAIN, retryable: false, paused: true });
        return;
      }
      if (isRouteRefused(e)) {
        // The route said no for this token (fail-closed): nothing started, the payment is still payable.
        setOutcome({ stage: 'failed', error: CANT_PAY_WITH_TOKEN, retryable: true });
        return;
      }
      setOutcome({ stage: 'failed', error: errorMessage(e, 'Something went wrong. Please try again.'), errorCode: errorCode(e) });
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
      });
    } catch (e) {
      if (e instanceof SwapBroadcastUnconfirmedError) {
        // It may be on its way: never a retry — the server decides from the hash.
        const known = e.txHash ?? (await readPendingSwapHash(intent.id));
        if (known && (await reportSwap(intent.id, known))) {
          setOutcome({ stage: 'waiting' });
        } else {
          held.current = intent.id;
          setOutcome({ stage: 'failed', error: BROADCAST_UNCONFIRMED, retryable: false });
        }
        return;
      }
      setOutcome({ stage: 'failed', error: `${errorMessage(e, 'The swap did not go through.')}${NOT_SENT}` });
      return;
    }

    if (await reportSwap(intent.id, hash)) {
      setOutcome({ stage: 'waiting' });
    } else {
      held.current = intent.id;
      setOutcome({ stage: 'failed', error: NOT_CONFIRMED, retryable: false });
    }
  };

  const { request, authorization } = usePayAuthorization<Pick>({
    title: () => 'Authorize payment',
    onAuthorized: (pick) => { void run(pick); },
  });

  const payWith = useCallback((token: LiFiToken, chainId: number) => request({ token, chainId }), [request]);

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
      throw isRouteRefused(e) ? new Error(CANT_PAY_WITH_TOKEN) : e;
    }
    const pay = payAmountLabel(q.fromAmount, token.decimals, token.symbol);
    const symbol = checkout.settleTokenSymbol;
    const receives = q.destAmount && q.destDecimals != null && symbol
      ? receiveAmountLabel(q.destAmount, q.destDecimals, symbol)
      : '';
    return receives ? { pay, receives } : { pay };
  };

  const reset = useCallback(async () => {
    if (running.current) return;
    if (outcomeRef.current.retryable === false) {
      // A swap that may have reached the merchant, unknown to the server: the
      // payment being freed must not become a second payment. Try the report
      // again; let go only into waiting once it lands, otherwise hold the state.
      const id = held.current;
      const hash = id ? await readPendingSwapHash(id) : null;
      if (id && hash && (await reportSwap(id, hash))) {
        held.current = null;
        setOutcome({ stage: 'waiting' });
      }
      return;
    }
    setOutcome({ stage: 'idle' });
  }, []);
  const retryable = outcome.retryable !== false;

  return {
    ...outcome,
    retryable,
    canContinue: outcome.stage === 'failed' && !retryable && !outcome.paused,
    authorization,
    payWith,
    estimate,
    resume: resumeOpen,
    continuePayment,
    reset,
  };
}
