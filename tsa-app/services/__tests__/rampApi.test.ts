/* eslint-disable @typescript-eslint/no-require-imports */
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
/* eslint-disable import/first */

import {
  createRampBuy,
  getRampOrder,
  getRampQuote,
  itIntentIdOf,
  mergeBuyQuotes,
  openOrderOf,
  priceMovedQuote,
  quotedRateOf,
  RampError,
} from '../rampApi';
import { PaycrestError } from '../paycrestApi';

const ok = (data: unknown, status = 200) => ({ ok: true, status, json: async () => ({ success: true, data }) });
// Gin's "no route" answer: plain text, so res.json() rejects and body is null.
const routeMissing = () => ({ ok: false, status: 404, json: async () => { throw new Error('not json'); } });
const jsonError = (status: number, body: unknown) => ({ ok: false, status, json: async () => body });

const fetchMock = () => global.fetch as jest.Mock;
const urlOf = (i: number) => String(fetchMock().mock.calls[i][0]);
const bodyOf = (i: number) => JSON.parse(fetchMock().mock.calls[i][1].body);

describe('rampApi', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('quotes through /ramp and keeps tokenAmount a number', async () => {
    fetchMock().mockResolvedValueOnce(ok({ side: 'sell', venues: [{ handle: 'h', chain: 'base', token: 'USDC', rate: '1353.25', tokenAmount: 0.59 }], best: null }));
    const q = await getRampQuote('sell', '800', 'NGN');
    expect(urlOf(0)).toContain('/ramp/quote?side=sell&amount=800&currency=NGN');
    expect(typeof q.venues[0].tokenAmount).toBe('number');
    expect(q.venues[0].handle).toBe('h');
  });

  it('asks the converted quote when a target is named, and falls back when the route is missing', async () => {
    fetchMock().mockResolvedValueOnce(ok({ venues: [], best: null }));
    await getRampQuote('buy', '10000', 'NGN', { token: 'USDT', chainId: 10, address: '0xopt', chain: 'optimism' });
    expect(urlOf(0)).toContain('/ramp/converted-quote?');
    expect(urlOf(0)).toContain('requestedChainId=10');
    expect(urlOf(0)).toContain('requestedChain=optimism');
    fetchMock().mockResolvedValueOnce(routeMissing()).mockResolvedValueOnce(ok({ venues: [], best: null }));
    await getRampQuote('buy', '10000', 'NGN', { token: 'USDT', chainId: 10, address: '0xopt' });
    expect(urlOf(1)).toContain('/ramp/converted-quote?');
    expect(urlOf(2)).toContain('/ramp/quote?');
  });

  it('never asks the converted quote for a sell or without a target', async () => {
    fetchMock().mockResolvedValue(ok({ venues: [], best: null }));
    await getRampQuote('buy', '10000', 'NGN');
    await getRampQuote('sell', '10', 'NGN', { token: 'USDT', chainId: 10, address: '0xopt' });
    expect(urlOf(0)).toContain('/ramp/quote?');
    expect(urlOf(1)).toContain('/ramp/quote?');
  });

  it('falls back to /paycrest only when the ramp route is missing, dropping the quote fields', async () => {
    fetchMock()
      .mockResolvedValueOnce(routeMissing())
      .mockResolvedValueOnce(ok({ id: 'o1' }, 201));
    const o = await createRampBuy({
      amount: '1000', chain: 'base', token: 'USDC', walletAddress: '0xabc',
      refundBankCode: '044', refundAccount: '0123456789', currency: 'NGN',
      quotedRate: '1397.59', handle: 'h',
    });
    expect(o.id).toBe('o1');
    expect(urlOf(0)).toContain('/ramp/buy');
    expect(urlOf(1)).toContain('/paycrest/buy');
    expect(bodyOf(1).quotedRate).toBeUndefined();
    expect(bodyOf(1).handle).toBeUndefined();
    expect(bodyOf(1).refundAccount).toBe('0123456789');
  });

  it('does NOT fall back on a real JSON 404 (an order that is not hers)', async () => {
    fetchMock().mockResolvedValueOnce(jsonError(404, { success: false, message: 'Order not found' }));
    await expect(getRampOrder('abc')).rejects.toMatchObject({ status: 404, message: 'Order not found' });
    expect(fetchMock()).toHaveBeenCalledTimes(1);
  });

  it('surfaces price_moved with the fresh quote, as a PaycrestError the screens already catch', async () => {
    const fresh = { side: 'buy', venues: [{ rate: '1500', chain: 'base', token: 'USDC', tokenAmount: 0.6 }], best: null };
    fetchMock().mockResolvedValueOnce(jsonError(409, { success: false, code: 'price_moved', message: 'The price changed', data: fresh }));
    let caught: unknown;
    try {
      await createRampBuy({ amount: '1000', chain: 'base', token: 'USDC', walletAddress: '0xabc', refundBankCode: '044', refundAccount: '1', quotedRate: '1400' });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(RampError);
    expect(caught).toBeInstanceOf(PaycrestError);
    expect(priceMovedQuote(caught)?.venues[0].rate).toBe('1500');
    expect(priceMovedQuote(new Error('x'))).toBeNull();
  });

  it('never sends the converted-buy fields to /ramp/buy', async () => {
    fetchMock().mockResolvedValueOnce(ok({ id: 'o2' }, 201));
    await createRampBuy({ amount: '1', chain: 'base', token: 'USDC', walletAddress: '0xabc', refundBankCode: '044', refundAccount: '1', quotedRate: '1400' });
    const b = bodyOf(0);
    expect(b.requestedChainId).toBeUndefined();
    expect(b.requestedTokenAddress).toBeUndefined();
    expect(b.quotedRate).toBe('1400');
  });
});

