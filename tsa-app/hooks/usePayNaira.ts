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
import { useCallback, useRef } from 'react';
import { getChainByChainId } from '@/constants/chains';
import { usePayAuthorization, type AuthorizationProps } from '@/hooks/usePayAuthorization';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { payAttemptView, useAttempt, type PayLiveStage } from '@/hooks/payAttempt';
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
  classifyStartError, directPriceMovedCopy, errorCode, errorMessage, estimateError, exceedsPriced, intentForPayment, nairaFromKobo,
  nothingToContinueCopy, ORDER_AMOUNT_UNCHECKABLE, ORDER_MISMATCH, PAUSED_FAILURE, PAY_ATTEMPT_UNCERTAIN, PAYMENT_STILL_GOING, payAmountLabel, priceMovedMessage, REFUSED_FAILURE, startFailure, unsupportedNetworkMessage,
  type StartErrorKind,
} from '@/utils/payCheckout';
import type { PayEstimate } from '@/hooks/usePayLive';

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
  payWith(token: LiFiToken, chainId: number, priced: PayEstimate): void;
  /** What she will send, e.g. "0.084 BNB" — an estimate, before she taps Pay. */
  estimate(token: LiFiToken, chainId: number): Promise<PayEstimate>;
  /** Her wallet on this device, once known. */
  walletAddress: string | null;
  /** After a shortfall: pay the rest from her wallet's USDC. Asks for authorization first. */
  completeWithWallet(): void;
  /** Carry on a payment that is under way. Asks for authorization first. */
  continuePayment(): void;
  /** With `deposit`: send the direct order's deposit again. Asks for authorization, then re-reads the order first. */
  sendDeposit(): void;
  /** Follow her own attempt at this payment, e.g. on opening the screen. Reads only; never signs or sends. */
  resume(): Promise<void>;
  /** Back to idle — the server released the payment. */
  reset(): void;
};

type Action =
  | { kind: 'pay'; token: LiFiToken; chainId: number; priced: PayEstimate }
  | { kind: 'wallet'; intent: SellIntent; missingUsdc?: string }
  | { kind: 'continue'; intent: SellIntent }
  | { kind: 'deposit'; order: PaycrestOrder };

const authorizationTitle = (a: Action | null): string => {
  if (a?.kind === 'pay') return `Authorize payment of about ${a.priced.pay}`;
  if (a?.kind === 'deposit') {
    return a.order.tokenAmount ? `Authorize sending ${a.order.tokenAmount} ${a.order.token}` : `Authorize sending ${a.order.token}`;
  }
  if (a?.kind === 'wallet') return a.missingUsdc ? `Authorize paying about ${a.missingUsdc} USDC from your wallet` : 'Authorize paying from your wallet';
  if (a?.kind === 'continue') return 'Authorize continuing your payment';
  return 'Authorize payment';
};

/**
 * The payout order's deposit against the price she was shown: the amount in the
 * token's base units when it exceeds the tolerance, 'unreadable' when it cannot
 * be checked (never funded), null when it is fine.
 */
function orderAboveTolerance(tokenAmount: string | undefined, decimals: number, pricedBase: string): bigint | 'unreadable' | null {
  try {
    const amount = ethers.parseUnits(String(tokenAmount ?? ''), decimals);
    return exceedsPriced(amount, BigInt(pricedBase)) ? amount : null;
  } catch {
    return 'unreadable';
  }
}

/** Her wallet's balances, or null — a flaky indexer must not block a payment she can fund. */
async function balancesOf(wallet: string): Promise<WalletBalanceWithChain[] | null> {
  try {
    const res = await getWalletBalances(undefined, wallet);
    return res.success ? normalizeWalletBalances(res.data) : null;
  } catch {
    return null;
  }
}

/**
 * Send a direct order's deposit from her wallet (the sponsor covers gas where it
 * can), then tell the server it left. The deposit already left once this
 * returns; a lost claim only degrades to the ordinary unmatched-deposit path.
 */
async function sendAndClaim(order: PaycrestOrder, wallet: string, balances: WalletBalanceWithChain[] | null): Promise<void> {
  await sendSponsoredPaycrestDeposit(order, wallet, nativeBalanceFor(order.chain, balances));
  await claimPayment(order.id).catch((e) => console.warn('[pay] claimPayment failed', e));
}

