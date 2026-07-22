// Unit tests for the PM activation id-number validator. Both a Nigerian BVN and
// NIN are exactly 11 digits — the screen must reject anything else before it
// ever hits the network (no wasted provider round-trip).

// Mocks required by transitive dependencies (theme → AsyncStorage) pulled in by
// the screen module.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));

// eslint-disable-next-line import/first
import { isValidPmIdNumber } from '../activate-pm';

describe('isValidPmIdNumber', () => {
  it('accepts exactly 11 digits', () => {
    expect(isValidPmIdNumber('22233344455')).toBe(true);
    expect(isValidPmIdNumber('  22233344455  ')).toBe(true); // trimmed
  });

  it('rejects the wrong length', () => {
    expect(isValidPmIdNumber('1234567890')).toBe(false); // 10
    expect(isValidPmIdNumber('223334445556')).toBe(false); // 12
    expect(isValidPmIdNumber('')).toBe(false);
  });

  it('rejects non-numeric input', () => {
    expect(isValidPmIdNumber('2223334445a')).toBe(false);
    expect(isValidPmIdNumber('222-333-444-5')).toBe(false);
  });
});
