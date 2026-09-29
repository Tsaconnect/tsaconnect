/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

// "Pay with another token" offers any coin she holds on an EVM network — except
// wallet MCGP (never used to pay), non-EVM coins (the swap is signed by her EVM
// wallet, where the order token must land), and the order token itself on Sonic.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn(async () => null),
  setItem: jest.fn(async () => {}),
  removeItem: jest.fn(async () => {}),
}));
jest.mock('@/hooks/useNetwork', () => ({ getNetwork: () => 'mainnet' }));

import { fundingPickerAllow } from '../fundingPicker';

const tok = (address: string, symbol = 'X') => ({ address, symbol, name: symbol, decimals: 6, logoURI: '' });
const MCGP = '0x517600323e5e2938207fa2e2e915b9d80e5b2b21';
const SONIC_USDC = '0x29219dd400f2Bf60E5a23d13Be72B486D4038894';
const SONIC_USDT = '0x6047828dc181963ba44974801ff68e538da5eaf9';

test('wallet MCGP is never offered', () => {
  expect(fundingPickerAllow('USDC')(146, tok(MCGP, 'MCGP'))).toBe(false);
});

test('non-EVM networks are not offered', () => {
  const allow = fundingPickerAllow('USDC');
  expect(allow(1151111081099710, tok('So11111111111111111111111111111111111111112', 'SOL'))).toBe(false); // Solana
  expect(allow(20000000000001, tok('bitcoin', 'BTC'))).toBe(false); // Bitcoin
  expect(allow(9270000000000000, tok('0x2::sui::SUI', 'SUI'))).toBe(false); // Sui
});

test('the order token on Sonic is a direct payment, not a swap', () => {
  expect(fundingPickerAllow('USDC')(146, tok(SONIC_USDC, 'USDC'))).toBe(false);
  expect(fundingPickerAllow('USDT')(146, tok(SONIC_USDT, 'USDT'))).toBe(false);
  // The other stable on Sonic, and the same stable elsewhere, are swaps.
  expect(fundingPickerAllow('USDC')(146, tok(SONIC_USDT, 'USDT'))).toBe(true);
  expect(fundingPickerAllow('USDC')(8453, tok('0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', 'USDC'))).toBe(true);
});

test('any other EVM coin she holds is offered, native coins included', () => {
  const allow = fundingPickerAllow('USDC');
  expect(allow(56, tok('0x55d398326f99059fF775485246999027B3197955', 'USDT'))).toBe(true);
  expect(allow(146, tok('0x0000000000000000000000000000000000000000', 'S'))).toBe(true);
  expect(allow(1)).toBe(true); // a network row with no token yet
});
