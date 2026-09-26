/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
// Which coins the pay screen's picker may offer. Nothing can be sent from Solana,
// Bitcoin or Sui for a payment yet, and a crypto merchant's own token on their own
// chain isn't payable through the swap route (the server refuses it too).
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn().mockResolvedValue(null) }));
jest.mock('../../hooks/useNetwork', () => ({ getNetwork: () => 'mainnet' }));

import { payPickFilter } from '../payPick';
import { SOLANA_LIFI_CHAIN_ID, type LiFiToken } from '@/services/lifi';
import type { PayCheckout } from '@/services/payApi';

const USDT_BSC = '0x55d398326f99059fF775485246999027B3197955';
const fiat = { settlementMode: 'fiat' } as PayCheckout;
const crypto = { settlementMode: 'crypto', settleChainId: 56, settleTokenAddress: USDT_BSC } as PayCheckout;
const tok = (address: string) => ({ address, symbol: 'X', decimals: 18 }) as LiFiToken;

it('offers EVM networks and refuses non-EVM ones, for any merchant', () => {
  expect(payPickFilter(fiat)(8453, tok('0xabc'))).toBe(true);
  expect(payPickFilter(fiat)(SOLANA_LIFI_CHAIN_ID, tok('sol'))).toBe(false);
  expect(payPickFilter(crypto)(SOLANA_LIFI_CHAIN_ID)).toBe(false);
});

it('hides the crypto merchant’s own token on their own chain — in any letter case — and nothing else', () => {
  const allow = payPickFilter(crypto);
  expect(allow(56, tok(USDT_BSC))).toBe(false);
  expect(allow(56, tok(USDT_BSC.toLowerCase()))).toBe(false);
  expect(allow(8453, tok(USDT_BSC))).toBe(true); // same address, another chain
  expect(allow(56, tok('0x0000000000000000000000000000000000000000'))).toBe(true); // BNB pays USDT fine
});

it('asked about a chain alone (a search still looking for tokens) it keeps the chain', () => {
  expect(payPickFilter(crypto)(56)).toBe(true);
});

it('never hides anything for a naira merchant', () => {
  expect(payPickFilter(fiat)(56, tok(USDT_BSC))).toBe(true);
});
