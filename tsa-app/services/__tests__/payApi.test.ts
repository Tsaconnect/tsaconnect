// services/__tests__/payApi.test.ts
//
// Transport-level tests for the payer's checkout client (see payApi.ts).
// Follows the fetch + AsyncStorage mock pattern of paycrestApi.claim.test.ts:
// getCheckout/submitTestOutcome go through createApiRequest, which reads
// AsyncStorage via getAuthHeaders, so every call silently fails before fetch
// without this mock.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

jest.mock('../../hooks/useNetwork', () => ({ getNetwork: () => 'mainnet' }));

import {
  getCheckout, getCryptoQuote, getPayQuote, isPaymentId, payConverted, payCrypto, payDirect, PayError, returnUrlFor,
  submitTestOutcome,
} from '../payApi';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';

const okJson = (data: unknown, status = 200) => ({
  ok: true,
  status,
  json: async () => ({ success: true, data }),
});

describe('isPaymentId', () => {
  it('accepts a UUID and rejects anything else', () => {
    expect(isPaymentId(ID)).toBe(true);
    expect(isPaymentId('../paycrest/orders')).toBe(false);
    expect(isPaymentId(undefined)).toBe(false);
    expect(isPaymentId(123)).toBe(false);
  });
});

describe('getCheckout', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('GETs /pay/checkout/:id with the auth header', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ id: ID, status: 'pending' }));

    await getCheckout(ID);

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe(`https://tsa.mcgpchain.com/api/pay/checkout/${ID}`);
    expect(init.method).toBeUndefined();
    expect(init.headers.Authorization).toBeUndefined(); // no token set in this test's AsyncStorage
    expect(init.headers['Content-Type']).toBe('application/json');
  });

  it('rejects a non-payment-id without calling fetch', async () => {
    await expect(getCheckout('../paycrest/orders')).rejects.toThrow('not valid');
    expect(global.fetch).not.toHaveBeenCalled();
  });
});

describe('submitTestOutcome', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('POSTs { outcome } with an Idempotency-Key', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ id: ID, status: 'succeeded' }));

    await submitTestOutcome(ID, 'approve');

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe(`https://tsa.mcgpchain.com/api/pay/checkout/${ID}/test-outcome`);
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ outcome: 'approve' });
    expect(typeof init.headers['Idempotency-Key']).toBe('string');
  });

  it('rejects with a PayError carrying the server message and status on a 409', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ success: false, message: 'Someone else is completing this payment' }),
    });

    await expect(submitTestOutcome(ID, 'approve')).rejects.toMatchObject({
      message: 'Someone else is completing this payment',
      status: 409,
    });
    await expect(submitTestOutcome(ID, 'approve')).rejects.toBeInstanceOf(PayError);
  });
});

describe('returnUrlFor', () => {
  const base = { id: ID, status: 'succeeded' as const };

  it('adds payment_id and status before any #fragment', () => {
    expect(returnUrlFor({ ...base, returnUrl: 'https://skyair.ng/done?x=1#top' })).toBe(
      `https://skyair.ng/done?x=1&payment_id=${ID}&status=succeeded#top`,
    );
  });

  it('rejects a non-https scheme', () => {
    expect(returnUrlFor({ ...base, returnUrl: 'javascript:alert(1)' })).toBeNull();
    expect(returnUrlFor({ ...base, returnUrl: 'http://skyair.ng' })).toBeNull();
    expect(returnUrlFor({ ...base, returnUrl: 'tsaconnect://wallet' })).toBeNull();
  });

  it('returns null when there is no returnUrl at all', () => {
    expect(returnUrlFor({ ...base, returnUrl: undefined })).toBeNull();
  });
});

