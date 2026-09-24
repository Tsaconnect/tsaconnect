// services/sellSwapRun.ts
//
// Signing a sell intent's LiFi swap, shared by everything that drives one
// (hooks/useSwappedSell for a sale, hooks/usePayCrypto for a TSA Pay payment).
// What is common, and must stay one copy:
//   - the source-chain gas top-up, asked for exactly when the SERVER priced one
//     into the intent, never off a balance of our own;
//   - the signed swap's hash written to AsyncStorage BEFORE it is broadcast, so
//     neither a kill nor a lost reply loses it (resume reads it);
//   - a swap refused before or at the node closes the intent, so it does not
//     block the next one. A swap that may have been broadcast never does.

import { ethers } from 'ethers';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getChainByChainId } from '@/constants/chains';
import { executeLiFiSwap, SwapBroadcastUnconfirmedError } from '@/services/lifiFlow';
import { isNativeTokenAddress, isNonEVMLiFiChain, type LiFiQuote, type LiFiToken } from '@/services/lifi';
import { abandonSellIntent, requestSellIntentGasTopUp } from '@/services/paycrestSellSwap';
import { clearPayHold, writePayHold } from '@/services/payHold';
import { exceedsPriced, priceMovedMessage } from '@/utils/payCheckout';

export const pendingSwapKey = (intentId: string) => `paycrest.sellSwap.pendingTx.${intentId}`;

/** The swap hash this phone stored for an intent, if any. */
export const readPendingSwapHash = (intentId: string): Promise<string | null> =>
  AsyncStorage.getItem(pendingSwapKey(intentId)).catch(() => null);

export const clearPendingSwapHash = (intentId: string): Promise<void> =>
  AsyncStorage.removeItem(pendingSwapKey(intentId)).catch(() => {});

export type SellSwapRun = {
  intentId: string;
  /** The server's LiFi quote for this intent. */
  quote: LiFiQuote;
  /** The server priced a source-chain gas top-up into this intent. */
  gasTopUp: boolean;
  token: LiFiToken;
  sourceChainId: number;
  /** Her address on the source chain. */
  sourceAddress: string;
  /**
   * Human units she is spending. Wins when given (a sale signs what she typed);
   * a payment has no typed amount and signs what the server's quote pulls.
   */
  amount?: string;
  /**
   * TSA Pay: the amount (source base units) she was SHOWN and agreed to. The
   * quote actually signed comes from the server AFTER she authorises, so it is
   * held to this: past a 2% tolerance, or (for a native coin) with a value
   * that is not the amount, nothing is signed. An ordinary sale supplies none
   * and is bounded by nothing here, exactly as before.
   */
  pricedFromAmount?: string;
  /** TSA Pay: the payment this swap pays. Its "don't pay again" hold is persisted beside the hash. */
  paymentId?: string;
};

/** The fresh quote costs more than she agreed to (or cannot be checked against it). A plain refusal: retry after a new price. */
export class SwapPriceMovedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'SwapPriceMovedError';
  }
}

/** Refuse (before anything is signed) unless the quote honours the priced amount. Throws SwapPriceMovedError. */
function checkAgainstPrice(run: SellSwapRun): void {
  const { pricedFromAmount, quote, token } = run;
  if (pricedFromAmount === undefined) return;
  const cannotCheck = new SwapPriceMovedError("We couldn't check the price of this payment, so nothing was sent. Please try again.");
  if (!quote.fromAmount) throw cannotCheck;
  let fresh: bigint;
  let priced: bigint;
  try {
    fresh = BigInt(quote.fromAmount);
    priced = BigInt(pricedFromAmount);
  } catch {
    throw cannotCheck;
  }
  if (exceedsPriced(fresh, priced)) {
    throw new SwapPriceMovedError(priceMovedMessage(quote.fromAmount, pricedFromAmount, token.decimals, token.symbol));
  }
  // A native coin is spent through the transaction's own value: it must be exactly what the quote says.
  if (isNativeTokenAddress(token.address)) {
    let value: bigint | undefined;
    try {
      value = quote.transactionRequest?.value === undefined ? undefined : BigInt(quote.transactionRequest.value);
    } catch {
      value = undefined;
    }
    if (value !== fresh) {
      throw new SwapPriceMovedError("This payment's transaction doesn't match the price you were shown, so nothing was sent. Please try again.");
    }
  }
}

