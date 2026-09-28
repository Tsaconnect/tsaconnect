// services/orderApi.ts
// API calls to the Go backend for escrow order operations
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';
import { withWalletProof } from './walletApi';

// --- Types ---

export interface UnsignedTx {
  to: string;
  value: string;
  data: string;
  nonce: number;
  gasPrice: string;
  gasLimit: number;
  chainId: string;
}

export interface Order {
  id: string;
  buyerId: string;
  sellerId: string;
  productId: string;
  // "product" (default) or "service". A service order skips shipping and is
  // confirmed by the buyer after the provider marks it rendered.
  kind?: 'product' | 'service';
  quantity: number;
  token: string;
  productAmount: string;
  shippingAmount: string;
  platformFee: string;
  totalAmount: string;
  shippingZone: string;
  contractOrderId?: string;
  escrowTxHash?: string;
  approveTxHash?: string;
  releaseTxHash?: string;
  buyerUpline?: string;
  deliveryProofUrl?: string;
  trackingNumber?: string;
  merchantApprovedRefund?: boolean;
  cancelReason?: string;
  cancelRequestedAt?: string;
  merchantApprovedCancel?: boolean;
  disputedAt?: string;
  disputeReason?: string;
  disputeRaisedBy?: 'buyer' | 'seller' | string;
  shippingCity?: string;
  shippingState?: string;
  shippingCountry?: string;
  // Snapshot of the buyer's saved delivery address (product orders only).
  shippingRecipientName?: string;
  shippingPhone?: string;
  shippingStreet?: string;
  /**
   * How the order is funded: absent/"" is the escrow contract (the wallet
   * approved and deposited the token); "spendable_mcgp" is a hold on the
   * buyer's Spendable MCGP, released or returned by the backend — there is no
   * transaction for the app to sign on such an order.
   */
  paymentSource?: '' | 'spendable_mcgp';
  status: string;
  buyerConfirmedAt?: string;
  sellerShippedAt?: string;
  sellerDeliveredAt?: string;
  escrowExpiresAt?: string;
  notes?: string;
  // Buyer's chosen product specs (colour/size/other) for a product order, so the
  // merchant knows how to arrange the purchase. Carried from the cart at checkout.
  selectedAttributes?: { name: string; value: string }[];
  // Buyer's free-text arrangement note (separate from `notes`, which holds
  // merchant-side ship/refund notes).
  purchaseNote?: string;
  createdAt: string;
  updatedAt: string;
  // Enriched (returned by backend with related entities)
  buyer?: { id: string; name?: string; username?: string; email?: string };
  seller?: { id: string; name?: string; username?: string; email?: string };
  product?: { id: string; name: string; price: number; imageUrl?: string };
}

export interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  /** Stable machine code on some errors (e.g. "price_changed"). */
  code?: string;
  data?: T;
}

// --- Auth helper ---

export async function getAuthHeaders(): Promise<Record<string, string>> {
  const token = await AsyncStorage.getItem('authToken');
  if (!token) {
    throw new Error('Not authenticated. Please log in again.');
  }
  return {
    'Content-Type': 'application/json',
    'Authorization': `Bearer ${token}`,
  };
}

// --- Response helper ---

export async function handleResponse<T>(response: Response): Promise<ApiResponse<T>> {
  let data;
  try {
    data = await response.json();
  } catch {
    return { success: false, message: `HTTP ${response.status}: ${response.statusText}` };
  }
  if (!response.ok) {
    return {
      success: false,
      message: data.message || `Request failed with status ${response.status}`,
      code: typeof data.code === 'string' ? data.code : undefined,
    };
  }
  return { success: true, ...data };
}

// --- Wei formatting ---

export function formatTokenAmount(weiStr: string, token: string): string {
  const decimals = token === 'MCGP' ? 18 : 6;
  try {
    const value = BigInt(weiStr);
    const divisor = BigInt(10 ** decimals);
    const whole = value / divisor;
    const frac = value % divisor;
    const fracStr = frac.toString().padStart(decimals, '0').slice(0, token === 'MCGP' ? 4 : 2);
    return `${whole}.${fracStr} ${token}`;
  } catch {
    return `${weiStr} ${token}`;
  }
}

// --- API functions ---

/** One order-to-be in a cart quote. Amounts are the token's base units. */
export interface CartQuoteLine {
  productId: string;
  name: string;
  sellerId: string;
  sellerName: string;
  quantity: number;
  kind: string;
  shippingZone: string;
  productAmount: string;
  shippingAmount: string;
  platformFee: string;
  gasFee: string;
  /** Everything the approve/escrow pair pulls for this order. */
  total: string;
}

