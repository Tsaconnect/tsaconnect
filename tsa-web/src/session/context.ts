import { createContext, useContext } from 'react';

export type SessionUser = { id: string; name: string; email: string; verificationStatus: string };

export type Session = {
  user: SessionUser | null;
  loading: boolean;
  /** The session check itself failed (network, 5xx) — unknown, not signed out. */
  probeFailed: boolean;
  refreshUser(): Promise<void>;
  setUser(u: SessionUser | null): void;
  signOut(): Promise<void>;
};

/** Where sign-in sends her afterwards; carried in router state, never in storage. */
export type SignInState = { from?: string; sentTo?: string };

export const DEFAULT_HOME = '/business/live/overview';

export const SessionContext = createContext<Session | null>(null);

export function useSession(): Session {
  const s = useContext(SessionContext);
  if (!s) throw new Error('useSession outside SessionProvider');
  return s;
}

/**
 * Only in-app paths: never an absolute or protocol-relative URL (browsers read
 * `/\host` as `//host` too), never back to sign-in.
 */
export function returnPath(state: unknown): string {
  const from = (state as SignInState | null)?.from;
  if (typeof from !== 'string' || !from.startsWith('/') || /^\/[/\\]/.test(from) || from.startsWith('/signin')) return DEFAULT_HOME;
  return from;
}
