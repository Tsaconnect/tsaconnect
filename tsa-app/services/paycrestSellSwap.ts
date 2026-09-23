// services/paycrestSellSwap.ts
//
// Client for the swapped sell (design §5): she holds a token Paycrest cannot
// offramp, so LiFi swaps it — in her own wallet — into USDC on Base, and that
// USDC is offramped to her bank.
//
// Every figure here is the server's. The intent carries only what she is
// selling and where the naira goes; the server re-quotes, stores its own
// numbers and hands back the LiFi quote to sign. Auth and the idempotency key
// on every POST come from paycrestRequest.

import { PaycrestError, paycrestRequest, type PaycrestOrder } from './paycrestApi';
import { toLiFiQuote, type LiFiQuote } from './lifi';

export type SellIntent = {
  id: string;
  // 'ordering': the server is opening the payout right now (a claim in flight).
  // 'delivered': a TSA Pay crypto payment whose token reached the merchant — terminal.
  status: 'swapping' | 'swapped' | 'ordering' | 'ordered' | 'delivered' | 'failed';
  sourceChainId: number;
  sourceTokenAddress: string;
  /** In the source token's base units. */
  sourceAmount: string;
  quotedFiat: string;
  swapTxHash?: string;
  paycrestOrderId?: string;
  /** Set when this sale is a TSA Pay payment: which payment it pays. */
  payPaymentId?: string;
  /** ISO time of the sale's last step — how long it has sat where it is. */
  updatedAt?: string;
};

export type SellSwapQuote = {
  quote: LiFiQuote;
  costBps: number;
  gasBps: number;
  feeBps: number;
  /** USDC landing on Base after our fee, in whole units. */
  usdcAmount: string;
  /** An estimate — show it with `~`. */
  fiatEstimate: string;
  rate: string;
  /** The server priced a source-chain gas top-up into this quote. */
  gasTopUp?: boolean;
};

type RawSellSwapQuote = Omit<SellSwapQuote, 'quote'> & { lifiQuote: unknown };

/** Price a sale of `fromAmount` (base units). Moves nothing, opens nothing. */
export async function getSellSwapQuote(p: {
  fromChainId: number;
  fromToken: string;
  fromAmount: string;
  /** Her address on the SOURCE chain — a Solana address for a Solana token. */
  fromAddress: string;
  /** Her EVM wallet, where the USDC lands. */
  walletAddress: string;
  currency?: string;
}): Promise<SellSwapQuote> {
  const qs = new URLSearchParams({
    fromChainId: String(p.fromChainId),
    fromToken: p.fromToken,
    fromAmount: p.fromAmount,
    fromAddress: p.fromAddress,
    walletAddress: p.walletAddress,
    currency: p.currency ?? 'NGN',
  });
  const { lifiQuote, ...rest } = await paycrestRequest<RawSellSwapQuote>(`/sell-swap-quote?${qs.toString()}`);
  return { ...rest, quote: toLiFiQuote(lifiQuote) };
}

/**
 * Start a sale. The server validates the payout first (so a bad account is
 * caught before anything is swapped), then re-quotes and returns the LiFi
 * quote to sign. No quote figure is ever sent from here.
 */
export async function createSellIntent(input: {
  sourceChainId: number;
  sourceToken: string;
  /** Base units. */
  sourceAmount: string;
  sourceAddress: string;
  bankCode: string;
  accountNumber: string;
  accountName?: string;
  currency?: string;
}): Promise<{ intent: SellIntent; quote: LiFiQuote; gasTopUp: boolean }> {
  const data = await paycrestRequest<{ intent: SellIntent; lifiQuote: unknown; gasTopUp?: boolean }>('/sell-intents', {
    method: 'POST',
    body: JSON.stringify({
      sourceChainId: input.sourceChainId,
      sourceToken: input.sourceToken,
      sourceAmount: input.sourceAmount,
      sourceAddress: input.sourceAddress,
      bankCode: input.bankCode,
      accountNumber: input.accountNumber,
      ...(input.accountName ? { accountName: input.accountName } : {}),
      ...(input.currency ? { currency: input.currency } : {}),
    }),
  });
  // gasTopUp is the server's verdict, priced into the budget this sale was
  // admitted on. The app never re-derives it from a balance of its own.
  return { intent: data.intent, quote: toLiFiQuote(data.lifiQuote), gasTopUp: data.gasTopUp === true };
}

/** Her unfinished sale, if any — what "Finish your sale" resumes. */
export async function getOpenSellIntent(): Promise<SellIntent | null> {
  const data = await paycrestRequest<{ intent: SellIntent | null } | null>('/sell-intents/open');
  return data?.intent ?? null;
}

/**
 * Whether swapped sells are switched on for this server, and her unfinished
 * sale if they are. The sale routes are registered only with
 * RAMP_CONVERSION_SELL, so a 404 here is the capability answer "off" — not a
 * missing sale, and not the "Naira transfers are unavailable" outage
 * paycrestRequest would otherwise word it as. Any other failure is thrown: it
 * says nothing either way.
 */
export async function getSwappedSellState(): Promise<{ enabled: boolean; intent: SellIntent | null }> {
  try {
    return { enabled: true, intent: await getOpenSellIntent() };
  } catch (e) {
    if (e instanceof PaycrestError && e.status === 404) return { enabled: false, intent: null };
    throw e;
  }
}

/** Tell the server which on-chain swap this sale is. */
export async function recordSellSwap(id: string, txHash: string): Promise<void> {
  await paycrestRequest<unknown>(`/sell-intents/${id}/swap`, {
    method: 'POST',
    body: JSON.stringify({ txHash }),
  });
}

/**
 * Open the offramp for exactly the USDC that landed. The server re-proves the
 * swap first; a 409 "Still swapping" means its own view has not caught up yet.
 */
export async function openSellOrder(
  id: string,
  opts?: {
    /** TSA Pay: the swap landed short — cover the rest from the USDC already in her wallet. */
    useWalletBalance?: boolean;
  },
): Promise<PaycrestOrder> {
  return paycrestRequest<PaycrestOrder>(`/sell-intents/${id}/order`, {
    method: 'POST',
    ...(opts?.useWalletBalance ? { body: JSON.stringify({ useWalletBalance: true }) } : {}),
  });
}

/** Source-chain gas for a token she holds no gas coin for. Blocks until it lands. */
export async function requestSellIntentGasTopUp(id: string): Promise<{ txHash?: string; skipped: boolean }> {
  return paycrestRequest<{ txHash?: string; skipped: boolean }>(`/sell-intents/${id}/gas-topup`, { method: 'POST' });
}

/** Close a sale whose swap never happened or came back, freeing her to start another. */
export async function abandonSellIntent(id: string): Promise<void> {
  await paycrestRequest<unknown>(`/sell-intents/${id}/abandon`, { method: 'POST' });
}
