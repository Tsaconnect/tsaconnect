// services/__tests__/session.test.ts
//
// The session owner and the transport underneath every API client: the token
// is attached from one place, a 401 is refreshed once (shared by concurrent
// requests, because the refresh token rotates) and retried, and only a refresh
// the server refuses ends the session.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('@/services/localAuth', () => {
  let refresh: string | null = null;
  return {
    getRefreshToken: jest.fn(async () => refresh),
    storeRefreshToken: jest.fn(async (t: string) => { refresh = t; }),
    clearRefreshToken: jest.fn(async () => { refresh = null; }),
  };
});

import AsyncStorage from '@react-native-async-storage/async-storage';
import axios from 'axios';
import { API_BASE_URL } from '@/constants/api/config';
import { storeRefreshToken, getRefreshToken } from '@/services/localAuth';
import {
  SESSION_EXPIRED_MESSAGE,
  __resetSessionForTests,
  clearSession,
  endSession,
  getAccessToken,
  loadStoredSession,
  refreshSession,
  setSession,
  subscribeSession,
} from '../session';
import { attachSessionInterceptors, createSessionFetch, isSessionRequest } from '../sessionTransport';

const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const refreshed = (access: string, refresh = 'rt-2') =>
  json(200, { success: true, data: { accessToken: access, refreshToken: refresh } });

/** The Auth middleware's answer to a refused token — the only 401 that means "session gone". */
const refused = () => json(401, { success: false, message: 'Please authenticate' });

const authOf = (init?: RequestInit) => (init?.headers as Record<string, string> | undefined)?.Authorization;

let serverFetch: jest.Mock;

beforeEach(async () => {
  __resetSessionForTests();
  await AsyncStorage.clear();
  await storeRefreshToken('rt-1');
  serverFetch = jest.fn();
  // runRefresh calls the global fetch; the transport passes /auth/refresh straight through.
  (global as any).fetch = serverFetch;
});

describe('session storage', () => {
  it('stores the raw token under both keys, whatever format it arrives in', async () => {
    await setSession('Bearer abc');
    expect(getAccessToken()).toBe('abc');
    expect(await AsyncStorage.getItem('authToken')).toBe('abc');
    // Raw, never "Bearer …": the notifications WebSocket URL is built from this key,
    // and a space in it crashes Android's native WebSocket (prod crash 2026-09-28).
    expect(await AsyncStorage.getItem('token')).toBe('abc');
  });

  it('loads a token stored under the legacy key at cold start', async () => {
    await AsyncStorage.setItem('token', 'Bearer old');
    expect(await loadStoredSession()).toBe('old');
  });
});

describe('refreshSession', () => {
  it('shares one refresh between concurrent callers (the refresh token rotates)', async () => {
    await setSession('a1');
    serverFetch.mockResolvedValueOnce(refreshed('a2'));
    const [x, y] = await Promise.all([refreshSession(), refreshSession()]);
    expect(serverFetch).toHaveBeenCalledTimes(1);
    expect(x).toEqual({ kind: 'refreshed', accessToken: 'a2' });
    expect(y).toBe(x);
    expect(await getRefreshToken()).toBe('rt-2');
  });

  it('is rejected when the server refuses the refresh token', async () => {
    serverFetch.mockResolvedValueOnce(json(401, { success: false }));
    expect(await refreshSession()).toEqual({ kind: 'rejected' });
  });

  it('is unavailable — not rejected — on a network failure or server fault', async () => {
    serverFetch.mockRejectedValueOnce(new TypeError('Network request failed'));
    expect(await refreshSession()).toEqual({ kind: 'unavailable' });
    serverFetch.mockResolvedValueOnce(json(500, { success: false }));
    expect(await refreshSession()).toEqual({ kind: 'unavailable' });
  });
});

