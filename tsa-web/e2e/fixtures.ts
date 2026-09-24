// An in-browser stand-in for the Go API (page.route), for the Playwright run.
// Sign-in and every guarded change need a code we email, so the browser test
// cannot use a real server (see the plan's deviation note); the full server
// flows are covered by the Go tests. Shapes, status codes and machine codes
// follow tsa-api-go: web_auth_handler.go (login/verify/refresh/logout/me),
// middleware/auth.go (the uncoded 401), pay_owner_handler.go (the owner routes)
// and pay_owner_actions.go (what a confirm applies).
import type { Page, Route } from '@playwright/test';

export const PASSWORD = 'correct-horse';
export const CODE = '482913';

const user = { id: '0b8f2d4e-9c1a-4f3b-8e7d-6a5c4b3a2918', name: 'Ada Obi', email: 'ada@skyair.ng', verificationStatus: 'verified' };
const sentTo = 'a***@skyair.ng';

type Merchant = {
  id: string; businessName: string; status: 'active' | 'suspended'; version: number; settlementMode: 'fiat' | 'crypto' | '';
  bankCode?: string; accountNumber?: string; accountName?: string; websiteDomain?: string;
  hasLiveKey: boolean; hasTestKey: boolean; liveKeyPrefix?: string; testKeyPrefix?: string;
  webhookUrl: string; testWebhookUrl: string; returnDomains: string[];
};

type Payment = {
  id: string; reference: string; mode: 'live' | 'test'; status: string; amount: number; customerTotal: number; merchantNet: number;
  fee: number; currency: 'NGN'; description: string; returnUrl?: string; expiresAt: string; settlementRef?: string;
  failureReason?: string; succeededAt?: string; appUrl: string; checkoutUrl: string; createdAt: string;
};

type Pending =
  | { id: string; kind: 'keys'; mode: 'live' | 'test' }
  | { id: string; kind: 'endpoints'; webhookUrl: string; testWebhookUrl: string; returnDomains: string[] }
  | { id: string; kind: 'webhook_secret'; action: 'reveal' | 'roll' };

function payment(id: string, reference: string, mode: 'live' | 'test', status: string, amount: number, createdAt: string, extra: Partial<Payment> = {}): Payment {
  const customerTotal = Math.ceil((amount * 10_000) / 9_900);
  return {
    id, reference, mode, status, amount, customerTotal, merchantNet: amount, fee: customerTotal - amount, currency: 'NGN',
    description: 'LOS-ABV 12 Oct, 1 adult', expiresAt: new Date(Date.parse(createdAt) + 30 * 60_000).toISOString(),
    appUrl: `tsaconnect://pay?id=${id}`, checkoutUrl: `https://tsaconnectworld.com/pay/${id}`, createdAt, ...extra,
  };
}

const PAYMENTS: Payment[] = [
  payment('3f2c1a9e-7b4d-4e21-9c8a-1d2e3f4d5e6f', 'PNR-K8Q2ZL', 'live', 'succeeded', 8_450_000, '2026-09-24T16:01:00Z',
    { settlementRef: 'ord_8c1d42aa', succeededAt: '2026-09-24T16:03:00Z' }),
  payment('7a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d', 'PNR-M3X9TR', 'live', 'pending', 12_900_000, '2026-09-24T15:40:00Z'),
  payment('9c8b7a6d-5e4f-4d3c-8b2a-1f0e9d8c7b6a', 'TEST-0001', 'test', 'failed', 2_500_000, '2026-09-24T14:12:00Z',
    { failureReason: 'declined in test mode' }),
];

const envelope = (status: number, body: Record<string, unknown>) => ({ status, contentType: 'application/json', body: JSON.stringify(body) });
const ok = (data: unknown, message = 'OK', status = 200) => envelope(status, { success: true, message, data });
const fail = (status: number, message: string, code?: string) => envelope(status, { success: false, message, ...(code ? { code } : {}) });
// middleware/auth.go: no session is an uncoded 401 — the only 401 the web app refreshes on.
const signedOut = () => fail(401, 'Please authenticate');

/** A fresh key in GeneratePayKey's shape: tsapk_<mode>_ + 43 base64url characters. */
function newKey(mode: 'live' | 'test') {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_';
  const body = Array.from({ length: 43 }, () => alphabet[Math.floor(Math.random() * alphabet.length)]).join('');
  const raw = `tsapk_${mode}_${body}`;
  return { raw, prefix: raw.slice(0, `tsapk_${mode}_`.length + 6) };
}

