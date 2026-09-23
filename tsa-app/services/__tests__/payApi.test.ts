// services/__tests__/payApi.test.ts
//
// Transport-level tests for the payer's checkout client (see payApi.ts).
// Follows the fetch + AsyncStorage mock pattern of paycrestApi.claim.test.ts:
// getCheckout/submitTestOutcome go through createApiRequest, which reads
// AsyncStorage via getAuthHeaders, so every call silently fails before fetch
// without this mock.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { getCheckout, isPaymentId, PayError, returnUrlFor, submitTestOutcome } from '../payApi';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
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
