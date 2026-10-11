/* eslint-disable @typescript-eslint/no-require-imports */
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
/* eslint-disable import/first */
import { getRampQuote, itIntentIdOf } from '../rampApi';

const ok = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ success: true, data }) });
const fetchMock = () => global.fetch as jest.Mock;
const urlOf = (i: number) => String(fetchMock().mock.calls[i][0]);

describe('rampApi — TSA Connect pool', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('routes a tsa_pool order to its Instant Transfer screen', () => {
    expect(itIntentIdOf({ provider: 'tsa_pool', id: 'abc' })).toBe('abc');
    expect(itIntentIdOf({ provider: 'paycrest', id: 'abc' })).toBeNull();
  });

  it('asks the pool-only quote with pool=1 and keeps poolAvailable', async () => {
    fetchMock().mockResolvedValueOnce(ok({ venues: [], best: null, poolAvailable: false }));
    const q = await getRampQuote('buy', '5000', 'NGN', undefined, { pool: true });
    expect(urlOf(0)).toContain('/ramp/quote?');
    expect(urlOf(0)).toContain('pool=1');
    expect(q.poolAvailable).toBe(false);
  });

  it('passes pool=1 on the converted quote too, and never without the option', async () => {
    fetchMock().mockResolvedValue(ok({ venues: [], best: null }));
    await getRampQuote('buy', '5000', 'NGN', { token: 'USDT', chainId: 56, address: '0x55d3', chain: 'bsc' }, { pool: true });
    expect(urlOf(0)).toContain('/ramp/converted-quote?');
    expect(urlOf(0)).toContain('pool=1');
    await getRampQuote('buy', '5000', 'NGN');
    expect(urlOf(1)).not.toContain('pool=');
  });
});
