// hooks/usePayCrypto.ts
//
// Paying a TSA Pay merchant that settles in crypto: one LiFi swap, signed in
// her own wallet, that delivers the merchant's token straight to their wallet.
// There is no payout leg — after the swap is sent the SERVER confirms delivery
// and settles the payment, and this hook only waits.
//
// What must never be lost is the swap's hash: it is what lets the server follow
// a payment that has already left her wallet. So it goes to AsyncStorage before
// the broadcast (services/sellSwapRun, the same code the swapped sell uses), the
// server is told with retries, and a payment that is already under way is
// followed rather than started again (409 `pay_in_progress`).
import { useCallback, useState } from 'react';
import { usePayAuthorization, type AuthorizationProps } from '@/hooks/usePayAuthorization';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { SwapBroadcastUnconfirmedError } from '@/services/lifiFlow';
import { isNonEVMLiFiChain, type LiFiToken } from '@/services/lifi';
import { getOpenSellIntent, recordSellSwap, type SellIntent } from '@/services/paycrestSellSwap';
import { getCryptoQuote, payCrypto, type PayCheckout } from '@/services/payApi';
import { clearPendingSwapHash, executeSellSwap, readPendingSwapHash } from '@/services/sellSwapRun';
import {
  errorCode, errorMessage, PAY_IN_PROGRESS, payAmountLabel, unsupportedNetworkMessage,
} from '@/utils/payCheckout';

export type PayCryptoStage = 'idle' | 'paying' | 'waiting' | 'failed';

export type UsePayCrypto = {
  stage: PayCryptoStage;
  error?: string;
  /** The server's machine code behind `error`, when it sent one (e.g. KYC). */
  errorCode?: string;
  authorization: AuthorizationProps;
  payWith(token: LiFiToken, chainId: number): void;
  /** What she will send, e.g. "0.084 BNB" — an estimate, before she taps Pay. */
  estimate(token: LiFiToken, chainId: number): Promise<string>;
  /** Pick up her own attempt at this payment, e.g. after the app was closed mid-swap. */
  resume(): Promise<void>;
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
type Outcome = { stage: PayCryptoStage; error?: string; errorCode?: string };

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
  if (intent.swapTxHash) return true;
  const stored = await readPendingSwapHash(intent.id);
  return stored ? reportSwap(intent.id, stored) : true;
}

export function usePayCrypto(checkout: PayCheckout): UsePayCrypto {
  const { address } = useSignableWallet();
  const [outcome, setOutcome] = useState<Outcome>({ stage: 'idle' });

  /** An attempt of hers already exists: make sure the server has its hash, then wait. */
  const resumeOpen = useCallback(async () => {
    if (!address) return;
    try {
      const open = await getOpenSellIntent();
      if (open && !(await reportStoredSwap(open))) {
        setOutcome({ stage: 'failed', error: NOT_CONFIRMED });
        return;
      }
      setOutcome({ stage: 'waiting' });
    } catch (e) {
      setOutcome({ stage: 'failed', error: errorMessage(e, 'Could not check on your payment. Please try again.') });
    }
  }, [address]);

  const run = async ({ token, chainId }: Pick) => {
    if (!address) return;
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
        if (known && (await reportSwap(intent.id, known))) setOutcome({ stage: 'waiting' });
        else setOutcome({ stage: 'failed', error: BROADCAST_UNCONFIRMED });
        return;
      }
      setOutcome({ stage: 'failed', error: `${errorMessage(e, 'The swap did not go through.')}${NOT_SENT}` });
      return;
    }

    setOutcome(
      (await reportSwap(intent.id, hash)) ? { stage: 'waiting' } : { stage: 'failed', error: NOT_CONFIRMED },
    );
  };

  const { request, authorization } = usePayAuthorization<Pick>({
    title: () => 'Authorize payment',
    onAuthorized: (pick) => { void run(pick); },
  });

  const payWith = useCallback((token: LiFiToken, chainId: number) => request({ token, chainId }), [request]);

  const estimate = async (token: LiFiToken, chainId: number): Promise<string> => {
    if (!address) return '';
    const q = await getCryptoQuote(checkout.id, {
      sourceChainId: chainId,
      sourceToken: token.address,
      sourceAddress: address,
    });
    return payAmountLabel(q.fromAmount, token.decimals, token.symbol);
  };

  return { ...outcome, authorization, payWith, estimate, resume: resumeOpen };
}
