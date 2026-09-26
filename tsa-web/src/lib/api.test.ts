import { json } from '@/test/harness';
import { api, ApiError, onSessionExpired } from './api';

const unauthorized = () => json(401, { success: false, message: 'Please authenticate' });

describe('api', () => {
  let fetchMock: ReturnType<typeof vi.fn>;
  beforeEach(() => { fetchMock = vi.fn(); vi.stubGlobal('fetch', fetchMock); });
  afterEach(() => vi.unstubAllGlobals());

  it('sends same-origin credentials and the CSRF header, and unwraps data', async () => {
    fetchMock.mockResolvedValue(json(200, { success: true, data: { ok: 1 } }));
    await expect(api('/pay/merchant', { method: 'PUT', body: { a: 1 } })).resolves.toEqual({ ok: 1 });
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/pay/merchant');
    expect(init.method).toBe('PUT');
    expect(init.credentials).toBe('same-origin');
    expect(init.headers['X-TSA-Web']).toBe('1');
    expect(init.body).toBe('{"a":1}');
  });

  it('sends extra headers alongside, never instead of, the CSRF header', async () => {
    fetchMock.mockResolvedValue(json(201, { success: true, data: {} }));
    await api('/x', { method: 'POST', headers: { 'Idempotency-Key': 'k1', 'X-TSA-Web': '0' } });
    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers['Idempotency-Key']).toBe('k1');
    expect(init.headers['X-TSA-Web']).toBe('1');
  });

  it('throws an ApiError with the server message and code', async () => {
    fetchMock.mockResolvedValue(json(422, { success: false, message: "That code isn't right", code: 'code_invalid' }));
    const err = await api('/x', { method: 'POST' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, code: 'code_invalid', message: "That code isn't right" });
  });

  // A guarded change answers a wrong password with 401 — the session itself is fine.
  it('a wrong password (401 password_incorrect) is a refusal, not an expired session', async () => {
    const expired = vi.fn();
    const off = onSessionExpired(expired);
    fetchMock.mockResolvedValue(json(401, { success: false, message: 'Incorrect password', code: 'password_incorrect' }));
    const err = await api('/pay/merchant/keys', { method: 'POST', body: {} }).catch((e: unknown) => e);
    off();
    expect(err).toMatchObject({ status: 401, code: 'password_incorrect' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(expired).not.toHaveBeenCalled();
  });

  // The access cookie expired mid-action: refresh, retry — and the retry is the refusal.
  it('a refusal on the retry after a refresh is thrown, not a signed-out session', async () => {
    const expired = vi.fn();
    const off = onSessionExpired(expired);
    fetchMock
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(json(200, { success: true, data: { user: {} } }))
      .mockResolvedValueOnce(json(401, { success: false, message: 'Incorrect password', code: 'password_incorrect' }));
    const err = await api('/pay/merchant/keys', { method: 'POST', body: {} }).catch((e: unknown) => e);
    off();
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 401, code: 'password_incorrect' });
    expect(fetchMock.mock.calls.map(([u]) => u)).toEqual(['/api/pay/merchant/keys', '/api/auth/web/refresh', '/api/pay/merchant/keys']);
    expect(expired).not.toHaveBeenCalled();
  });

  it('a 401 carrying any code never refreshes', async () => {
    const expired = vi.fn();
    const off = onSessionExpired(expired);
    fetchMock.mockResolvedValue(json(401, { success: false, message: 'Nope', code: 'some_future_refusal' }));
    const err = await api('/pay/merchant/webhook-secret', { method: 'POST', body: {} }).catch((e: unknown) => e);
    off();
    expect(err).toMatchObject({ status: 401, code: 'some_future_refusal' });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls.some(([u]) => u === '/api/auth/web/refresh')).toBe(false);
    expect(expired).not.toHaveBeenCalled();
  });

  it('a network failure becomes a readable transport error', async () => {
    fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
    await expect(api('/x')).rejects.toMatchObject({ status: 0, code: 'transport' });
  });

  // Review Focus 2.
  it('on 401 refreshes once and retries', async () => {
    fetchMock
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(json(200, { success: true, data: { user: {} } }))
      .mockResolvedValueOnce(json(200, { success: true, data: { ok: 2 } }));
    await expect(api('/pay/merchant')).resolves.toEqual({ ok: 2 });
    expect(fetchMock.mock.calls.map(([u]) => u)).toEqual(['/api/pay/merchant', '/api/auth/web/refresh', '/api/pay/merchant']);
    expect(fetchMock.mock.calls[1][1].method).toBe('POST');
  });

  it('/auth/web/me refreshes once on 401', async () => {
    fetchMock
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(json(200, { success: true, data: { user: {} } }))
      .mockResolvedValueOnce(json(200, { success: true, data: { user: { id: 'u1' } } }));
    await expect(api('/auth/web/me')).resolves.toEqual({ user: { id: 'u1' } });
    expect(fetchMock.mock.calls[1][0]).toBe('/api/auth/web/refresh');
  });

  it.each(['/auth/web/login', '/auth/web/verify', '/auth/web/refresh', '/auth/web/logout'])('%s never triggers a refresh', async (path) => {
    const expired = vi.fn();
    const off = onSessionExpired(expired);
    fetchMock.mockResolvedValue(unauthorized());
    await expect(api(path, { method: 'POST' })).rejects.toMatchObject({ status: 401 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(expired).not.toHaveBeenCalled();
    off();
  });

  it('a failed refresh ends the session instead of looping', async () => {
    const expired = vi.fn();
    const off = onSessionExpired(expired);
    fetchMock.mockResolvedValue(unauthorized());
    await expect(api('/pay/merchant')).rejects.toBeInstanceOf(ApiError);
    // request, refresh, one retry — then stop.
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(expired).toHaveBeenCalledTimes(1);
    off();
  });

  // A refresh that lost a race with a concurrent one gets 401, but the winner
  // already left fresh cookies in the browser: the retry must still be tried.
  it('a lost refresh race still retries, and a successful retry keeps the session', async () => {
    const expired = vi.fn();
    const off = onSessionExpired(expired);
    fetchMock
      .mockResolvedValueOnce(unauthorized())
      .mockResolvedValueOnce(json(401, { success: false, message: 'Please sign in again', code: 'session_expired' }))
      .mockResolvedValueOnce(json(200, { success: true, data: { ok: 3 } }));
    await expect(api('/pay/merchant')).resolves.toEqual({ ok: 3 });
    expect(expired).not.toHaveBeenCalled();
    off();
  });

  it('a silent 401 does not end the session', async () => {
    const expired = vi.fn();
    const off = onSessionExpired(expired);
    fetchMock.mockResolvedValue(unauthorized());
    await expect(api('/auth/web/me', { silent: true })).rejects.toMatchObject({ status: 401 });
    expect(expired).not.toHaveBeenCalled();
    off();
  });

  it('requests that hit 401 together share one refresh', async () => {
    fetchMock.mockImplementation(async (url: string) => {
      if (url === '/api/auth/web/refresh') return json(200, { success: true, data: {} });
      return fetchMock.mock.calls.filter(([u]) => u === url).length === 1 ? unauthorized() : json(200, { success: true, data: url });
    });
    await expect(Promise.all([api('/a'), api('/b')])).resolves.toEqual(['/api/a', '/api/b']);
    expect(fetchMock.mock.calls.filter(([u]) => u === '/api/auth/web/refresh')).toHaveLength(1);
  });

  describe("the caller's abort signal", () => {
    // fetch that only settles when its signal aborts, like a slow request.
    // Like real fetch, an already-aborted signal rejects at once.
    const hang = () => fetchMock.mockImplementation((_url: string, init: RequestInit) => new Promise((_, reject) => {
      const aborted = () => reject(new DOMException('The operation was aborted.', 'AbortError'));
      if (init.signal!.aborted) aborted();
      else init.signal!.addEventListener('abort', aborted);
    }));

    it('cancels a request in flight (AbortSignal.any)', async () => {
      hang();
      const caller = new AbortController();
      const pending = api('/x', { signal: caller.signal });
      caller.abort();
      await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
    });

    it('cancels a request in flight where AbortSignal.any is missing', async () => {
      const any = AbortSignal.any;
      Reflect.deleteProperty(AbortSignal, 'any');
      try {
        expect(typeof AbortSignal.any).toBe('undefined');
        hang();
        const caller = new AbortController();
        const pending = api('/x', { signal: caller.signal });
        caller.abort();
        await expect(pending).rejects.toMatchObject({ name: 'AbortError' });
        // an already-aborted signal is honoured too
        const done = new AbortController();
        done.abort();
        await expect(api('/y', { signal: done.signal })).rejects.toMatchObject({ name: 'AbortError' });
      } finally {
        AbortSignal.any = any;
      }
    });
  });

  describe('the timeout', () => {
    beforeEach(() => vi.useFakeTimers());
    afterEach(() => vi.useRealTimers());

    // Like real fetch: headers arrive, then the body never finishes until the signal aborts it.
    const stalledBody = () => fetchMock.mockImplementation(async (_url: string, init: RequestInit) => new Response(
      new ReadableStream({
        start(c) {
          init.signal!.addEventListener('abort', () => c.error(new DOMException('The operation was aborted.', 'AbortError')));
        },
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    ));

    it('still runs while the body is read, so a stalled body times out', async () => {
      stalledBody();
      const pending = api('/x').catch((e: unknown) => e);
      await vi.advanceTimersByTimeAsync(30_000);
      expect(await pending).toMatchObject({ status: 0, code: 'transport' });
    });

    it("holds the caller's abort until the body is read (no AbortSignal.any)", async () => {
      const any = AbortSignal.any;
      Reflect.deleteProperty(AbortSignal, 'any');
      try {
        stalledBody();
        const caller = new AbortController();
        const remove = vi.spyOn(caller.signal, 'removeEventListener');
        const pending = api('/x', { signal: caller.signal }).catch((e: unknown) => e);
        await vi.advanceTimersByTimeAsync(0);
        expect(remove).not.toHaveBeenCalled(); // headers are in, the body is not
        caller.abort();
        expect(await pending).toMatchObject({ name: 'AbortError' });
        expect(remove).toHaveBeenCalledWith('abort', expect.any(Function));
      } finally {
        AbortSignal.any = any;
      }
    });
  });
});
