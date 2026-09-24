// services/__tests__/apiRequest.test.ts
//
// The shared JSON-call helper extracted out of paycrestApi.ts. Covers the
// behaviour paycrestApi and payApi both depend on: auth header, an
// Idempotency-Key only on POST, envelope unwrapping, the 404/503
// "feature unavailable" fallback, and a caller-supplied error type that
// receives the whole body.
//
// The AsyncStorage mock is required: createApiRequest goes through
// getAuthHeaders (walletApi.ts), which reads AsyncStorage — see
// itStableApi.test.ts for the same requirement on itApi.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import { ApiError, createApiRequest } from '../apiRequest';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
});

describe('createApiRequest', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('adds an Idempotency-Key only on POST', async () => {
    const request = createApiRequest({
      prefix: '/pay',
      unavailableMessage: 'unavailable',
      networkMessage: 'network',
    });
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ ok: true }));

    await request('/checkout/1');
    let [, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(init.headers['Idempotency-Key']).toBeUndefined();

    await request('/checkout/1/test-outcome', { method: 'POST' });
    [, init] = (global.fetch as jest.Mock).mock.calls[1];
    expect(typeof init.headers['Idempotency-Key']).toBe('string');
    expect(init.headers['Idempotency-Key'].length).toBeGreaterThan(0);
  });

  it('unwraps the { success, data } envelope', async () => {
    const request = createApiRequest({
      prefix: '/pay',
      unavailableMessage: 'unavailable',
      networkMessage: 'network',
    });
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ id: 'abc' }));

    await expect(request('/checkout/abc')).resolves.toEqual({ id: 'abc' });
  });

  it('falls back to unavailableMessage on 404 and 503', async () => {
    const request = createApiRequest({
      prefix: '/pay',
      unavailableMessage: 'Payments are not available right now.',
      networkMessage: 'Could not reach the server.',
    });

    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 404,
      json: async () => ({ success: false }),
    });
    await expect(request('/checkout/abc')).rejects.toThrow('Payments are not available right now.');

    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 503,
      json: async () => ({ success: false }),
    });
    await expect(request('/checkout/abc')).rejects.toThrow('Payments are not available right now.');
  });

  it('falls back to networkMessage on any other failure without a server message', async () => {
    const request = createApiRequest({
      prefix: '/pay',
      unavailableMessage: 'Payments are not available right now.',
      networkMessage: 'Could not reach the server.',
    });
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 500,
      json: async () => ({ success: false }),
    });
    await expect(request('/checkout/abc')).rejects.toThrow('Could not reach the server.');
  });

  it('prefers the server message over either fallback', async () => {
    const request = createApiRequest({
      prefix: '/pay',
      unavailableMessage: 'unavailable',
      networkMessage: 'network',
    });
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ success: false, message: 'Someone else is completing this payment' }),
    });
    await expect(request('/checkout/abc')).rejects.toThrow('Someone else is completing this payment');
  });

  it('defaults to ApiError, carrying status and code', async () => {
    const request = createApiRequest({
      prefix: '/pay',
      unavailableMessage: 'unavailable',
      networkMessage: 'network',
    });
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 409,
      json: async () => ({ success: false, message: 'Conflict', code: 'pay_in_progress' }),
    });
    await expect(request('/checkout/abc')).rejects.toMatchObject({
      status: 409,
      code: 'pay_in_progress',
    });
    await expect(request('/checkout/abc')).rejects.toBeInstanceOf(ApiError);
  });

  it('uses makeError and hands it the whole body', async () => {
    class MyError extends Error {
      status: number;
      body: unknown;
      constructor(message: string, status: number, _code?: string, body?: unknown) {
        super(message);
        this.status = status;
        this.body = body;
      }
    }
    const request = createApiRequest({
      prefix: '/pay',
      unavailableMessage: 'unavailable',
      networkMessage: 'network',
      makeError: (message, status, code, body) => new MyError(message, status, code, body),
    });
    const body = { success: false, message: 'Nope', code: 'nope', extra: 'field' };
    (global.fetch as jest.Mock).mockResolvedValue({ ok: false, status: 400, json: async () => body });

    let caught: any;
    try {
      await request('/checkout/abc');
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(MyError);
    expect(caught.body).toEqual(body);
  });

  describe('a request with no answer', () => {
    const make = () => createApiRequest({ prefix: '/pay', unavailableMessage: 'unavailable', networkMessage: 'network' });

    it('a dropped connection is an ApiError coded transport, never the platform\'s own words', async () => {
      (global.fetch as jest.Mock).mockRejectedValue(new TypeError('Network request failed'));
      await expect(make()('/checkout/1/direct', { method: 'POST' })).rejects.toMatchObject({
        name: 'ApiError', message: 'network', status: 0, code: 'transport',
      });
    });

    it('a request that never answers is aborted at the timeout, as transport', async () => {
      jest.useFakeTimers();
      try {
        (global.fetch as jest.Mock).mockImplementation((_url: string, init: RequestInit) => new Promise((_resolve, reject) => {
          init.signal?.addEventListener('abort', () => reject(new Error('Aborted')));
        }));
        const pending = make()('/checkout/1/crypto', { method: 'POST' });
        const assertion = expect(pending).rejects.toMatchObject({ status: 0, code: 'transport' });
        await jest.advanceTimersByTimeAsync(30_000);
        await assertion;
      } finally {
        jest.useRealTimers();
      }
    });

    it('a success whose body is not our JSON is an error, not undefined data', async () => {
      (global.fetch as jest.Mock).mockResolvedValue({ ok: true, status: 200, json: async () => { throw new SyntaxError('html'); } });
      await expect(make()('/checkout/1/converted', { method: 'POST' })).rejects.toBeInstanceOf(ApiError);
    });
  });
});
