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
// escrowOrder is the one approve → escrow → submit sequence: the cart checkout
// (app/(detail)/checkout) runs it per order, with the progress that lets a
// retry resume where a failed payment stopped. An MCGP order is never paid
// that way: MCGP is paid from Spendable MCGP (the entitlement balance), which
// the backend holds with one call and no signature — payOrder picks the path. Money math is untouched — the server
// recomputes every price from menuSelections / bookingSlots / serviceTiers; the
// amounts the pickers show are indicative only.
import {
  createServiceOrder,
  paySpendable,
  prepareApprove,
  prepareEscrow,
  submitEscrow,
  type Order,
  type UnsignedTx,
} from './orderApi';
import { signAndBroadcast, TxNotConfirmedError } from './transaction';
import { getProvider } from './wallet';

export interface ServiceCheckoutParams {
  token: string;
  productId: string;
  buyerCity?: string;
  buyerState?: string;
  buyerCountry?: string;
  serviceTier?: string;
  menuSelections?: { itemId: string; quantity: number }[];
  bookingSlot?: { slotStart: string; partySize: number };
  // Auto-generated booking-form answers (unified booking engine), keyed by the
  // service's FieldConfig field keys.
  answers?: Record<string, any>;
}

/** Human-friendly signing/broadcast error copy. */
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
  | 'paying_spendable'
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
    answers: params.answers && Object.keys(params.answers).length ? params.answers : undefined,
  });
  if (!createResult.success || !createResult.data?.orders?.length) {
    throw new Error(createResult.message || 'Failed to create order');
  }

  // A single service product yields a single order.
  const order = createResult.data.orders[0];
  return payOrder(order, onPhase);
}

/** A sent escrow-flow tx: confirmed, or recorded when its confirmation was not seen. */
export type SentTx = { hash: string; confirmed: boolean };

/**
 * What an order's payment has already done on-chain. Pass the same object to
 * every escrowOrder attempt for that order, so a retry resumes where the last
 * one stopped instead of signing (and paying gas for) it all again.
 */
export interface EscrowProgress {
  approve?: SentTx;
  escrow?: SentTx;
}

/** The order is no longer awaiting payment (cancelled, expired or paid): a retry cannot help. */
export class OrderNotPayableError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'OrderNotPayableError';
  }
}

// Deliberately not "try again": outside the cart checkout, trying again places
// a new order and would pay twice.
const STILL_CONFIRMING =
  'Your payment was sent and is still confirming on Sonic. Check your orders in a minute before paying again.';

/**
 * Signs and sends an escrow-flow tx. When its confirmation is not seen the
 * hash still goes to onUnconfirmed — the tx may yet be mined, so a retry must
 * settle it rather than send another.
 */
export async function sendTx(tx: UnsignedTx, onUnconfirmed: (sent: SentTx) => void): Promise<SentTx> {
  try {
    return { hash: await signAndBroadcast(tx, 'sonic'), confirmed: true };
  } catch (e: any) {
    if (e instanceof TxNotConfirmedError) {
      onUnconfirmed({ hash: e.txHash, confirmed: false });
      throw new Error(STILL_CONFIRMING);
    }
    throw new Error(friendlyTxError(e));
  }
}

/**
 * Settles an unconfirmed tx against the chain: confirmed once mined, dropped
 * (undefined, so it is sent afresh) when it reverted or the network no longer
 * knows it, and an error while it is still in flight.
 */
export async function settleSent(sent: SentTx | undefined): Promise<SentTx | undefined> {
  if (!sent || sent.confirmed) return sent;
  const provider = getProvider('sonic');
  const receipt = await provider.getTransactionReceipt(sent.hash);
  if (receipt) return receipt.status === 1 ? { hash: sent.hash, confirmed: true } : undefined;
  if (await provider.getTransaction(sent.hash)) throw new Error(STILL_CONFIRMING);
  return undefined;
}

function refused(res: { message?: string; code?: string }, fallback: string): Error {
  const message = res.message || fallback;
  return res.code === 'order_not_payable' ? new OrderNotPayableError(message) : new Error(message);
}

