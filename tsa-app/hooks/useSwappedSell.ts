// hooks/useSwappedSell.ts
//
// The swapped sell (design §5.1), run for her after one tap: open the sale,
// sign the server's LiFi quote, wait for USDC to land on Base, have the server
// open the payout for exactly what landed, send it with the sponsored relay,
// and claim the payment. She picks a token and an amount and taps Sell; she
// does not drive any of the steps.
//
// Three things make this safe to kill or leave at any point:
//   - the signed swap's hash is written to AsyncStorage BEFORE it is
//     broadcast, so neither a kill nor a lost reply loses it (resume reads it);
//   - one run per sale across every screen: a sale being driven here is never
//     resumed or closed from another copy of this hook (see salesInFlight);
//   - nothing is ever held by us — before the swap her tokens are in her
//     wallet, after it the USDC is. A sale that stops is finished later from
//     "Finish your sale", never started over.

import { useCallback, useRef, useState } from 'react';
import { ethers } from 'ethers';
import {
  abandonSellIntent, createSellIntent, openSellOrder, recordSellSwap, type SellIntent,
} from '@/services/paycrestSellSwap';
import { SwapBroadcastUnconfirmedError } from '@/services/lifiFlow';
import {
  clearPendingSwapHash, executeSellSwap, pendingSwapKey, readPendingSwapHash,
} from '@/services/sellSwapRun';
import { getSwapDelivery, type LiFiQuote, type LiFiToken } from '@/services/lifi';
import { sendSponsoredPaycrestDeposit } from '@/services/paycrestDeposit';
import { claimPayment, getOrder, PaycrestError, type PaycrestOrder } from '@/services/paycrestApi';
import { nativeBalance } from '@/services/wallet';

/** 'shortfall' is TSA Pay only: the swap landed short of what the payout needs. */
export type SellStage =
  | 'idle' | 'swapping' | 'waiting' | 'ordering' | 'sending' | 'shortfall' | 'done' | 'failed';

export type StartSellArgs = {
  sourceChainId: number;
  token: LiFiToken;
  /** Human units, as she typed it. */
  amount: string;
  /** Her EVM wallet: the USDC receiver on Base, and the deposit's sender. */
  walletAddress: string;
  /** Her address on the source chain — a Solana address for a Solana token. */
  sourceAddress: string;
  bank: { code: string; account: string; name?: string };
  currency?: string;
};

/**
 * What a run is started with. A sale supplies all of StartSellArgs; a payment
 * (an injected `createIntent`) supplies only where the tokens are — the amount
 * is the server's quote, and the bank is the merchant's, held server-side.
 */
export type StartSwapArgs = Omit<StartSellArgs, 'amount' | 'bank' | 'currency'> &
  Partial<Pick<StartSellArgs, 'amount' | 'bank' | 'currency'>> & {
    /** A payment: the amount (source base units) she was shown. The quote signed is held to it. */
    pricedFromAmount?: string;
    /** A crypto payment: its "don't pay again" hold is persisted when the swap is signed. */
    paymentId?: string;
  };

/** A started intent: what to sign, and whether the server priced a gas top-up into it. */
export type CreatedSellIntent = { intent: SellIntent; quote: LiFiQuote; gasTopUp?: boolean };

/** Where a sale's intent comes from. The default is her own sale; TSA Pay injects the payment's. */
export type CreateIntent = (a: StartSwapArgs) => Promise<CreatedSellIntent>;

/** Her own sale: the amount and the bank are hers. */
const createOwnSale: CreateIntent = async (a) => {
  if (a.amount === undefined || !a.bank) throw new Error('A sale needs an amount and a payout account.');
  return createSellIntent({
    sourceChainId: a.sourceChainId,
    sourceToken: a.token.address,
    sourceAmount: ethers.parseUnits(a.amount, a.token.decimals).toString(),
    sourceAddress: a.sourceAddress,
    bankCode: a.bank.code,
    accountNumber: a.bank.account,
    accountName: a.bank.name,
    currency: a.currency,
  });
};

export { pendingSwapKey };

const POLL_MS = 15_000;
/** 40 × 15 s = 10 minutes: past any normal bridge, short of giving up on one. */
const MAX_POLLS = 40;
/** How often "still swapping" from the server is waited out before handing back. */
const MAX_ORDER_TRIES = 8;

/** The terminal ways a swap can end short — each has its own sentence (§5.5). */
export const SWAP_OUTCOME_COPY = {
  FAILED: "The swap didn't go through — your tokens are still in your wallet. Nothing was sold.",
  REFUNDED: 'The swap was refunded — your tokens are on their way back to your wallet. Nothing was sold.',
  PARTIAL:
    'You received a different token than the one quoted — check your wallet, then contact support and we will help you finish.',
} as const;
type SwapOutcome = keyof typeof SWAP_OUTCOME_COPY;

