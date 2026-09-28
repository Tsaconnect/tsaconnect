// Wallet MCGP (the plain ERC-20 on Sonic) is matched by chain + contract, never
// by symbol — the pay and naira-sell pickers hide exactly that token and nothing else.
import { isWalletMcgp, notWalletMcgp } from '../walletMcgp';
import type { LiFiToken } from '@/services/lifi';

const MCGP = '0x517600323e5E2938207fA2e2e915B9D80e5B2b21';
const tok = (address: string, symbol = 'MCGP') => ({ address, symbol, decimals: 18 }) as LiFiToken;

it('is the MCGP contract on Sonic mainnet and testnet, in any letter case', () => {
  expect(isWalletMcgp(146, tok(MCGP))).toBe(true);
  expect(isWalletMcgp(146, tok(MCGP.toUpperCase().replace('0X', '0x')))).toBe(true);
  expect(isWalletMcgp(14601, tok(MCGP.toLowerCase()))).toBe(true);
});

it('is not a look-alike called MCGP at another address, nor the address on another chain', () => {
  expect(isWalletMcgp(146, tok('0x000000000000000000000000000000000000bEEF'))).toBe(false);
  expect(isWalletMcgp(56, tok(MCGP))).toBe(false);
});

it('is not another Sonic coin, and a chain asked about alone is kept', () => {
  expect(isWalletMcgp(146, tok('0x6047828dc181963ba44974801ff68e538da5eaf9', 'USDT'))).toBe(false);
  expect(isWalletMcgp(146)).toBe(false);
  expect(notWalletMcgp(146)).toBe(true);
});

it('as a picker filter, drops only wallet MCGP', () => {
  expect(notWalletMcgp(146, tok(MCGP))).toBe(false);
  expect(notWalletMcgp(146, tok('0x6047828dc181963ba44974801ff68e538da5eaf9', 'USDT'))).toBe(true);
  expect(notWalletMcgp(8453, tok('0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', 'USDC'))).toBe(true);
});
