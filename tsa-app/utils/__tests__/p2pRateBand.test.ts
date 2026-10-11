import { fixedRateBandRefusal, P2P_FIXED_RATE_BAND_BPS } from '../p2pRateBand';

// babel-jest hoists this above the imports.
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn().mockResolvedValue(null) }));

describe('fixedRateBandRefusal (pre-deposit guard; the server is authoritative)', () => {
  const base = { token: 'USDT', reference: 1500 };
  it('mirrors the API default band of ±20%', () => {
    expect(P2P_FIXED_RATE_BAND_BPS).toBe(2000);
  });
  it('passes inside the band, edges included', () => {
    for (const rate of ['1200', '1500', '1800']) expect(fixedRateBandRefusal({ ...base, rate })).toBeNull();
  });
  it('refuses outside it with the same copy as the server refusal', () => {
    expect(fixedRateBandRefusal({ ...base, rate: '15' })).toBe('Rate must be between 1200 and 1800.');
    expect(fixedRateBandRefusal({ ...base, rate: '1801' })).toBe('Rate must be between 1200 and 1800.');
  });
  it('passes when there is nothing to compare against (like the server)', () => {
    expect(fixedRateBandRefusal({ ...base, rate: '15', reference: null })).toBeNull();
    expect(fixedRateBandRefusal({ ...base, rate: '15', referenceStale: true })).toBeNull();
    expect(fixedRateBandRefusal({ ...base, token: 'MCGP', rate: '15' })).toBeNull();
  });
  it('formats fractional edges like the server (2 dp, trailing zeros trimmed)', () => {
    expect(fixedRateBandRefusal({ token: 'usdc', reference: 1.37, rate: '5' })).toBe('Rate must be between 1.1 and 1.64.');
  });
});