const STILL_ON_ITS_WAY =
  'Your swap is still on its way. Come back in a few minutes and tap Finish your sale — nothing is lost.';
const NOT_CONFIRMED =
  "Your swap was sent, but we couldn't confirm it with our server. Tap Finish your sale to carry on — nothing is lost.";
const PAYOUT_NOT_OPEN =
  "Your USDC is in your wallet, but the payout couldn't be opened yet. Tap Finish your sale to try again.";
const PAYOUT_NOT_OPEN_UNPROVEN =
  "The payout couldn't be opened yet. Tap Finish your sale to try again.";
const NO_SWAP_FOUND =
  "We couldn't find a swap for this sale, so we've closed it. Check your wallet before starting a new sale.";
const BROADCAST_UNCONFIRMED =
  "We couldn't confirm your swap was sent. Give it a few minutes, then tap Finish your sale to check on it — don't sell these tokens again until then.";
const STILL_RUNNING =
  'Your sale is still going through. Give it a few minutes, then tap Finish your sale if it has not finished.';
const ORDER_CLOSED =
  'Your payout order closed before the USDC was sent. The USDC is in your wallet on Base — you can sell it here as USDC.';

/**
 * A stop with a sentence already written for her. `retryable: false` means the
 * sale (or a TSA Pay payment) cannot safely be started over from here: a swap
 * may be under way, or its USDC is already in her wallet.
 */
class SellStop extends Error {
  constructor(message: string, readonly retryable = true) {
    super(message);
  }
}

/** TSA Pay: the swap landed short. Not a failure — the sale stays open at its own stage. */
class SellShortfall extends Error {
  constructor(readonly missingUsdc: string | undefined) {
    super('The swap landed short of the payout.');
  }
}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
const messageOf = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

/**
 * The codes the server names a sale's outcome with (tsa-api-go
 * handlers/paycrest_sell_intents.go). These decide what happens next; the
 * message beside them is copy, free to change.
 */
const TERMINAL_CODES = new Map<string, SwapOutcome>([
  ['swap_failed', 'FAILED'],
  ['swap_refunded', 'REFUNDED'],
  ['swap_partial', 'PARTIAL'],
]);
const WAIT_CODES = new Set(['swap_pending', 'payout_opening']);