describe('createSessionFetch', () => {
  const api = `${API_BASE_URL}/users/profile`;

  it('attaches the current token, replacing whatever the caller sent', async () => {
    await setSession('current');
    serverFetch.mockResolvedValueOnce(json(200, { success: true }));
    await createSessionFetch(serverFetch)(api, { headers: { authorization: 'stale-raw-token' } });
    const [, init] = serverFetch.mock.calls[0];
    expect(init.headers).toEqual({ Authorization: 'Bearer current' });
  });

  it('removes a stale Authorization a screen still holds once signed out', async () => {
    serverFetch.mockResolvedValueOnce(json(200, { success: true }));
    await createSessionFetch(serverFetch)(api, { headers: { Authorization: 'Bearer old-user', 'x-a': '1' } });
    expect(serverFetch.mock.calls[0][1].headers).toEqual({ 'x-a': '1' });
  });

  it('only treats our exact API origin and path as the API', () => {
    expect(isSessionRequest(`${API_BASE_URL}/cart?x=1`)).toBe(true);
    expect(isSessionRequest('HTTPS://TSA.MCGPCHAIN.COM/api/cart')).toBe(true);
    for (const url of [
      'https://tsa.mcgpchain.com/api.evil.com/x',
      'https://tsa.mcgpchain.com/apix/cart',
      'https://tsa.mcgpchain.com/api/../admin',
      'https://tsa.mcgpchain.com/api/%2e%2e/admin',
      'https://tsa.mcgpchain.com@evil.com/api/cart',
      'https://tsa.mcgpchain.com.evil.com/api/cart',
      'http://tsa.mcgpchain.com/api/cart',
      '//evil.com/api/cart',
    ]) {
      expect([url, isSessionRequest(url)]).toEqual([url, false]);
    }
  });

  it('returns a wrong-password 401 untouched — no refresh, no second attempt against the lockout', async () => {
    await setSession('valid');
    const wrongPassword = json(401, { success: false, message: 'Incorrect password', code: 'password_incorrect' });
    serverFetch.mockResolvedValueOnce(wrongPassword);

    const res = await createSessionFetch(serverFetch)(`${API_BASE_URL}/pay/merchant/settlement`, { method: 'PUT' });

    expect(res).toBe(wrongPassword);
    expect(serverFetch).toHaveBeenCalledTimes(1);
    expect(getAccessToken()).toBe('valid');
  });

  it('does not replay a request as whoever signed in after it was sent', async () => {
    await setSession('user-a');
    serverFetch.mockImplementationOnce(async () => {
      await setSession('user-b'); // a different sign-in lands while A's request is out
      return refused();
    });

    const res = await createSessionFetch(serverFetch)(api);

    expect(res.status).toBe(401);
    expect(serverFetch).toHaveBeenCalledTimes(1);
  });

  it('leaves other hosts and the sign-in endpoints alone', async () => {
    await setSession('current');
    serverFetch.mockResolvedValue(json(401, { success: false }));
    const f = createSessionFetch(serverFetch);
    await f('https://li.quest/v1/quote', { headers: { 'x-a': '1' } });
    await f(`${API_BASE_URL}/auth/login`, { method: 'POST' });
    expect(authOf(serverFetch.mock.calls[0][1])).toBeUndefined();
    expect(authOf(serverFetch.mock.calls[1][1])).toBeUndefined();
    expect(isSessionRequest(`${API_BASE_URL}/auth/verify-otp`)).toBe(true);
    expect(isSessionRequest(`${API_BASE_URL}/auth/revoke`)).toBe(false); // sent with its own tokens
    expect(getAccessToken()).toBe('current');
  });

  it('refreshes once on a 401 and retries with the new token', async () => {
    await setSession('expired');
    serverFetch
      .mockResolvedValueOnce(refused())
      .mockResolvedValueOnce(refreshed('fresh'))
      .mockResolvedValueOnce(json(200, { success: true, data: { id: 1 } }));

    const res = await createSessionFetch(serverFetch)(api, { method: 'POST', body: '{}' });

    expect(res.status).toBe(200);
    expect(authOf(serverFetch.mock.calls[2][1])).toBe('Bearer fresh');
    expect(serverFetch.mock.calls[2][1].body).toBe('{}');
    expect(getAccessToken()).toBe('fresh');
  });

  it('expires the session once, however many requests hit the refused refresh', async () => {
    await setSession('expired');
    const events: string[] = [];
    subscribeSession((e) => events.push(e.type));
    serverFetch.mockImplementation(async (url: string) =>
      url.endsWith('/auth/refresh') ? json(401, { success: false }) : refused());

    const f = createSessionFetch(serverFetch);
    const [a, b] = await Promise.all([f(api), f(`${API_BASE_URL}/cart`)]);

    expect(events).toEqual(['expired']);
    expect(getAccessToken()).toBeNull();
    expect(await AsyncStorage.getItem('authToken')).toBeNull();
    expect(await getRefreshToken()).toBeNull();
    expect(a.status).toBe(401);
    expect((await a.json()).message).toBe(SESSION_EXPIRED_MESSAGE);
    expect(b.status).toBe(401);
  });

  it('keeps the session when the refresh could not be attempted (offline)', async () => {
    await setSession('maybe-expired');
    const original = refused();
    serverFetch.mockResolvedValueOnce(original).mockRejectedValueOnce(new TypeError('Network request failed'));

    const res = await createSessionFetch(serverFetch)(api);

    expect(res).toBe(original);
    expect(getAccessToken()).toBe('maybe-expired');
  });

  it('retries with a token another request already refreshed, without refreshing again', async () => {
    await setSession('old');
    let first = true;
    serverFetch.mockImplementation(async (url: string) => {
      if (url.endsWith('/auth/refresh')) return refreshed('newer');
      if (!first) return json(200, { success: true });
      first = false;
      await refreshSession(); // another request's refresh lands while this one is out
      return refused();
    });

    const res = await createSessionFetch(serverFetch)(api);

    expect(res.status).toBe(200);
    expect(serverFetch.mock.calls.filter(([url]) => url.endsWith('/auth/refresh'))).toHaveLength(1);
    expect(authOf(serverFetch.mock.calls.at(-1)[1])).toBe('Bearer newer');
  });

  it('passes through untouched when signed out', async () => {
    serverFetch.mockResolvedValueOnce(json(401, { success: false }));
    const res = await createSessionFetch(serverFetch)(api, { headers: { 'x-a': '1' } });
    expect(res.status).toBe(401);
    expect(serverFetch).toHaveBeenCalledTimes(1);
    expect(serverFetch.mock.calls[0][1].headers).toEqual({ 'x-a': '1' });
  });
});

