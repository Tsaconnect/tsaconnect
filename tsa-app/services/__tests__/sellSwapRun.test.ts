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
import AsyncStorage from '@react-native-async-storage/async-storage';
import { executeSellSwap, SwapPriceMovedError } from '../sellSwapRun';

const exec = jest.mocked(LifiFlow.executeLiFiSwap);
const abandon = jest.mocked(SellSwap.abandonSellIntent);
const bnb = { address: '0x0000000000000000000000000000000000000000', symbol: 'BNB', decimals: 18 } as LiFiToken;
/** A native-coin quote as the server builds it, and the amount it was priced at. */
const PRICED = '1000000000000000';
const pricedQuote = { fromAmount: PRICED, transactionRequest: { value: `0x${BigInt(PRICED).toString(16)}` } } as unknown as LiFiQuote;
const base = { intentId: 'i1', gasTopUp: false, token: bnb, sourceChainId: 56, sourceAddress: '0xUser' };

beforeEach(async () => {
  jest.clearAllMocks();
  await AsyncStorage.clear();
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

describe('the bound between the price she was shown and what is signed', () => {
  const priced = '84000000000000000'; // 0.084 BNB
  const within = '85680000000000000'; // +2%
  const over = '85680000000000001'; // one wei past it
  const native = { ...base, token: { ...bnb } };
  const quoteOf = (fromAmount: string, value = `0x${BigInt(fromAmount).toString(16)}`) =>
    ({ fromAmount, transactionRequest: { value } }) as unknown as LiFiQuote;

  it('signs a quote within 2% of the priced amount', async () => {
    await executeSellSwap({ ...native, quote: quoteOf(within), pricedFromAmount: priced });
    expect(exec).toHaveBeenCalled();
  });

  it('signs a quote cheaper than the priced amount', async () => {
    await executeSellSwap({ ...native, quote: quoteOf('80000000000000000'), pricedFromAmount: priced });
    expect(exec).toHaveBeenCalled();
  });

  it('refuses a quote past it BEFORE signing: closes the intent, says what happened, a plain Error the caller can retry', async () => {
    const err = await executeSellSwap({ ...native, quote: quoteOf(over), pricedFromAmount: priced }).catch((e) => e);
    expect(err).toBeInstanceOf(SwapPriceMovedError);
    expect(err.message).toMatch(/nothing was sent/i);
    expect(err.message).toContain('0.084 BNB');
    expect(exec).not.toHaveBeenCalled();
    expect(abandon).toHaveBeenCalledWith('i1');
  });

  it('refuses when there is a priced amount but the quote does not say what it pulls', async () => {
    await expect(executeSellSwap({ ...native, quote: {} as LiFiQuote, pricedFromAmount: priced })).rejects.toThrow();
    expect(exec).not.toHaveBeenCalled();
    expect(abandon).toHaveBeenCalledWith('i1');
  });

  it('a native coin whose transaction sends a different value is refused, never signed', async () => {
    const q = quoteOf(priced, '0x1');
    await expect(executeSellSwap({ ...native, quote: q, pricedFromAmount: priced })).rejects.toThrow(/nothing was sent/i);
    expect(exec).not.toHaveBeenCalled();
    expect(abandon).toHaveBeenCalledWith('i1');
  });

  it('a native coin whose value equals the quote (hex or decimal) is fine', async () => {
    await executeSellSwap({ ...native, quote: quoteOf(priced, `0x${BigInt(priced).toString(16)}`), pricedFromAmount: priced });
    await executeSellSwap({ ...native, quote: quoteOf(priced, priced), pricedFromAmount: priced });
    expect(exec).toHaveBeenCalledTimes(2);
  });

  it('a token (not the native coin) carries no value to check', async () => {
    const usdc = { address: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', symbol: 'USDC', decimals: 6 } as LiFiToken;
    await executeSellSwap({
      ...base, token: usdc, quote: { fromAmount: '33500000', transactionRequest: { value: '0x0' } } as unknown as LiFiQuote,
      pricedFromAmount: '33500000',
    });
    expect(exec).toHaveBeenCalled();
  });

  it('an ordinary sale (no priced amount) is bounded by nothing: what she typed is signed, as before', async () => {
    await executeSellSwap({
      ...native, amount: '0.016',
      quote: { fromAmount: '999000000000000000', transactionRequest: { value: '0x1' } } as unknown as LiFiQuote,
    });
    expect(exec).toHaveBeenCalledWith(expect.objectContaining({ fromAmount: '0.016' }));
    expect(abandon).not.toHaveBeenCalled();
  });
});

describe('the hold that survives an app kill', () => {
  it('writes tsapay.pendingSwap.<payment> beside the hash the moment the swap is signed, before it is broadcast', async () => {
    exec.mockImplementation(async (p: any) => {
      await p.onSigned('0xsigned');
      expect(await AsyncStorage.getItem('paycrest.sellSwap.pendingTx.i1')).toBe('0xsigned');
      expect(JSON.parse((await AsyncStorage.getItem('tsapay.pendingSwap.pay-1'))!)).toEqual({ intentId: 'i1', at: expect.any(Number) });
      return '0xsigned';
    });
    await executeSellSwap({ ...base, quote: pricedQuote, paymentId: 'pay-1', pricedFromAmount: PRICED });
  });

  it('writes nothing for an ordinary sale', async () => {
    exec.mockImplementation(async (p: any) => { await p.onSigned('0xsigned'); return '0xsigned'; });
    await executeSellSwap({ ...base, quote: {} as LiFiQuote, amount: '0.016' });
    expect(await AsyncStorage.getAllKeys()).not.toEqual(expect.arrayContaining([expect.stringContaining('tsapay.')]));
  });

  it('drops the hold when the swap is refused before it left her wallet', async () => {
    await AsyncStorage.setItem('tsapay.pendingSwap.pay-1', JSON.stringify({ intentId: 'i1', at: 1 }));
    exec.mockRejectedValue(new Error('User rejected'));
    await expect(executeSellSwap({ ...base, quote: pricedQuote, paymentId: 'pay-1', pricedFromAmount: PRICED })).rejects.toThrow();
    expect(await AsyncStorage.getItem('tsapay.pendingSwap.pay-1')).toBeNull();
  });
});

describe('storage that fails closes the swap, it does not skip the safeguard (TSA Pay)', () => {
  // Patch the AsyncStorage mock's methods and put them back (jest's restoreAllMocks/mockRestore would also
  // reset the mock's own implementations, breaking every later test).
  const undo: Array<() => void> = [];
  const patch = (name: 'getItem' | 'setItem', impl: (...a: any[]) => Promise<any>) => {
    const original = AsyncStorage[name];
    (AsyncStorage as any)[name] = impl;
    undo.push(() => { (AsyncStorage as any)[name] = original; });
  };
  afterEach(() => { undo.splice(0).forEach((u) => u()); });
  afterEach(() => { jest.restoreAllMocks(); }); // (console spies only: AsyncStorage is patched, not spied)
  const payRun = { ...base, quote: pricedQuote, paymentId: 'pay-1', pricedFromAmount: PRICED };
  const signs = () => exec.mockImplementation(async (p: any) => { await p.onSigned('0xsigned'); return '0xsigned'; });

  it('a hash that cannot be written stops the swap before it is broadcast: intent closed, error thrown', async () => {
    signs();
    patch('setItem', async () => { throw new Error('disk full'); });
    await expect(executeSellSwap(payRun)).rejects.toThrow(/nothing was sent/i);
    expect(abandon).toHaveBeenCalledWith('i1');
  });

  it('a hold that cannot be written does the same, and the hash written beside it is dropped', async () => {
    signs();
    const real = AsyncStorage.setItem.bind(AsyncStorage);
    patch('setItem', async (k: string, v: string) => {
      if (k.startsWith('tsapay.')) throw new Error('disk full');
      return real(k, v);
    });
    await expect(executeSellSwap(payRun)).rejects.toThrow(/nothing was sent/i);
    expect(abandon).toHaveBeenCalledWith('i1');
    expect(await AsyncStorage.getItem('paycrest.sellSwap.pendingTx.i1')).toBeNull();
  });

  it('the ORDINARY sale is exactly as strict as it was: a hash that cannot be written is only logged', async () => {
    signs();
    jest.spyOn(console, 'warn').mockImplementation(() => {});
    patch('setItem', async () => { throw new Error('disk full'); });
    await expect(executeSellSwap({ ...base, quote: {} as LiFiQuote, amount: '0.016' })).resolves.toBe('0xsigned');
    expect(abandon).not.toHaveBeenCalled();
  });
});

describe('a payment with no priced amount is not signed — it fails closed', () => {
  it.each([undefined, ''])('paymentId set and pricedFromAmount %j: refused before signing, intent closed', async (priced) => {
    await expect(executeSellSwap({
      ...base, quote: { fromAmount: '1000000000000000' } as LiFiQuote, paymentId: 'pay-1', pricedFromAmount: priced,
    })).rejects.toBeInstanceOf(SwapPriceMovedError);
    expect(exec).not.toHaveBeenCalled();
    expect(abandon).toHaveBeenCalledWith('i1');
  });

  it('an ordinary sale (no paymentId) with no priced amount is bounded by nothing, as before', async () => {
    await executeSellSwap({ ...base, quote: {} as LiFiQuote, amount: '0.016' });
    expect(exec).toHaveBeenCalled();
  });
});
