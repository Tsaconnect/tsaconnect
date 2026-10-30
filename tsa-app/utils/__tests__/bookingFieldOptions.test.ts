import { fieldsMissingOptions, MAX_FIELD_OPTIONS, parseOptionList } from '../bookingFieldOptions';

describe('bookingFieldOptions', () => {
  it('parses typed choices', () => {
    expect(parseOptionList('Saloon, SUV ,, suv, Bus ')).toEqual(['Saloon', 'SUV', 'Bus']);
    expect(parseOptionList('  ,  ')).toEqual([]);
    expect(parseOptionList('Economy,')).toEqual(['Economy']);
  });

  it('caps the number of choices', () => {
    const many = Array.from({ length: MAX_FIELD_OPTIONS + 5 }, (_, i) => `o${i}`).join(',');
    expect(parseOptionList(many)).toHaveLength(MAX_FIELD_OPTIONS);
  });

  it('names choice fields that have nothing to choose from', () => {
    expect(
      fieldsMissingOptions([
        { key: 'vehicle_type', type: 'select', label: 'Vehicle type', required: false },
        { key: 'cabin', type: 'multiselect', label: 'Cabin class', options: ['Economy'], required: false },
        { key: 'notes', type: 'text', label: 'Notes', required: false },
      ] as any),
    ).toEqual(['Vehicle type']);
  });
});