describe('session generations', () => {
  it('a refresh that lands after sign-out does not bring the session back, and revokes what it got', async () => {
    await setSession('a1');
    let answerRefresh!: (r: Response) => void;
    serverFetch.mockImplementation((url: string) =>
      url.endsWith('/auth/refresh')
        ? new Promise<Response>((resolve) => { answerRefresh = resolve; })
        : Promise.resolve(json(200, { success: true })));

    const pending = refreshSession();
    await clearSession({ forgetRefreshToken: false }); // the user taps Log out meanwhile
    answerRefresh(refreshed('a2', 'rt-2'));

    expect(await pending).toEqual({ kind: 'unavailable' });
    expect(getAccessToken()).toBeNull();
    expect(await AsyncStorage.getItem('authToken')).toBeNull();
    const revoke = serverFetch.mock.calls.find(([url]) => url.endsWith('/auth/revoke'));
    expect(JSON.parse(revoke[1].body)).toEqual({ refreshToken: 'rt-2' });
    expect(authOf(revoke[1])).toBe('Bearer a2');
  });

  it('gives up on a refresh that never answers', async () => {
    jest.useFakeTimers();
    try {
      await setSession('a1');
      serverFetch.mockImplementation((_url: string, init: RequestInit) =>
        new Promise((_resolve, reject) =>
          init.signal?.addEventListener('abort', () => reject(new Error('aborted')))));
      const pending = refreshSession();
      await jest.advanceTimersByTimeAsync(31_000);
      expect(await pending).toEqual({ kind: 'unavailable' });
    } finally {
      jest.useRealTimers();
    }
  });

  it('treats an unreadable keychain as unavailable, not as a refused session', async () => {
    await setSession('a1');
    (getRefreshToken as jest.Mock).mockRejectedValueOnce(new Error('User interaction is not allowed'));
    expect(await refreshSession()).toEqual({ kind: 'unavailable' });
    expect(getAccessToken()).toBe('a1');
  });
});

