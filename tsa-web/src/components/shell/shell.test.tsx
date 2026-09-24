import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SessionProvider } from '@/session/session';
import { Account } from '@/features/account/Account';
import { Toaster } from '@/components/ui/toaster';
import { AppShell } from './AppShell';

const ok = (data: unknown) => new Response(JSON.stringify({ success: true, data }), { status: 200 });
const ada = { id: 'u1', name: 'Ada Obi', email: 'ada@skyair.ng', verificationStatus: 'verified' };
const skyAir = { businessName: 'Sky Air Ltd', settlementMode: 'fiat' as const };

function LocationProbe() {
  return <p data-testid="location">{useLocation().pathname}</p>;
}

function mount(start: string, merchant: typeof skyAir | null | undefined = skyAir) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter initialEntries={[start]}>
        <SessionProvider>
          <Routes>
            <Route path="/signin" element={<p>sign in page</p>} />
            <Route path="/account" element={<Account />} />
            <Route path="/business/:mode/*" element={<AppShell section="business" title="Payments" merchant={merchant}><p>page</p></AppShell>} />
          </Routes>
          <LocationProbe />
          <Toaster />
        </SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('app shell', () => {
  let f: ReturnType<typeof vi.fn>;
  // A fresh Response per call: a body can only be read once.
  beforeEach(() => { f = vi.fn(async () => ok({ user: ada })); vi.stubGlobal('fetch', f); });
  afterEach(() => vi.unstubAllGlobals());

  it('business navigation keeps the current mode', async () => {
    mount('/business/test/payments');
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    expect(within(nav).getByRole('link', { name: 'Overview' })).toHaveAttribute('href', '/business/test/overview');
    expect(within(nav).getByRole('link', { name: 'Payouts' })).toHaveAttribute('href', '/business/test/payouts');
    expect(within(nav).getByRole('link', { name: 'API keys & webhooks' })).toHaveAttribute('href', '/business/test/developers');
    expect(within(nav).getByRole('link', { name: 'Docs' })).toHaveAttribute('href', '/docs');
    expect(within(nav).getByRole('link', { name: 'Payments' })).toHaveAttribute('aria-current', 'page');
  });

  it('test mode shows the banner and the Live switch keeps the page and its filters', async () => {
    mount('/business/test/developers?x=1');
    expect(await screen.findByText('Test mode — no real money moves')).toBeInTheDocument();
    const modes = screen.getByRole('group', { name: 'Mode' });
    expect(within(modes).getByRole('link', { name: 'Live' })).toHaveAttribute('href', '/business/live/developers?x=1');
    expect(within(modes).getByRole('link', { name: 'Test' })).toHaveAttribute('aria-current', 'page');
  });

  // A payment and a page number belong to one mode's list: the other mode opens its list at the start.
  it('switching mode from a payment goes to the other list, without the payment or the page', async () => {
    mount('/business/test/payments/3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f?status=failed&page=3&q=abv');
    const modes = await screen.findByRole('group', { name: 'Mode' });
    expect(within(modes).getByRole('link', { name: 'Live' })).toHaveAttribute('href', '/business/live/payments?status=failed&q=abv');
  });

  it('live mode has no test banner', async () => {
    mount('/business/live/payments');
    await screen.findByRole('heading', { name: 'Payments' });
    expect(screen.queryByText('Test mode — no real money moves')).not.toBeInTheDocument();
  });

  it('shows the merchant, the signed-in person, and the Personal | Business switch', async () => {
    mount('/business/live/payments');
    expect(await screen.findByText('Sky Air Ltd')).toBeInTheDocument();
    expect(screen.getByText('TSA Pay · paid in naira')).toBeInTheDocument();
    expect(await screen.findByText('Ada Obi')).toBeInTheDocument();
    const ws = screen.getByRole('navigation', { name: 'Workspace' });
    expect(within(ws).getByRole('link', { name: 'Personal' })).toHaveAttribute('href', '/account');
    expect(within(ws).getByRole('link', { name: 'Business' })).toHaveAttribute('aria-current', 'page');
  });

  it('a crypto merchant reads "paid in crypto"', async () => {
    mount('/business/live/payments', { businessName: 'Sky Air Ltd', settlementMode: 'crypto' } as never);
    expect(await screen.findByText('TSA Pay · paid in crypto')).toBeInTheDocument();
  });

  it('sign out ends the session on the server and goes to sign in', async () => {
    mount('/business/live/payments');
    await screen.findByText('Ada Obi');
    await userEvent.click(screen.getAllByRole('button', { name: 'Sign out' })[0]);
    expect(await screen.findByText('sign in page')).toBeInTheDocument();
    const logout = f.mock.calls.find(([u]) => u === '/api/auth/web/logout');
    expect(logout![1].method).toBe('POST');
  });

  it('sign out that cannot reach the server keeps the session and says so', async () => {
    f.mockImplementation(async (url: string) =>
      url === '/api/auth/web/logout' ? Promise.reject(new TypeError('Failed to fetch')) : ok({ user: ada }),
    );
    mount('/business/live/payments');
    await screen.findByText('Ada Obi');
    await userEvent.click(screen.getAllByRole('button', { name: 'Sign out' })[0]);
    expect(await screen.findByText("Couldn't reach TSA Connect — you're still signed in. Try again.")).toBeInTheDocument();
    expect(screen.getByTestId('location')).toHaveTextContent('/business/live/payments');
    expect(screen.getByText('Ada Obi')).toBeInTheDocument();
    expect(screen.queryByText('sign in page')).not.toBeInTheDocument();
  });

  it('the mobile menu closes when the window widens to the desktop layout', async () => {
    const listeners: ((e: { matches: boolean }) => void)[] = [];
    vi.stubGlobal('matchMedia', vi.fn((query: string) => ({
      media: query,
      matches: false,
      addEventListener: (_: string, cb: (e: { matches: boolean }) => void) => listeners.push(cb),
      removeEventListener: (_: string, cb: (e: { matches: boolean }) => void) => listeners.splice(listeners.indexOf(cb), 1),
    })));
    mount('/business/live/payments');
    await userEvent.click(await screen.findByRole('button', { name: 'Open menu' }));
    expect(screen.getByRole('dialog', { name: 'Menu' })).toBeInTheDocument();
    expect(window.matchMedia).toHaveBeenCalledWith('(min-width: 64rem)');
    act(() => listeners.forEach((cb) => cb({ matches: true })));
    expect(screen.queryByRole('dialog', { name: 'Menu' })).not.toBeInTheDocument();
    expect(listeners).toHaveLength(0); // unsubscribed once closed
  });

  it('the Account page shows who is signed in', async () => {
    mount('/account');
    const card = (await screen.findByRole('heading', { name: 'Your account' })).closest('section')!;
    expect(within(card).getByText('Ada Obi')).toBeInTheDocument();
    expect(within(card).getByText('ada@skyair.ng')).toBeInTheDocument();
    expect(within(card).getByText('Verified')).toBeInTheDocument();
    expect(within(card).getByText(/change your name or password in the TSA Connect app/i)).toBeInTheDocument();
    expect(screen.queryByRole('navigation', { name: 'Main' })?.textContent).not.toMatch(/Payouts/);
  });
});