/** Her deposit is still to send: the order waits for it, and nothing from this app said it left. */
const awaitsHerDeposit = (o: PaycrestOrder): boolean => o.status === 'awaiting_deposit' && !o.paymentClaimedAt;

const SEND_FAILED = 'Could not send your payment. Please try again.';

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
  // One state machine (hooks/payAttempt): every stage, guard and public flag is decided there.
  const { attempt, ref, dispatch } = useAttempt();
  // What the last failed start left for the pipeline's own stop to be read against.
  const startError = useRef<{ kind: StartErrorKind; code?: string } | null>(null);
  const lastStart = () => startError.current; // read fresh: the pipeline sets it mid-await

  // The payment's own intent, through the swapped-sell pipeline. A failed start
  // is remembered by kind: the pipeline itself reports only the server's words.
  const createIntent: CreateIntent = async (a) => {
    try {
      return await payConverted(checkout.id, {
        sourceChainId: a.sourceChainId,
        sourceToken: a.token.address,
        sourceAddress: a.sourceAddress,
      });
    } catch (e) {
      startError.current = { kind: classifyStartError(e), code: errorCode(e) };
      throw e;
    }
  };

  const swapped = useSwappedSell({
    createIntent,
    onStage: (stage, snap) => {
      if (stage === 'done') dispatch({ type: 'waiting', swapStage: 'done', order: snap.order });
      // 'idle' from a resume: the sale had nothing left to follow (already delivered or closed) — the server settles it.
      else if (stage === 'idle') { if (ref.current.kind === 'paying') dispatch({ type: 'waiting' }); }
      else dispatch({ type: 'progress', swapStage: stage, order: snap.order });
    },
    onShortfall: (missingUsdc, snap) => dispatch({ type: 'shortfall', missingUsdc, intent: snap.intent }),
    onStop: ({ message, retryable, code }, snap) => {
      const start = startError.current;
      if (start?.kind === 'follow') return; // an attempt already exists: run() follows it
      // Opening the payout, the server paused the payment for support: the same answer as a
      // paused start, so the same state — never Continue, never Pay.
      if (code === PAY_ATTEMPT_UNCERTAIN) {
        dispatch({ type: 'fail', ...PAUSED_FAILURE, paused: true, intent: snap.intent, order: snap.order });
        return;
      }
      if (start?.kind === 'paused' || start?.kind === 'refused') {
        dispatch({
          type: 'fail',
          ...(start.kind === 'paused' ? PAUSED_FAILURE : REFUSED_FAILURE),
          paused: start.kind === 'paused',
        });
        return;
      }
      if (start?.code) {
        dispatch({ type: 'fail', error: message, errorCode: start.code, retryable, intent: snap.intent, order: snap.order });
        return;
      }
      // Her own words when it is safe to start over; payment words when a swap may be under way.
      dispatch({ type: 'stop', message: retryable ? message : PAYMENT_STILL_GOING, retryable, intent: snap.intent, order: snap.order });
    },
  });

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
      dispatch({ type: 'fail', error: errorMessage(e, 'Could not check on your payment. Please try again.'), retryable: true });
      return;
    }
    if (!open) {
      await followDirect();
      return;
    }
    if (open.status === 'delivered' || open.status === 'failed') {
      dispatch({ type: 'waiting' });
      return;
    }
    if (open.status === 'ordered') {
      try {
        const o = open.paycrestOrderId ? await getOrder(open.paycrestOrderId) : null;
        // Her deposit is still to send only while the payout waits for it.
        if (o && o.status !== 'awaiting_deposit') {
          dispatch({ type: 'waiting', order: o });
          return;
        }
      } catch (e) {
        dispatch({ type: 'fail', error: errorMessage(e, 'Could not check on your payment. Please try again.'), retryable: true });
        return;
      }
    }
    dispatch({ type: 'continue', intent: open });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- followDirect reads only checkout.orderId.
  }, [address, checkout.id, checkout.orderId, dispatch]);

  /**
   * No sale of hers pays this payment: it is a direct order, or none. Read the
   * order rather than assume it was sent — a send that failed leaves it waiting
   * on a deposit that never left her wallet, and "waiting for confirmation"
   * would then wait for nothing until the order lapsed.
   */
  async function followDirect() {
    if (!checkout.orderId) {
      dispatch({ type: 'waiting' });
      return;
    }
    try {
      const o = await getOrder(checkout.orderId);
      dispatch(awaitsHerDeposit(o) ? { type: 'deposit', order: o } : { type: 'waiting', order: o });
    } catch (e) {
      dispatch({ type: 'fail', error: errorMessage(e, 'Could not check on your payment. Please try again.'), retryable: true });
    }
  }

  const payDirectly = async (venue: PaycrestVenue, wallet: string, token: LiFiToken, priced: PayEstimate) => {
    const balances = await balancesOf(wallet);
    const routes = await getSponsoredRoutes().catch(() => []);
    const funding = checkFunding(venue, balances, { gasSponsored: isGasSponsored(routes, venue.chain, venue.token) });
    if (!funding.ok) {
      dispatch({ type: 'fail', error: fundingMessage(funding, 'payment') ?? 'This payment cannot be funded.', retryable: true });
      return;
    }

    const order = await payDirect(checkout.id, { chainKey: venue.chain, token: venue.token });
    // The money that moves is the deposit built from THIS order — the server's own quote, made after
    // our check above — so the bound is applied to it, before anything is signed.
    // ...and it must be for what she chose (compared the way the venue was matched), or it is not funded.
    if (order.chain?.toLowerCase() !== venue.chain.toLowerCase() || order.token?.toUpperCase() !== venue.token.toUpperCase()) {
      dispatch({ type: 'fail', error: ORDER_MISMATCH, retryable: false });
      return;
    }
    const moved = orderAboveTolerance(order.tokenAmount, token.decimals, priced.fromAmount);
    if (moved) {
      // No money moved and none will: the order simply lapses. But the payment is linked to it, so
      // a tap would only be refused — hold it for the window rather than offer Pay.
      dispatch({
        type: 'fail',
        error: moved === 'unreadable'
          ? ORDER_AMOUNT_UNCHECKABLE
          : directPriceMovedCopy(moved.toString(), priced.fromAmount, token.decimals, venue.token),
        retryable: false,
      });
      return;
    }
    dispatch({ type: 'progress', swapStage: 'idle', order });
    try {
      await sendAndClaim(order, wallet, balances);
    } catch (e) {
      // The order exists, so Pay (a new one) is gone: the way forward is sending THIS order's deposit again.
      dispatch({ type: 'deposit', order, error: errorMessage(e, SEND_FAILED) });
      return;
    }
    dispatch({ type: 'waiting', order });
  };

  /**
   * Send a direct order's deposit again. The order is read first: one no longer
   * waiting for a deposit, or whose deposit this app already reported sent, is
   * only waited on — never paid twice. A sponsored relay is idempotent per order
   * on the server as well.
   */
  const depositAgain = async (held: PaycrestOrder) => {
    if (!address || isPaying()) return;
    dispatch({ type: 'begin' });
    if (!isPaying()) return;
    let fresh: PaycrestOrder;
    try {
      fresh = await getOrder(held.id);
    } catch (e) {
      dispatch({ type: 'deposit', order: held, error: errorMessage(e, 'Could not check on your payment. Please try again.') });
      return;
    }
    if (!awaitsHerDeposit(fresh)) {
      dispatch({ type: 'waiting', order: fresh });
      return;
    }
    dispatch({ type: 'progress', swapStage: 'sending', order: fresh });
    try {
      await sendAndClaim(fresh, address, await balancesOf(address));
    } catch (e) {
      dispatch({ type: 'deposit', order: fresh, error: errorMessage(e, SEND_FAILED) });
      return;
    }
    dispatch({ type: 'waiting', order: fresh });
  };

  // Read fresh (not narrowed): the machine's answer to what was just dispatched.
  const isPaying = () => ref.current.kind === 'paying';

  const run = async (token: LiFiToken, chainId: number, priced: PayEstimate) => {
    // One attempt at a time: `begin` is refused (and the ref says so at once) while one is in flight.
    if (!address || isPaying()) return;
    dispatch({ type: 'begin' });
    if (!isPaying()) return; // refused (a payment the server paused): nothing is started
    startError.current = null;
    if (isNonEVMLiFiChain(chainId)) {
      dispatch({ type: 'fail', error: unsupportedNetworkMessage(token.symbol), retryable: true });
      return;
    }
    let venue: PaycrestVenue | null;
    try {
      venue = await directVenue(checkout.merchantNet, token, chainId);
    } catch (e) {
      // The rate lookup is not a start route: its words (e.g. the provider being unavailable) are the useful ones.
      dispatch({ type: 'fail', error: errorMessage(e, 'Could not get a rate. Please try again.'), retryable: true });
      return;
    }
    try {
      if (venue) {
        // The fresh amount, held to the price she was shown — before any order exists.
        const fresh = ethers.parseUnits((Math.ceil(venue.tokenAmount * 100) / 100).toFixed(2), token.decimals);
        if (exceedsPriced(fresh, BigInt(priced.fromAmount))) {
          dispatch({
            type: 'fail',
            error: priceMovedMessage(fresh.toString(), priced.fromAmount, token.decimals, venue.token),
            retryable: true,
          });
          return;
        }
        await payDirectly(venue, address, token, priced);
      } else {
        const args: StartSwapArgs = {
          sourceChainId: chainId,
          token: { ...token, chainId },
          walletAddress: address,
          sourceAddress: address,
          pricedFromAmount: priced.fromAmount,
        };
        await swapped.start(args);
        if (lastStart()?.kind === 'follow') {
          startError.current = null;
          await followOpen();
        }
      }
    } catch (e) {
      const kind = classifyStartError(e);
      if (kind === 'follow') await followOpen();
      else dispatch({ type: 'fail', ...startFailure(e), paused: kind === 'paused' });
    }
  };

  const { request, authorization } = usePayAuthorization<Action>({
    title: authorizationTitle,
    onAuthorized: (action) => {
      if (action.kind === 'pay') {
        void run(action.token, action.chainId, action.priced);
      } else if (action.kind === 'wallet') {
        dispatch({ type: 'walletOffered' });
        void swapped.completeWithWallet(action.intent);
      } else if (action.kind === 'deposit') {
        void depositAgain(action.order);
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
      dispatch({ type: 'fail', error: errorMessage(e, 'Could not check on your payment. Please try again.'), retryable: true });
      return;
    }
    if (!fresh) {
      dispatch({ type: 'fail', error: nothingToContinueCopy('fiat'), retryable: false });
    } else if (fresh.status === 'delivered' || fresh.status === 'failed') {
      dispatch({ type: 'waiting' });
    } else {
      dispatch({ type: 'begin' });
      void swapped.resume(fresh, address);
    }
  }

  const payWith = useCallback(
    // Never without the price she was shown: nothing is authorized (or signed) unbound.
    (token: LiFiToken, chainId: number, priced: PayEstimate) => { if (priced?.fromAmount) request({ kind: 'pay', token, chainId, priced }); },
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

  const heldIntent = attempt.kind === 'continue' || attempt.kind === 'failed' ? attempt.intent : undefined;
  const completeWithWallet = useCallback(() => {
    if (attempt.kind === 'shortfall' && attempt.intent) {
      request({ kind: 'wallet', intent: attempt.intent, missingUsdc: attempt.missingUsdc });
    }
  }, [attempt, request]);
  const continuePayment = useCallback(() => {
    if (heldIntent) request({ kind: 'continue', intent: heldIntent });
  }, [heldIntent, request]);
  const depositOrder = attempt.kind === 'deposit' ? attempt.order : undefined;
  const sendDeposit = useCallback(() => {
    if (depositOrder) request({ kind: 'deposit', order: depositOrder });
  }, [depositOrder, request]);

  const reset = useCallback(() => {
    // The machine refuses while an attempt is in flight and for a payment the
    // server paused. A swap that may be under way does not hold a naira reset:
    // it lands in HER wallet, so freeing the payment strands nothing — unlike a
    // crypto payment, which is delivered to the merchant (see usePayCrypto).
    dispatch({ type: 'reset' });
    if (ref.current.kind === 'idle') {
      startError.current = null;
      swapped.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- swapped.reset is stable.
  }, [dispatch, swapped.reset]);

  return {
    ...payAttemptView(attempt),
    authorization,
    walletAddress: address,
    payWith,
    estimate,
    completeWithWallet,
    continuePayment,
    sendDeposit,
    resume: followOpen,
    reset,
  };
}