/**
 * The active cart priced for a pay token by the same backend code that creates
 * the orders, so what the buyer confirms is what the wallet is asked for.
 */
export interface CartQuote {
  token: string;
  decimals: number;
  /** USD value of one token: 1 for USDC/USDT, the OTC buy price for MCGP. */
  usdPerToken: number;
  lines: CartQuoteLine[];
  totals: Pick<CartQuoteLine, 'productAmount' | 'shippingAmount' | 'platformFee' | 'gasFee' | 'total'>;
}

/** Prices the active cart without creating orders. Same inputs as createOrders. */
export async function quoteCart(
  token: string,
  buyerCity?: string,
  buyerState?: string,
  buyerCountry?: string,
  serviceTiers?: Record<string, string>,
): Promise<ApiResponse<CartQuote>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/quote`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ token, buyerCity, buyerState, buyerCountry, serviceTiers }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to price your cart' };
  }
}

export async function createOrders(
  token: string,
  buyerCity?: string,
  buyerState?: string,
  buyerCountry?: string,
  // serviceTiers optionally maps a service product ID → the buyer-declared area
  // tier ("residential", "same_lga", …) so a "provider is in my area" / LGA
  // declaration reaches the backend. Honored only when geographically plausible.
  serviceTiers?: Record<string, string>,
  // menuSelections maps an Order-type service product ID → the picked items.
  // The server recomputes prices from the DB (never trusts client totals).
  menuSelections?: Record<string, { itemId: string; quantity: number }[]>,
  // bookingSlots maps a Booking-type service product ID → the reserved slot.
  bookingSlots?: Record<string, { slotStart: string; partySize: number }>,
  // expectedTotal is the quote total (base units, gas included) the buyer
  // confirmed. The backend refuses a different total with code "price_changed".
  expectedTotal?: string,
  // deliveryAddressId picks a saved address; the server prices from its
  // location and snapshots the recipient, phone and street onto each order.
  deliveryAddressId?: string,
): Promise<ApiResponse<{ orders: Order[] }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders`, {
      method: 'POST',
      headers,
      body: JSON.stringify({
        token,
        buyerCity,
        buyerState,
        buyerCountry,
        serviceTiers,
        menuSelections,
        bookingSlots,
        expectedTotal,
        deliveryAddressId,
      }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Create orders error:', error);
    return { success: false, message: error.message || 'Failed to create orders' };
  }
}

// createServiceOrder creates a SINGLE service order directly (no cart) for the
// order/booking/contact "Pay now" flows. The server recomputes price from
// menuSelections / bookingSlot / serviceTier and returns one order.
export async function createServiceOrder(params: {
  token: string;
  productId: string;
  buyerCity?: string;
  buyerState?: string;
  buyerCountry?: string;
  serviceTier?: string;
  menuSelections?: { itemId: string; quantity: number }[];
  bookingSlot?: { slotStart: string; partySize: number };
  answers?: Record<string, any>;
}): Promise<ApiResponse<{ orders: Order[] }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/service`, {
      method: 'POST',
      headers,
      body: JSON.stringify(params),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Create service order error:', error);
    return { success: false, message: error.message || 'Failed to create service order' };
  }
}

export async function getOrders(params: {
  page?: number;
  limit?: number;
  status?: string;
  role?: string;
}): Promise<ApiResponse<{ orders: Order[]; pagination: any }>> {
  try {
    const headers = await getAuthHeaders();
    const queryParams = new URLSearchParams();
    if (params.page) queryParams.append('page', String(params.page));
    if (params.limit) queryParams.append('limit', String(params.limit));
    if (params.status) queryParams.append('status', params.status);
    if (params.role) queryParams.append('role', params.role);
    const response = await fetch(`${API_BASE_URL}/orders?${queryParams.toString()}`, {
      method: 'GET',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Get orders error:', error);
    return { success: false, message: error.message || 'Failed to fetch orders' };
  }
}

export async function getOrderById(id: string): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${id}`, {
      method: 'GET',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Get order error:', error);
    return { success: false, message: error.message || 'Failed to fetch order' };
  }
}

