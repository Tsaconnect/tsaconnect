// Shared test harness: a route-keyed fetch stub, response builders, fixtures,
// and the app's providers around either the whole app or a test's own routes.
import { useState, type ReactNode } from 'react';
import { render } from '@testing-library/react';
import { MemoryRouter, Routes, useLocation, useNavigationType } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SessionProvider } from '@/session/session';
import { Toaster } from '@/components/ui/toaster';
import { AppRoutes } from '@/routes';
import type { OpenLink, OwnMerchant, Payment } from '@/features/business/api';

/** A fixed answer, or one worked out per call (it gets the URL, query included). */
type Route = Response | ((url: string) => Response | Promise<Response>);

export const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

export const ok = (data: unknown) => json(200, { success: true, data });
export const accepted = (data: unknown) => json(202, { success: true, message: "We've emailed you a code to confirm this change", data });
export const fail = (status: number, message: string, code?: string) => json(status, { success: false, message, ...(code ? { code } : {}) });

/** An expiry 15 minutes ahead, as the API sets on a guarded action. */
export const soon = new Date(Date.now() + 15 * 60_000).toISOString();

export const ada = { id: 'u1', name: 'Ada Obi', email: 'ada@skyair.ng', verificationStatus: 'verified' };

export const merchant: OwnMerchant = {
  id: '6f1c2b7a-0d4e-4c8b-9a51-3e2f7d9c1b20',
  businessName: 'Sky Air Ltd',
  status: 'active',
  liveEnabled: false,
  version: 7,
  websiteDomain: 'skyair.ng',
  settlementMode: 'fiat',
  bankCode: '044',
  accountNumber: '******3210',
  accountName: 'SKY AIR LIMITED',
  hasLiveKey: true,
  hasTestKey: true,
  liveKeyPrefix: 'tsapk_live_7Hq2',
  testKeyPrefix: 'tsapk_test_Qz91',
  webhookUrl: '',
  testWebhookUrl: '',
  returnDomains: ['skyair.ng'],
};

export const payment: Payment = {
  id: '3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f',
  reference: 'PNR-K8Q2ZL',
  mode: 'live',
  status: 'succeeded',
  amount: 8_450_000,
  customerTotal: 8_535_354,
  merchantNet: 8_450_000,
  fee: 85_354,
  currency: 'NGN',
  description: 'LOS–ABV 12 Oct, 1 adult',
  source: 'api',
  checkoutUrl: 'https://tsaconnectworld.com/pay/3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f',
  expiresAt: '2026-09-24T16:31:00Z',
  settlementRef: 'ord_8c1d42aa',
  succeededAt: '2026-09-24T16:03:00Z',
  createdAt: '2026-09-24T16:01:00Z',
};

/** A payment link the owner made, still waiting to be paid. */
export const link: Payment = {
  ...payment,
  id: '8e4d2c1b-6a5f-4e3d-9c2b-7a1f0e9d8c7b',
  reference: 'link_k3m9x2p7q4wz',
  status: 'pending',
  amount: 500_000,
  customerTotal: 505_051,
  merchantNet: 500_000,
  fee: 5_051,
  description: 'Balance for LOS–ABV, 12 Oct',
  source: 'link',
  customerName: 'Tolu Adeyemi',
  checkoutUrl: 'https://tsaconnectworld.com/pay/8e4d2c1b-6a5f-4e3d-9c2b-7a1f0e9d8c7b',
  expiresAt: '2026-10-01T16:01:00Z',
  settlementRef: undefined,
  succeededAt: undefined,
};

/** A reusable link: one address many customers pay, each entering the amount. */
export const openLink: OpenLink = {
  id: '4a7b9c1d-2e3f-4a5b-8c6d-7e8f9a0b1c2d',
  mode: 'live',
  slug: 'sky-air-ltd-7k3q',
  url: 'https://tsaconnectworld.com/l/sky-air-ltd-7k3q',
  label: 'Sky Air — Ikeja ticket desk',
  minAmount: 100_000,
  maxAmount: 0,
  merchantMinAmount: 100_000,
  merchantMaxAmount: 500_000_000,
  active: true,
  takingPayments: true,
  version: 3,
  paidCount: 2,
  paidTotal: 1_250_000,
  receivedTotal: 1_225_000,
  createdAt: '2026-09-20T09:00:00Z',
};

/**
 * Stubs global fetch by path (the query is ignored). Signed in by default; an
 * unmatched path answers 404. Every call gets a fresh Response.
 */
export function stubFetch(routes: Record<string, Route>) {
  const table: Record<string, Route> = { '/api/auth/web/me': ok({ user: ada }), ...routes };
  // Typed as api() calls it (body is its JSON string), so tests can JSON.parse(init.body).
  const f = vi.fn<(url: string, init: { method: string; body: string; headers: Record<string, string> }) => Promise<Response>>(async (url) => {
    const route = table[new URL(url, 'http://x').pathname];
    if (route === undefined) return fail(404, 'Not found');
    return typeof route === 'function' ? route(url) : route.clone();
  });
  vi.stubGlobal('fetch', f);
  return f;
}

export function LocationProbe() {
  const { pathname } = useLocation();
  const how = useNavigationType();
  return (
    <>
      <p data-testid="location">{pathname}</p>
      <p data-testid="navigation">{how}</p>
    </>
  );
}

/** A query client for one test: no retries, so a refusal shows at once. */
export const testQueryClient = () => new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });

function Providers({ path, children }: { path: string; children: ReactNode }) {
  const [client] = useState(testQueryClient);
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <SessionProvider>
          {children}
          <LocationProbe />
          <Toaster />
        </SessionProvider>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

/** The whole signed-in app at `path`. */
export const mountAt = (path: string) => render(<Providers path={path}><AppRoutes /></Providers>);

/** A test's own `<Route>`s at `path`, inside the app's providers. */
export const renderWith = (routes: ReactNode, path: string) => render(<Providers path={path}><Routes>{routes}</Routes></Providers>);
