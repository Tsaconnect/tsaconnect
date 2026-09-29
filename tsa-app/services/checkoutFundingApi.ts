// services/checkoutFundingApi.ts
//
// Client for "pay with another token" at marketplace checkout
// (tsa-api-go handlers/checkout_funding.go). The buyer's token is swapped
// through LiFi into the order token (Sonic USDC/USDT) in HER OWN wallet on
// Sonic mainnet; the escrow is then paid from it exactly as a stablecoin
// checkout. Every amount is the server's: it sizes the swap from the orders it
// holds (or, before they exist, from its own price of the cart) less what her
// wallet already holds. This file only names the token she pays with.
//
// Auth, the Idempotency-Key on every POST and the wallet-proof retry come from
// createApiRequest.
import { ApiError, createApiRequest } from '@/services/apiRequest';
import { toLiFiQuote, type LiFiQuote } from '@/services/lifi';

export class FundingError extends ApiError {}

const request = createApiRequest({
  prefix: '/orders/funding',
  unavailableMessage: "Paying with another token isn't available right now. Pay with USDC or USDT instead.",
  networkMessage: 'Could not reach the server. Check your connection and try again.',
  makeError: (message, status, code) => new FundingError(message, status, code),
});

/** The server's machine codes a funding answer carries (the message is copy). */
export const FUNDING_CODES = {
  tooCostly: 'funding_too_costly',
  notNeeded: 'funding_not_needed',
  needsGas: 'funding_needs_gas',
  inProgress: 'funding_in_progress',
  swapPending: 'swap_pending',
  swapFailed: 'swap_failed',
  swapRefunded: 'swap_refunded',
  swapPartial: 'swap_partial',
  gasUnavailable: 'gas_unavailable',
  notPayable: 'order_not_payable',
} as const;

/** What paying the checkout with one token costs her. Amounts in base units. */
export type FundingQuote = {
  groupId: string;
  orders: number;
  /** The order token: 'USDC' | 'USDT' (6 dp). */
  token: string;
  tokenAddress: string;
  chainId: number;
  orderTotal: string;
  walletBalance: string;
  /** What the swap must land at least (the order total less her balance). */
  shortfall: string;
  toAmount: string;
  /** What leaves her wallet, in the source token's base units. */
  fromAmount: string;
  fromAmountUsd: number;
  /** LiFi's own cost of the route, and the gas we pay for her — all she pays. */
  lifiCostUsd: number;
  gasUsd: number;
  costBps: number;
  gasBps: number;
  budgetBps: number;
  etaSeconds: number;
  tool: string;
  sourceGasTopUp: boolean;
  destGasTopUp: boolean;
};

export type FundingIntent = {
  id: string;
  status: 'swapping' | 'swapped' | 'delivered' | 'failed' | string;
  groupId?: string;
  sourceChainId: number;
  sourceTokenAddress: string;
  sourceAmount: string;
  destChainId: number;
  destTokenAddress: string;
  destAmount: string;
  swapTxHash?: string | null;
  destGasTopUp?: boolean;
  destGasSent?: boolean;
  createdAt?: string;
};

export type FundingSource = { sourceChainId: number; sourceToken: string; walletAddress: string };

/** The cart quote's own fields, so the server prices the same cart. */
export type FundingCart = {
  token: string;
  buyerCity: string;
  buyerState: string;
  buyerCountry: string;
  serviceTiers?: Record<string, string>;
  deliveryAddressId?: string;
};

const source = (src: FundingSource) => ({
  sourceChainId: src.sourceChainId,
  sourceToken: src.sourceToken,
  sourceAddress: src.walletAddress,
  walletAddress: src.walletAddress,
});

/** Price paying the ACTIVE CART with a token — before any order exists. */
export function quoteCartFunding(cart: FundingCart, src: FundingSource): Promise<FundingQuote> {
  return request<FundingQuote>('/cart-quote', { method: 'POST', body: JSON.stringify({ ...cart, ...source(src) }) });
}

/** Price paying an existing checkout group with a token. */
export function quoteFunding(groupId: string, src: FundingSource): Promise<FundingQuote> {
  return request<FundingQuote>('/quote', { method: 'POST', body: JSON.stringify({ groupId, ...source(src) }) });
}

export type FundingStart = { intent: FundingIntent; quote: LiFiQuote; gasTopUp: boolean; priced: FundingQuote };

/** Start the funding: the server re-plans from the orders and returns the route she signs. */
export async function startFunding(groupId: string, src: FundingSource): Promise<FundingStart> {
  const data = await request<{ intent: FundingIntent; lifiQuote: unknown; gasTopUp?: boolean; quote: FundingQuote }>(
    '/intents',
    { method: 'POST', body: JSON.stringify({ groupId, ...source(src) }) },
  );
  return { intent: data.intent, quote: toLiFiQuote(data.lifiQuote), gasTopUp: data.gasTopUp === true, priced: data.quote };
}

export async function getFunding(id: string): Promise<FundingIntent | null> {
  const data = await request<{ intent: FundingIntent | null }>(`/intents/${id}`);
  return data?.intent ?? null;
}

export async function getOpenFunding(): Promise<FundingIntent | null> {
  const data = await request<{ intent: FundingIntent | null } | null>('/intents/open');
  return data?.intent ?? null;
}

export async function recordFundingSwap(id: string, txHash: string): Promise<void> {
  await request<unknown>(`/intents/${id}/swap`, { method: 'POST', body: JSON.stringify({ txHash }) });
}

export type FundingConfirm = { intent: FundingIntent; destTxHash: string; needsGas: boolean };

/** Has the swap landed in her wallet? 409 swap_pending means wait. */
export function confirmFunding(id: string): Promise<FundingConfirm> {
  return request<FundingConfirm>(`/intents/${id}/confirm`, { method: 'POST' });
}

export async function abandonFunding(id: string): Promise<void> {
  await request<unknown>(`/intents/${id}/abandon`, { method: 'POST' });
}

/** Source-chain gas for the swap (only when the server priced it in). */
export function requestFundingSourceGas(id: string): Promise<{ txHash?: string; skipped: boolean }> {
  return request<{ txHash?: string; skipped: boolean }>(`/intents/${id}/gas-topup`, { method: 'POST' });
}

/** S on Sonic for the escrow txs, once the funds have landed. */
export function requestFundingSonicGas(id: string): Promise<{ txHash?: string; skipped: boolean }> {
  return request<{ txHash?: string; skipped: boolean }>(`/intents/${id}/sonic-gas`, { method: 'POST' });
}
