// hooks/useSwappedSell.ts
//
// The swapped sell (design §5.1), run for her after one tap: open the sale,
// sign the server's LiFi quote, wait for USDC to land on Base, have the server
// open the payout for exactly what landed, send it with the sponsored relay,
// and claim the payment. She picks a token and an amount and taps Sell; she
// does not drive any of the steps.
//
// Two things make this safe to kill at any point:
//   - the swap hash is written to AsyncStorage BEFORE the server is told
//     about it, so a kill between the two loses nothing (resume reads it);
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
import { executeLiFiSwap } from '@/services/lifiFlow';
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
const NEVER_STARTED =
  "That sale's swap never started, so we've closed it — nothing was sold. You can start a new sale.";
const ORDER_CLOSED =
  'Your payout order closed before the USDC was sent. The USDC is in your wallet on Base — you can sell it here as USDC.';

/** A stop with a sentence already written for her. */
class SellStop extends Error {}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));
const messageOf = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

/** The server's "your swap didn't complete (DONE/REFUNDED)" names the outcome too. */
function outcomeFromServer(e: unknown): SwapOutcome | null {
  if (!(e instanceof PaycrestError) || e.status !== 409) return null;
  if (/DONE\/REFUNDED/.test(e.message)) return 'REFUNDED';
  if (/DONE\/PARTIAL/.test(e.message)) return 'PARTIAL';
  if (/\(FAILED/.test(e.message)) return 'FAILED';
  return null;
}

/** 409s that mean "not yet" — the server's LiFi view or claim has not caught up. */
const isWaitAnswer = (e: unknown) =>
  e instanceof PaycrestError && e.status === 409 && /try again in a moment/i.test(e.message);

/** Deposit statuses where the USDC has left her wallet or is on its way. */
const PAID_STATUSES = new Set(['pending', 'settling', 'settled', 'converting']);

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

  const waitForSwap = async (intentId: string, hash: string) => {
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
        return;
      }
    }
    throw new SellStop(STILL_ON_ITS_WAY);
  };

  const openOrder = async (intentId: string): Promise<PaycrestOrder> => {
    setStage('ordering');
    for (let i = 0; i < MAX_ORDER_TRIES; i++) {
      if (i > 0) await sleep(POLL_MS);
      try {
        return await openSellOrder(intentId);
      } catch (e) {
        const outcome = outcomeFromServer(e);
        if (outcome) return endShort(intentId, outcome);
        if (isWaitAnswer(e)) continue;
        throw new SellStop(`${messageOf(e, 'The payout could not be opened.')} Your USDC is in your wallet on Base.`);
      }
    }
    throw new SellStop(PAYOUT_NOT_OPEN);
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
    await waitForSwap(intentId, hash);
    const o = await openOrder(intentId);
    await send(o, walletAddress, intentId);
  };

  const guarded = async (body: () => Promise<void>) => {
    if (running.current) return;
    running.current = true;
    setError(undefined);
    setOrder(undefined);
    setDepositTx(undefined);
    try {
      await body();
    } catch (e) {
      fail(e instanceof SellStop ? e.message : messageOf(e, 'Something went wrong. Please try again.'));
    } finally {
      running.current = false;
    }
  };

  const start = useCallback((a: StartSellArgs) => guarded(async () => {
    setStage('swapping');
    const { intent, quote } = await createSellIntent({
      sourceChainId: a.sourceChainId,
      sourceToken: a.token.address,
      sourceAmount: ethers.parseUnits(a.amount, a.token.decimals).toString(),
      sourceAddress: a.sourceAddress,
      bankCode: a.bank.code,
      accountNumber: a.bank.account,
      accountName: a.bank.name,
      currency: a.currency,
    });

    // Gas on an EVM source: best-effort and silent. null = unknown, never
    // zero — try the swap; the node refuses cleanly if she is really short.
    const chain = isNonEVMLiFiChain(a.sourceChainId) ? undefined : getChainByChainId(a.sourceChainId);
    let topUpFailed = false;
    if (chain && quote.gasCostWei) {
      const nb = await nativeBalance(chain.key, a.sourceAddress);
      if (nb !== null && nb < BigInt(quote.gasCostWei)) {
        try {
          await requestSellIntentGasTopUp(intent.id);
        } catch (e) {
          console.warn('[sell] gas top-up failed', e);
          topUpFailed = true;
        }
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
      });
    } catch (e) {
      // Nothing left her wallet: close the sale so it does not block the next.
      await abandonSellIntent(intent.id).catch((err) => console.warn('[sell] abandon failed', err));
      if (topUpFailed && chain) {
        throw new SellStop(
          `You need a little ${chain.nativeCurrency.symbol} on ${chain.name} to pay the network fee, and we couldn't cover it right now. Nothing was sold.`,
        );
      }
      throw e;
    }

    // On the phone first, THEN the server: a kill in between loses nothing.
    await AsyncStorage.setItem(pendingSwapKey(intent.id), hash).catch((e) =>
      console.warn('[sell] could not persist the swap hash', e));
    await finishFromHash(intent.id, hash, a.walletAddress, false);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the steps read only their arguments and stable setters.
  }), []);

  /** Pick up a sale where it stopped. `walletAddress` is her EVM wallet. */
  const resume = useCallback((intent: SellIntent, walletAddress?: string) => guarded(async () => {
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
      await abandonSellIntent(intent.id).catch((e) => console.warn('[sell] abandon failed', e));
      throw new SellStop(NEVER_STARTED);
    }
    await finishFromHash(intent.id, hash, wallet, !!intent.swapTxHash);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- as for start.
  }), []);

  return { stage, error, order, depositTx, start, resume };
}
