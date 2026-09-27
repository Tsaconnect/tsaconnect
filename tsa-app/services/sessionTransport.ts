// services/sessionTransport.ts
//
// Puts every request to our API under the session in services/session.ts,
// whichever client sends it. The app talks to the API through ~180 raw
// fetch() calls, the api.ts singleton, a shared axios instance and bare axios
// calls — each with its own idea of where the token lives and in what format.
// Rather than rewrite every call site, this sits underneath all of them:
//
//  - It sets the Authorization header of every API request from the session —
//    replacing whatever the caller had, and removing it when signed out — so a
//    stale, raw or doubly-prefixed token held by a screen can't reach the server.
//  - When the auth middleware refuses the token, it refreshes (once, shared
//    across concurrent requests) and retries the request once. The API's
//    Idempotency middleware runs after auth and never stores a non-2xx reply,
//    so re-sending a POST with the same Idempotency-Key is safe.
//  - When the server refuses the refresh, the session is expired (AuthContext
//    drops to the sign-in screens) and the caller gets a 401 that says so,
//    instead of the bare "Please authenticate".
//
// Only the auth middleware's own 401 means "the session is gone". Handlers
// answer 401 for other things on a perfectly good session — a wrong password
// on a re-authentication (pay settings, delete account), counted toward the
// login lockout — and those must reach the caller untouched: retrying would
// count the wrong password twice.
import type { AxiosError, AxiosInstance, InternalAxiosRequestConfig } from 'axios';
import { API_BASE_URL } from '@/constants/api/config';
import {
  SESSION_EXPIRED_MESSAGE,
  bearer,
  getAccessToken,
  getSessionGeneration,
  recoverFromUnauthorized,
} from '@/services/session';

/** The message tsa-api-go's Auth middleware answers every refused token with. */
const AUTH_MIDDLEWARE_REFUSAL = 'Please authenticate';

/**
 * Auth endpoints the transport leaves alone: the ones that take no session
 * (a 401 there is about the credentials in the body), and revoke, which
 * session.ts sends with the exact tokens it is revoking.
 */
const UNMANAGED_PATHS = [
  '/auth/signup',
  '/auth/register',
  '/auth/login',
  '/auth/refresh',
  '/auth/revoke',
  '/auth/forgot-password',
  '/auth/reset-password',
  '/auth/reactivate',
];

const ORIGIN_AND_PATH = /^https:\/\/([^/?#]*)([^?#]*)/i;
const [, API_HOST, API_PATH] = ORIGIN_AND_PATH.exec(API_BASE_URL)!;

/** Whether `url` is a request to our API that should carry the session. */
export function isSessionRequest(url: string): boolean {
  const match = ORIGIN_AND_PATH.exec(url);
  // The authority must be exactly our host — no userinfo, no port, no lookalike.
  if (!match || match[1].toLowerCase() !== API_HOST.toLowerCase()) return false;
  const path = match[2];
  if (!path.startsWith(`${API_PATH}/`)) return false;
  // A dot segment would be normalized by the network stack to a path outside the API.
  if (/(^|\/)(\.|%2e){1,2}(\/|$)/i.test(path)) return false;
  return !UNMANAGED_PATHS.includes(path.slice(API_PATH.length));
}

function isMiddlewareRefusal(body: unknown): boolean {
  return (body as { message?: unknown } | null)?.message === AUTH_MIDDLEWARE_REFUSAL;
}

function expiredBody() {
  return { success: false, message: SESSION_EXPIRED_MESSAGE };
}

function withAuthorization(headers: HeadersInit | undefined, token: string | null): Record<string, string> {
  const out: Record<string, string> = {};
  if (headers instanceof Headers) headers.forEach((value, key) => (out[key] = value));
  else if (Array.isArray(headers)) headers.forEach(([key, value]) => (out[key] = value));
  else if (headers) Object.assign(out, headers);
  for (const key of Object.keys(out)) if (key.toLowerCase() === 'authorization') delete out[key];
  if (token) out.Authorization = bearer(token);
  return out;
}

function urlOf(input: RequestInfo | URL): string {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.toString();
  return input.url;
}

/** Wraps `baseFetch` so API requests carry, refresh and expire the session. */
export function createSessionFetch(baseFetch: typeof fetch): typeof fetch {
  return async function sessionFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    // A Request object's body can be read only once, so it could not be
    // retried; nothing in the app sends one, and it passes through as-is.
    const isRequestObject = typeof input === 'object' && !(input instanceof URL);
    if (isRequestObject || !isSessionRequest(urlOf(input))) return baseFetch(input, init);

    const token = getAccessToken();
    const sentIn = getSessionGeneration();
    const send = (t: string | null) => baseFetch(input, { ...init, headers: withAuthorization(init?.headers, t) });
    const res = await send(token);
    if (res.status !== 401 || !token) return res;
    if (!isMiddlewareRefusal(await res.clone().json().catch(() => null))) return res;

    const recovered = await recoverFromUnauthorized(token, sentIn);
    if (recovered === null) return res;
    if (recovered === 'expired') {
      return new Response(JSON.stringify(expiredBody()), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    return send(recovered);
  } as typeof fetch;
}

type SessionConfig = InternalAxiosRequestConfig & { _sessionRetried?: boolean; _sessionGeneration?: number };

/** Adds the same attach / refresh-and-retry / expire behaviour to an axios instance. */
export function attachSessionInterceptors(instance: AxiosInstance): void {
  instance.interceptors.request.use((config: SessionConfig) => {
    // getUri resolves baseURL + url exactly as the request will (absolute URLs included).
    if (!isSessionRequest(instance.getUri(config))) return config;
    const token = getAccessToken();
    config._sessionGeneration = getSessionGeneration();
    if (token) config.headers.set('Authorization', bearer(token));
    else config.headers.delete('Authorization');
    return config;
  });

  instance.interceptors.response.use(undefined, async (error: AxiosError) => {
    const config = error.config as SessionConfig | undefined;
    const sent = config?.headers?.get?.('Authorization');
    if (error.response?.status !== 401 || !config || config._sessionRetried || typeof sent !== 'string'
      || config._sessionGeneration === undefined || !isMiddlewareRefusal(error.response.data)) {
      throw error;
    }

    const recovered = await recoverFromUnauthorized(sent, config._sessionGeneration);
    if (recovered === 'expired') {
      error.response.data = expiredBody();
      error.message = SESSION_EXPIRED_MESSAGE;
    }
    if (recovered === null || recovered === 'expired') throw error;

    config._sessionRetried = true;
    config.headers.set('Authorization', bearer(recovered));
    return instance.request(config);
  });
}

let installed = false;

/** Installs the session transport under global fetch and the given axios instances. Idempotent. */
export function installSessionTransport(axiosInstances: AxiosInstance[]): void {
  if (installed) return;
  installed = true;
  globalThis.fetch = createSessionFetch(globalThis.fetch.bind(globalThis));
  axiosInstances.forEach(attachSessionInterceptors);
}
