/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The shared swap-signing step (a sale's and a TSA Pay payment's). What matters
 * most is what it signs FOR: an explicit amount from the caller wins over the
 * quote's own figure, and with neither it refuses — it never signs "0".
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('@/services/lifiFlow', () => ({
  __esModule: true,
  executeLiFiSwap: jest.fn(),
  SwapBroadcastUnconfirmedError: class extends Error {},
}));
jest.mock('@/services/paycrestSellSwap', () => ({
  __esModule: true,
  abandonSellIntent: jest.fn(),
  requestSellIntentGasTopUp: jest.fn(),
}));

import * as LifiFlow from '@/services/lifiFlow';
import * as SellSwap from '@/services/paycrestSellSwap';
import type { LiFiQuote, LiFiToken } from '@/services/lifi';
import { executeSellSwap } from '../sellSwapRun';

const exec = jest.mocked(LifiFlow.executeLiFiSwap);
const abandon = jest.mocked(SellSwap.abandonSellIntent);
const bnb = { address: '0x0000000000000000000000000000000000000000', symbol: 'BNB', decimals: 18 } as LiFiToken;
const base = { intentId: 'i1', gasTopUp: false, token: bnb, sourceChainId: 56, sourceAddress: '0xUser' };

beforeEach(() => {
  jest.clearAllMocks();
  exec.mockResolvedValue('0xswap');
  abandon.mockResolvedValue(undefined);
});

it('signs the amount the caller gave, even when the quote carries a different figure', async () => {
  await executeSellSwap({ ...base, quote: { fromAmount: '999000000000000000' } as LiFiQuote, amount: '0.016' });
  expect(exec).toHaveBeenCalledWith(expect.objectContaining({ fromAmount: '0.016', feeFeature: 'none' }));
});

it('with no explicit amount signs what the quote pulls, in the token’s decimals', async () => {
  await executeSellSwap({ ...base, quote: { fromAmount: '84000000000000000' } as LiFiQuote });
  expect(exec).toHaveBeenCalledWith(expect.objectContaining({ fromAmount: '0.084' }));
});

it('with neither, refuses — never signs 0 — and closes the intent so it does not block the next', async () => {
  await expect(executeSellSwap({ ...base, quote: {} as LiFiQuote })).rejects.toThrow(/amount/i);
  expect(exec).not.toHaveBeenCalled();
  expect(abandon).toHaveBeenCalledWith('i1');
});

describe('an amount that is zero is no amount', () => {
  it.each([
    ['a quote that pulls 0', { quote: { fromAmount: '0' } as LiFiQuote }],
    ['an explicit 0', { quote: { fromAmount: '84000000000000000' } as LiFiQuote, amount: '0' }],
    ['an explicit 0.0', { quote: {} as LiFiQuote, amount: '0.0' }],
    ['an explicit amount that is not a number', { quote: {} as LiFiQuote, amount: 'abc' }],
  ])('%s: refuses, signs nothing, closes the intent', async (_name, extra) => {
    await expect(executeSellSwap({ ...base, ...extra })).rejects.toThrow(/amount/i);
    expect(exec).not.toHaveBeenCalled();
    expect(abandon).toHaveBeenCalledWith('i1');
  });
});
