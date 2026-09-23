/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

// services/__tests__/paycrestSellSwap.test.ts
//
// Transport for the swapped sell (design §5.1): what each call sends and where.
// The one rule that matters most here is what the intent does NOT send — the
// server re-quotes and keeps its own figures, so a client-supplied number
// would only be something to distrust.
//
// The AsyncStorage mock is required: paycrestRequest goes through
// getAuthHeaders, which reads AsyncStorage.
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import {
  abandonSellIntent, createSellIntent, getOpenSellIntent, getSellSwapQuote, getSwappedSellState, openSellOrder,
  recordSellSwap, requestSellIntentGasTopUp,
} from '../paycrestSellSwap';

const okJson = (data: unknown, status = 200) => ({
  ok: true,
  status,
  json: async () => ({ success: true, data }),
  text: async () => JSON.stringify({ success: true, data }),
});

// A minimal raw LiFi quote body, as the server passes it through.
const rawQuoteFixture = {
  estimate: { toAmount: '9950000', toAmountUSD: '9.95', gasCosts: [{ amount: '21000000000000', amountUSD: '0.01' }] },
  tool: '1inch',
  transactionRequest: {
    to: '0xrouter', data: '0xdeadbeef', value: '0x0', chainId: 56, gasLimit: '200000', gasPrice: '5000000000',
  },
};

const API = 'https://tsa.mcgpchain.com/api/paycrest';

beforeEach(() => {
  (global as any).fetch = jest.fn();
});

it('creates an intent with only the source and bank — never quote figures', async () => {
  (global.fetch as jest.Mock).mockResolvedValueOnce(
    okJson({ intent: { id: 'i1', status: 'swapping' }, lifiQuote: rawQuoteFixture }, 201));
  const res = await createSellIntent({
    sourceChainId: 56, sourceToken: '0x0000000000000000000000000000000000000000',
    sourceAmount: '1000', sourceAddress: '0xUser', bankCode: '058', accountNumber: '0123456789',
  });
  const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
  expect(url).toBe(`${API}/sell-intents`);
  expect(init.method).toBe('POST');
  // Starting a sale must not double-fire: paycrestRequest's idempotency key.
  expect(init.headers['Idempotency-Key']).toBeTruthy();
  const body = JSON.parse(init.body);
  expect(body).toMatchObject({ sourceChainId: 56, sourceAmount: '1000', sourceAddress: '0xUser', bankCode: '058' });
  expect(body).not.toHaveProperty('quotedUsdc');
  expect(body).not.toHaveProperty('quotedFiat');
  expect(body).not.toHaveProperty('feeBps');
  expect(res.intent.id).toBe('i1');
  // The raw body comes back as the app's own quote shape, ready to sign.
  expect(res.quote.transactionRequest).toBeDefined();
  expect(res.quote.gasCostWei).toBe('21000000000000');
});

it('reports a swap hash, then opens the order', async () => {
  (global.fetch as jest.Mock).mockResolvedValue(okJson({}));
  await recordSellSwap('i1', '0xswap');
  await openSellOrder('i1');
  const calls = (global.fetch as jest.Mock).mock.calls;
  expect(calls.map((c) => c[0])).toEqual([`${API}/sell-intents/i1/swap`, `${API}/sell-intents/i1/order`]);
  expect(JSON.parse(calls[0][1].body)).toEqual({ txHash: '0xswap' });
  expect(calls.every((c) => c[1].method === 'POST')).toBe(true);
});

it('returns null when there is no open sale', async () => {
  (global.fetch as jest.Mock).mockResolvedValueOnce(okJson(null));
  await expect(getOpenSellIntent()).resolves.toBeNull();
  // The server wraps it: {intent: null} is the same answer.
  (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ intent: null }));
  await expect(getOpenSellIntent()).resolves.toBeNull();
});

it('returns the open sale when there is one', async () => {
  (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ intent: { id: 'i1', status: 'swapped', swapTxHash: '0xs' } }));
  await expect(getOpenSellIntent()).resolves.toMatchObject({ id: 'i1', status: 'swapped' });
  expect((global.fetch as jest.Mock).mock.calls[0][0]).toBe(`${API}/sell-intents/open`);
});

