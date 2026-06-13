jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn().mockResolvedValue(undefined),
}));

import { resolveP2pCurrency } from './useP2pCurrency';
import { CurrencyConfig } from '../constants/currencies';

const supported: CurrencyConfig[] = [
  { code: 'USD', symbol: '$', name: 'US Dollar' },
  { code: 'CAD', symbol: 'CA$', name: 'Canadian Dollar' },
  { code: 'NGN', symbol: '₦', name: 'Nigerian Naira' },
];

describe('resolveP2pCurrency', () => {
  it('prefers the explicit P2P selection over the app currency', () => {
    expect(resolveP2pCurrency('CAD', 'NGN', supported)).toEqual(supported[1]);
  });

  it('falls back to the app currency when no P2P selection', () => {
    expect(resolveP2pCurrency(null, 'NGN', supported)).toEqual(supported[2]);
  });

  it('stubs metadata when the code is not in the supported list', () => {
    expect(resolveP2pCurrency('GHS', 'NGN', supported)).toEqual({
      code: 'GHS',
      symbol: 'GHS',
      name: 'GHS',
    });
  });

  it('uppercases the code and defaults to USD when both are empty', () => {
    expect(resolveP2pCurrency(null, '', supported)).toEqual(supported[0]);
    expect(resolveP2pCurrency('cad', '', supported)).toEqual(supported[1]);
  });
});
