// services/payApi.ts
//
// Client for the payer's side of TSA Pay — app/pay.tsx viewing or completing a
// merchant's payment link. This is the user-JWT surface
// (GET /api/pay/checkout/:id, POST /api/pay/checkout/:id/test-outcome), never
// the merchant's key-authenticated /api/pay/v1 API.
import type { PaycrestStatus } from '@/services/paycrestStatus';
import { ApiError, createApiRequest } from '@/services/apiRequest';
import { toLiFiQuote, type LiFiQuote } from '@/services/lifi';
import type { PaycrestOrder } from '@/services/paycrestApi';
import type { SellIntent } from '@/services/paycrestSellSwap';
import { isPaymentId } from '@/utils/paymentId';

export type PayStatus = 'pending' | 'processing' | 'succeeded' | 'failed' | 'expired';

export type PayCheckout = {
  id: string;
  merchantName: string;
  mode: 'live' | 'test';
  status: PayStatus;
  amount: number;
  customerTotal: number;
  fee: number;
  merchantNet: number;
  currency: 'NGN';
  description: string;
  /** Who the merchant made this link for, when she named them. */
  customerName?: string;
  expiresAt: string;
  settlementMode: 'fiat' | 'crypto';
  settleTokenSymbol?: string;
  settleTokenAddress?: string;
  settleChainId?: number;
  returnUrl?: string;
  payable: boolean;
  notPayableWhy?: string;
  youArePaying: boolean;
  /** The Paycrest order this payment is linked to — only for the paying user, only once linked. */
  orderId?: string;
  /** That order's status as the server last recorded it — same visibility as orderId. Moves the payer's progress steps. */
  orderStatus?: PaycrestStatus;
  /** Why her payment failed — only to her, only once it has. */
  failureReason?: string;
};

export class PayError extends ApiError {}

const request = createApiRequest({
  prefix: '/pay',
  // A 404/503 here means the route isn't registered for this build, not a
  // broken link — see apiRequest.ts.
  unavailableMessage: 'Payments are not available right now. Please try again later.',
  networkMessage: 'Could not reach the payment provider. Check your connection and try again.',
  makeError: (message, status, code) => new PayError(message, status, code),
});

/** Re-exported so every existing caller of `isPaymentId` from this module
 *  keeps working — see utils/paymentId.ts for the check itself and why it
 *  lives in its own zero-dependency module rather than here. */
export { isPaymentId };

/** One of her own payments, as her activity lists it (GET /pay/history). */
export type PayHistoryItem = {
  id: string;
  merchantName: string;
  description?: string;
  /** A payment's status — or `refunded`, for a past attempt of hers whose money came back. */
  status: PayStatus | 'refunded';
  /** Integer kobo — what she paid, fee included. */
  customerTotal: number;
  currency: 'NGN';
  /** When she paid it, not when the merchant raised it. */
  paidAt: string;
  succeededAt?: string;
  /** Set on a refunded past attempt: its refund, and any report of it, are on this order. */
  orderId?: string;
};

/** Her live payments, newest first (the server caps the list). */
export async function listMyPayments(): Promise<PayHistoryItem[]> {
  const data = await request<{ payments: PayHistoryItem[] }>('/history');
  return data?.payments ?? [];
}

/** Refuses a non-UUID before it ever reaches fetch. */
export async function getCheckout(id: string): Promise<PayCheckout> {
  if (!isPaymentId(id)) throw new PayError('This payment link is not valid.', 400);
  return request<PayCheckout>(`/checkout/${encodeURIComponent(id)}`);
}

export async function submitTestOutcome(
  id: string,
  outcome: 'approve' | 'decline',
): Promise<PayCheckout> {
  if (!isPaymentId(id)) throw new PayError('This payment link is not valid.', 400);
  return request<PayCheckout>(`/checkout/${encodeURIComponent(id)}/test-outcome`, {
    method: 'POST',
    body: JSON.stringify({ outcome }),
  });
}

/** Where the payer's tokens are: the chain, the token on it, and her address there. */
export type PaySource = { sourceChainId: number; sourceToken: string; sourceAddress: string };

const sourceQuery = (src: PaySource) =>
  new URLSearchParams({
    sourceChainId: String(src.sourceChainId),
    sourceToken: src.sourceToken,
    sourceAddress: src.sourceAddress,
  }).toString();