/** How the server says the swap ended short, or null when it did not say so. */
function outcomeFromServer(e: unknown): SwapOutcome | null {
  if (!(e instanceof PaycrestError) || e.status !== 409) return null;
  if (e.code) return TERMINAL_CODES.get(e.code) ?? null;
  // No code: an API build that predates them. Read its wording, pinned by tests.
  if (/DONE\/REFUNDED/.test(e.message)) return 'REFUNDED';
  if (/DONE\/PARTIAL/.test(e.message)) return 'PARTIAL';
  if (/\(FAILED/.test(e.message)) return 'FAILED';
  return null;
}

const isCode = (e: unknown, code: string): e is PaycrestError =>
  e instanceof PaycrestError && e.status === 409 && e.code === code;

/** 409s that mean "not yet" — the server's LiFi view or claim has not caught up. */
function isWaitAnswer(e: unknown): boolean {
  if (!(e instanceof PaycrestError) || e.status !== 409) return false;
  if (e.code) return WAIT_CODES.has(e.code);
  return /try again in a moment/i.test(e.message);
}

/** Deposit statuses where the USDC has left her wallet or is on its way. */
const PAID_STATUSES = new Set(['pending', 'settling', 'settled', 'converting']);

// Module scope, not per hook: she can leave the screen mid-swap and come back
// to a NEW copy of this hook while the first run is still going (an approval
// wait, a gas top-up). "No hash yet" then means "not signed yet", not "never
// will be" — so a sale held here is never resumed or closed from elsewhere.
const salesInFlight = new Set<string>();
/** Starts that have not heard their intent id back yet — their sale may already exist. */
let startsAwaitingIntent = 0;
const anySaleRunning = () => salesInFlight.size > 0 || startsAwaitingIntent > 0;

/** What a run holds at the moment an event is emitted. */
export type SellSnapshot = { intent?: SellIntent; order?: PaycrestOrder };

/**
 * A run's progress as events, for a caller that drives it rather than renders
 * it (TSA Pay keeps its own state machine and dispatches these into it). The
 * ordinary sell screen passes none and reads the returned state instead.
 */
export type SellEvents = {
  /** Each stage a run enters: swapping, waiting, ordering, sending, done (and idle when a resume has nothing to follow). */
  onStage?: (stage: SellStage, snapshot: SellSnapshot) => void;
  /** The run stopped with a sentence for her, and whether it is safe to start over from. */
  onStop?: (stop: { message: string; retryable: boolean }, snapshot: SellSnapshot) => void;
  /** The swap landed short of what the payout needs (TSA Pay). */
  onShortfall?: (missingUsdc: string | undefined, snapshot: SellSnapshot) => void;
};

export function useSwappedSell(opts?: { createIntent?: CreateIntent } & SellEvents) {
  const [stage, setStage] = useState<SellStage>('idle');
  const [error, setError] = useState<string | undefined>();
  const [order, setOrder] = useState<PaycrestOrder | undefined>();
  const [depositTx, setDepositTx] = useState<string | undefined>();
  const [intent, setIntent] = useState<SellIntent | undefined>();
  const [missingUsdc, setMissingUsdc] = useState<string | undefined>();
  const [retryable, setRetryable] = useState(true);
  const running = useRef(false);
  // start is memoised for good, so the creator is read through a ref: a payment
  // screen's creator closes over its own payment id and must never go stale.
  const createIntentRef = useRef(opts?.createIntent);
  createIntentRef.current = opts?.createIntent;
  // Events are read through a ref too, and from what the run holds RIGHT NOW.
  const eventsRef = useRef<SellEvents>({});
  eventsRef.current = opts ?? {};
  const held = useRef<SellSnapshot>({});
  const keepIntent = (i: SellIntent | undefined) => { held.current = { ...held.current, intent: i }; setIntent(i); };
  const keepOrder = (o: PaycrestOrder | undefined) => { held.current = { ...held.current, order: o }; setOrder(o); };
  const snapshot = (): SellSnapshot => ({ intent: held.current.intent, order: held.current.order });

  /** Enter a stage, and tell whoever is listening. */
  const enter = (next: SellStage) => {
    setStage(next);
    eventsRef.current.onStage?.(next, snapshot());
  };

  const fail = (message: string, canRetry = true) => {
    setError(message);
    setRetryable(canRetry);
    setStage('failed');
    eventsRef.current.onStop?.({ message, retryable: canRetry }, snapshot());
  };

  /** A swap that ended short: close the sale so she can start another. */
  const endShort = async (intentId: string, outcome: SwapOutcome): Promise<never> => {
    await abandonSellIntent(intentId).catch((e) => console.warn('[sell] abandon failed', e));
    await clearPendingSwapHash(intentId);
    throw new SellStop(SWAP_OUTCOME_COPY[outcome]);
  };

  /** Resolves true only when LiFi confirmed DONE/COMPLETED. */
  const waitForSwap = async (intentId: string, hash: string): Promise<boolean> => {
    enter('waiting');
    for (let i = 0; i < MAX_POLLS; i++) {
      if (i > 0) await sleep(POLL_MS);
      const d = await getSwapDelivery(hash);
      if (d.status === 'FAILED') return endShort(intentId, 'FAILED');
      if (d.status === 'DONE') {
        if (d.substatus === 'REFUNDED' || d.substatus === 'PARTIAL') return endShort(intentId, d.substatus);
        // COMPLETED, or a feed that carried no qualifier: the server re-checks
        // DONE + COMPLETED itself before opening anything, and names any other
        // outcome in its answer (see openOrder).
        return d.substatus === 'COMPLETED';
      }
    }
    throw new SellStop(STILL_ON_ITS_WAY, false);
  };

  /**
   * `landed`: LiFi itself said DONE/COMPLETED — the only evidence here that
   * her USDC is on Base. Without it no failure message claims so.
   */
  const openOrder = async (
    intentId: string,
    landed: boolean,
    openOpts?: { useWalletBalance?: boolean },
  ): Promise<PaycrestOrder> => {
    enter('ordering');
    const open = () => (openOpts ? openSellOrder(intentId, openOpts) : openSellOrder(intentId));
    for (let i = 0; i < MAX_ORDER_TRIES; i++) {
      if (i > 0) await sleep(POLL_MS);
      try {
        try {
          return await open();
        } catch (e) {
          // The payout's rate moved while it was being opened; the server has
          // dropped that order and a fresh one is priced from the new rate.
          if (!isCode(e, 'rate_moved')) throw e;
          return await open();
        }
      } catch (e) {
        if (isCode(e, 'pay_shortfall')) throw new SellShortfall(e.missingUsdc);
        const outcome = outcomeFromServer(e);
        if (outcome) return endShort(intentId, outcome);
        if (isWaitAnswer(e)) continue;
        const why = messageOf(e, 'The payout could not be opened.');
        throw new SellStop(landed
          ? `${why} Your USDC is in your wallet on Base.`
          : `${why} Tap Finish your sale to try again.`, false);
      }
    }
    throw new SellStop(landed ? PAYOUT_NOT_OPEN : PAYOUT_NOT_OPEN_UNPROVEN, false);
  };

  const send = async (o: PaycrestOrder, walletArg: string, intentId: string) => {
    // The order's own wallet is the USDC's owner; a resume may not know it otherwise.
    const walletAddress = walletArg || o.walletAddress;
    keepOrder(o);
    enter('sending');
    const baseNative = await nativeBalance('base', walletAddress);
    let tx: string;
    try {
      tx = await sendSponsoredPaycrestDeposit(o, walletAddress, baseNative === null ? null : Number(ethers.formatEther(baseNative)));
    } catch (e) {
      throw new SellStop(`Your USDC is ready, but sending it for payout failed: ${messageOf(e, 'please try again.')}`, false);
    }
    setDepositTx(tx);
    // The deposit already left; a lost claim only degrades to the ordinary
    // unmatched-deposit path, so it never fails the sale.
    await claimPayment(o.id).catch((e) => console.warn('[sell] claimPayment failed', e));
    await clearPendingSwapHash(intentId);
    enter('done');
  };

  /** Report the hash (idempotent server-side), wait, order, send. */
  const finishFromHash = async (intentId: string, hash: string, walletAddress: string, alreadyRecorded: boolean) => {
    if (!alreadyRecorded) {
      try {
        await recordSellSwap(intentId, hash);
      } catch (e) {
        console.warn('[sell] recordSellSwap failed', e);
        throw new SellStop(NOT_CONFIRMED, false);
      }
    }
    const landed = await waitForSwap(intentId, hash);
    const o = await openOrder(intentId, landed);
    await send(o, walletAddress, intentId);
  };

  /** `hold(id)` claims a sale for this run app-wide; the claim ends with the run. */
  const guarded = async (body: (hold: (intentId: string) => void) => Promise<void>) => {
    if (running.current) return;
    running.current = true;
    setError(undefined);
    keepOrder(undefined);
    // What events report is THIS run's: the previous run's intent must not ride along on a failure before this one has its own.
    held.current = {};
    setDepositTx(undefined);
    setMissingUsdc(undefined);
    setRetryable(true);
    const heldIds: string[] = [];
    try {
      await body((intentId) => {
        salesInFlight.add(intentId);
        heldIds.push(intentId);
      });
    } catch (e) {
      if (e instanceof SellShortfall) {
        setMissingUsdc(e.missingUsdc);
        setStage('shortfall');
        eventsRef.current.onShortfall?.(e.missingUsdc, snapshot());
      } else {
        fail(
          e instanceof SellStop ? e.message : messageOf(e, 'Something went wrong. Please try again.'),
          e instanceof SellStop ? e.retryable : true,
        );
      }
    } finally {
      heldIds.forEach((id) => salesInFlight.delete(id));
      running.current = false;
    }
  };

  const start = useCallback((a: StartSwapArgs) => guarded(async (hold) => {
    if (anySaleRunning()) throw new SellStop(STILL_RUNNING, false);
    enter('swapping');
    startsAwaitingIntent++;
    let created: CreatedSellIntent;
    try {
      created = await (createIntentRef.current ?? createOwnSale)(a);
      hold(created.intent.id);
      keepIntent(created.intent);
    } finally {
      startsAwaitingIntent--;
    }
    const { intent, quote, gasTopUp } = created;

    let hash: string;
    try {
      hash = await executeSellSwap({
        intentId: intent.id,
        quote,
        gasTopUp: gasTopUp === true,
        token: a.token,
        sourceChainId: a.sourceChainId,
        sourceAddress: a.sourceAddress,
        amount: a.amount,
        pricedFromAmount: a.pricedFromAmount,
        paymentId: a.paymentId,
      });
    } catch (e) {
      // It may be on its way: keep the hash and the sale for resume.
      if (e instanceof SwapBroadcastUnconfirmedError) throw new SellStop(BROADCAST_UNCONFIRMED, false);
      throw e;
    }
    await finishFromHash(intent.id, hash, a.walletAddress, false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the steps read only their arguments and stable setters.
  }), []);

  /** Pick up a sale where it stopped. `walletAddress` is her EVM wallet. */
  const resume = useCallback((intent: SellIntent, walletAddress?: string) => guarded(async (hold) => {
    // Another screen is driving a sale right now (maybe this one, before its
    // id came back): leave it alone rather than race it.
    // Its own intent goes on the record BEFORE the still-running check: that stop is about this sale, and
    // whoever is listening (TSA Pay's Continue) must be able to carry on with it.
    keepIntent(intent);
    if (salesInFlight.has(intent.id) || startsAwaitingIntent > 0) throw new SellStop(STILL_RUNNING, false);
    hold(intent.id);
    const wallet = walletAddress ?? '';
    if (intent.status === 'ordered') {
      if (!intent.paycrestOrderId) throw new SellStop(PAYOUT_NOT_OPEN, false);
      enter('sending');
      const o = await getOrder(intent.paycrestOrderId);
      keepOrder(o);
      if (o.status === 'awaiting_deposit') return send(o, wallet, intent.id);
      if (PAID_STATUSES.has(o.status)) {
        enter('done');
        return;
      }
      throw new SellStop(ORDER_CLOSED, false);
    }
    if (intent.status === 'failed' || intent.status === 'delivered') {
      enter('idle');
      return;
    }
    // 'ordering' (a claim in flight, or one the server will reclaim) is the
    // same step as 'swapped': ask for the order, which is idempotent.
    const stored = await readPendingSwapHash(intent.id);
    const hash = intent.swapTxHash || stored;
    if (!hash) {
      // No run of this sale is going in this app (checked above), and an EVM
      // swap's hash is stored before it is broadcast — so there is no swap to
      // follow. Say only that, not that nothing ever moved.
      await abandonSellIntent(intent.id).catch((e) => console.warn('[sell] abandon failed', e));
      throw new SellStop(NO_SWAP_FOUND);
    }
    await finishFromHash(intent.id, hash, wallet, !!intent.swapTxHash);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- as for start.
  }), []);

  /**
   * TSA Pay, after a shortfall: open the payout for what the swap landed PLUS
   * the rest from the USDC already in her wallet on Base, send it, and finish.
   * Her USDC is not touched until she taps this — the swap having landed is
   * why the order is opened with `landed`.
   */
  const completeWithWallet = useCallback((shortIntent: SellIntent) => guarded(async (hold) => {
    keepIntent(shortIntent); // as in resume: a still-running stop is about this sale
    if (salesInFlight.has(shortIntent.id) || startsAwaitingIntent > 0) throw new SellStop(STILL_RUNNING, false);
    hold(shortIntent.id);
    const o = await openOrder(shortIntent.id, true, { useWalletBalance: true });
    // The order's own wallet is the USDC's owner.
    await send(o, '', shortIntent.id);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- as for start.
  }), []);

  /**
   * Close a sale that has sat stuck long enough (see canCancelSale) — a lost
   * reply that left it "on its way" forever, or a hash someone else recorded
   * first. Never one this app is still driving. If her swap does land later,
   * the USDC is in her own wallet on Base and sells directly as USDC.
   */
  const cancel = useCallback(async (intent: SellIntent) => {
    if (salesInFlight.has(intent.id) || startsAwaitingIntent > 0) throw new Error(STILL_RUNNING);
    await abandonSellIntent(intent.id);
    await clearPendingSwapHash(intent.id);
  }, []);

  /** Back to idle, dropping what a finished or stopped run held. Never while a run is going. */
  const reset = useCallback(() => {
    if (running.current) return;
    setStage('idle');
    held.current = {};
    setError(undefined);
    setOrder(undefined);
    setDepositTx(undefined);
    setIntent(undefined);
    setMissingUsdc(undefined);
    setRetryable(true);
  }, []);

  return {
    stage, error, retryable, order, depositTx, intent, missingUsdc, start, resume, completeWithWallet, cancel, reset,
  };
}

/** How long a sale must sit unmoved before she may cancel it herself. */
export const CANCEL_AFTER_MS = 10 * 60_000;

/**
 * Whether "Cancel this sale" is offered: only before the payout opens (the
 * server refuses anything else) and only once the sale has sat in the same
 * step for CANCEL_AFTER_MS — so it is never a way to cancel a swap that is
 * genuinely still going through. An unknown time never qualifies.
 */
export function canCancelSale(intent: SellIntent, now: number = Date.now()): boolean {
  if (intent.status !== 'swapping' && intent.status !== 'swapped') return false;
  const since = intent.updatedAt ? Date.parse(intent.updatedAt) : NaN;
  return Number.isFinite(since) && now - since >= CANCEL_AFTER_MS;
}
