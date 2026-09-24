// hooks/usePayLive.ts
//
// What the live-payment components (PayNaira, PayCrypto) share around their
// hook: the shape the panel needs of either hook, and the effects that keep the
// hook in step with the screen —
//   - follow a payment already under way once, when the screen opens on it;
//   - reset when the server releases the payment (processing → pending), or a
//     refetch shows it pending while the hook only waits, so a hook holding
//     'waiting' does not hide Pay from a payer who can now try again;
//   - tell the screen an attempt is unresolved — any stage but idle, waiting and
//     "don't pay again" failures included, so the expiry warning does not tear
//     the form (and its safety copy) down under a swap that outlasts it. A
//     failure that is safe to retry (nothing was started, or it was closed) is
//     not unresolved. Only reset() ends it;
//   - refresh the checkout when the attempt hands over to the server.
import { useEffect, useRef } from 'react';
import type { LiFiToken } from '@/services/lifi';
import { isUnresolved, type PayLiveStage } from '@/hooks/payAttempt';
import type { SellStage } from '@/hooks/useSwappedSell';
import type { PaycrestOrder } from '@/services/paycrestApi';
import type { PayCheckout } from '@/services/payApi';

export type { PayLiveStage };

/**
 * What paying would cost her, and — for a crypto merchant — what they receive.
 * `fromAmount` is the same figure in the paying token's base units: what she is
 * shown is what the signature is held to (see executeSellSwap).
 */
export type PayEstimate = { pay: string; fromAmount: string; receives?: string };

/** What the panel and the effects need of either live-payment hook. */
export type PayHook = {
  stage: PayLiveStage;
  error?: string;
  errorCode?: string;
  /** False when starting the payment over is not safe: Pay is hidden. */
  retryable?: boolean;
  canContinue?: boolean;
  swapStage?: SellStage;
  order?: PaycrestOrder;
  missingUsdc?: string;
  walletShort?: boolean;
  /** Her wallet on this device, once known — a price needs it, so a wait for it is retried on arrival. */
  walletAddress?: string | null;
  payWith(token: LiFiToken, chainId: number, priced?: PayEstimate): void;
  estimate(token: LiFiToken, chainId: number): Promise<PayEstimate>;
  completeWithWallet?(): void;
  continuePayment?(): void;
  resume(): Promise<void>;
  /** Back to idle. A hook may refuse (an attempt in flight, or a swap that may have reached the merchant). */
  reset(): void | Promise<void>;
};

export function usePayLive({
  pay, checkout, showForm, onWaiting, onActive,
}: {
  pay: PayHook;
  checkout: Pick<PayCheckout, 'status'>;
  showForm: boolean;
  /** The attempt reached the server-settles stage — refresh the checkout. */
  onWaiting: () => void;
  onActive: (active: boolean) => void;
}) {
  const { stage, resume, reset } = pay;
  const unresolved = isUnresolved(stage, pay.retryable !== false);

  useEffect(() => {
    if (stage === 'waiting') onWaiting();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- fire on the stage change only.
  }, [stage]);

  useEffect(() => {
    onActive(unresolved);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- report on the stage change only.
  }, [stage, unresolved]);
  useEffect(() => () => onActive(false),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- on unmount only.
    []);

  const followed = useRef<unknown>(null);
  useEffect(() => {
    if (showForm || stage !== 'idle' || followed.current === resume) return;
    followed.current = resume; // resume changes once her wallet address is known
    void resume();
  }, [showForm, stage, resume]);

  // The server let go of the payment. Two ways to see it, both on a REFETCH (a
  // new checkout object, never on mount): it was processing and is pending
  // again (reaped), or the hook only waits while the checkout says pending
  // (nothing of hers ever belonged to it). A hook that must not forget an
  // attempt (crypto, hash unreported) refuses the reset itself.
  const previous = useRef(checkout);
  useEffect(() => {
    const was = previous.current;
    previous.current = checkout;
    if (was === checkout) return;
    const released = was.status === 'processing' && checkout.status === 'pending';
    const stuck = stage === 'waiting' && checkout.status === 'pending';
    if (released || stuck) {
      followed.current = null;
      void reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- react to a refetch only.
  }, [checkout]);
}
