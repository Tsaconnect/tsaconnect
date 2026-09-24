// services/apiRequest.ts
//
// One JSON call to our own API, extracted out of paycrestApi.ts so a second
// client (payApi.ts, for TSA Pay) does not have to re-copy the same shape:
// auth header, an Idempotency-Key on every POST (so the same request delivered
// twice never double-fires — a retry is a new request with a new key), the
// `{ success, data }` envelope unwrapped, a typed error with the server's own
// wording, and a timeout.
//
// A 404/503 is treated specially: it usually means the route is not
// registered because this build has no credentials for the feature (see
// paycrestApi's original comment) rather than a bug in the request, so
// callers get `unavailableMessage` instead of a generic "try again" that
// would send the user retrying forever against a feature that is off.
import { API_BASE_URL, REQUEST_TIMEOUT_MS, TRANSPORT_ERROR } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';
import { makeIdempotencyKey } from '@/services/itApi';

export class ApiError extends Error {
  status: number;
  code?: string;
  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

export function createApiRequest(opts: {
  /** Prepended to every path, e.g. '/paycrest' or '/pay'. */
  prefix: string;
  /** Shown on a 404/503 — the route isn't registered. */
  unavailableMessage: string;
  /** Shown on any other failure that carries no server message. */
  networkMessage: string;
  /** Builds the thrown error. Defaults to a plain ApiError. */
  makeError?: (message: string, status: number, code?: string, body?: unknown) => Error;
}): <T>(path: string, init?: RequestInit) => Promise<T> {
  return async function request<T>(path: string, init?: RequestInit): Promise<T> {
    const headers = await getAuthHeaders();
    if (init?.method === 'POST') {
      (headers as Record<string, string>)['Idempotency-Key'] = makeIdempotencyKey();
    }
    const fail = (message: string, status: number, code?: string, body?: unknown) =>
      opts.makeError ? opts.makeError(message, status, code, body) : new ApiError(message, status, code);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
    let res: Response;
    try {
      res = await fetch(`${API_BASE_URL}${opts.prefix}${path}`, {
        ...init,
        signal: init?.signal ?? controller.signal,
        headers: { 'Content-Type': 'application/json', ...headers, ...(init?.headers ?? {}) },
      });
    } catch {
      throw fail(opts.networkMessage, 0, TRANSPORT_ERROR);
    } finally {
      clearTimeout(timer);
    }
    const body = await res.json().catch(() => null);
    if (res.ok && body === null) {
      // A success with no readable body: whatever the caller was waiting for is not in it.
      throw fail(opts.networkMessage, res.status);
    }
    if (!res.ok || body?.success === false) {
      const fallback =
        res.status === 404 || res.status === 503 ? opts.unavailableMessage : opts.networkMessage;
      throw fail(body?.message ?? fallback, res.status, body?.code, body);
    }
    return body?.data as T;
  };
}