export type MockApi = {
  signedIn: boolean;
  merchant: Merchant;
  /** Every API call, in order, with its JSON body. */
  calls: { method: string; path: string; body: unknown }[];
  /** API calls the mock had no answer for — a test should end with none. */
  unhandled: string[];
  /** The last key a confirm issued (it is shown once). */
  lastKey?: string;
};

export async function mockApi(page: Page, opts: { signedIn?: boolean } = {}): Promise<MockApi> {
  const api: MockApi = {
    signedIn: opts.signedIn ?? false,
    merchant: {
      id: '6f1c2b7a-0d4e-4c8b-9a51-3e2f7d9c1b20', businessName: 'Sky Air Ltd', status: 'active', version: 7, settlementMode: 'fiat',
      bankCode: '044', accountNumber: '******3210', accountName: 'SKY AIR LIMITED', websiteDomain: 'skyair.ng',
      hasLiveKey: true, hasTestKey: true, liveKeyPrefix: 'tsapk_live_7Hq2xP', testKeyPrefix: 'tsapk_test_Qz91mK',
      webhookUrl: '', testWebhookUrl: '', returnDomains: ['skyair.ng'],
    },
    calls: [],
    unhandled: [],
  };
  let challenged = false;
  let seq = 0;
  // Action ids are UUIDs in the API (pay_owner_actions.id): shaped the same here.
  const actionId = () => `5d1e8a2c-4b7f-4c3a-9e6d-${String(++seq).padStart(12, '0')}`;
  const pending = new Map<string, Pending>();

  const confirmation = (p: Pending) => {
    pending.set(p.id, p);
    return envelope(202, {
      success: true,
      message: "We've emailed you a code to confirm this change",
      data: { confirmation: { id: p.id, kind: p.kind, expiresAt: new Date(Date.now() + 15 * 60_000).toISOString(), sentTo } },
    });
  };

  const answer = (method: string, path: string, query: URLSearchParams, body: Record<string, unknown>) => {
    const route = `${method} ${path}`;
    switch (route) {
      case 'GET /api/auth/web/me':
        return api.signedIn ? ok({ user }, 'Me') : signedOut();
      case 'POST /api/auth/web/login':
        if (body.email !== user.email || body.password !== PASSWORD) return fail(401, 'Invalid credentials', 'invalid_credentials');
        challenged = true; // a new browser: no trusted-browser cookie
        return ok({ challenge: { sentTo } }, 'Check your email');
      case 'POST /api/auth/web/verify':
        if (!challenged) return fail(410, 'This code has expired. Sign in again to get a new one.', 'challenge_expired');
        if (body.code !== CODE) return fail(422, "That code isn't right", 'code_invalid');
        challenged = false;
        api.signedIn = true;
        return ok({ user }, 'Signed in');
      case 'POST /api/auth/web/refresh':
        return api.signedIn ? ok({ user }, 'Refreshed') : fail(401, 'Please sign in again', 'session_expired');
      case 'POST /api/auth/web/logout':
        api.signedIn = false;
        return ok(null, 'Signed out');
    }

    if (!path.startsWith('/api/pay/merchant')) return null;
    if (!api.signedIn) return signedOut();
    const m = api.merchant;
    const passwordRefused = () => (body.password === PASSWORD ? null : fail(401, 'Incorrect password', 'password_incorrect'));

    if (route === 'GET /api/pay/merchant') return ok(m, 'Merchant');
    if (route === 'GET /api/pay/merchant/payments') {
      const mode = query.get('mode') ?? 'live';
      if (mode !== 'live' && mode !== 'test') return fail(400, 'mode must be live or test');
      const status = query.get('status') ?? '';
      const q = (query.get('q') ?? '').toLowerCase();
      const page = Math.max(Number(query.get('page')) || 1, 1);
      const limit = Math.min(Number(query.get('limit')) > 0 ? Number(query.get('limit')) : 20, 50);
      const rows = PAYMENTS.filter((p) => p.mode === mode && (!status || p.status === status)
        && (!q || p.reference.toLowerCase().includes(q) || p.description.toLowerCase().includes(q) || p.id.startsWith(q)));
      return ok({ items: rows.slice((page - 1) * limit, page * limit), total: rows.length, page, limit }, 'Payments');
    }
    const one = /^\/api\/pay\/merchant\/payments\/([^/]+)$/.exec(path);
    if (method === 'GET' && one) {
      const p = PAYMENTS.find((x) => x.id === one[1] && x.mode === (query.get('mode') ?? 'live'));
      if (!p) return fail(404, 'Payment not found');
      const deliveries = p.status === 'succeeded' ? [{
        id: 'b7e0c1d2-5a4f-4c3e-8d2b-9f1a6e7c3d40', event: 'payment.succeeded', attempts: 1, lastStatusCode: 200,
        deliveredAt: p.succeededAt, createdAt: p.succeededAt!,
      }] : [];
      return ok({ payment: p, deliveries }, 'Payment');
    }
    if (route === 'GET /api/pay/merchant/banks') return ok([{ code: '044', name: 'Access Bank' }, { code: '058', name: 'GTBank' }], 'Banks');
    if (route === 'GET /api/pay/merchant/settlement-options') {
      return ok([{ chainId: 8453, chain: 'base', symbol: 'USDC', tokenAddress: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913' }], 'Settlement options');
    }
    if (route === 'POST /api/pay/merchant/keys') {
      if (body.mode !== 'live' && body.mode !== 'test') return fail(400, 'Invalid request body');
      return passwordRefused() ?? confirmation({ id: actionId(), kind: 'keys', mode: body.mode });
    }
    if (route === 'PUT /api/pay/merchant/endpoints') {
      const refused = passwordRefused();
      if (refused) return refused;
      if (body.expectedVersion !== m.version) {
        return fail(409, 'Your payment settings changed since you opened this screen — reload and try again', 'merchant_stale');
      }
      return confirmation({
        id: actionId(), kind: 'endpoints', webhookUrl: String(body.webhookUrl ?? ''),
        testWebhookUrl: String(body.testWebhookUrl ?? ''), returnDomains: (body.returnDomains as string[]) ?? [],
      });
    }
    if (route === 'POST /api/pay/merchant/webhook-secret') {
      if (body.action !== 'reveal' && body.action !== 'roll') return fail(400, 'Invalid request body');
      return passwordRefused() ?? confirmation({ id: actionId(), kind: 'webhook_secret', action: body.action });
    }
    const confirm = /^\/api\/pay\/merchant\/actions\/([^/]+)\/confirm$/.exec(path);
    if (method === 'POST' && confirm) {
      const action = pending.get(confirm[1]);
      if (!action) return fail(410, 'This change has expired or had too many wrong codes — start again', 'no_pending_change');
      if (body.code !== CODE) return fail(422, "That code isn't right", 'code_invalid');
      pending.delete(action.id);
      if (action.kind === 'keys') {
        // Keys don't bump the version; the key is returned once, the merchant is not.
        const key = newKey(action.mode);
        api.lastKey = key.raw;
        if (action.mode === 'live') Object.assign(m, { hasLiveKey: true, liveKeyPrefix: key.prefix });
        else Object.assign(m, { hasTestKey: true, testKeyPrefix: key.prefix });
        return ok({ mode: action.mode, key: key.raw }, 'Change confirmed');
      }
      if (action.kind === 'endpoints') {
        Object.assign(m, { webhookUrl: action.webhookUrl, testWebhookUrl: action.testWebhookUrl, returnDomains: action.returnDomains, version: m.version + 1 });
        return ok({ merchant: m }, 'Change confirmed');
      }
      return ok({
        secretAction: action.action,
        webhookSecretLive: `whsec_${'a1'.repeat(32)}`,
        webhookSecretTest: `whsec_${'b2'.repeat(32)}`,
      }, 'Change confirmed');
    }
    return null;
  };

  // Fonts come from Google in production; the test needs none of them.
  await page.route(/^https:\/\/fonts\.(googleapis|gstatic)\.com\//, (route) => route.abort());
  await page.route('**/api/**', async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const method = req.method();
    const body = (req.postDataJSON() ?? {}) as Record<string, unknown>;
    api.calls.push({ method, path: url.pathname, body });
    // The web app's CSRF header (webAuthGuard / middleware.Auth): every /auth/web call and every cookie write.
    if ((url.pathname.startsWith('/api/auth/web/') || method !== 'GET') && req.headers()['x-tsa-web'] !== '1') {
      await route.fulfill(fail(403, 'Missing request header', 'csrf'));
      return;
    }
    const res = answer(method, url.pathname, url.searchParams, body);
    if (!res) {
      api.unhandled.push(`${method} ${url.pathname}`);
      await route.fulfill(fail(404, 'Not found'));
      return;
    }
    await route.fulfill(res);
  });
  return api;
}
