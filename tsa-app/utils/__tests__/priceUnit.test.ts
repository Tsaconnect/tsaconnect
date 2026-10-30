import { listingPriceUnits, priceUnitSuffix, quantityLabel } from '../priceUnit';

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
    expect(quantityLabel('fixed')).toBe('Quantity');
  });

  it("offers only the category's units, never on_request", () => {
    expect(listingPriceUnits(['per_seat', 'on_request'])).toEqual(['per_seat']);
    expect(listingPriceUnits(null)).toContain('per_hour');
    expect(listingPriceUnits(null)).not.toContain('on_request');
    expect(listingPriceUnits([])).toEqual([]);
  });
});
