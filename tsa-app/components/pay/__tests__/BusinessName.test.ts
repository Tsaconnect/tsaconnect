/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
jest.mock('@/theme', () => ({ __esModule: true, useTheme: () => ({ spacing: new Proxy({}, { get: () => 8 }) }) }));
jest.mock('@/components/ui', () => ({ __esModule: true }));

import { businessBaseName, businessNameChangeFrom, businessNameSuffix } from '../BusinessName';

const shown = { businessName: 'Ojay store - Ademu Emmanuel', businessBaseName: 'Ojay store' };

describe('the shown name and the owner\'s own part of it', () => {
  it('splits the legal name off the owner\'s name', () => {
    expect(businessBaseName(shown)).toBe('Ojay store');
    expect(businessNameSuffix(shown)).toBe(' - Ademu Emmanuel');
  });

  it('a verified business, or an older API, has no suffix', () => {
    expect(businessNameSuffix({ businessName: 'Ojay store', businessBaseName: 'Ojay store' })).toBe('');
    expect(businessNameSuffix({ businessName: 'Ojay store' })).toBe('');
    expect(businessBaseName({ businessName: 'Ojay store' })).toBe('Ojay store');
  });
});

describe('asking for a new name', () => {
  it('sends the owner\'s part only', () => {
    expect(businessNameChangeFrom('  Kite   Travel ', shown)).toEqual({ name: 'Kite Travel' });
  });

  it('pasting the shown name back does not add the legal name twice', () => {
    expect(businessNameChangeFrom('Kite Travel - ademu emmanuel', shown)).toEqual({ name: 'Kite Travel' });
  });

  it('refuses the name they already have, even typed with its legal name', () => {
    expect(businessNameChangeFrom('Ojay store', shown)).toEqual({ missing: 'That is already your business name.' });
    expect(businessNameChangeFrom('Ojay store - Ademu Emmanuel', shown)).toEqual({ missing: 'That is already your business name.' });
  });

  it('a name already waiting for approval is not asked twice', () => {
    const waiting = { ...shown, pendingBusinessName: 'Kite Travel - Ademu Emmanuel' };
    expect(businessNameChangeFrom('Kite Travel', waiting)).toEqual({ missing: 'That name is already waiting for approval.' });
  });

  it('keeps the length rule on the owner\'s part', () => {
    expect(businessNameChangeFrom('K', shown)).toEqual({ missing: 'Enter a name of 2 to 100 characters.' });
  });
});
