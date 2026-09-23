// services/payApi.ts
//
// Client for the payer's side of TSA Pay — app/pay.tsx viewing or completing a
// merchant's payment link. This is the user-JWT surface
// (GET /api/pay/checkout/:id, POST /api/pay/checkout/:id/test-outcome), never
// the merchant's key-authenticated /api/pay/v1 API.
import { ApiError, createApiRequest } from '@/services/apiRequest';
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
  expiresAt: string;
  settlementMode: 'fiat' | 'crypto';
  settleTokenSymbol?: string;
  settleTokenAddress?: string;
  settleChainId?: number;
  returnUrl?: string;
  payable: boolean;
  notPayableWhy?: string;
  youArePaying: boolean;
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
