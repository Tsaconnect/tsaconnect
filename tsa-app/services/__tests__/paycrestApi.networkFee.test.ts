jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { networkFeeText } from '../paycrestApi';

describe('networkFeeText', () => {
  it('shows the fee in tokens and its naira value at the order rate', () => {
    expect(networkFeeText({ networkFee: '0.02', token: 'USDC', rate: '1386.64' })).toBe('0.02 USDC (₦27.73)');
  });

  it('is null when she paid none', () => {
    expect(networkFeeText({ token: 'USDC', rate: '1386.64' })).toBeNull();
    expect(networkFeeText({ networkFee: '0', token: 'USDC', rate: '1386.64' })).toBeNull();
  });

  it('drops the naira value without a rate', () => {
    expect(networkFeeText({ networkFee: '0.02', token: 'USDC' })).toBe('0.02 USDC');
  });
});
