/* eslint-disable @typescript-eslint/no-require-imports */
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
/* eslint-disable import/first */

import {
  createRampBuy,
  getRampOrder,
  getRampQuote,
  priceMovedQuote,
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
