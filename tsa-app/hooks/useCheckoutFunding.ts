// hooks/useCheckoutFunding.ts
//
// The screen's handle on "pay with another token" at checkout: the app-wide
// funding run (services/checkoutFundingRun — one per app, so leaving the screen
// mid-bridge and coming back follows the same run), and the price of paying
// with the token she picked, looked up again every PRICE_STALE_MS while she may
// still tap Pay — the same re-price TSA Pay's panel does, because what she is
// shown is what the signed route is held to (sellSwapRun's 2% guard).
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { escrowOrder } from '@/services/serviceCheckout';
import { clearPendingSwapHash, executeSellSwap, readPendingSwapHash } from '@/services/sellSwapRun';
import {
  abandonFunding,
  confirmFunding,
  getFunding,
  quoteCartFunding,
  quoteFunding,
  recordFundingSwap,
  requestFundingSonicGas,
  requestFundingSourceGas,
  startFunding,
  type FundingCart,
  type FundingQuote,
} from '@/services/checkoutFundingApi';
import { CheckoutFundingRunner, type FundingRunState } from '@/services/checkoutFundingRun';
import type { LiFiToken } from '@/services/lifi';
import { PRICE_STALE_MS } from '@/utils/payCheckout';

let runner: CheckoutFundingRunner | null = null;

/** The app's one funding run, wired to the real API, signer and escrow. */
export function checkoutFundingRunner(): CheckoutFundingRunner {
  runner ??= new CheckoutFundingRunner({
    start: startFunding,
    record: recordFundingSwap,
    confirm: confirmFunding,
    get: getFunding,
    abandon: abandonFunding,
    sourceGas: requestFundingSourceGas,
    sonicGas: requestFundingSonicGas,
    executeSwap: executeSellSwap,
    pendingHash: readPendingSwapHash,
    clearPendingHash: clearPendingSwapHash,
    escrow: (order, onPhase, progress) => escrowOrder(order, onPhase, progress),
    sleep: (ms) => new Promise<void>((r) => setTimeout(r, ms)),
    now: () => Date.now(),
  });
  return runner;
}

/** The funding run's state, live. */
export function useCheckoutFundingRun(): { state: FundingRunState; runner: CheckoutFundingRunner } {
  const r = checkoutFundingRunner();
  const state = useSyncExternalStore(r.subscribe, r.getState, r.getState);
  return { state, runner: r };
}

export type FundingPick = { token: LiFiToken; chainId: number };
export type FundingPrice = { pick: FundingPick; quote?: FundingQuote; error?: string; errorCode?: string; at: number };

/**
 * The price of paying with `pick`: the active cart's (before any order exists)
 * or a checkout group's (a retry after its orders were created). Only the
 * latest lookup counts, and a figure is looked up again when it is 30 s old
 * while `live`.
 */
export function useFundingPrice(opts: {
  pick: FundingPick | null;
  wallet: string | null;
  cart?: FundingCart | null;
  groupId?: string | null;
  live: boolean;
}) {
  const { pick, wallet, cart, groupId, live } = opts;
  const [price, setPrice] = useState<FundingPrice | null>(null);
  const seq = useRef(0);
  const cartKey = cart ? JSON.stringify(cart) : '';

  const reprice = useCallback(async () => {
    if (!pick || !wallet || (!cart && !groupId)) return;
    const mine = ++seq.current;
    const src = { sourceChainId: pick.chainId, sourceToken: pick.token.address, walletAddress: wallet };
    try {
      const quote = groupId ? await quoteFunding(groupId, src) : await quoteCartFunding(cart!, src);
      if (mine === seq.current) setPrice({ pick, quote, at: Date.now() });
    } catch (e) {
      if (mine === seq.current) {
        setPrice({
          pick,
          error: e instanceof Error && e.message ? e.message : 'Could not price this token.',
          errorCode: (e as { code?: string } | null)?.code,
          at: Date.now(),
        });
      }
    }
    // cartKey stands in for cart, whose identity changes every render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pick, wallet, cartKey, groupId]);

  useEffect(() => {
    setPrice(null);
    void reprice();
  }, [reprice]);

  useEffect(() => {
    if (!pick || !live) return;
    const t = setInterval(() => { void reprice(); }, PRICE_STALE_MS);
    return () => clearInterval(t);
  }, [pick, live, reprice]);

  const current = price && pick && price.pick === pick ? price : null;
  return {
    price: current,
    pricing: !!pick && !current,
    stale: !!current && Date.now() - current.at > PRICE_STALE_MS,
    reprice,
  };
}