/**
 * Human units to spend: the caller's explicit amount if given, else what the
 * quote pulls. Undefined when it is not a positive amount — never "0", never a
 * guess (formatUnits('0') is "0.0", which is truthy).
 */
function spendAmount({ amount, quote, token }: SellSwapRun): string | undefined {
  try {
    if (amount !== undefined) return ethers.parseUnits(amount, token.decimals) > 0n ? amount : undefined;
    if (!quote.fromAmount) return undefined;
    const base = BigInt(quote.fromAmount);
    return base > 0n ? ethers.formatUnits(base, token.decimals) : undefined;
  } catch {
    return undefined;
  }
}

/**
 * Sign and send the swap; resolves to its hash, already stored on the phone.
 * Throws SwapBroadcastUnconfirmedError untouched (the swap may be on its way:
 * the caller decides what to say, and the hash stays stored). Any other
 * failure means nothing left her wallet: the stored hash is dropped, the
 * intent closed, and the error rethrown.
 */
export async function executeSellSwap(run: SellSwapRun): Promise<string> {
  const { intentId, quote, token, sourceChainId, sourceAddress } = run;

  try {
    checkAgainstPrice(run);
  } catch (e) {
    // Before anything is signed: close the intent so the payment is not left waiting on it.
    await abandonSellIntent(intentId).catch((err) => console.warn('[sell] abandon failed', err));
    throw e;
  }

  const fromAmount = spendAmount(run);
  if (!fromAmount) {
    // Never sign for "0" or a guess: close the intent so it does not block the next.
    await abandonSellIntent(intentId).catch((err) => console.warn('[sell] abandon failed', err));
    throw new Error("We couldn't work out the amount to send, so nothing was sent. Please try again.");
  }

  // Best-effort and silent: the server skips it if she already holds enough.
  const chain = isNonEVMLiFiChain(sourceChainId) ? undefined : getChainByChainId(sourceChainId);
  let topUpFailed = false;
  if (run.gasTopUp) {
    try {
      await requestSellIntentGasTopUp(intentId);
    } catch (e) {
      console.warn('[sell] gas top-up failed', e);
      topUpFailed = true;
    }
  }

  let hash: string;
  try {
    hash = await executeLiFiSwap({
      quote,
      fromSymbol: token.symbol,
      fromDecimals: token.decimals,
      fromTokenAddress: token.address,
      fromAmount,
      walletAddress: sourceAddress,
      fromChainId: sourceChainId,
      // Our fee is inside the quote the server built; nothing is booked at submit.
      feeFeature: 'none',
      // On the phone BEFORE the broadcast: a reply lost after the server
      // sent it must still leave a hash for resume to follow.
      onSigned: async (signed) => {
        await AsyncStorage.setItem(pendingSwapKey(intentId), signed).catch((e) =>
          console.warn('[sell] could not persist the signed swap hash', e));
        // A crypto payment's swap goes straight to the merchant: from here on a
        // second payment could pay them twice, even across an app kill.
        if (run.paymentId) await writePayHold(run.paymentId, intentId);
      },
    });
  } catch (e) {
    if (e instanceof SwapBroadcastUnconfirmedError) throw e;
    // Refused before or at the node, so nothing left her wallet.
    await clearPendingSwapHash(intentId);
    if (run.paymentId) await clearPayHold(run.paymentId);
    await abandonSellIntent(intentId).catch((err) => console.warn('[sell] abandon failed', err));
    if (topUpFailed && chain) {
      throw new Error(
        `You need a little ${chain.nativeCurrency.symbol} on ${chain.name} to pay the network fee, and we couldn't cover it right now. Nothing was sold.`,
      );
    }
    throw e;
  }

  // Already on the phone for an EVM swap (onSigned); the non-EVM paths only
  // learn their hash here. On the phone first, THEN the server.
  await AsyncStorage.setItem(pendingSwapKey(intentId), hash).catch((e) =>
    console.warn('[sell] could not persist the swap hash', e));
  return hash;
}