describe('endSession (sign out of this device)', () => {
  it('signs out locally at once and revokes the refresh token it held', async () => {
    await setSession('a1');
    await AsyncStorage.setItem('userId', 'u1');
    serverFetch.mockResolvedValue(json(200, { success: true }));

    await endSession();

    expect(getAccessToken()).toBeNull();
    expect(await getRefreshToken()).toBeNull();
    expect(await AsyncStorage.getItem('userId')).toBeNull();
    await new Promise((r) => setImmediate(r));
    const [url, init] = serverFetch.mock.calls[0];
    expect(url).toBe(`${API_BASE_URL}/auth/revoke`);
    expect(JSON.parse(init.body)).toEqual({ refreshToken: 'rt-1' });
  });

  it('with an expired access token, revokes the rotated refresh token, not the one it replaced', async () => {
    await setSession('expired');
    serverFetch.mockImplementation(async (url: string, init: RequestInit) => {
      if (url.endsWith('/auth/refresh')) return refreshed('a2', 'rt-2');
      return authOf(init) === 'Bearer a2' ? json(200, { success: true }) : refused();
    });

    await endSession();
    await new Promise((r) => setImmediate(r));
    await new Promise((r) => setImmediate(r));

    const revokes = serverFetch.mock.calls.filter(([url]) => url.endsWith('/auth/revoke'));
    expect(JSON.parse(revokes.at(-1)[1].body)).toEqual({ refreshToken: 'rt-2' });
    expect(authOf(revokes.at(-1)[1])).toBe('Bearer a2');
    // The rotation stored nothing: the device stays signed out.
    expect(getAccessToken()).toBeNull();
    expect(await getRefreshToken()).toBeNull();
  });
});

describe('attachSessionInterceptors (axios)', () => {
  function instanceAnswering(answers: Array<{ status: number; data: unknown }>) {
    const instance = axios.create({ baseURL: API_BASE_URL });
    const seen: string[] = [];
    instance.defaults.adapter = async (config) => {
      seen.push(String(config.headers.Authorization));
      const next = answers.shift()!;
      const response = { data: next.data, status: next.status, statusText: '', headers: {}, config };
      if (next.status >= 400) {
        const err: any = new axios.AxiosError('fail', undefined, config, undefined, response as any);
        throw err;
      }
      return response as any;
    };
    attachSessionInterceptors(instance);
    return { instance, seen };
  }

  it('attaches the token and retries once after a refresh', async () => {
    await setSession('expired');
    serverFetch.mockResolvedValueOnce(refreshed('fresh'));
    const { instance, seen } = instanceAnswering([
      { status: 401, data: { success: false, message: 'Please authenticate' } },
      { status: 200, data: { success: true } },
    ]);

    const res = await instance.get('/cart', { headers: { Authorization: 'Bearer something-else' } });

    expect(res.status).toBe(200);
    expect(seen).toEqual(['Bearer expired', 'Bearer fresh']);
  });

  it('never attaches the token to a request axios resolves to another host', async () => {
    await setSession('secret');
    const { instance, seen } = instanceAnswering([
      { status: 200, data: {} },
      { status: 200, data: {} },
    ]);
    await instance.get('HTTPS://evil.example/x');
    await instance.get('//evil.example/x');
    expect(seen).toEqual(['undefined', 'undefined']);
  });

  it('turns a refused refresh into a session-expired error', async () => {
    await setSession('expired');
    serverFetch.mockResolvedValueOnce(json(401, { success: false }));
    const { instance } = instanceAnswering([{ status: 401, data: { message: 'Please authenticate' } }]);

    await expect(instance.get('/cart')).rejects.toMatchObject({
      response: { status: 401, data: { message: SESSION_EXPIRED_MESSAGE } },
    });
    expect(getAccessToken()).toBeNull();
  });
});