const checkoutPath = (id: string, tail: string) => {
  if (!isPaymentId(id)) throw new PayError('This payment link is not valid.', 400);
  return `/checkout/${encodeURIComponent(id)}${tail}`;
};

/**
 * What paying a naira merchant with this token would cost: `fromAmount` of the
 * source token (base units) buys `usdcTarget` USDC (base units) on Base, the
 * deposit the payout needs plus the server's rate buffer. Moves nothing, locks
 * nothing.
 */
export async function getPayQuote(
  id: string,
  src: PaySource,
): Promise<{ fromAmount: string; usdcTarget: string }> {
  return request(checkoutPath(id, `/quote?${sourceQuery(src)}`));
}

export type CryptoQuote = {
  fromAmount: string;
  /** Exactly what the merchant receives, in the settle token's base units. Absent on an older server. */
  destAmount?: string;
  destDecimals?: number;
};

/**
 * What paying a crypto-settling merchant with this token would cost:
 * `fromAmount` of the source token (base units) delivers the merchant's exact
 * token amount on their own chain. Moves nothing, locks nothing.
 */
export async function getCryptoQuote(id: string, src: PaySource): Promise<CryptoQuote> {
  return request(checkoutPath(id, `/crypto-quote?${sourceQuery(src)}`));
}

/**
 * Pay with a stablecoin Paycrest takes as it is: opens the payout order the
 * payer then funds. The server sizes it to exactly the merchant's net.
 */
export async function payDirect(id: string, route: { chainKey: string; token: string }): Promise<PaycrestOrder> {
  const data = await request<PaycrestOrder | { order: PaycrestOrder }>(checkoutPath(id, '/direct'), {
    method: 'POST',
    body: JSON.stringify(route),
  });
  // /paycrest/sell returns the order as the data itself; the plan's route table
  // wraps it as {order}. Either way it is one order.
  return 'order' in data && data.order ? data.order : (data as PaycrestOrder);
}

type RawSwapStart = { intent: SellIntent; lifiQuote: unknown; gasTopUp?: boolean };
export type PaySwapStart = { intent: SellIntent; quote: LiFiQuote; gasTopUp: boolean };

/** Both swap routes start the payment's sell intent and hand back the LiFi quote to sign. */
async function startSwap(id: string, tail: string, src: PaySource): Promise<PaySwapStart> {
  const data = await request<RawSwapStart>(checkoutPath(id, tail), {
    method: 'POST',
    body: JSON.stringify(src),
  });
  return { intent: data.intent, quote: toLiFiQuote(data.lifiQuote), gasTopUp: data.gasTopUp === true };
}

/**
 * Pay a naira merchant with any other token: a swap into the USDC the payout
 * needs. Same shape as a sell intent, so the swapped-sell pipeline drives it.
 */
export async function payConverted(id: string, src: PaySource): Promise<PaySwapStart> {
  return startSwap(id, '/converted', src);
}

/** Pay a crypto-settling merchant: one swap that delivers their token straight to their wallet. */
export async function payCrypto(id: string, src: PaySource): Promise<PaySwapStart> {
  return startSwap(id, '/crypto', src);
}

/**
 * Where to send the payer back once a payment is done: the merchant's own
 * `returnUrl`, with `payment_id` and `status` appended (before any
 * `#fragment`, which must stay last) so their page can read the outcome.
 *
 * `https` only — a merchant-supplied field is otherwise an open redirect or a
 * scheme-launch vector (`javascript:`, a bare `http:`, our own or another
 * app's custom scheme). Validated with `URL` so a malformed value never
 * reaches `Linking.openURL`; the query string is appended by hand rather than
 * through `URL`'s own serialization so the merchant's original path/fragment
 * come back byte-for-byte.
 */
export function returnUrlFor(
  checkout: Pick<PayCheckout, 'returnUrl' | 'id' | 'status'>,
): string | null {
  const raw = checkout.returnUrl;
  if (!raw) return null;

  try {
    if (new URL(raw).protocol !== 'https:') return null;
  } catch {
    return null;
  }

  const hashIndex = raw.indexOf('#');
  const withoutHash = hashIndex === -1 ? raw : raw.slice(0, hashIndex);
  const hash = hashIndex === -1 ? '' : raw.slice(hashIndex);
  const sep = withoutHash.includes('?') ? '&' : '?';
  const query = `payment_id=${encodeURIComponent(checkout.id)}&status=${encodeURIComponent(checkout.status)}`;
  return `${withoutHash}${sep}${query}${hash}`;
}
