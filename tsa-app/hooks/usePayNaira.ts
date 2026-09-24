// hooks/usePayNaira.ts
//
// Paying a TSA Pay merchant that settles in naira, with any token she holds.
//
// One route is chosen by the ramp's own quote, never by us:
//   - Paycrest fills the token as it is (a verified stablecoin on a live
//     route): open the payment's payout order, fund it with the sponsored
//     deposit, claim it;
//   - anything else: the swapped-sell pipeline (hooks/useSwappedSell) run with
//     the PAYMENT as its intent — swap into USDC on Base, open the payout for
//     what landed, send it. That pipeline is injected, not copied.
//
// Everything that signs or sends — paying, "Use my USDC", "Continue payment" —
// goes through usePayAuthorization (PIN/biometric), nothing else. Opening the
// screen on a payment already under way only FOLLOWS it (`resume`): reads, never
// signs. A 409 `pay_in_progress` means an attempt of hers already exists: it is
// followed, never duplicated. Codes are keyed on, never sentences.
import { ethers } from 'ethers';
import { useCallback, useRef, useState } from 'react';
import { getChainByChainId } from '@/constants/chains';
import { usePayAuthorization, type AuthorizationProps } from '@/hooks/usePayAuthorization';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import {
  useSwappedSell, type CreateIntent, type SellStage, type StartSwapArgs,
} from '@/hooks/useSwappedSell';
import { getSponsoredRoutes, isGasSponsored } from '@/services/gasSponsorship';
import { isNonEVMLiFiChain, type LiFiToken } from '@/services/lifi';
import {
  claimPayment, getOrder, getQuote, isConvertedVenue, selectVenue, type PaycrestOrder, type PaycrestVenue,
} from '@/services/paycrestApi';
import {
  checkFunding, fmtTokenAmount, fundingMessage, nativeBalanceFor, sendSponsoredPaycrestDeposit,
} from '@/services/paycrestDeposit';
import { getOpenSellIntent, type SellIntent } from '@/services/paycrestSellSwap';
import { getPayQuote, payConverted, payDirect, type PayCheckout } from '@/services/payApi';
import { getWalletBalances, normalizeWalletBalances, type WalletBalanceWithChain } from '@/services/walletApi';
import {
  CANT_PAY_WITH_TOKEN, classifyStartError, errorCode, errorMessage, estimateError, exceedsPriced, intentForPayment,
  nairaFromKobo, NOTHING_TO_CONTINUE, PAY_ATTEMPT_UNCERTAIN, PAYMENT_PAUSED, PAYMENT_STILL_GOING, payAmountLabel, priceMovedMessage,
  startFailure, unsupportedNetworkMessage,
} from '@/utils/payCheckout';
import type { PayEstimate, PayLiveStage } from '@/hooks/usePayLive';

export type PayNairaStage = PayLiveStage;

export type UsePayNaira = {
  stage: PayNairaStage;
  /** With `shortfall`: USDC (whole units) the swap landed short by. */
  missingUsdc?: string;
  /** With `shortfall`: her wallet's USDC was already offered and did not cover the rest. */
  walletShort: boolean;
  error?: string;
  /** The server's machine code behind `error`, when it sent one (e.g. KYC). */
  errorCode?: string;
  /** False when starting the payment over is not safe (a swap may be under way): hide Pay. */
  retryable: boolean;
  /** Continue payment is the way forward (a sale of hers pays this payment and needs her). */
  canContinue: boolean;
  /** The swapped-sell pipeline's own stage, for progress copy while `paying`. */
  swapStage: SellStage;
  /** A direct payment's payout order — reachable from the order screen if the send failed. */
  order?: PaycrestOrder;
  authorization: AuthorizationProps;
  /** `priced` is what she was shown: the authorization says so, and what is signed is held to it. */
  payWith(token: LiFiToken, chainId: number, priced?: PayEstimate): void;
  /** What she will send, e.g. "0.084 BNB" — an estimate, before she taps Pay. */
  estimate(token: LiFiToken, chainId: number): Promise<PayEstimate>;
  /** Her wallet on this device, once known. */
  walletAddress: string | null;
  /** After a shortfall: pay the rest from her wallet's USDC. Asks for authorization first. */
  completeWithWallet(): void;
  /** Carry on a payment that is under way. Asks for authorization first. */
  continuePayment(): void;
  /** Follow her own attempt at this payment, e.g. on opening the screen. Reads only; never signs or sends. */
  resume(): Promise<void>;
  /** Back to idle — the server released the payment. */
  reset(): void;
};

