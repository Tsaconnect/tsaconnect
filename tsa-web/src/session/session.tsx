import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { useQueryClient } from '@tanstack/react-query';
import { api, ApiError, onSessionExpired } from '@/lib/api';
import { BrandMark } from '@/components/shell/Brand';
import { ErrorState } from '@/components/ui/states';
import { toast } from '@/components/ui/toaster';
import { SessionContext, useSession, type SessionUser, type SignInState } from './context';

// Silent: a signed-out visitor to /signin or /docs is simply signed out, never redirected.
// Only a 401 means signed out; any other failure is thrown so the page can offer a retry
// instead of sending a signed-in person to sign-in because the network blinked.
async function probe(): Promise<SessionUser | null> {
  try {
    return (await api<{ user: SessionUser }>('/auth/web/me', { silent: true })).user;
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return null;
    throw err;
  }
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<SessionUser | null>(null);
  const [loading, setLoading] = useState(true);
  const [probeFailed, setProbeFailed] = useState(false);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const location = useLocation();
  const here = useRef('');
  useEffect(() => {
    here.current = location.pathname + location.search;
  }, [location]);

  useEffect(() => {
    let live = true;
    probe().then(
      (u) => {
        if (!live) return;
        setUser(u);
        setLoading(false);
      },
      () => {
        if (!live) return;
        setProbeFailed(true);
        setLoading(false);
      },
    );
    return () => {
      live = false;
    };
  }, []);

  // Set once a session has ended, so concurrent 401s end it once: a second run
  // would navigate again from /signin and lose the page to come back to.
  const ended = useRef(false);
  const signedIn = useCallback((u: SessionUser | null) => {
    if (u) ended.current = false;
    setUser(u);
  }, []);

  const refreshUser = useCallback(async () => {
    setLoading(true);
    try {
      signedIn(await probe());
      setProbeFailed(false);
    } catch {
      setProbeFailed(true);
    } finally {
      setLoading(false);
    }
  }, [signedIn]);

  // Nothing from one person's session may show in the next one's.
  const endSession = useCallback((state?: SignInState) => {
    if (ended.current) return;
    ended.current = true;
    setUser(null);
    queryClient.clear();
    navigate('/signin', { replace: true, state });
  }, [navigate, queryClient]);

  useEffect(() => onSessionExpired(() => endSession({ from: here.current })), [endSession]);

  // The session cookies are HttpOnly, so only the server can end the session. If it
  // didn't answer, the session is still live: say so rather than look signed out.
  const signOut = useCallback(async () => {
    try {
      await api('/auth/web/logout', { method: 'POST' });
    } catch {
      toast.error("Couldn't reach TSA Connect — you're still signed in. Try again.");
      return;
    }
    endSession();
  }, [endSession]);

  const value = useMemo(
    () => ({ user, loading, probeFailed, refreshUser, setUser: signedIn, signOut }),
    [user, loading, probeFailed, refreshUser, signedIn, signOut],
  );
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function SessionLoading() {
  return (
    <div className="grid h-full place-items-center" role="status" aria-label="Loading">
      <BrandMark className="size-9 animate-pulse" />
    </div>
  );
}

/** Signed-in pages. The page asked for comes back after sign-in (router state, not storage). */
export function RequireSession({ children }: { children: ReactNode }) {
  const { user, loading, probeFailed, refreshUser } = useSession();
  const location = useLocation();
  if (loading) return <SessionLoading />;
  if (!user && probeFailed) {
    return (
      <div className="grid h-full place-items-center">
        <ErrorState
          title="We couldn't reach TSA Connect"
          body="Check your connection and try again."
          onRetry={() => void refreshUser()}
        />
      </div>
    );
  }
  if (!user) return <Navigate to="/signin" replace state={{ from: location.pathname + location.search } satisfies SignInState} />;
  return <>{children}</>;
}
