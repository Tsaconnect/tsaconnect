import {
  cleanSchemaAnswers,
  filterQuery,
  formatSchemaAnswer,
  validateSchemaAnswers,
  type SchemaField,
} from '../schemaFields';

const fields: SchemaField[] = [
  { key: 'institution', type: 'select', label: 'Institution', required: true, options: ['UNN', 'UNILAG'] },
  { key: 'amenities', type: 'multiselect', label: 'Amenities', required: false, options: ['Wifi', 'Water'] },
  { key: 'has_curfew', type: 'boolean', label: 'Has curfew', required: false },
  { key: 'guests', type: 'counter', label: 'Guests', required: true },
  { key: 'distance', type: 'number', label: 'Distance', required: false },
];

describe('validateSchemaAnswers', () => {
  it('flags missing required fields and a zero required counter', () => {
    expect(validateSchemaAnswers(fields, { guests: 0 })).toEqual({
      institution: 'Institution is required',
      guests: 'Guests is required',
    });
  });

  it('accepts complete answers and leaves booleans alone', () => {
    expect(validateSchemaAnswers(fields, { institution: 'UNN', guests: 2 })).toEqual({});
  });

  it('flags a non-numeric number', () => {
    expect(validateSchemaAnswers(fields, { institution: 'UNN', guests: 1, distance: 'far' })).toEqual({
      distance: 'Distance must be a number',
    });
  });
});

describe('number answers', () => {
  it('accepts typed text that is a number and rejects text that is not', () => {
    expect(validateSchemaAnswers(fields, { institution: 'UNN', guests: 1, distance: '1.5' })).toEqual({});
    expect(validateSchemaAnswers(fields, { institution: 'UNN', guests: 1, distance: '1.' })).toEqual({});
    expect(validateSchemaAnswers(fields, { institution: 'UNN', guests: 1, distance: '-' })).toEqual({
      distance: 'Distance must be a number',
    });
  });

  it('sends typed text as a number', () => {
    expect(cleanSchemaAnswers(fields, { institution: 'UNN', guests: 1, distance: ' 2.50 ' })).toEqual({
      institution: 'UNN',
      has_curfew: false,
      guests: 1,
      distance: 2.5,
    });
  });
});

describe('cleanSchemaAnswers', () => {
  it('keeps only form keys, drops empties and always sends booleans', () => {
    expect(
      cleanSchemaAnswers(fields, { institution: 'UNN', amenities: [], stale: 'x', guests: 1 }),
    ).toEqual({ institution: 'UNN', has_curfew: false, guests: 1 });
  });
});

describe('formatSchemaAnswer', () => {
  it('formats each kind of answer', () => {
    expect(formatSchemaAnswer(fields[1], ['Wifi', 'Water'])).toBe('Wifi, Water');
    expect(formatSchemaAnswer(fields[2], true)).toBe('Yes');
    expect(formatSchemaAnswer(fields[2], undefined)).toBeNull();
    expect(formatSchemaAnswer(fields[0], '')).toBeNull();
  });
});

describe('filterQuery', () => {
  it('builds attr[] params, skipping empty filters', () => {
    expect(filterQuery({ institution: 'UNN', gender: '', 'room type': '2 person' })).toBe(
      'attr[institution]=UNN&attr[room%20type]=2%20person',
    );
  });
});
