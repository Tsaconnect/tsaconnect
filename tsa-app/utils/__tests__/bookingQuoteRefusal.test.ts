import { bookingQuoteRefusal } from '../bookingQuoteRefusal';
import type { SchemaField } from '../schemaFields';

const fields: SchemaField[] = [
  { key: 'departure', type: 'location', label: 'From', required: true },
  { key: 'travel_date', type: 'date', label: 'Travel date', required: true },
];
const soldOut = { success: false, message: 'Lagos to Abuja Coach is sold out on 31 Oct 2026' };
const filled = { departure: 'Lagos', travel_date: '2026-10-31' };

describe('bookingQuoteRefusal', () => {
  it('tells a buyer with a complete form why it cannot be booked', () => {
    expect(bookingQuoteRefusal(soldOut, { fields, answers: filled, slotMissing: false })).toBe(soldOut.message);
  });

  it('stays quiet while the form is still being filled', () => {
    expect(bookingQuoteRefusal({ success: false, message: 'From is required' },
      { fields, answers: { travel_date: '2026-10-31' }, slotMissing: false })).toBeNull();
    expect(bookingQuoteRefusal(soldOut, { fields, answers: filled, slotMissing: true })).toBeNull();
  });

  it('has nothing to say about a priced booking', () => {
    expect(bookingQuoteRefusal({ success: true }, { fields, answers: filled, slotMissing: false })).toBeNull();
  });
});