export async function prepareApprove(
  orderId: string
): Promise<ApiResponse<{ approveTx: UnsignedTx }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/prepare-approve`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Prepare approve error:', error);
    return { success: false, message: error.message || 'Failed to prepare approval' };
  }
}

export async function prepareEscrow(
  orderId: string
): Promise<ApiResponse<{ createOrderTx: UnsignedTx; contractOrderId: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/prepare-escrow`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Prepare escrow error:', error);
    return { success: false, message: error.message || 'Failed to prepare escrow' };
  }
}

export async function submitEscrow(
  orderId: string,
  approveTxHash: string,
  escrowTxHash: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/submit-escrow`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ approveTxHash, escrowTxHash }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Submit escrow error:', error);
    return { success: false, message: error.message || 'Failed to submit escrow' };
  }
}

/** Whether an order is paid from Spendable MCGP rather than the escrow contract. */
export const isSpendableOrder = (order: Pick<Order, 'paymentSource'> | null | undefined) =>
  order?.paymentSource === 'spendable_mcgp';

/**
 * Pay an MCGP order from the buyer's Spendable MCGP. Nothing is signed: the
 * backend holds the MCGP until the order resolves, so callers must put the
 * app's authorization prompt in front of this. Safe to retry — the hold is
 * keyed by the order. The operator only debits a wallet the account proved it
 * owns, so a WALLET_PROOF_REQUIRED refusal proves the active wallet and retries.
 */
export async function paySpendable(
  orderId: string
): Promise<ApiResponse<{ orderId: string; status: string; paymentSource: string; escrowExpiresAt?: string }>> {
  return withWalletProof(async () => {
    try {
      const headers = await getAuthHeaders();
      const response = await fetch(`${API_BASE_URL}/orders/${orderId}/pay-spendable`, {
        method: 'POST',
        headers,
      });
      return await handleResponse<{ orderId: string; status: string; paymentSource: string; escrowExpiresAt?: string }>(response);
    } catch (error: any) {
      console.error('Pay spendable error:', error);
      return { success: false, message: error.message || 'Failed to pay with Spendable MCGP' };
    }
  });
}

/** Confirm a Spendable MCGP order: the backend releases the hold to the seller. */
export async function confirmReceipt(
  orderId: string
): Promise<ApiResponse<{ orderId: string; status: string; buyerConfirmedAt: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/confirm-receipt`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Confirm receipt error:', error);
    return { success: false, message: error.message || 'Failed to confirm receipt' };
  }
}

export async function prepareConfirm(
  orderId: string
): Promise<ApiResponse<{ confirmTx: UnsignedTx }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/prepare-confirm`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Prepare confirm error:', error);
    return { success: false, message: error.message || 'Failed to prepare confirm' };
  }
}

export async function submitConfirm(
  orderId: string,
  txHash: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/submit-confirm`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ txHash }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Submit confirm error:', error);
    return { success: false, message: error.message || 'Failed to submit confirm' };
  }
}

/**
 * Seller marks an escrowed order as shipped. Optional tracking number + notes.
 */
export async function markOrderShipped(
  orderId: string,
  params?: { trackingNumber?: string; notes?: string }
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/ship`, {
      method: 'POST',
      headers,
      body: JSON.stringify(params || {}),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Mark shipped error:', error);
    return { success: false, message: error.message || 'Failed to mark as shipped' };
  }
}

/**
 * Seller marks an order as delivered. Requires a delivery proof URL.
 */
export async function markOrderDelivered(
  orderId: string,
  deliveryProofUrl: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/deliver`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ deliveryProofUrl }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Mark delivered error:', error);
    return { success: false, message: error.message || 'Failed to mark as delivered' };
  }
}

/**
 * Seller approves a refund request. Order remains in refund_requested
 * but is flagged for admin to finalize on-chain.
 */
export async function approveRefund(
  orderId: string,
  notes?: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/approve-refund`, {
      method: 'POST',
      headers,
      body: JSON.stringify(notes ? { notes } : {}),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Approve refund error:', error);
    return { success: false, message: error.message || 'Failed to approve refund' };
  }
}

/**
 * Seller rejects a refund request. Order returns to delivered status.
 */
export async function rejectRefund(
  orderId: string,
  notes?: string
): Promise<ApiResponse<Order>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/reject-refund`, {
      method: 'POST',
      headers,
      body: JSON.stringify(notes ? { notes } : {}),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Reject refund error:', error);
    return { success: false, message: error.message || 'Failed to reject refund' };
  }
}

