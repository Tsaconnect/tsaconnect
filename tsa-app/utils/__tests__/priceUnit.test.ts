import {
  bookingActionLabel,
  listingPriceUnits,
  placesCapacityCopy,
  quantityCount,
  priceUnitSuffix,
  quantityLabel,
  servicePriceLabel,
  stayUnitNoun,
  type ServicePriceSummary,
} from '../priceUnit';

describe('priceUnit', () => {
  it('suffixes per-unit prices only', () => {
    expect(priceUnitSuffix('per_seat')).toBe(' / seat');
    expect(priceUnitSuffix('per_night')).toBe(' / night');
    expect(priceUnitSuffix('fixed')).toBe('');
    expect(priceUnitSuffix('')).toBe('');
    expect(priceUnitSuffix(undefined)).toBe('');
  });

  it('names what a quantity booking counts', () => {
    expect(quantityLabel('per_seat')).toBe('Seats');
    expect(quantityLabel('per_session')).toBe('Sessions');
    expect(quantityLabel('per_ticket')).toBe('Tickets');
    expect(quantityLabel('fixed')).toBe('Number of people');
    expect(quantityLabel('')).toBe('Number of people');
  });

  it("offers only the category's units, never on_request", () => {
    expect(listingPriceUnits(['per_seat', 'on_request'])).toEqual(['per_seat']);
    expect(listingPriceUnits(null)).toContain('per_hour');
    expect(listingPriceUnits(null)).not.toContain('on_request');
    expect(listingPriceUnits([])).toEqual([]);
  });

  it('names what a stay is charged per', () => {
    expect(stayUnitNoun('per_night')).toBe('night');
    expect(stayUnitNoun('')).toBe('night');
    expect(stayUnitNoun('per_day')).toBe('day');
    expect(stayUnitNoun('per_month')).toBe('month');
  });

  const usd = (n: number) => `$${n.toFixed(2)}`;
  const booking = (over: Partial<ServicePriceSummary>): ServicePriceSummary => ({
    serviceType: 'booking',
    fromUsd: 50,
    ...over,
  });

  it('labels a listing price by how it is charged', () => {
    expect(servicePriceLabel(booking({ bookingMode: 'date_range', priceUnit: 'per_night', chargesPerUnit: true }), usd)).toBe(
      'From $50.00 / night',
    );
    expect(servicePriceLabel(booking({ bookingMode: 'date_range', priceUnit: '', chargesPerUnit: true }), usd)).toBe(
      'From $50.00 / night',
    );
    expect(servicePriceLabel(booking({ bookingMode: 'quantity', priceUnit: 'per_ticket', chargesPerUnit: true }), usd)).toBe(
      'From $50.00 / ticket',
    );
    expect(servicePriceLabel(booking({ bookingMode: 'slot', priceUnit: 'per_hour' }), usd)).toBe('From $50.00');
    expect(servicePriceLabel(booking({ onRequest: true, fromUsd: 0 }), usd)).toBe('Price on request');
    expect(servicePriceLabel({ serviceType: 'contact', fromUsd: 12 }, usd)).toBe('$12.00');
    expect(servicePriceLabel(booking({ fromUsd: 0 }), usd)).toBeNull();
    expect(servicePriceLabel(undefined, usd)).toBeNull();
  });

  it('names the booking action by how the listing books', () => {
    expect(bookingActionLabel(booking({ bookingMode: 'date_range' }))).toBe('Book your stay');
    expect(bookingActionLabel(booking({ bookingMode: 'quantity', priceUnit: 'per_ticket' }))).toBe('Get tickets');
    expect(bookingActionLabel(booking({ bookingMode: 'quantity', priceUnit: 'per_seat' }))).toBe('Book seats');
    expect(bookingActionLabel(booking({ bookingMode: 'slot' }))).toBe('Book a slot');
    expect(bookingActionLabel(booking({ bookingType: 'request', bookingMode: 'date_range' }))).toBe('Request booking');
    expect(bookingActionLabel(undefined)).toBe('Book a slot');
  });

  it('names a slotless listing\'s capacity by date or in total', () => {
    expect(placesCapacityCopy('per_ticket', false).label).toBe('Tickets available');
    expect(placesCapacityCopy('per_seat', true).label).toBe('Seats per date');
    expect(placesCapacityCopy('', false).label).toBe('Places available');
    expect(placesCapacityCopy(null, true).helper).toContain('same date');
  });

  it('counts a quantity booking in the right number', () => {
    expect(quantityCount('per_seat', 1)).toBe('1 seat');
    expect(quantityCount('per_ticket', 3)).toBe('3 tickets');
    expect(quantityCount('', 1)).toBe('1 person');
    expect(quantityCount(undefined, 2)).toBe('2 people');
  });
});
