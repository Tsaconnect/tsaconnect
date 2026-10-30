// utils/bookingQuoteRefusal.ts
// What the booking sheet tells the buyer when the live quote is refused. Once
// the booking is filled in, the server marks any refusal booking_refused: it is
// about the booking itself (sold out, a full slot, dates the listing does not
// take). Anything else is a form still being filled in, or a failed request,
// neither of which says why the booking cannot be made, so it is not shown.
export const BOOKING_REFUSED = 'booking_refused';

export function bookingQuoteRefusal(res: { success: boolean; message?: string; code?: string }): string | null {
  if (res.success || res.code !== BOOKING_REFUSED) return null;
  return res.message || null;
}