it('quotes with the source leg and her EVM wallet, and maps the LiFi quote', async () => {
  (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({
    lifiQuote: rawQuoteFixture, costBps: 12, gasBps: 3, feeBps: 35, usdcAmount: '9.90', fiatEstimate: '15000', rate: '1515',
  }));
  const q = await getSellSwapQuote({
    fromChainId: 56, fromToken: '0xabc', fromAmount: '1000', fromAddress: '0xUser', walletAddress: '0xUser',
  });
  const url: string = (global.fetch as jest.Mock).mock.calls[0][0];
  expect(url.startsWith(`${API}/sell-swap-quote?`)).toBe(true);
  const qs = new URLSearchParams(url.split('?')[1]);
  expect(Object.fromEntries(qs)).toEqual({
    fromChainId: '56', fromToken: '0xabc', fromAmount: '1000', fromAddress: '0xUser', walletAddress: '0xUser', currency: 'NGN',
  });
  expect(q).toMatchObject({ fiatEstimate: '15000', feeBps: 35, usdcAmount: '9.90' });
  expect(q.quote.transactionRequest.to).toBe('0xrouter');
});

it('asks for a gas top-up and abandons through their own routes', async () => {
  (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ txHash: '0xgas', skipped: false }));
  await expect(requestSellIntentGasTopUp('i1')).resolves.toEqual({ txHash: '0xgas', skipped: false });
  (global.fetch as jest.Mock).mockResolvedValueOnce(okJson(null));
  await abandonSellIntent('i1');
  expect((global.fetch as jest.Mock).mock.calls.map((c) => c[0])).toEqual([
    `${API}/sell-intents/i1/gas-topup`, `${API}/sell-intents/i1/abandon`,
  ]);
});

// The server's own verdict on source-chain gas rides on the Create answer; an
// older server that sends none reads as "no top-up priced", never as "yes".
it("carries the server's gas top-up verdict from Create", async () => {
  const body = (gasTopUp?: boolean) => okJson({
    intent: { id: 'i1', status: 'swapping' }, lifiQuote: rawQuoteFixture, ...(gasTopUp === undefined ? {} : { gasTopUp }),
  }, 201);
  const input = {
    sourceChainId: 56, sourceToken: '0xabc', sourceAmount: '1000', sourceAddress: '0xUser', bankCode: '058', accountNumber: '0123456789',
  };
  (global.fetch as jest.Mock).mockResolvedValueOnce(body(true));
  expect((await createSellIntent(input)).gasTopUp).toBe(true);
  (global.fetch as jest.Mock).mockResolvedValueOnce(body(false));
  expect((await createSellIntent(input)).gasTopUp).toBe(false);
  (global.fetch as jest.Mock).mockResolvedValueOnce(body());
  expect((await createSellIntent(input)).gasTopUp).toBe(false);
});

describe('whether swapped sells are on at all', () => {
  const status = (code: number, message: string) => ({
    ok: false, status: code,
    json: async () => ({ success: false, message }),
    text: async () => JSON.stringify({ success: false, message }),
  });

  it('reads a 404 from /sell-intents/open as "switched off", not as an outage', async () => {
    // Gin's own 404 for an unregistered route carries no JSON body at all.
    (global.fetch as jest.Mock).mockResolvedValueOnce({
      ok: false, status: 404, json: async () => { throw new Error('not json'); }, text: async () => '404 page not found',
    });
    await expect(getSwappedSellState()).resolves.toEqual({ enabled: false, intent: null });
    // The handler's own "not available" 404 means the same.
    (global.fetch as jest.Mock).mockResolvedValueOnce(status(404, "Selling this token isn't available"));
    await expect(getSwappedSellState()).resolves.toEqual({ enabled: false, intent: null });
  });

  it('reports "on" with her sale, or none', async () => {
    (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ intent: { id: 'i1', status: 'ordered' } }));
    await expect(getSwappedSellState()).resolves.toMatchObject({ enabled: true, intent: { id: 'i1' } });
    (global.fetch as jest.Mock).mockResolvedValueOnce(okJson({ intent: null }));
    await expect(getSwappedSellState()).resolves.toEqual({ enabled: true, intent: null });
  });

  it('says nothing either way on any other failure', async () => {
    (global.fetch as jest.Mock).mockResolvedValueOnce(status(500, 'boom'));
    await expect(getSwappedSellState()).rejects.toMatchObject({ status: 500 });
  });
});