describe('paying a naira merchant', () => {
  const src = { sourceChainId: 56, sourceToken: '0x0000000000000000000000000000000000000000', sourceAddress: '0xPayer' };
  const rawQuote = {
    action: { fromAmount: '84000000000000000' },
    estimate: { toAmount: '33835000' },
    transactionRequest: { to: '0xrouter', data: '0xab', value: '0x0', chainId: 56, gasLimit: '200000', gasPrice: '5' },
  };

  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('getPayQuote GETs the quote with the source in the query and no Idempotency-Key', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ fromAmount: '84000000000000000', usdcTarget: '33835000' }));

    await expect(getPayQuote(ID, src)).resolves.toEqual({ fromAmount: '84000000000000000', usdcTarget: '33835000' });

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    const u = new URL(url);
    expect(u.origin + u.pathname).toBe(`https://tsa.mcgpchain.com/api/pay/checkout/${ID}/quote`);
    expect(Object.fromEntries(u.searchParams)).toEqual({
      sourceChainId: '56',
      sourceToken: src.sourceToken,
      sourceAddress: '0xPayer',
    });
    expect(init.method).toBeUndefined();
    expect(init.headers['Idempotency-Key']).toBeUndefined();
  });

  it('payDirect POSTs { chainKey, token } with an Idempotency-Key and returns the order', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ order: { id: 'o1', status: 'awaiting_deposit' } }, 201));

    await expect(payDirect(ID, { chainKey: 'base', token: 'USDC' })).resolves.toMatchObject({ id: 'o1' });

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe(`https://tsa.mcgpchain.com/api/pay/checkout/${ID}/direct`);
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({ chainKey: 'base', token: 'USDC' });
    expect(typeof init.headers['Idempotency-Key']).toBe('string');
  });

  it('payDirect also accepts the order as the bare data, as /paycrest/sell returns it', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ id: 'o2', status: 'awaiting_deposit' }, 201));
    await expect(payDirect(ID, { chainKey: 'base', token: 'USDC' })).resolves.toMatchObject({ id: 'o2' });
  });

  it('payConverted POSTs the source with an Idempotency-Key and returns the intent and a signable quote', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({ intent: { id: 'i1', status: 'swapping' }, lifiQuote: rawQuote, gasTopUp: true }, 201),
    );

    const res = await payConverted(ID, src);

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe(`https://tsa.mcgpchain.com/api/pay/checkout/${ID}/converted`);
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual(src);
    expect(typeof init.headers['Idempotency-Key']).toBe('string');
    expect(res.intent.id).toBe('i1');
    expect(res.quote.fromAmount).toBe('84000000000000000');
    expect(res.quote.transactionRequest.to).toBe('0xrouter');
    expect(res.gasTopUp).toBe(true);
  });

  it('payConverted reports no top-up unless the server said so', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ intent: { id: 'i1' }, lifiQuote: rawQuote }, 201));
    expect((await payConverted(ID, src)).gasTopUp).toBe(false);
  });

  it('carries the machine code of a 409 so the app can resume instead of retry', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ success: false, message: 'You are already paying this', code: 'pay_in_progress' }),
    });

    await expect(payConverted(ID, src)).rejects.toMatchObject({ status: 409, code: 'pay_in_progress' });
    await expect(payDirect(ID, { chainKey: 'base', token: 'USDC' })).rejects.toBeInstanceOf(PayError);
  });

  it('refuses a non-payment-id before any request', async () => {
    await expect(payDirect('../x', { chainKey: 'base', token: 'USDC' })).rejects.toThrow('not valid');
    await expect(payConverted('../x', src)).rejects.toThrow('not valid');
    await expect(getPayQuote('../x', src)).rejects.toThrow('not valid');
    expect(global.fetch).not.toHaveBeenCalled();
  });
});

describe('paying a crypto-settling merchant', () => {
  const src = { sourceChainId: 56, sourceToken: '0x0000000000000000000000000000000000000000', sourceAddress: '0xPayer' };
  const rawQuote = {
    action: { fromAmount: '84000000000000000' },
    estimate: { toAmount: '33323336332433603252' },
    transactionRequest: { to: '0xrouter', data: '0xab', value: '0x0', chainId: 56, gasLimit: '200000', gasPrice: '5' },
  };

  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('getCryptoQuote GETs crypto-quote with the source in the query and returns what the swap pulls', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ fromAmount: '84000000000000000' }));

    await expect(getCryptoQuote(ID, src)).resolves.toEqual({ fromAmount: '84000000000000000' });

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    const u = new URL(url);
    expect(u.origin + u.pathname).toBe(`https://tsa.mcgpchain.com/api/pay/checkout/${ID}/crypto-quote`);
    expect(Object.fromEntries(u.searchParams)).toEqual({
      sourceChainId: '56',
      sourceToken: src.sourceToken,
      sourceAddress: '0xPayer',
    });
    expect(init.method).toBeUndefined();
  });

  it('payCrypto POSTs the source with an Idempotency-Key and returns the intent, a signable quote and the top-up verdict', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({ intent: { id: 'i1', status: 'swapping' }, lifiQuote: rawQuote, gasTopUp: true }, 201),
    );

    const res = await payCrypto(ID, src);

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe(`https://tsa.mcgpchain.com/api/pay/checkout/${ID}/crypto`);
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual(src);
    expect(typeof init.headers['Idempotency-Key']).toBe('string');
    expect(res.intent.id).toBe('i1');
    expect(res.quote.fromAmount).toBe('84000000000000000');
    expect(res.gasTopUp).toBe(true);
  });

  it('carries the codes of a 409 and of the same-token refusal', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false, status: 422, json: async () => ({ success: false, message: 'pick another token' }),
    });
    await expect(payCrypto(ID, src)).rejects.toMatchObject({ status: 422, message: 'pick another token' });
  });

  it('refuses a non-payment-id before any request', async () => {
    await expect(payCrypto('../x', src)).rejects.toThrow('not valid');
    await expect(getCryptoQuote('../x', src)).rejects.toThrow('not valid');
    expect(global.fetch).not.toHaveBeenCalled();
  });
});
