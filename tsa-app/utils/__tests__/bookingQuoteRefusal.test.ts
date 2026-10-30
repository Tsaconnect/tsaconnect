import { BOOKING_REFUSED, bookingQuoteRefusal } from '../bookingQuoteRefusal';

describe('bookingQuoteRefusal', () => {
  it('shows a refusal about the booking itself', () => {
    for (const msg of ['Lagos to Abuja Coach is sold out on 31 Oct 2026', 'The 10:00 slot is full', 'Stays booked instantly must end within 28 days']) {
      expect(bookingQuoteRefusal({ success: false, message: msg, code: BOOKING_REFUSED })).toBe(msg);
    }
  });

  it('stays quiet about an unfinished form or a failed request', () => {
    expect(bookingQuoteRefusal({ success: false, message: 'Pick a travel date' })).toBeNull();
    expect(bookingQuoteRefusal({ success: false, message: 'Network request failed' })).toBeNull();
    expect(bookingQuoteRefusal({ success: false, message: 'Failed to start transaction' })).toBeNull();
  });

  it('has nothing to say about a priced booking', () => {
    expect(bookingQuoteRefusal({ success: true })).toBeNull();
  });
});
