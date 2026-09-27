// services/session.ts
//
// The one owner of the signed-in session's access token. Before this, the
// token lived in five places at once — AsyncStorage under two keys (each
// sometimes raw, sometimes "Bearer "-prefixed), the api.ts singleton, AuthContext state, and a stack
// of axios interceptors that were added on every sign-in and never removed —
// and nothing refreshed it after cold start. An expired access token surfaced
// on whichever screen asked first, as a "Please authenticate" alert over a
// screen the user could no longer use.
//
// Now every request to our API reads the token from here (sessionTransport
// attaches it), a 401 from the auth middleware triggers one shared refresh,
// and only a refresh the server itself refuses ends the session — announced to
// AuthContext, whose route guard then leaves nothing but the sign-in screens
// reachable.
//
// The refresh token rotates on every use (tsa-api-go rotateRefresh), so two
// refreshes racing with the same token would leave one of them holding a
// revoked token. refreshSession is single-flight for that reason: every caller
// that arrives while a refresh is running shares its outcome.
//
// Every sign-in and sign-out starts a new session *generation*. A refresh or a
// retry belongs to the generation it started in: one that lands after the user
// signed out (or someone else signed in) is discarded, never allowed to bring
// the old session back or to act as the new user.
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL, REQUEST_TIMEOUT_MS } from '@/constants/api/config';
import { clearRefreshToken, getRefreshToken, storeRefreshToken } from '@/services/localAuth';

/** api.ts and walletApi read this key (raw token; a "Bearer " prefix is tolerated). */
const ACCESS_TOKEN_KEY = 'authToken';
/**
 * Older readers of this key expect the raw token: NotificationContext puts it
 * straight into the notifications WebSocket URL, where a "Bearer " prefix (a
 * space) makes Android's native WebSocket throw and crash the app.
 */
const LEGACY_TOKEN_KEY = 'token';
/** Account details written at sign-in; kept across a plain sign-out for biometric sign-in back. */
const ACCOUNT_KEYS = ['userId', 'role', 'userData'];

export const SESSION_EXPIRED_MESSAGE = 'Your session has expired. Please sign in again.';

type TokenPair = { accessToken: string; refreshToken: string };

export type RefreshOutcome =
  | { kind: 'refreshed'; accessToken: string }
  /** The server refused the refresh token: the session is over. */
  | { kind: 'rejected' }
  /** No answer, a server fault, or the session changed meanwhile: nothing to conclude. */
  | { kind: 'unavailable' };

export type SessionEvent = { type: 'refreshed'; accessToken: string } | { type: 'expired' };

let accessToken: string | null = null;
let generation = 0;
let inFlightRefresh: Promise<RefreshOutcome> | null = null;
const listeners = new Set<(event: SessionEvent) => void>();

function stripBearer(token: string): string {
  return token.startsWith('Bearer ') ? token.slice('Bearer '.length) : token;
}

function emit(event: SessionEvent) {
  listeners.forEach((listener) => listener(event));
}

/** The raw access token (no "Bearer " prefix), or null when signed out. */
export function getAccessToken(): string | null {
  return accessToken;
}

/** Changes on every sign-in and sign-out; a request remembers the one it was sent under. */
export function getSessionGeneration(): number {
  return generation;
}

export function bearer(token: string): string {
  return `Bearer ${stripBearer(token)}`;
}

/** Subscribe to refreshes and expiry. Returns the unsubscribe function. */
export function subscribeSession(listener: (event: SessionEvent) => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

/** fetch with a deadline: React Native's fetch has none, and a hung refresh would stall every request behind it. */
async function fetchWithTimeout(url: string, init: RequestInit): Promise<Response> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
  try {
    return await fetch(url, { ...init, signal: controller.signal });
  } finally {
    clearTimeout(timer);
  }
}

async function storeTokens(token: string, refreshToken?: string) {
  // The rotated refresh token first: once the server has issued it, the old one
  // is already revoked, so it is the one copy that can't be recovered.
  if (refreshToken) await storeRefreshToken(refreshToken);
  accessToken = stripBearer(token);
  await AsyncStorage.multiSet([
    [ACCESS_TOKEN_KEY, accessToken],
    [LEGACY_TOKEN_KEY, accessToken],
  ]);
}

/** Reads the stored access token into memory at cold start. Tolerates either key and either format. */
export async function loadStoredSession(): Promise<string | null> {
  const stored =
    (await AsyncStorage.getItem(ACCESS_TOKEN_KEY)) ?? (await AsyncStorage.getItem(LEGACY_TOKEN_KEY));
  if (!stored) return null;
  await setSession(stored);
  return accessToken;
}

/**
 * Starts a session with a freshly issued access token (sign-in, signup,
 * biometric restore, cold start), in memory and in both storage keys. Pass the
 * refresh token when the server issued one alongside it.
 */
export async function setSession(token: string, refreshToken?: string): Promise<void> {
  generation += 1;
  await storeTokens(token, refreshToken);
}

/**
 * Forgets the access token. A plain sign-out keeps the refresh token and the
 * account keys so the login screen can offer biometric sign-in; an expired or
 * fully signed-out session forgets them too.
 */
