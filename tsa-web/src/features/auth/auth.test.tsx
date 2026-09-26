import { useEffect } from 'react';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Route, useLocation } from 'react-router-dom';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { RequireSession } from '@/session/session';
import { ada, fail, ok, renderWith } from '@/test/harness';
import { SignIn } from './SignIn';
import { SignInCode } from './SignInCode';

// A signed-in page whose request the server refuses (Review Focus 2).
function Payouts() {
  return <Button onClick={() => void api('/pay/merchant').catch(() => undefined)}>Load payouts</Button>;
}

// Two requests at once, as a page with several queries makes.
function TwoAtOnce() {
  const load = () => ['/pay/merchant', '/pay/merchant/payments'].forEach((p) => void api(p).catch(() => undefined));
  return <Button onClick={load}>Load both</Button>;
}

/** Every location this element is shown at: a second navigation to the same page adds one. */
function Visits({ log }: { log: string[] }) {
  const location = useLocation();
  useEffect(() => {
    log.push(location.pathname);
  }, [location, log]);
  return null;
}

function mount(start: string, signInVisits: string[] = []) {
  return renderWith(
    <>
      <Route path="/signin" element={<><SignIn /><Visits log={signInVisits} /></>} />
      <Route path="/signin/code" element={<SignInCode />} />
      <Route path="/business/live/overview" element={<p>overview</p>} />
      <Route path="/business/live/payouts" element={<RequireSession><Payouts /></RequireSession>} />
      <Route path="/business/live/both" element={<RequireSession><TwoAtOnce /></RequireSession>} />
    </>,
    start,
  );
}

// The session probe when signed out: me 401 → refresh 401 → me retried once 401 (R2.3).
const signedOutProbe = (f: ReturnType<typeof vi.fn>) =>
  f.mockResolvedValueOnce(fail(401, 'Please authenticate')).mockResolvedValueOnce(fail(401, 'Please sign in again', 'session_expired'))
    .mockResolvedValueOnce(fail(401, 'Please authenticate'));

async function submitPassword(password = 'correct-horse') {
  await userEvent.type(await screen.findByLabelText('Email'), 'ada@skyair.ng');
  await userEvent.type(screen.getByLabelText('Password'), password);
  await userEvent.click(screen.getByRole('button', { name: 'Continue' }));
}

