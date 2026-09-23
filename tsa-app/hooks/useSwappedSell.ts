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
import AsyncStorage from '@react-native-async-storage/async-storage';
import {
  abandonSellIntent, createSellIntent, openSellOrder, recordSellSwap, requestSellIntentGasTopUp,
  type SellIntent,
} from '@/services/paycrestSellSwap';
import { executeLiFiSwap, SwapBroadcastUnconfirmedError } from '@/services/lifiFlow';
import { getSwapDelivery, isNonEVMLiFiChain, type LiFiToken } from '@/services/lifi';
import { sendSponsoredPaycrestDeposit } from '@/services/paycrestDeposit';
import { claimPayment, getOrder, PaycrestError, type PaycrestOrder } from '@/services/paycrestApi';
import { nativeBalance } from '@/services/wallet';
import { getChainByChainId } from '@/constants/chains';

export type SellStage = 'idle' | 'swapping' | 'waiting' | 'ordering' | 'sending' | 'done' | 'failed';

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

export const pendingSwapKey = (intentId: string) => `paycrest.sellSwap.pendingTx.${intentId}`;

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

/** A stop with a sentence already written for her. */
class SellStop extends Error {}

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

export function useSwappedSell() {
  const [stage, setStage] = useState<SellStage>('idle');
  const [error, setError] = useState<string | undefined>();
  const [order, setOrder] = useState<PaycrestOrder | undefined>();
  const [depositTx, setDepositTx] = useState<string | undefined>();
  const running = useRef(false);

  const fail = (message: string) => {
    setError(message);
    setStage('failed');
  };

  /** A swap that ended short: close the sale so she can start another. */
  const endShort = async (intentId: string, outcome: SwapOutcome): Promise<never> => {
    await abandonSellIntent(intentId).catch((e) => console.warn('[sell] abandon failed', e));
    await AsyncStorage.removeItem(pendingSwapKey(intentId)).catch(() => {});
    throw new SellStop(SWAP_OUTCOME_COPY[outcome]);
  };

  /** Resolves true only when LiFi confirmed DONE/COMPLETED. */
  const waitForSwap = async (intentId: string, hash: string): Promise<boolean> => {
    setStage('waiting');
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
    throw new SellStop(STILL_ON_ITS_WAY);
  };

  /**
   * `landed`: LiFi itself said DONE/COMPLETED — the only evidence here that
   * her USDC is on Base. Without it no failure message claims so.
   */
  const openOrder = async (intentId: string, landed: boolean): Promise<PaycrestOrder> => {
    setStage('ordering');
    for (let i = 0; i < MAX_ORDER_TRIES; i++) {
      if (i > 0) await sleep(POLL_MS);
      try {
        return await openSellOrder(intentId);
      } catch (e) {
        const outcome = outcomeFromServer(e);
        if (outcome) return endShort(intentId, outcome);
        if (isWaitAnswer(e)) continue;
        const why = messageOf(e, 'The payout could not be opened.');
        throw new SellStop(landed
          ? `${why} Your USDC is in your wallet on Base.`
          : `${why} Tap Finish your sale to try again.`);
      }
    }
    throw new SellStop(landed ? PAYOUT_NOT_OPEN : PAYOUT_NOT_OPEN_UNPROVEN);
  };

  const send = async (o: PaycrestOrder, walletArg: string, intentId: string) => {
    // The order's own wallet is the USDC's owner; a resume may not know it otherwise.
    const walletAddress = walletArg || o.walletAddress;
    setOrder(o);
    setStage('sending');
    const baseNative = await nativeBalance('base', walletAddress);
    let tx: string;
    try {
      tx = await sendSponsoredPaycrestDeposit(o, walletAddress, baseNative === null ? null : Number(ethers.formatEther(baseNative)));
    } catch (e) {
      throw new SellStop(`Your USDC is ready, but sending it for payout failed: ${messageOf(e, 'please try again.')}`);
    }
    setDepositTx(tx);
    // The deposit already left; a lost claim only degrades to the ordinary
    // unmatched-deposit path, so it never fails the sale.
    await claimPayment(o.id).catch((e) => console.warn('[sell] claimPayment failed', e));
    await AsyncStorage.removeItem(pendingSwapKey(intentId)).catch(() => {});
    setStage('done');
  };

  /** Report the hash (idempotent server-side), wait, order, send. */
  const finishFromHash = async (intentId: string, hash: string, walletAddress: string, alreadyRecorded: boolean) => {
    if (!alreadyRecorded) {
      try {
        await recordSellSwap(intentId, hash);
      } catch (e) {
        console.warn('[sell] recordSellSwap failed', e);
        throw new SellStop(NOT_CONFIRMED);
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
    setOrder(undefined);
    setDepositTx(undefined);
    const held: string[] = [];
    try {
      await body((intentId) => {
        salesInFlight.add(intentId);
        held.push(intentId);
      });
    } catch (e) {
      fail(e instanceof SellStop ? e.message : messageOf(e, 'Something went wrong. Please try again.'));
    } finally {
      held.forEach((id) => salesInFlight.delete(id));
      running.current = false;
    }
  };

  const start = useCallback((a: StartSellArgs) => guarded(async (hold) => {
    if (anySaleRunning()) throw new SellStop(STILL_RUNNING);
    setStage('swapping');
    startsAwaitingIntent++;
    let created;
    try {
      created = await createSellIntent({
        sourceChainId: a.sourceChainId,
        sourceToken: a.token.address,
        sourceAmount: ethers.parseUnits(a.amount, a.token.decimals).toString(),
        sourceAddress: a.sourceAddress,
        bankCode: a.bank.code,
        accountNumber: a.bank.account,
        accountName: a.bank.name,
        currency: a.currency,
      });
      hold(created.intent.id);
    } finally {
      startsAwaitingIntent--;
    }
    const { intent, quote, gasTopUp } = created;
    const key = pendingSwapKey(intent.id);

    // Gas on the source chain: asked for exactly when the SERVER priced a
    // top-up into this sale — never off a balance check of our own, which
    // would judge a different threshold (the server counts the approve too)
    // and could skip gas the sale was budgeted for. Best-effort and silent:
    // the server skips it if she already holds enough.
    const chain = isNonEVMLiFiChain(a.sourceChainId) ? undefined : getChainByChainId(a.sourceChainId);
    let topUpFailed = false;
    if (gasTopUp) {
      try {
        await requestSellIntentGasTopUp(intent.id);
      } catch (e) {
        console.warn('[sell] gas top-up failed', e);
        topUpFailed = true;
      }
    }

    let hash: string;
    try {
      hash = await executeLiFiSwap({
        quote,
        fromSymbol: a.token.symbol,
        fromDecimals: a.token.decimals,
        fromTokenAddress: a.token.address,
        fromAmount: a.amount,
        walletAddress: a.sourceAddress,
        fromChainId: a.sourceChainId,
        // Our fee is inside the quote the server built; nothing is booked at submit.
        feeFeature: 'none',
        // On the phone BEFORE the broadcast: a reply lost after the server
        // sent it must still leave a hash for resume to follow.
        onSigned: (signed) => AsyncStorage.setItem(key, signed).catch((e) =>
          console.warn('[sell] could not persist the signed swap hash', e)),
      });
    } catch (e) {
      if (e instanceof SwapBroadcastUnconfirmedError) {
        // It may be on its way: keep the hash and the sale for resume.
        throw new SellStop(BROADCAST_UNCONFIRMED);
      }
      // Refused before or at the node, so nothing left her wallet: drop any
      // signed hash and close the sale so it does not block the next.
      await AsyncStorage.removeItem(key).catch(() => {});
      await abandonSellIntent(intent.id).catch((err) => console.warn('[sell] abandon failed', err));
      if (topUpFailed && chain) {
        throw new SellStop(
          `You need a little ${chain.nativeCurrency.symbol} on ${chain.name} to pay the network fee, and we couldn't cover it right now. Nothing was sold.`,
        );
      }
      throw e;
    }

    // Already on the phone for an EVM swap (onSigned); the non-EVM paths only
    // learn their hash here. On the phone first, THEN the server.
    await AsyncStorage.setItem(key, hash).catch((e) =>
      console.warn('[sell] could not persist the swap hash', e));
    await finishFromHash(intent.id, hash, a.walletAddress, false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the steps read only their arguments and stable setters.
  }), []);

  /** Pick up a sale where it stopped. `walletAddress` is her EVM wallet. */
  const resume = useCallback((intent: SellIntent, walletAddress?: string) => guarded(async (hold) => {
    // Another screen is driving a sale right now (maybe this one, before its
    // id came back): leave it alone rather than race it.
    if (salesInFlight.has(intent.id) || startsAwaitingIntent > 0) throw new SellStop(STILL_RUNNING);
    hold(intent.id);
    const wallet = walletAddress ?? '';
    if (intent.status === 'ordered') {
      if (!intent.paycrestOrderId) throw new SellStop(PAYOUT_NOT_OPEN);
      setStage('sending');
      const o = await getOrder(intent.paycrestOrderId);
      setOrder(o);
      if (o.status === 'awaiting_deposit') return send(o, wallet, intent.id);
      if (PAID_STATUSES.has(o.status)) {
        setStage('done');
        return;
      }
      throw new SellStop(ORDER_CLOSED);
    }
    if (intent.status === 'failed') {
      setStage('idle');
      return;
    }
    // 'ordering' (a claim in flight, or one the server will reclaim) is the
    // same step as 'swapped': ask for the order, which is idempotent.
    const stored = await AsyncStorage.getItem(pendingSwapKey(intent.id)).catch(() => null);
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
   * Close a sale that has sat stuck long enough (see canCancelSale) — a lost
   * reply that left it "on its way" forever, or a hash someone else recorded
   * first. Never one this app is still driving. If her swap does land later,
   * the USDC is in her own wallet on Base and sells directly as USDC.
   */
  const cancel = useCallback(async (intent: SellIntent) => {
    if (salesInFlight.has(intent.id) || startsAwaitingIntent > 0) throw new Error(STILL_RUNNING);
    await abandonSellIntent(intent.id);
    await AsyncStorage.removeItem(pendingSwapKey(intent.id)).catch(() => {});
  }, []);

  return { stage, error, order, depositTx, start, resume, cancel };
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
