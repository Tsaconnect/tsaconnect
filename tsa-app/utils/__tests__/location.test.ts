import { formatLocation, parseLocation } from '../location';

describe('location string', () => {
  it('round-trips a full location', () => {
    const value = { city: 'Nsukka', state: 'Enugu', country: 'Nigeria' };
    expect(parseLocation(formatLocation(value))).toEqual(value);
  });

  it('reads parts from the end when the city is missing', () => {
    expect(parseLocation('Enugu, Nigeria')).toEqual({ city: '', state: 'Enugu', country: 'Nigeria' });
    expect(parseLocation('Nigeria')).toEqual({ city: '', state: '', country: 'Nigeria' });
  });

  it('tolerates empty and unspaced input', () => {
    expect(parseLocation(undefined)).toEqual({ city: '', state: '', country: '' });
    expect(parseLocation('Nsukka,Enugu,Nigeria')).toEqual({ city: 'Nsukka', state: 'Enugu', country: 'Nigeria' });
  });
});