describe('sign in', () => {
  let f: ReturnType<typeof vi.fn>;
  beforeEach(() => { f = vi.fn(); vi.stubGlobal('fetch', f); });
  afterEach(() => vi.unstubAllGlobals());

  it('a new browser goes to the code step and says where the code went', async () => {
    signedOutProbe(f).mockResolvedValueOnce(ok({ challenge: { sentTo: 'a***@skyair.ng' } }));
    mount('/signin');
    await submitPassword();
    expect(await screen.findByText(/a\*\*\*@skyair\.ng/)).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/signin\/code$/);
    const login = f.mock.calls.find(([u]) => u === '/api/auth/web/login');
    expect(JSON.parse(login![1].body)).toEqual({ email: 'ada@skyair.ng', password: 'correct-horse' });
  });

  it('a trusted browser goes straight in', async () => {
    signedOutProbe(f).mockResolvedValueOnce(ok({ user: ada }));
    mount('/signin');
    await submitPassword();
    expect(await screen.findByText('overview')).toBeInTheDocument();
  });

  it('a wrong password is said plainly and keeps the email', async () => {
    signedOutProbe(f).mockResolvedValueOnce(fail(401, 'Invalid credentials', 'invalid_credentials'));
    mount('/signin');
    await submitPassword('nope');
    expect(await screen.findByRole('alert')).toHaveTextContent(/email or password isn't right/i);
    expect(screen.getByLabelText('Email')).toHaveValue('ada@skyair.ng');
  });

  it('a locked account is told to wait', async () => {
    signedOutProbe(f).mockResolvedValueOnce(fail(423, 'Account locked', 'account_locked'));
    mount('/signin');
    await submitPassword();
    expect(await screen.findByRole('alert')).toHaveTextContent(/too many attempts/i);
  });

  // The web has no restore flow, so the server's "you can restore" offer is not repeated here.
  it('an account in its deletion grace period is sent to the app to restore it', async () => {
    const graceBody = {
      success: false,
      message: 'Your account is scheduled for deletion on 8 October 2026 (14 days left). You can restore your account to continue.',
      code: 'ACCOUNT_DELETED_GRACE_PERIOD',
      data: { canRestore: true, purgeAfter: '2026-10-08T10:00:00Z', graceDaysLeft: 14 },
    };
    signedOutProbe(f).mockResolvedValueOnce(new Response(JSON.stringify(graceBody), { status: 403 }));
    mount('/signin');
    await submitPassword();
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('This account is scheduled for deletion. Open the TSA Connect app to restore it.');
    expect(alert).not.toHaveTextContent(/days left/);
    expect(screen.queryByRole('button', { name: /restore/i })).not.toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/signin$/);
  });

  // Review Focus 5, client side: the server's 503 is shown, not swallowed.
  it("a mailer outage says the code couldn't be sent", async () => {
    signedOutProbe(f).mockResolvedValueOnce(fail(503, "We couldn't send your sign-in code right now. Try again in a few minutes.", 'code_unavailable'));
    mount('/signin');
    await submitPassword();
    expect(await screen.findByRole('alert')).toHaveTextContent("We couldn't send your sign-in code right now.");
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/signin$/);
  });

  it('empty fields are caught before any request', async () => {
    signedOutProbe(f);
    mount('/signin');
    await userEvent.click(await screen.findByRole('button', { name: 'Continue' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/enter your email and password/i);
    expect(f).toHaveBeenCalledTimes(3);
  });

  it('the code step signs in and lands on the business overview', async () => {
    signedOutProbe(f).mockResolvedValueOnce(ok({ user: ada }));
    mount('/signin/code');
    await userEvent.type(await screen.findByLabelText('Code'), '482913');
    await userEvent.click(screen.getByRole('button', { name: 'Verify and sign in' }));
    expect(await screen.findByText('overview')).toBeInTheDocument();
    const verify = f.mock.calls.find(([u]) => u === '/api/auth/web/verify');
    expect(JSON.parse(verify![1].body)).toEqual({ code: '482913', rememberBrowser: true });
  });

  it('the code field takes digits only and "remember" can be turned off', async () => {
    signedOutProbe(f).mockResolvedValueOnce(ok({ user: ada }));
    mount('/signin/code');
    const code = await screen.findByLabelText('Code');
    await userEvent.type(code, '48a2 9-13x7');
    expect(code).toHaveValue('482913');
    await userEvent.click(screen.getByRole('checkbox', { name: /remember this browser/i }));
    await userEvent.click(screen.getByRole('button', { name: 'Verify and sign in' }));
    await screen.findByText('overview');
    const verify = f.mock.calls.find(([u]) => u === '/api/auth/web/verify');
    expect(JSON.parse(verify![1].body)).toEqual({ code: '482913', rememberBrowser: false });
  });

  it('a pasted code with a space keeps all six digits', async () => {
    signedOutProbe(f).mockResolvedValueOnce(ok({ user: ada }));
    mount('/signin/code');
    await userEvent.click(await screen.findByLabelText('Code'));
    await userEvent.paste('482 913');
    expect(screen.getByLabelText('Code')).toHaveValue('482913');
    await userEvent.click(screen.getByRole('button', { name: 'Verify and sign in' }));
    await screen.findByText('overview');
    const verify = f.mock.calls.find(([u]) => u === '/api/auth/web/verify');
    expect(JSON.parse(verify![1].body)).toMatchObject({ code: '482913' });
  });

  it('a wrong code says so and lets her try again', async () => {
    signedOutProbe(f).mockResolvedValueOnce(fail(422, "That code isn't right", 'code_invalid'));
    mount('/signin/code');
    await userEvent.type(await screen.findByLabelText('Code'), '000000');
    await userEvent.click(screen.getByRole('button', { name: 'Verify and sign in' }));
    expect(await screen.findByRole('alert')).toHaveTextContent("That code isn't right.");
    expect(screen.getByRole('button', { name: 'Verify and sign in' })).toBeEnabled();
  });

  it('an expired code sends her back to sign in', async () => {
    signedOutProbe(f).mockResolvedValueOnce(fail(410, 'This code has expired. Sign in again to get a new one.', 'challenge_expired'));
    mount('/signin/code');
    await userEvent.type(await screen.findByLabelText('Code'), '482913');
    await userEvent.click(screen.getByRole('button', { name: 'Verify and sign in' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('This code has expired. Sign in again to get a new one.');
    await userEvent.click(screen.getByRole('button', { name: 'Sign in again' }));
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/signin$/);
  });

  it('a signed-in visitor to /signin goes on to the business area', async () => {
    f.mockResolvedValueOnce(ok({ user: ada }));
    mount('/signin');
    expect(await screen.findByText('overview')).toBeInTheDocument();
  });

  it('a signed-out visitor to a signed-in page comes back to it after signing in', async () => {
    signedOutProbe(f).mockResolvedValueOnce(ok({ user: ada }));
    mount('/business/live/payouts');
    await waitFor(() => expect(screen.getByTestId('location')).toHaveTextContent(/^\/signin$/));
    await submitPassword();
    expect(await screen.findByRole('button', { name: 'Load payouts' })).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/live\/payouts$/);
  });

  it('a new-browser code also returns her to the page she asked for', async () => {
    signedOutProbe(f).mockResolvedValueOnce(ok({ challenge: { sentTo: 'a***@skyair.ng' } })).mockResolvedValueOnce(ok({ user: ada }));
    mount('/business/live/payouts');
    await submitPassword();
    await userEvent.type(await screen.findByLabelText('Code'), '482913');
    await userEvent.click(screen.getByRole('button', { name: 'Verify and sign in' }));
    expect(await screen.findByRole('button', { name: 'Load payouts' })).toBeInTheDocument();
  });

  // Only a 401 means signed out. A network or server failure is offered a retry,
  // never a redirect to sign-in.
  it.each([
    ['a server error', () => Promise.resolve(fail(503, 'Service unavailable'))],
    ['a network failure', () => Promise.reject(new TypeError('Failed to fetch'))],
  ])('a session check that fails with %s offers a retry instead of sign-in', async (_, failure) => {
    f.mockImplementationOnce(failure).mockResolvedValueOnce(ok({ user: ada }));
    mount('/business/live/payouts');
    expect(await screen.findByRole('alert')).toHaveTextContent("We couldn't reach TSA Connect");
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/live\/payouts$/);
    expect(f).toHaveBeenCalledTimes(1); // no refresh: it wasn't a 401
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByRole('button', { name: 'Load payouts' })).toBeInTheDocument();
    expect(f.mock.calls.map(([u]) => u)).toEqual(['/api/auth/web/me', '/api/auth/web/me']);
  });

  // Review Focus 2: refresh fails and the retry is refused → sign-in, remembering the page.
  it('a session that cannot be renewed goes to sign in and comes back to the same page', async () => {
    f.mockResolvedValueOnce(ok({ user: ada })) // probe
      .mockResolvedValueOnce(fail(401, 'Please authenticate')) // /pay/merchant
      .mockResolvedValueOnce(fail(401, 'Please sign in again', 'session_expired')) // refresh
      .mockResolvedValueOnce(fail(401, 'Please authenticate')) // retry
      .mockResolvedValueOnce(ok({ user: ada })); // login from a trusted browser
    mount('/business/live/payouts');
    await userEvent.click(await screen.findByRole('button', { name: 'Load payouts' }));
    expect(await screen.findByLabelText('Email')).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/signin$/);
    await submitPassword();
    expect(await screen.findByRole('button', { name: 'Load payouts' })).toBeInTheDocument();
  });

  // Both requests fail to renew: the session ends once, and sign-in still
  // knows the page to come back to (a second end would start from /signin).
  it('two requests that both find the session gone end it once, keeping the page', async () => {
    const visits: string[] = [];
    let slowRetry!: (r: Response) => void;
    const tries: Record<string, number> = {};
    f.mockImplementation(async (url: string) => {
      if (url === '/api/auth/web/me') return ok({ user: ada });
      if (url === '/api/auth/web/refresh') return fail(401, 'Please sign in again', 'session_expired');
      if (url === '/api/auth/web/login') return ok({ user: ada });
      tries[url] = (tries[url] ?? 0) + 1;
      // The payments retry answers only after the first end has reached sign-in.
      if (url === '/api/pay/merchant/payments' && tries[url] === 2) return new Promise<Response>((r) => { slowRetry = r; });
      return fail(401, 'Please authenticate');
    });
    mount('/business/live/both', visits);
    await userEvent.click(await screen.findByRole('button', { name: 'Load both' }));
    expect(await screen.findByLabelText('Email')).toBeInTheDocument();
    await waitFor(() => expect(slowRetry).toBeDefined());
    slowRetry(fail(401, 'Please authenticate'));
    await submitPassword();
    expect(await screen.findByRole('button', { name: 'Load both' })).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/business\/live\/both$/);
    expect(visits).toEqual(['/signin']); // one navigation to sign-in, not two
  });
});
