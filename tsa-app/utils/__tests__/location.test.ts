import { appendListingLocation, formatLocation, listingLocation, locationLabel, parseLocation } from '../location';

describe('location string', () => {
  it('round-trips a full location', () => {
    const value = { city: 'Nsukka', state: 'Enugu State', country: 'Nigeria' };
    expect(parseLocation(formatLocation(value))).toEqual(value);
  });

  it('reads parts from the end, keeping a typed city with commas whole', () => {
    expect(parseLocation('Enugu State, Nigeria')).toEqual({ city: '', state: 'Enugu State', country: 'Nigeria' });
    expect(parseLocation('Dutse , Kubwa extension Abuja., Federal Capital Territory, Nigeria')).toEqual({
      city: 'Dutse, Kubwa extension Abuja.',
      state: 'Federal Capital Territory',
      country: 'Nigeria',
    });
  });

  it('leaves a string it cannot split blank', () => {
    expect(parseLocation('Enugu Nigeria')).toEqual({ city: '', state: '', country: '' });
    expect(parseLocation(undefined)).toEqual({ city: '', state: '', country: '' });
  });
});

describe('listingLocation', () => {
  it('prefers the structured parts and falls back to parsing', () => {
    expect(listingLocation({ location: 'x, y', country: 'Nigeria', state: 'Lagos State', area: 'Yaba' })).toEqual({
      value: { country: 'Nigeria', state: 'Lagos State', city: '' },
      area: 'Yaba',
    });
    expect(listingLocation({ location: 'Mushin, Lagos State, Nigeria' }).value).toEqual({
      city: 'Mushin',
      state: 'Lagos State',
      country: 'Nigeria',
    });
  });
});

describe('appendListingLocation / locationLabel', () => {
  it('sends the display string and every part', () => {
    const sent: Record<string, string> = {};
    const form = { append: (k: string, v: string) => (sent[k] = v) } as unknown as FormData;
    appendListingLocation(form, { country: 'Nigeria', state: 'Enugu State', city: ' Nsukka ' }, ' Odenigbo ');
    expect(sent).toEqual({
      location: 'Nsukka, Enugu State, Nigeria',
      country: 'Nigeria',
      state: 'Enugu State',
      city: 'Nsukka',
      area: 'Odenigbo',
    });
  });

  it('puts the area before the location', () => {
    expect(locationLabel({ location: 'Nsukka, Enugu State, Nigeria', area: 'Odenigbo' })).toBe(
      'Odenigbo · Nsukka, Enugu State, Nigeria',
    );
    expect(locationLabel({ location: 'Nsukka, Enugu State, Nigeria' })).toBe('Nsukka, Enugu State, Nigeria');
  });
});
