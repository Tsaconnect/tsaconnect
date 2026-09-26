import { useRef, useState, type FormEvent } from 'react';
import { Link, Navigate, useLocation, useNavigate } from 'react-router-dom';
import { api, ApiError } from '@/lib/api';
import { SessionLoading } from '@/session/session';
import { returnPath, useSession, type SessionUser, type SignInState } from '@/session/context';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { FormError } from '@/components/ui/form-error';
import { AuthLayout } from './AuthLayout';

type Problem = { message: string; expired?: boolean };

function codeProblem(err: unknown): Problem {
  if (err instanceof ApiError) {
    if (err.code === 'code_invalid') return { message: "That code isn't right." };
    if (err.code === 'challenge_expired') return { message: 'This code has expired. Sign in again to get a new one.', expired: true };
    return { message: err.message };
  }
  return { message: 'Something went wrong. Please try again.' };
}

export function SignInCode() {
  const { user, loading, setUser } = useSession();
  const location = useLocation();
  const navigate = useNavigate();
  const [code, setCode] = useState('');
  const [remember, setRemember] = useState(true);
  const [problem, setProblem] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const codeRef = useRef<HTMLInputElement>(null);
  const from = returnPath(location.state);
  const sentTo = (location.state as SignInState | null)?.sentTo;

  if (loading) return <SessionLoading />;
  if (user) return <Navigate to={from} replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    if (code.length !== 6) {
      setProblem({ message: 'Enter the 6-digit code from your email.' });
      return;
    }
    setBusy(true);
    setProblem(null);
    try {
      const res = await api<{ user: SessionUser }>('/auth/web/verify', { method: 'POST', body: { code, rememberBrowser: remember } });
      setUser(res.user); // this page then redirects to `from`
    } catch (err) {
      setProblem(codeProblem(err));
      setCode('');
      codeRef.current?.focus();
      setBusy(false);
    }
  }

  return (
    <AuthLayout
      heading="New browser, quick check."
      lead="We only ask for this code the first time you sign in on a browser, so a stolen password alone can't open your account."
    >
      <form onSubmit={submit} noValidate className="grid gap-4">
        <div className="mb-1">
          <h1 className="text-[22px] leading-[28px] font-bold tracking-[-0.01em]">Check your email</h1>
          <p className="mt-1.5 text-[13px] text-text-secondary">
            We sent a 6-digit code to <b className="font-semibold text-text-primary">{sentTo ?? 'your email'}</b>. It expires in 15 minutes.
          </p>
        </div>
        {problem && (
          <FormError>
            <span className="flex-1">{problem.message}</span>
          </FormError>
        )}
        {problem?.expired ? (
          <Button onClick={() => navigate('/signin', { replace: true, state: { from } satisfies SignInState })} className="h-11 w-full">
            Sign in again
          </Button>
        ) : (
          <>
            <Field label="Code">
              <Input
                ref={codeRef}
                inputMode="numeric"
                autoComplete="one-time-code"
                autoFocus
                placeholder="••••••"
                value={code}
                onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                className="h-14 text-center font-mono text-[24px] font-semibold tracking-[0.5em] tabular placeholder:tracking-[0.5em]"
              />
            </Field>
            <label className="flex items-center gap-2.5 text-[13px] text-text-primary">
              <input
                type="checkbox"
                checked={remember}
                onChange={(e) => setRemember(e.target.checked)}
                className="size-4 rounded-[4px] accent-[var(--accent)]"
              />
              Remember this browser for 90 days
            </label>
            <Button type="submit" loading={busy} className="mt-1 h-11 w-full">Verify and sign in</Button>
          </>
        )}
        <p className="mt-2 text-center text-caption text-text-secondary">
          Didn't get it?{' '}
          <Link to="/signin" state={{ from } satisfies SignInState} className="font-semibold text-accent-strong hover:underline">
            Sign in again
          </Link>
        </p>
      </form>
    </AuthLayout>
  );
}
