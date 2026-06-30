// components/services/serviceCart.ts
//
// Service line items ride the SAME shared backend cart as products (one cart,
// service-aware line items — spec §6 D3). The cart API only carries
// productId / quantity / notes per item, so we smuggle the buyer's resolved
// pricing context (declared area tier + the price/fee they were quoted) inside
// the item's `notes` field behind a sentinel prefix. Checkout decodes it to:
//   1. recognise which line items are services, and
//   2. build the serviceTiers map (productId → tier) passed to createOrders().
//
// This keeps the frozen orderApi / cart contracts untouched (we only ever set
// `notes`, an existing free-text field) while plumbing the declared tier
// end-to-end so the buyer is charged what they saw (spec §10 "quote == charge").
import type { BookingTier } from '../../services/bookingFeeApi';

// Sentinel-tagged JSON marker. A normal product note never starts with this.
const SVC_PREFIX = '__svc__';

export interface ServiceCartMeta {
  // Declared/resolved area tier for this service ('residential', a booking
  // tier, etc.). Sent to the backend as serviceTiers[productId].
  tier: BookingTier;
  // The buyer-facing service price (USD) at add time — informational, for the
  // cart line label. The authoritative charge is recomputed server-side.
  price: number;
  // The call-out / booking fee (USD) at add time — informational.
  fee: number;
  // 'standard' | 'booking' charging mode, for the cart line label copy.
  mode: 'standard' | 'booking';
}

// Encode service pricing context into a cart item's `notes` string.
export function encodeServiceNotes(meta: ServiceCartMeta): string {
  return SVC_PREFIX + JSON.stringify(meta);
}

// Decode a cart item's `notes`. Returns null for a normal (product) note.
export function decodeServiceNotes(notes?: string | null): ServiceCartMeta | null {
  if (!notes || !notes.startsWith(SVC_PREFIX)) return null;
  try {
    const parsed = JSON.parse(notes.slice(SVC_PREFIX.length));
    if (parsed && typeof parsed.tier === 'string') {
      return {
        tier: parsed.tier,
        price: Number(parsed.price) || 0,
        fee: Number(parsed.fee) || 0,
        mode: parsed.mode === 'booking' ? 'booking' : 'standard',
      };
    }
  } catch {
    // Malformed — treat as a plain note.
  }
  return null;
}

// True when a cart item is a service line item (carries service meta).
export function isServiceNote(notes?: string | null): boolean {
  return decodeServiceNotes(notes) != null;
}
