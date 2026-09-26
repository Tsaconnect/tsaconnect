// A cross-chain swap is not finished when its source transaction confirms, so
// the app has to ask what happened on the far side. Everything here is about
// not lying to the user in either direction: not claiming delivery that has not
// happened, and not calling a swap failed when the bridge is merely slow.
import { getSwapDelivery, getLiFiQuote } from '../lifi';

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue('Bearer test-token'),
}));

jest.mock('../../hooks/useNetwork', () => ({ getNetwork: () => 'mainnet' }));

const mockFetch = jest.fn();
global.fetch = mockFetch;

beforeEach(() => mockFetch.mockReset());

describe('getSwapDelivery', () => {
  it('reports delivery once the bridge has landed the funds', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ data: { status: 'DONE', destTxHash: '0xdest', receivedAmount: '250957' } }),
    });
    await expect(getSwapDelivery('0xsrc')).resolves.toEqual({
      status: 'DONE', destTxHash: '0xdest', receivedAmount: '250957',
    });
    expect(mockFetch.mock.calls[0][0]).toContain('/swap/lifi/status?txHash=0xsrc');
  });

  // DONE alone does not say she got what was quoted: LiFi qualifies it as
  // COMPLETED, PARTIAL (a different token landed) or REFUNDED. A sale must
  // tell those apart, so the substatus is passed through untouched.
  it('passes through the substatus that qualifies a DONE', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ data: { status: 'DONE', substatus: 'REFUNDED' } }) });
    expect(await getSwapDelivery('0xsrc')).toMatchObject({ status: 'DONE', substatus: 'REFUNDED' });
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ data: { status: 'DONE' } }) });
    expect((await getSwapDelivery('0xsrc')).substatus).toBeUndefined();
  });

  it('reports a failed bridge as failed', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ data: { status: 'FAILED' } }) });
    expect((await getSwapDelivery('0xsrc')).status).toBe('FAILED');
  });

  // Right after a broadcast LiFi does not know the hash yet, which looks
  // identical to a transient error. Calling that "failed" would tell a user
  // their swap died while it was in fact working.
  it('treats an unreachable or unknown status as still pending', async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, status: 502, json: async () => ({}) });
    expect((await getSwapDelivery('0xsrc')).status).toBe('PENDING');

    mockFetch.mockRejectedValueOnce(new Error('offline'));
    expect((await getSwapDelivery('0xsrc')).status).toBe('PENDING');

    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ data: { status: 'WEIRD' } }) });
    expect((await getSwapDelivery('0xsrc')).status).toBe('PENDING');
  });
});

describe('quote fee reporting', () => {
  const quoteBody = (feeCosts: any) => ({
    estimate: { toAmount: '1000', toAmountUSD: '1000', feeCosts },
    transactionRequest: { to: '0xr', data: '0x', value: '0x0', chainId: 42161, gasLimit: '1' },
  });

  // The displayed fee has to come from the route, not a constant: the rate is
  // configured at portal.li.fi and a hardcoded number silently goes stale.
  it('totals the route fees LiFi reports', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => quoteBody([
        { name: 'LIFI Fixed Fee', amountUSD: '0.0200' },
        { name: 'Integrator Fee', amountUSD: '3.0000' },
      ]),
    });
    const q = await getLiFiQuote({
      fromChain: 42161, toChain: 42161, fromToken: 'USDC', toToken: 'WETH',
      fromAmount: '1000000', fromAddress: '0xabc',
    });
    // Summed, not just the first entry — the integrator fee and LiFi's own
    // fixed fee are separate rows and the user pays both.
    expect(q.feeCostUSD).toBe('3.02');
  });

  it('reports no fee rather than zero when the route names none', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => quoteBody(undefined) });
    const q = await getLiFiQuote({
      fromChain: 42161, toChain: 42161, fromToken: 'USDC', toToken: 'WETH',
      fromAmount: '1000000', fromAddress: '0xabc',
    });
    expect(q.feeCostUSD).toBeUndefined();
  });
});