/**
 * Fund an already-created order through escrow (approve → escrow → submit).
 * Shared by the service flows and the cart checkout. progress carries what
 * earlier attempts already did (see EscrowProgress).
 */
export async function escrowOrder(
  order: Order,
  onPhase?: (phase: ServiceCheckoutPhase) => void,
  progress: EscrowProgress = {},
): Promise<Order> {
  progress.approve = await settleSent(progress.approve);
  progress.escrow = await settleSent(progress.escrow);
  const reusedApprove = !!progress.approve;

  let approve = progress.approve;
  if (!approve) {
    onPhase?.('approving_token');
    const approveResult = await prepareApprove(order.id);
    if (!approveResult.success || !approveResult.data) {
      throw refused(approveResult, 'Failed to prepare approval');
    }
    approve = progress.approve = await sendTx(approveResult.data.approveTx, (sent) => {
      progress.approve = sent;
    });
  }

  // The escrow tx is prepared after the approve is on-chain, so its gas
  // estimation sees the allowance.
  let escrow = progress.escrow;
  if (!escrow) {
    onPhase?.('creating_escrow');
    const escrowResult = await prepareEscrow(order.id);
    if (!escrowResult.success || !escrowResult.data) {
      // An approval reused from an earlier attempt may no longer cover the
      // escrow (spent, or replaced by another approve): sign a fresh one next time.
      if (reusedApprove) progress.approve = undefined;
      throw refused(escrowResult, 'Failed to prepare escrow');
    }
    escrow = progress.escrow = await sendTx(escrowResult.data.createOrderTx, (sent) => {
      progress.escrow = sent;
    });
  }

  onPhase?.('submitting');
  const submitResult = await submitEscrow(order.id, approve.hash, escrow.hash);
  if (!submitResult.success) {
    throw refused(submitResult, 'Failed to submit escrow');
  }

  onPhase?.('done');
  return submitResult.data || order;
}

/** An order the server says is still confirming its Spendable MCGP hold. */
export class PaymentPendingError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'PaymentPendingError';
  }
}

/**
 * Pay an MCGP order from the buyer's Spendable MCGP: one backend call, no
 * wallet signature, no gas. The caller must have put the app's authorization
 * prompt in front of it. Retrying is safe — the hold is keyed by the order.
 */
export async function paySpendableOrder(
  order: Order,
  onPhase?: (phase: ServiceCheckoutPhase) => void,
): Promise<Order> {
  onPhase?.('paying_spendable');
  const res = await paySpendable(order.id);
  if (!res.success) {
    if (res.code === 'payment_pending') {
      throw new PaymentPendingError(res.message || 'Your Spendable MCGP payment is still being confirmed.');
    }
    throw refused(res, 'Failed to pay with Spendable MCGP');
  }
  onPhase?.('done');
  return { ...order, status: res.data?.status ?? 'escrowed', paymentSource: 'spendable_mcgp' };
}

/**
 * Fund a created order the way its token is paid: MCGP from Spendable MCGP,
 * stablecoins through the escrow contract.
 */
export function payOrder(
  order: Order,
  onPhase?: (phase: ServiceCheckoutPhase) => void,
  progress: EscrowProgress = {},
): Promise<Order> {
  if (order.token?.toUpperCase() === 'MCGP') return paySpendableOrder(order, onPhase);
  return escrowOrder(order, onPhase, progress);
}

/**
 * Pay an approved booking request: the server creates the order, then the
 * buyer funds it (payOrder).
 */
export async function payBookingRequestAndEscrow(
  reqId: string,
  onPhase?: (phase: ServiceCheckoutPhase) => void,
): Promise<Order> {
  const { payBookingRequest } = await import('./bookingRequestApi');
  onPhase?.('creating_order');
  const payResult = await payBookingRequest(reqId);
  if (!payResult.success || !payResult.data?.orders?.length) {
    throw new Error(payResult.message || 'Failed to start payment');
  }
  return payOrder(payResult.data.orders[0], onPhase);
}