type Action =
  | { kind: 'pay'; token: LiFiToken; chainId: number; priced?: PayEstimate }
  | { kind: 'wallet'; intent: SellIntent; missingUsdc?: string }
  | { kind: 'continue'; intent: SellIntent };

const authorizationTitle = (a: Action | null): string => {
  if (a?.kind === 'pay' && a.priced) return `Authorize payment of about ${a.priced.pay}`;
  if (a?.kind === 'wallet') return a.missingUsdc ? `Authorize paying about ${a.missingUsdc} USDC from your wallet` : 'Authorize paying from your wallet';
  if (a?.kind === 'continue') return 'Authorize continuing your payment';
  return 'Authorize payment';
};

type Direct = {
  stage: 'paying' | 'waiting' | 'failed' | 'continue';
  error?: string;
  errorCode?: string;
  order?: PaycrestOrder;
  /** With `continue`: the sale that pays this payment. */
  intent?: SellIntent;
  retryable?: boolean;
};

/**
 * The venue Paycrest fills as it is for `token` on `chainId` when the merchant
 * is owed `merchantNetKobo` — or null, meaning the token must be swapped first.
 *
 * A converted venue is Paycrest's own conversion, never what a payment uses.
 * And a venue is matched to the token by NAME, which anyone can give a token: a
 * lookalike "USDC" from her holdings would resolve to the real USDC venue. So a
 * direct route needs the token to be LiFi-verified with that same coin key.
 */
async function directVenue(merchantNetKobo: number, token: LiFiToken, chainId: number): Promise<PaycrestVenue | null> {
  const quote = await getQuote('sell', nairaFromKobo(merchantNetKobo));
  const chain = getChainByChainId(chainId)?.key;
  const venue = selectVenue(quote.venues, { token: token.symbol, chainId, address: token.address, chain });
  if (!venue || isConvertedVenue(venue)) return null;
  const known = token.verificationStatus === 'verified' && token.coinKey?.toUpperCase() === venue.token.toUpperCase();
  return known ? venue : null;
}

