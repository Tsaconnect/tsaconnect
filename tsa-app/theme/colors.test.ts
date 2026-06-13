import { darkColors, lightColors, type ColorTokens } from './colors';

describe('TSA Signature color tokens', () => {
  it('light and dark define exactly the same key set (no missing token)', () => {
    const lightKeys = Object.keys(lightColors).sort();
    const darkKeys = Object.keys(darkColors).sort();
    expect(lightKeys).toEqual(darkKeys);
  });

  it('every token value is a non-empty string', () => {
    for (const [scheme, tokens] of [
      ['light', lightColors],
      ['dark', darkColors],
    ] as const) {
      for (const [key, value] of Object.entries(tokens)) {
        expect(typeof value).toBe('string');
        expect(`${scheme}.${key}=${value}`).toMatch(/=#|=rgba|=.+[0-9A-Fa-f]/);
      }
    }
  });

  it('all color values are valid hex (#RGB / #RRGGBB / #RRGGBBAA)', () => {
    const hex = /^#([0-9A-Fa-f]{3}|[0-9A-Fa-f]{6}|[0-9A-Fa-f]{8})$/;
    for (const tokens of [lightColors, darkColors]) {
      for (const value of Object.values(tokens as ColorTokens)) {
        expect(value).toMatch(hex);
      }
    }
  });

  it('accent + surfaces differ between schemes (adaptive, not identical)', () => {
    expect(lightColors.background).not.toBe(darkColors.background);
    expect(lightColors.accent).not.toBe(darkColors.accent);
    expect(lightColors.textPrimary).not.toBe(darkColors.textPrimary);
  });
});