export async function clearSession(opts: { forgetRefreshToken: boolean }): Promise<void> {
  generation += 1;
  accessToken = null;
  const keys = [ACCESS_TOKEN_KEY, LEGACY_TOKEN_KEY];
  if (opts.forgetRefreshToken) keys.push(...ACCOUNT_KEYS);
  await AsyncStorage.multiRemove(keys);
  if (opts.forgetRefreshToken) await clearRefreshToken();
}

/** Ends the session because the server no longer accepts it, and tells AuthContext. */
export async function expireSession(): Promise<void> {
  await clearSession({ forgetRefreshToken: true });
  emit({ type: 'expired' });
}

/** Trades a refresh token for a new pair. Never throws. */
async function exchangeRefreshToken(
  refreshToken: string,
): Promise<({ kind: 'refreshed' } & TokenPair) | { kind: 'rejected' } | { kind: 'unavailable' }> {
  let res: Response;
  try {
    res = await fetchWithTimeout(`${API_BASE_URL}/auth/refresh`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refreshToken }),
    });
  } catch {
    return { kind: 'unavailable' };
  }
  // 400 is a malformed/missing token, 401 an invalid, revoked or expired one.
  if (res.status === 400 || res.status === 401) return { kind: 'rejected' };
  if (!res.ok) return { kind: 'unavailable' };
  const body = await res.json().catch(() => null);
  const next = body?.data;
  if (!next?.accessToken || !next?.refreshToken) return { kind: 'unavailable' };
  return { kind: 'refreshed', accessToken: next.accessToken, refreshToken: next.refreshToken };
}

/** Revokes one refresh token server-side. Resolves to the HTTP status, or null on no answer. */
async function postRevoke(tokens: TokenPair): Promise<number | null> {
  try {
    const res = await fetchWithTimeout(`${API_BASE_URL}/auth/revoke`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Authorization: bearer(tokens.accessToken) },
      body: JSON.stringify({ refreshToken: tokens.refreshToken }),
    });
    return res.status;
  } catch {
    return null;
  }
}

/** Trades the refresh token for a new access token. Concurrent callers share one request. */
export function refreshSession(): Promise<RefreshOutcome> {
  if (!inFlightRefresh) {
    inFlightRefresh = runRefresh().finally(() => {
      inFlightRefresh = null;
    });
  }
  return inFlightRefresh;
}

async function runRefresh(): Promise<RefreshOutcome> {
  const startedIn = generation;
  let refreshToken: string | null;
  try {
    refreshToken = await getRefreshToken();
  } catch {
    // The keychain can be locked (iOS, device locked): that is not a refusal.
    return { kind: 'unavailable' };
  }
  if (!refreshToken) return { kind: 'rejected' };

  const outcome = await exchangeRefreshToken(refreshToken);
  if (outcome.kind !== 'refreshed') return outcome;

  if (generation !== startedIn) {
    // Signed out, or someone else signed in, while this was out. The server has
    // already rotated the token: revoke the new pair rather than keep it or drop it live.
    void postRevoke(outcome);
    return { kind: 'unavailable' };
  }
  await storeTokens(outcome.accessToken, outcome.refreshToken);
  emit({ type: 'refreshed', accessToken: accessToken! });
  return { kind: 'refreshed', accessToken: accessToken! };
}

/**
 * Signs this device out for good: forgets the session locally at once, then
 * revokes its refresh token server-side in the background. The revoke uses the
 * tokens captured here, and if the access token has expired it trades the
 * refresh token for a fresh pair first and revokes *that* — revoking the old
 * one after a rotation would leave the new one live for its whole lifetime.
 */
export async function endSession(): Promise<void> {
  const access = accessToken;
  const refresh = await getRefreshToken().catch(() => null);
  await clearSession({ forgetRefreshToken: true });
  if (refresh) void revokeRemotely(access, refresh);
}

async function revokeRemotely(access: string | null, refresh: string): Promise<void> {
  if (access && (await postRevoke({ accessToken: access, refreshToken: refresh })) !== 401) return;
  const fresh = await exchangeRefreshToken(refresh);
  if (fresh.kind === 'refreshed') await postRevoke(fresh);
}

/**
 * What to do after a request sent with `sentToken` under `sentGeneration` was
 * refused by the auth middleware: the token to retry with, 'expired' when the
 * session has ended, or null to leave the 401 as it is.
 */
export async function recoverFromUnauthorized(
  sentToken: string,
  sentGeneration: number,
): Promise<string | 'expired' | null> {
  // Signed out or signed in as someone else since: never replay it as them.
  if (generation !== sentGeneration || !accessToken) return null;
  // Another request already refreshed this session since this one left.
  if (accessToken !== stripBearer(sentToken)) return accessToken;

  const outcome = await refreshSession();
  if (outcome.kind === 'rejected') {
    // Only the first of the requests that shared this refusal ends the session
    // (its expiry moves the generation on, so the rest just report it).
    if (generation === sentGeneration) await expireSession();
    return 'expired';
  }
  if (generation !== sentGeneration) return null;
  return outcome.kind === 'refreshed' ? outcome.accessToken : null;
}

/** Test seam: forget module state between tests. */
export function __resetSessionForTests() {
  accessToken = null;
  generation = 0;
  inFlightRefresh = null;
  listeners.clear();
}
