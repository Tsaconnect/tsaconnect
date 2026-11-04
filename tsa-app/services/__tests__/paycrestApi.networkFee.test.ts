jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { networkFeeText } from '../paycrestApi';

describe('networkFeeText', () => {
  it('shows the fee in the token alone', () => {
    expect(networkFeeText({ networkFee: '0.02', token: 'USDC' })).toBe('0.02 USDC');
  });

  it('is null when she paid none', () => {
    expect(networkFeeText({ token: 'USDC' })).toBeNull();
    expect(networkFeeText({ networkFee: '0', token: 'USDC' })).toBeNull();
  });
});
