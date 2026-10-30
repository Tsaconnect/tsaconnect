// utils/bookingQuoteRefusal.ts
// What the booking sheet tells the buyer when the live quote is refused. The
// server marks a refusal about the booking itself — its nights or places are
// taken ("sold out on 31 Oct 2026", "Only 2 left") — with booking_unavailable.
// Anything else is a form still being filled in, or a failed request, which is
// not a reason the booking cannot be made, so it is not shown here.
export const BOOKING_UNAVAILABLE = 'booking_unavailable';

export function bookingQuoteRefusal(res: { success: boolean; message?: string; code?: string }): string | null {
  if (res.success || res.code !== BOOKING_UNAVAILABLE) return null;
  return res.message || null;
}