describe('the router with the company wallet and Flutterwave', () => {
  const fw = {
    chain: 'polygon', token: 'USDC', rate: '1692.57', effectiveRate: '1745.61', spreadPercent: 5.8, tokenAmount: 94.5375,
    kind: 'direct' as const, handle: 'h',
    fees: [{ kind: 'network', label: 'Network fee', provider: 'Flutterwave', token: '1.5', ngn: '2538.85' }],
  };
  const pcDirect = { chain: 'base', token: 'USDC', rate: '1397.59', spreadPercent: 1, tokenAmount: 0.71, kind: 'direct' as const };
  const converted = { chain: 'polygon', token: 'USDT', rate: '1410', spreadPercent: 1, tokenAmount: 0.7, kind: 'converted' as const };
  const quote = (venues: any[]) =>
    ({ currency: 'NGN', amount: '1000', side: 'buy', marketRate: 1380, venues, best: venues[0] ?? null, quotedAt: '' }) as any;

  it('keeps the router direct venues, in its order, and only the converted ones from /paycrest', () => {
    const merged = mergeBuyQuotes(quote([fw]), quote([pcDirect, converted]))!;
    expect(merged.venues).toEqual([fw, converted]);
    expect(merged.best).toEqual(fw);
  });

  it('falls back to a merged venue for best, never a dropped /paycrest direct one', () => {
    const d = { ...quote([]), best: null };
    const merged = mergeBuyQuotes(d, { ...quote([pcDirect, converted]), best: pcDirect })!;
    expect(merged.venues).toEqual([converted]);
    expect(merged.best).toEqual(converted);
  });

  it('stands on either quote alone', () => {
    expect(mergeBuyQuotes(null, quote([pcDirect, converted]))!.venues).toEqual([pcDirect, converted]);
    expect(mergeBuyQuotes(quote([fw]), undefined)!.venues).toEqual([fw]);
    expect(mergeBuyQuotes(undefined, null)).toBeNull();
  });

  it('holds an order to the all-in rate when the server sends one (spec §14)', () => {
    expect(quotedRateOf(fw)).toBe('1745.61');
    expect(quotedRateOf(pcDirect)).toBe('1397.59');
  });

  it('reads the purchase a ramp_order_open refusal names', () => {
    const body = { success: false, code: 'ramp_order_open', data: { orderId: 'i9' } };
    expect(openOrderOf(new RampError('waiting', 409, 'ramp_order_open', body))).toEqual({ orderId: 'i9' });
    expect(openOrderOf(new RampError('waiting', 409, 'ramp_order_open', { data: null }))).toBeNull();
    expect(openOrderOf(new RampError('x', 409, 'price_moved', body))).toBeNull();
    expect(openOrderOf(new Error('x'))).toBeNull();
  });

  it('finds the Instant Transfer purchase behind a company-wallet or Flutterwave order', () => {
    expect(itIntentIdOf({ id: 't1', provider: 'treasury' })).toBe('t1');
    expect(itIntentIdOf({ id: 'f1', provider: 'flutterwave' })).toBe('f1');
    expect(itIntentIdOf({ id: 'o1', reference: 'PCB-1' })).toBeNull();
    expect(itIntentIdOf({ id: 'o2', provider: 'paycrest' })).toBeNull();
    expect(itIntentIdOf(null)).toBeNull();
  });
});
