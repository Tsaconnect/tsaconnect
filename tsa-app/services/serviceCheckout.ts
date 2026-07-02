// services/serviceCheckout.ts
//
// Inline escrow checkout for the three service-type buyer flows (order / booking
// and the contact access-fee). The shared cart → /checkout route can only carry
// a per-item declared tier (serviceCart.ts notes), so it cannot thread the
// richer menuSelections / bookingSlots payloads. These flows therefore run their
// own single-order escrow checkout right from the pricing sheet:
//
//   createOrders(token, city/state/country, serviceTiers?, menuSelections?, bookingSlots?)
//     → prepareApprove → sign+broadcast
//     → prepareEscrow  → sign+broadcast
//     → submitEscrow
//
// This mirrors the multi-order loop in app/(…)/checkout/index.tsx exactly (same
// prepare/sign/submit contract, same friendly error mapping) but for the single
// service order the buyer just configured. Money math is untouched — the server
// recomputes every price from menuSelections / bookingSlots / serviceTiers; the
// amounts the pickers show are indicative only.
import {
  createServiceOrder,
  prepareApprove,
  prepareEscrow,
  submitEscrow,
  type Order,
} from './orderApi';
import { signAndBroadcast } from './transaction';

export interface ServiceCheckoutParams {
  token: string;
  productId: string;
  buyerCity?: string;
  buyerState?: string;
  buyerCountry?: string;
  serviceTier?: string;
  menuSelections?: { itemId: string; quantity: number }[];
  bookingSlot?: { slotStart: string; partySize: number };
}

/** Human-friendly signing/broadcast error copy (mirrors the checkout screen). */
export function friendlyTxError(err: any): string {
  const msg = err?.message || err?.toString() || '';
  if (msg.includes('INSUFFICIENT_FUNDS') || msg.includes('insufficient funds'))
    return 'Not enough S (Sonic) in your wallet to cover gas fees. Please top up your wallet with S and try again.';
  if (msg.includes('user rejected') || msg.includes('User denied') || msg.includes('ACTION_REJECTED'))
    return 'Transaction was cancelled.';
  if (msg.includes('nonce')) return 'Transaction conflict — please wait a moment and try again.';
  if (msg.includes('UNPREDICTABLE_GAS_LIMIT') || msg.includes('execution reverted'))
    return 'Transaction would fail on-chain. Please check your token balance and try again.';
  return msg || 'Something went wrong';
}

export type ServiceCheckoutPhase =
  | 'creating_order'
  | 'approving_token'
  | 'creating_escrow'
  | 'submitting'
  | 'done';

/**
 * Run the full escrow checkout for the just-configured service order and return
 * the created + funded order. Throws with a friendly message on failure.
 *
 * A service order is created for a single product, so this handles exactly one
 * order (unlike the cart checkout which loops). onPhase lets the caller drive a
 * progress label.
 */
export async function runServiceCheckout(
  params: ServiceCheckoutParams,
  onPhase?: (phase: ServiceCheckoutPhase) => void,
): Promise<Order> {
  onPhase?.('creating_order');
  const createResult = await createServiceOrder({
    token: params.token,
    productId: params.productId,
    buyerCity: params.buyerCity,
    buyerState: params.buyerState,
    buyerCountry: params.buyerCountry,
    serviceTier: params.serviceTier,
    menuSelections: params.menuSelections?.length ? params.menuSelections : undefined,
    bookingSlot: params.bookingSlot,
  });
  if (!createResult.success || !createResult.data?.orders?.length) {
    throw new Error(createResult.message || 'Failed to create order');
  }

  // A single service product yields a single order.
  const order = createResult.data.orders[0];

  onPhase?.('approving_token');
  const approveResult = await prepareApprove(order.id);
  if (!approveResult.success || !approveResult.data) {
    throw new Error(approveResult.message || 'Failed to prepare approval');
  }
  let approveTxHash: string;
  try {
    approveTxHash = await signAndBroadcast(approveResult.data.approveTx, 'sonic');
  } catch (e: any) {
    throw new Error(friendlyTxError(e));
  }

  onPhase?.('creating_escrow');
  const escrowResult = await prepareEscrow(order.id);
  if (!escrowResult.success || !escrowResult.data) {
    throw new Error(escrowResult.message || 'Failed to prepare escrow');
  }
  let escrowTxHash: string;
  try {
    escrowTxHash = await signAndBroadcast(escrowResult.data.createOrderTx, 'sonic');
  } catch (e: any) {
    throw new Error(friendlyTxError(e));
  }

  onPhase?.('submitting');
  const submitResult = await submitEscrow(order.id, approveTxHash, escrowTxHash);
  if (!submitResult.success) {
    throw new Error(submitResult.message || 'Failed to submit escrow');
  }

  onPhase?.('done');
  return submitResult.data || order;
}
