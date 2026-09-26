import { DEFAULT_HOME, returnPath } from './context';

describe('returnPath', () => {
  it('keeps an in-app path with its query', () => {
    expect(returnPath({ from: '/business/test/payments?status=failed' })).toBe('/business/test/payments?status=failed');
  });

  it.each([
    ['nothing', undefined],
    ['a non-string', { from: 42 }],
    ['an absolute URL', { from: 'https://evil.example/' }],
    ['a protocol-relative URL', { from: '//evil.example/' }],
    ['a backslash the browser reads as a slash', { from: '/\\evil.example/' }],
    ['sign-in itself', { from: '/signin/code' }],
  ])('refuses %s and goes home', (_, state) => {
    expect(returnPath(state)).toBe(DEFAULT_HOME);
  });
});
