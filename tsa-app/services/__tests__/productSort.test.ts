import { hasPlace, locationKey, serverSort, sortProducts } from '../productSort';

const p = (name: string, extra: Record<string, unknown> = {}) => ({ name, createdAt: '2026-11-01T00:00:00Z', ...extra }) as any;
const names = (list: any[]) => list.map((x) => x.name).join(',');

describe('serverSort', () => {
  it('asks for newest, most popular and top rated first; only low-to-high price ascends', () => {
    expect(serverSort('recent')).toEqual({ sortBy: 'createdAt', sortOrder: 'desc' });
    expect(serverSort('popular')).toEqual({ sortBy: 'sales', sortOrder: 'desc' });
    expect(serverSort('rating')).toEqual({ sortBy: 'rating.average', sortOrder: 'desc' });
    expect(serverSort('price_low')).toEqual({ sortBy: 'price', sortOrder: 'asc' });
    expect(serverSort('price_high')).toEqual({ sortBy: 'price', sortOrder: 'desc' });
    expect(serverSort('nearest').sortBy).toBe('nearest');
  });
});

describe('locationKey', () => {
  it('matches the API: case, spacing, a trailing stop and " state" ignored', () => {
    expect(locationKey(' Enugu  State. ')).toBe('enugu');
    expect(locationKey('enugu')).toBe('enugu');
    expect(hasPlace({ state: ' ' })).toBe(false);
    expect(hasPlace({ city: 'Nsukka' })).toBe(true);
  });
});

describe('sortProducts nearest', () => {
  it('ranks same city (within the state), same state, same country, then the rest; newest within each', () => {
    const list = [
      p('douala', { country: 'Cameroon' }),
      p('kubwa', { country: 'Nigeria', state: 'FCT' }),
      p('other-nsukka', { country: 'Nigeria', state: 'Kogi', city: 'Nsukka' }),
      p('enugu-old', { country: 'Nigeria', state: 'Enugu State', createdAt: '2026-10-01T00:00:00Z' }),
      p('enugu-new', { country: 'Nigeria', state: 'Enugu', createdAt: '2026-11-03T00:00:00Z' }),
      p('nsukka', { country: 'Nigeria', state: 'Enugu', city: 'nsukka' }),
    ];
    expect(names(sortProducts(list, 'nearest', { country: 'Nigeria', state: 'Enugu State', city: 'Nsukka' })))
      .toBe('nsukka,enugu-new,enugu-old,kubwa,other-nsukka,douala');
  });

  it('is newest first with no place', () => {
    const list = [p('old', { createdAt: '2026-01-01T00:00:00Z' }), p('new', { createdAt: '2026-11-01T00:00:00Z' })];
    expect(names(sortProducts(list, 'nearest', null))).toBe('new,old');
  });
});
