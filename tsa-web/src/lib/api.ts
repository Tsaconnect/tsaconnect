// The one way the web app talks to the API. Same origin, so the HttpOnly
// session cookies ride along on their own — no token is ever visible to page
// scripts, and nothing is kept in web storage. Every request carries the
// X-TSA-Web header the API requires on cookie-authenticated writes.

export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
  }
}

export type ApiInit = {
  method?: 'GET' | 'POST' | 'PUT' | 'DELETE';
  body?: unknown;
  signal?: AbortSignal;
  /** A 401 that survives the refresh throws without ending the session (the sign-in probe). */
  silent?: boolean;
};

const TIMEOUT_MS = 30_000;
// The sign-in endpoints answer 401 for their own reasons; refreshing on them is meaningless.
const NO_REFRESH = new Set(['/auth/web/login', '/auth/web/verify', '/auth/web/refresh', '/auth/web/logout']);
const listeners = new Set<() => void>();

/** Called when the session cannot be renewed: the app routes to sign-in. */
export function onSessionExpired(cb: () => void): () => void {
  listeners.add(cb);
  return () => {
    listeners.delete(cb);
  };
}

/** The caller's signal and the timeout as one signal, with or without AbortSignal.any. */
function combine(caller: AbortSignal | undefined, timeout: AbortController): { signal: AbortSignal; release(): void } {
  if (!caller) return { signal: timeout.signal, release: () => undefined };
  if (typeof AbortSignal.any === 'function') return { signal: AbortSignal.any([caller, timeout.signal]), release: () => undefined };
  // Older browsers: forward the caller's abort to our controller by hand.
  const forward = () => timeout.abort(caller.reason);
  if (caller.aborted) forward();
  else caller.addEventListener('abort', forward, { once: true });
  return { signal: timeout.signal, release: () => caller.removeEventListener('abort', forward) };
}

type Envelope = { success?: boolean; message?: string; code?: string; data?: unknown };
type Reply = { ok: boolean; status: number; body: Envelope | null };

const unreachable = () => new ApiError('Could not reach TSA Connect. Check your connection and try again.', 0, 'transport');

/**
 * One request, body and all. The timeout covers the body too: headers can
 * arrive and the body stall, so the timer and the caller's abort stay wired
 * until the body is read.
 */
async function exchange(path: string, init: ApiInit): Promise<Reply> {
  const timeout = new AbortController();
  const timer = setTimeout(() => timeout.abort(), TIMEOUT_MS);
  const { signal, release } = combine(init.signal, timeout);
  try {
    const res = await fetch(`/api${path}`, {
      method: init.method ?? 'GET',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-TSA-Web': '1' },
      body: init.body === undefined ? undefined : JSON.stringify(init.body),
      signal,
    });
    const body = (await res.json().catch((err: unknown) => {
      if (signal.aborted) throw err;
      return null; // not JSON: a proxy's error page
    })) as Envelope | null;
    return { ok: res.ok, status: res.status, body };
  } catch (err) {
    // A caller's own cancel stays a cancel (TanStack Query relies on it).
    if (init.signal?.aborted) throw err;
    throw unreachable();
  } finally {
    clearTimeout(timer);
    release();
  }
}

function unwrap<T>({ ok, status, body }: Reply): T {
  if (!ok || body === null || body.success === false) {
    throw new ApiError(body?.message || 'Something went wrong. Please try again.', status, body?.code);
  }
  return body.data as T;
}

let refreshing: Promise<boolean> | null = null;

/** One refresh at a time, shared by every request that hit a 401 together. */
function refresh(): Promise<boolean> {
  refreshing ??= exchange('/auth/web/refresh', { method: 'POST' })
    .then((r) => r.ok)
    .catch(() => false)
    .finally(() => {
      refreshing = null;
    });
  return refreshing;
}

/**
 * Only an uncoded 401 (the auth middleware's "Please authenticate") means the
 * session is gone. A 401 with a machine code was answered by a handler that had
 * already accepted the session — a guarded change's wrong password — and is a
 * refusal to show, never a reason to refresh or sign out.
 */
const sessionEnded = (r: Reply) => r.status === 401 && !r.body?.code;

export async function api<T>(path: string, init: ApiInit = {}): Promise<T> {
  let reply = await exchange(path, init);
  if (!NO_REFRESH.has(path) && sessionEnded(reply)) {
    // Retry even when this refresh failed: losing a race to a concurrent refresh
    // answers 401 while the winner has already put fresh cookies in the browser.
    await refresh();
    reply = await exchange(path, init);
    if (!init.silent && sessionEnded(reply)) listeners.forEach((cb) => cb());
  }
  return unwrap<T>(reply);
}