export async function requestRefund(orderId: string): Promise<ApiResponse<any>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/request-refund`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Request refund error:', error);
    return { success: false, message: error.message || 'Failed to request refund' };
  }
}

// Cancel an order. Buyer may cancel while pending_payment. Seller may cancel
// while pending_payment (DB transition) or while escrowed (returns an unsigned
// on-chain cancelTx that must be signed and broadcast by the seller).
export async function cancelOrder(
  orderId: string
): Promise<ApiResponse<{ cancelTx?: UnsignedTx; status?: string; orderId?: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/cancel`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Cancel order error:', error);
    return { success: false, message: error.message || 'Failed to cancel order' };
  }
}

// Buyer requests cancellation of an escrowed order. Seller must approve or reject.
export async function requestCancelOrder(
  orderId: string,
  reason: string
): Promise<ApiResponse<{ orderId: string; status: string; cancelRequestedAt: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/request-cancel`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ reason }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Request cancel error:', error);
    return { success: false, message: error.message || 'Failed to request cancel' };
  }
}

// Seller approves the buyer's cancel request; returns an unsigned on-chain cancel tx.
export async function approveCancelOrder(
  orderId: string
): Promise<
  // An escrow order returns a cancelTx for the seller to sign; a Spendable MCGP
  // order is refunded by the call itself and returns cancelled: true.
  ApiResponse<{ cancelTx?: UnsignedTx; orderId: string; status?: string; cancelled?: boolean }>
> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/approve-cancel`, {
      method: 'POST',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Approve cancel error:', error);
    return { success: false, message: error.message || 'Failed to approve cancel' };
  }
}

// Seller submits the broadcast cancel tx hash; completes the cancel.
export async function submitCancelOrder(
  orderId: string,
  txHash: string
): Promise<ApiResponse<{ orderId: string; status: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/submit-cancel`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ txHash }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Submit cancel error:', error);
    return { success: false, message: error.message || 'Failed to submit cancel' };
  }
}

// Seller rejects the buyer's cancel request; order returns to escrowed.
export async function rejectCancelOrder(
  orderId: string,
  notes?: string
): Promise<ApiResponse<{ orderId: string; status: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/reject-cancel`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ notes: notes || '' }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Reject cancel error:', error);
    return { success: false, message: error.message || 'Failed to reject cancel' };
  }
}

// Either party escalates the order to admin for resolution.
export async function raiseDispute(
  orderId: string,
  reason: string
): Promise<ApiResponse<{ orderId: string; disputedAt: string }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/orders/${orderId}/dispute`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ reason }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Raise dispute error:', error);
    return { success: false, message: error.message || 'Failed to raise dispute' };
  }
}

// Admin-only: list disputed orders.
export async function getDisputedOrders(
  page: number = 1,
  limit: number = 20
): Promise<ApiResponse<{ orders: Order[]; pagination: any }>> {
  try {
    const headers = await getAuthHeaders();
    const qp = new URLSearchParams({ page: String(page), limit: String(limit) });
    const response = await fetch(`${API_BASE_URL}/admin/orders/disputed?${qp.toString()}`, {
      method: 'GET',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Get disputed orders error:', error);
    return { success: false, message: error.message || 'Failed to fetch disputed orders' };
  }
}

// Admin-only: resolve a dispute / cancel_requested / refund_requested order.
// Returns an unsigned resolve tx the admin must sign and broadcast.
export async function adminResolveDispute(
  orderId: string,
  refundBuyer: boolean
): Promise<ApiResponse<{ resolveTx: UnsignedTx; refundBuyer: boolean }>> {
  try {
    const headers = await getAuthHeaders();
    const response = await fetch(`${API_BASE_URL}/admin/orders/${orderId}/resolve`, {
      method: 'POST',
      headers,
      body: JSON.stringify({ refundBuyer }),
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Admin resolve dispute error:', error);
    return { success: false, message: error.message || 'Failed to resolve dispute' };
  }
}

export async function getShippingEstimate(
  productId: string,
  buyerCity: string,
  buyerState: string,
  buyerCountry: string
): Promise<ApiResponse<{ zone: string; shippingCost: number }>> {
  try {
    const headers = await getAuthHeaders();
    const params = new URLSearchParams({
      productId,
      buyerCity,
      buyerState,
      buyerCountry,
    });
    const response = await fetch(`${API_BASE_URL}/orders/shipping-estimate?${params.toString()}`, {
      method: 'GET',
      headers,
    });
    return await handleResponse(response);
  } catch (error: any) {
    console.error('Shipping estimate error:', error);
    return { success: false, message: error.message || 'Failed to get shipping estimate' };
  }
}
