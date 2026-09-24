import { useRef, useState, type FormEvent } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { api, ApiError } from '@/lib/api';
import { SessionLoading } from '@/session/session';
import { returnPath, useSession, type SessionUser, type SignInState } from '@/session/context';
import { Button } from '@/components/ui/button';
import { Field } from '@/components/ui/field';
import { Input, PasswordInput } from '@/components/ui/input';
import { AuthLayout, FormError } from './AuthLayout';

type LoginResult = { user?: SessionUser; challenge?: { sentTo: string } };

function signInMessage(err: unknown): string {
  if (!(err instanceof ApiError)) return 'Something went wrong. Please try again.';
  if (err.code === 'invalid_credentials' || err.status === 401) return "Your email or password isn't right.";
  if (err.code === 'account_locked' || err.status === 423) return 'Too many attempts. Your account is locked for a while — try again later.';
  return err.message;
}

export function SignIn() {
  const { user, loading, setUser } = useSession();
  const location = useLocation();
  const navigate = useNavigate();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const passwordRef = useRef<HTMLInputElement>(null);
  const from = returnPath(location.state);

  if (loading) return <SessionLoading />;
  if (user) return <Navigate to={from} replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    if (!email.trim() || !password) {
      setError('Enter your email and password.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const res = await api<LoginResult>('/auth/web/login', { method: 'POST', body: { email: email.trim(), password } });
      if (res.user) {
        setUser(res.user); // this page then redirects to `from`
      } else if (res.challenge) {
        navigate('/signin/code', { state: { sentTo: res.challenge.sentTo, from } satisfies SignInState });
      } else {
        throw new Error('unexpected sign-in response');
      }
    } catch (err) {
      setError(signInMessage(err));
      setPassword('');
      passwordRef.current?.focus();
      setBusy(false);
    }
  }

  return (
    <AuthLayout
      heading="Your TSA Connect account, on the web."
      lead="Everything you do in the app, from a bigger screen — and your business tools when you sell with TSA."
      points={[
        'Your account, activity and settings',
        'TSA Pay: payments, payouts, API keys and webhooks',
        'Your wallet keys stay on your phone — the web never holds them',
      ]}
    >
      <form onSubmit={submit} noValidate className="grid gap-4">
        <div className="mb-2">
          <h1 className="text-[22px] leading-[28px] font-bold tracking-[-0.01em]">Sign in to TSA Connect</h1>
          <p className="mt-1.5 text-[13px] text-text-secondary">The same email and password as the app.</p>
        </div>
        {error && <FormError>{error}</FormError>}
        <Field label="Email">
          <Input type="email" autoComplete="username" autoFocus value={email} onChange={(e) => setEmail(e.target.value)} />
        </Field>
        <Field label="Password">
          <PasswordInput ref={passwordRef} autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
        <p className="-mt-1 text-caption text-text-secondary">Forgot your password? Reset it in the TSA Connect app.</p>
        <Button type="submit" loading={busy} className="mt-2 h-11 w-full">Continue</Button>
        <p className="mt-2 text-center text-caption text-text-secondary">No account yet? Get the TSA Connect app to create one.</p>
      </form>
    </AuthLayout>
  );
}