export function usePayNaira(checkout: PayCheckout): UsePayNaira {
  const { address } = useSignableWallet();
  const [direct, setDirect] = useState<Direct | null>(null);
  const [createCode, setCreateCode] = useState<string | undefined>();
  const [walletTried, setWalletTried] = useState(false);
  // The server could not prove whether an attempt exists and paused the payment
  // for support: not retryable, not followed, never reset by us. (A ref beside
  // the state so `reset`, a stable callback, sees it.)
  const [startKind, setStartKind] = useState<'paused' | 'refused' | null>(null);
  const paused = startKind === 'paused';
  const pausedRef = useRef(false);
  const inProgress = useRef(false);
  const running = useRef(false);
  const markStartKind = (k: 'paused' | 'refused' | null) => {
    pausedRef.current = k === 'paused';
    setStartKind(k);
  };

  // The payment's own intent, through the swapped-sell pipeline. Refusals are
  // remembered by code: the pipeline reports only their words.
  const createIntent: CreateIntent = async (a) => {
    try {
      return await payConverted(checkout.id, {
        sourceChainId: a.sourceChainId,
        sourceToken: a.token.address,
        sourceAddress: a.sourceAddress,
      });
    } catch (e) {
      const kind = classifyStartError(e);
      if (kind === 'follow') inProgress.current = true;
      else if (kind === 'paused' || kind === 'refused') markStartKind(kind);
      else setCreateCode(errorCode(e));
      throw e;
    }
  };
  const swapped = useSwappedSell({ createIntent });

  /**
   * An attempt of hers already exists: FOLLOW it. Only reads — nothing here
   * signs or sends. Where her own signature is needed (swap under way, deposit
   * still to send) the answer is `continue`, and Continue payment asks for
   * authorization. No sale of hers pays this payment: a direct order, or one
   * the server has already dealt with — the server settles it, so she waits.
   */
  const followOpen = useCallback(async () => {
    if (!address) return;
    let open: SellIntent | null;
    try {
      open = intentForPayment(await getOpenSellIntent(), checkout.id);
    } catch (e) {
      setDirect({ stage: 'failed', error: errorMessage(e, 'Could not check on your payment. Please try again.') });
      return;
    }
    if (!open || open.status === 'delivered' || open.status === 'failed') {
      setDirect({ stage: 'waiting' });
      return;
    }
    if (open.status === 'ordered') {
      try {
        const o = open.paycrestOrderId ? await getOrder(open.paycrestOrderId) : null;
        // Her deposit is still to send only while the payout waits for it.
        if (o && o.status !== 'awaiting_deposit') {
          setDirect({ stage: 'waiting', order: o });
          return;
        }
      } catch (e) {
        setDirect({ stage: 'failed', error: errorMessage(e, 'Could not check on your payment. Please try again.') });
        return;
      }
    }
    setDirect({ stage: 'continue', intent: open });
  }, [address, checkout.id]);

  const payDirectly = async (venue: PaycrestVenue, wallet: string) => {
    let balances: WalletBalanceWithChain[] | null = null;
    try {
      const res = await getWalletBalances(undefined, wallet);
      balances = res.success ? normalizeWalletBalances(res.data) : null;
    } catch {
      balances = null; // a flaky indexer must not block a payment she can fund
    }
    const routes = await getSponsoredRoutes().catch(() => []);
    const funding = checkFunding(venue, balances, { gasSponsored: isGasSponsored(routes, venue.chain, venue.token) });
    if (!funding.ok) {
      setDirect({ stage: 'failed', error: fundingMessage(funding, 'payment') ?? 'This payment cannot be funded.' });
      return;
    }

    const order = await payDirect(checkout.id, { chainKey: venue.chain, token: venue.token });
    setDirect({ stage: 'paying', order });
    try {
      await sendSponsoredPaycrestDeposit(order, wallet, nativeBalanceFor(order.chain, balances));
    } catch (e) {
      // The order exists and Pay is gone: the way forward is the order screen.
      setDirect({
        stage: 'failed',
        error: errorMessage(e, 'Could not send the deposit. Open the payment order to finish it.'),
        order,
        retryable: false,
      });
      return;
    }
    // The deposit already left; a lost claim only degrades to the ordinary
    // unmatched-deposit path, so it never fails the payment.
    await claimPayment(order.id).catch((e) => console.warn('[pay] claimPayment failed', e));
    setDirect({ stage: 'waiting', order });
  };

  const run = async (token: LiFiToken, chainId: number, priced?: PayEstimate) => {
    if (!address || running.current) return;
    running.current = true;
    try {
      setCreateCode(undefined);
      setWalletTried(false);
      markStartKind(null);
      inProgress.current = false;
      if (isNonEVMLiFiChain(chainId)) {
        setDirect({ stage: 'failed', error: unsupportedNetworkMessage(token.symbol) });
        return;
      }
      setDirect({ stage: 'paying' });
      let venue: PaycrestVenue | null;
      try {
        venue = await directVenue(checkout.merchantNet, token, chainId);
      } catch (e) {
        // The rate lookup is not a start route: its words (e.g. the provider being unavailable) are the useful ones.
        setDirect({ stage: 'failed', error: errorMessage(e, 'Could not get a rate. Please try again.') });
        return;
      }
      try {
        if (venue) {
          // The fresh amount, held to the price she was shown — before any order exists.
          const fresh = ethers.parseUnits((Math.ceil(venue.tokenAmount * 100) / 100).toFixed(2), token.decimals);
          if (priced && exceedsPriced(fresh, BigInt(priced.fromAmount))) {
            setDirect({
              stage: 'failed',
              error: priceMovedMessage(fresh.toString(), priced.fromAmount, token.decimals, venue.token),
            });
            return;
          }
          await payDirectly(venue, address);
        } else {
          setDirect(null);
          const args: StartSwapArgs = {
            sourceChainId: chainId,
            token: { ...token, chainId },
            walletAddress: address,
            sourceAddress: address,
            pricedFromAmount: priced?.fromAmount,
          };
          await swapped.start(args);
          if (inProgress.current) {
            inProgress.current = false;
            await followOpen();
          }
        }
      } catch (e) {
        const kind = classifyStartError(e);
        if (kind === 'follow') {
          await followOpen();
        } else {
          if (kind === 'paused') markStartKind('paused');
          setDirect({ stage: 'failed', ...startFailure(e) });
        }
      }
    } finally {
      running.current = false;
    }
  };

  const { request, authorization } = usePayAuthorization<Action>({
    title: authorizationTitle,
    onAuthorized: (action) => {
      if (action.kind === 'pay') {
        void run(action.token, action.chainId, action.priced);
      } else if (action.kind === 'wallet') {
        setWalletTried(true);
        void swapped.completeWithWallet(action.intent);
      } else {
        void continueFresh();
      }
    },
  });

  /**
   * Continue payment: resume the sale as the SERVER has it now, not the
   * snapshot the screen took when it opened — a hash recorded since then must
   * not be missed, or `resume` would treat the sale as having no swap to follow
   * and close it. If nothing of hers pays this payment any more, say so.
   */
  async function continueFresh() {
    if (!address) return;
    let fresh: SellIntent | null;
    try {
      fresh = intentForPayment(await getOpenSellIntent(), checkout.id);
    } catch (e) {
      setDirect({ stage: 'failed', error: errorMessage(e, 'Could not check on your payment. Please try again.') });
      return;
    }
    if (!fresh) {
      setDirect({ stage: 'failed', error: NOTHING_TO_CONTINUE, retryable: false });
    } else if (fresh.status === 'delivered' || fresh.status === 'failed') {
      setDirect({ stage: 'waiting' });
    } else {
      setDirect(null);
      void swapped.resume(fresh, address);
    }
  }

  const payWith = useCallback(
    (token: LiFiToken, chainId: number, priced?: PayEstimate) => request({ kind: 'pay', token, chainId, priced }),
    [request],
  );

  const estimate = async (token: LiFiToken, chainId: number): Promise<PayEstimate> => {
    if (!address) throw new Error('Connect a wallet on this device first — you pay from it yourself.');
    try {
      const venue = await directVenue(checkout.merchantNet, token, chainId);
      if (venue) {
        // Stablecoins: to the cent, upward — an "about" figure never understates.
        const amount = Math.ceil(venue.tokenAmount * 100) / 100;
        return {
          pay: `${fmtTokenAmount(amount)} ${venue.token}`,
          fromAmount: ethers.parseUnits(amount.toFixed(2), token.decimals).toString(),
        };
      }
      const q = await getPayQuote(checkout.id, {
        sourceChainId: chainId,
        sourceToken: token.address,
        sourceAddress: address,
      });
      return { pay: payAmountLabel(q.fromAmount, token.decimals, token.symbol), fromAmount: q.fromAmount };
    } catch (e) {
      throw estimateError(e);
    }
  };

  const heldIntent = direct?.intent ?? swapped.intent;
  const completeWithWallet = useCallback(() => {
    if (swapped.intent) request({ kind: 'wallet', intent: swapped.intent, missingUsdc: swapped.missingUsdc });
  }, [swapped.intent, swapped.missingUsdc, request]);
  const continuePayment = useCallback(() => {
    if (heldIntent) request({ kind: 'continue', intent: heldIntent });
  }, [heldIntent, request]);

  const reset = useCallback(() => {
    // Never pull state out from under an attempt that is running. (The swapped
    // sell guards its own run the same way.) A swap that may be under way does
    // not hold a naira reset: it lands in HER wallet, so freeing the payment
    // strands nothing — unlike a crypto payment, which is delivered to the
    // merchant (see usePayCrypto).
    if (running.current || pausedRef.current) return;
    setDirect(null);
    setCreateCode(undefined);
    setWalletTried(false);
    markStartKind(null);
    swapped.reset();
    // eslint-disable-next-line react-hooks/exhaustive-deps -- swapped.reset is stable.
  }, [swapped.reset]);

  const fromSwap: PayNairaStage =
    swapped.stage === 'idle' ? 'idle'
      : swapped.stage === 'shortfall' ? 'shortfall'
        : swapped.stage === 'done' ? 'waiting'
          : swapped.stage === 'failed' ? 'failed'
            : 'paying';
  const stage = direct?.stage ?? fromSwap;
  const swapStopped = !direct && stage === 'failed';
  const retryable = paused
    ? false
    : direct ? direct.retryable !== false : !(swapStopped && swapped.retryable === false);
  // The swapped sell reports its own words; a start the server refused or paused has better ones.
  const swapError = paused ? PAYMENT_PAUSED
    : startKind === 'refused' ? CANT_PAY_WITH_TOKEN
      : retryable ? swapped.error : PAYMENT_STILL_GOING;

  return {
    stage,
    missingUsdc: stage === 'shortfall' ? swapped.missingUsdc : undefined,
    walletShort: stage === 'shortfall' && walletTried,
    error: direct?.error ?? (swapStopped ? swapError : undefined),
    errorCode: direct?.errorCode ?? (swapStopped ? (paused ? PAY_ATTEMPT_UNCERTAIN : createCode) : undefined),
    retryable,
    canContinue: !paused && !!heldIntent && (stage === 'continue' || (stage === 'failed' && !retryable)),
    swapStage: swapped.stage,
    walletAddress: address,
    order: direct?.order ?? swapped.order,
    authorization,
    payWith,
    estimate,
    completeWithWallet,
    continuePayment,
    resume: followOpen,
    reset,
  };
}
