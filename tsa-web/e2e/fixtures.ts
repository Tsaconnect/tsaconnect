// An in-browser stand-in for the Go API (page.route), for the Playwright run.
// Sign-in and every guarded change need a code we email, so the browser test
// cannot use a real server (see the plan's deviation note); the full server
// flows are covered by the Go tests. Shapes, status codes and machine codes
// follow tsa-api-go: web_auth_handler.go (login/verify/refresh/logout/me),
// middleware/auth.go (the uncoded 401), pay_owner_handler.go (the owner routes,
// payment links included), pay_owner_actions.go (what a confirm applies),
// pay_business_name.go (a rename waits for an admin) and
// middleware/idempotency.go (a repeated Idempotency-Key replays its answer).
import type { Page, Route } from '@playwright/test';

export const PASSWORD = 'correct-horse';
export const CODE = '482913';

const user = { id: '0b8f2d4e-9c1a-4f3b-8e7d-6a5c4b3a2918', name: 'Ada Obi', email: 'ada@skyair.ng', verificationStatus: 'verified' };
const sentTo = 'a***@skyair.ng';

type Merchant = {
  id: string; businessName: string; status: 'active' | 'suspended'; liveEnabled: boolean; liveAllowed: boolean; version: number; settlementMode: 'fiat' | 'crypto' | '';
  pendingBusinessName?: string; pendingSince?: string; businessNameChangeEnabled?: boolean;
  bankCode?: string; accountNumber?: string; accountName?: string; websiteDomain?: string;
  hasLiveKey: boolean; hasTestKey: boolean; liveKeyPrefix?: string; testKeyPrefix?: string;
  webhookUrl: string; testWebhookUrl: string; returnDomains: string[];
};

type Payment = {
  id: string; reference: string; mode: 'live' | 'test'; status: string; amount: number; customerTotal: number; merchantNet: number;
  fee: number; currency: 'NGN'; description: string; source: 'api' | 'link' | 'open'; customerName?: string; cancelled?: boolean;
  openLinkId?: string; linkLabel?: string;
  returnUrl?: string; expiresAt: string | null; settlementRef?: string;
  failureReason?: string; succeededAt?: string; appUrl: string; checkoutUrl: string; createdAt: string;
};

type Pending =
  | { id: string; kind: 'keys'; mode: 'live' | 'test' }
  | { id: string; kind: 'endpoints'; webhookUrl: string; testWebhookUrl: string; returnDomains: string[] }
  | { id: string; kind: 'webhook_secret'; action: 'reveal' | 'roll' }
  | { id: string; kind: 'business_name'; name: string };

function payment(id: string, reference: string, mode: 'live' | 'test', status: string, amount: number, createdAt: string, extra: Partial<Payment> = {}): Payment {
  const customerTotal = Math.ceil((amount * 10_000) / 9_900);
  return {
    id, reference, mode, status, amount, customerTotal, merchantNet: amount, fee: customerTotal - amount, currency: 'NGN',
    description: 'LOS-ABV 12 Oct, 1 adult', source: 'api', expiresAt: new Date(Date.parse(createdAt) + 30 * 60_000).toISOString(),
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
  /** Every API call, in order, with its JSON body (and Idempotency-Key, when sent). */
  calls: { method: string; path: string; body: unknown; idempotencyKey?: string }[];
  /** API calls the mock had no answer for — a test should end with none. */
  unhandled: string[];
  /** The last key a confirm issued (it is shown once). */
  lastKey?: string;
  /** Every payment, API-made and links; a test may add its own. */
  payments: Payment[];
  /** Reusable links, newest first. */
  openLinks: OpenLink[];
  /** A customer pays `amount` kobo through a reusable link (what the public checkout would do). */
  payThrough: (link: OpenLink, amount: number, note?: string) => Payment;
};

type OpenLink = {
  id: string; mode: 'live' | 'test'; slug: string; url: string; label: string; minAmount: number; maxAmount: number;
  active: boolean; version: number; paidCount: number; paidTotal: number; receivedTotal: number; paymentExpiresIn: number; createdAt: string;
};

// The merchant's own bounds (pay_merchant_service.go defaults), in kobo.
const MIN_KOBO = 100_000;
const MAX_KOBO = 500_000_000;

const MERCHANT_CANCELLED = 'cancelled by the merchant';

export async function mockApi(page: Page, opts: { signedIn?: boolean } = {}): Promise<MockApi> {
  const api: MockApi = {
    signedIn: opts.signedIn ?? false,
    merchant: {
      // Approved and testing: live payments not switched on yet.
      id: '6f1c2b7a-0d4e-4c8b-9a51-3e2f7d9c1b20', businessName: 'Sky Air Ltd', status: 'active', liveEnabled: false, liveAllowed: false, version: 7, settlementMode: 'fiat',
      businessNameChangeEnabled: true,
      bankCode: '044', accountNumber: '******3210', accountName: 'SKY AIR LIMITED', websiteDomain: 'skyair.ng',
      hasLiveKey: true, hasTestKey: true, liveKeyPrefix: 'tsapk_live_7Hq2xP', testKeyPrefix: 'tsapk_test_Qz91mK',
      webhookUrl: '', testWebhookUrl: '', returnDomains: ['skyair.ng'],
    },
    calls: [],
    unhandled: [],
    payments: PAYMENTS.map((p) => ({ ...p })),
    openLinks: [],
    payThrough: (link, amount, note) => payThrough(link, amount, note),
  };
  // middleware/idempotency.go: a key seen before replays the answer it got (2xx only).
  const replays = new Map<string, ReturnType<typeof ok>>();
  let slugs = 0;
  // Business-name slug + 4 base32 characters, like the API's.
  const newSlug = () => `sky-air-ltd-${(1000 + ++slugs * 37).toString(32).slice(-4).padStart(4, 'k')}`;
  const openUrl = (slug: string) => `https://tsaconnectworld.com/l/${slug}`;
  // A reusable-link payment's description is the customer's own note (often blank); linkLabel names the link.
  function payThrough(link: OpenLink, amount: number, note = '') {
    const n = api.payments.length;
    const now = new Date();
    const made = payment(`0e11ed00-0000-4000-8000-${String(n).padStart(12, '0')}`, `open_${String(n).padStart(12, 'q')}`, link.mode, 'succeeded', amount,
      now.toISOString(), {
        source: 'open', openLinkId: link.id, linkLabel: link.label, description: note, succeededAt: now.toISOString(),
        expiresAt: link.paymentExpiresIn ? new Date(now.getTime() + link.paymentExpiresIn * 1000).toISOString() : null,
      });
    api.payments.unshift(made);
    Object.assign(link, { paidCount: link.paidCount + 1, paidTotal: link.paidTotal + amount, receivedTotal: link.receivedTotal + made.merchantNet });
    return made;
  }
  /** A link as the owner reads it: the merchant's bounds, and whether it takes payments now (and why not). */
  const openView = (l: OpenLink) => {
    const m = api.merchant;
    const reason = m.status === 'suspended' ? 'suspended' : !m.settlementMode ? 'no_payout'
      : l.mode === 'live' && !m.liveAllowed ? 'live_off' : !l.active ? 'paused' : undefined;
    return { ...l, merchantMinAmount: MIN_KOBO, merchantMaxAmount: MAX_KOBO, takingPayments: !reason, ...(reason ? { notTakingReason: reason } : {}) };
  };
  /** Label, bounds and payment expiry as the API checks them; null when they pass. */
  const openLinkRefused = (body: Record<string, unknown>) => {
    const label = String(body.label ?? '').trim();
    const min = Number(body.minAmount ?? 0);
    const max = Number(body.maxAmount ?? 0);
    const expiresIn = Number(body.paymentExpiresIn ?? 0);
    if (expiresIn && !(Number.isInteger(expiresIn) && expiresIn >= 900 && expiresIn <= 86_400)) {
      return fail(422, 'A payment can expire after 900 to 86400 seconds, or never');
    }
    if (!label || label.length > 80) return fail(422, 'Give the link a label of up to 80 characters');
    const inBounds = (k: number) => k === 0 || (Number.isInteger(k) && k >= MIN_KOBO && k <= MAX_KOBO);
    if (!inBounds(min) || !inBounds(max)) return fail(422, 'Enter an amount between ₦1,000.00 and ₦5,000,000.00');
    if (min && max && min > max) return fail(422, 'The maximum must be at least the minimum');
    return null;
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
    const mode = query.get('mode') ?? 'live';
    if (mode !== 'live' && mode !== 'test') return fail(400, 'mode must be live or test');
    if (route === 'GET /api/pay/merchant/payments' || route === 'GET /api/pay/merchant/links') {
      const source = route.endsWith('/links') ? 'link' : '';
      const status = query.get('status') ?? '';
      const q = (query.get('q') ?? '').toLowerCase();
      const page = Math.max(Number(query.get('page')) || 1, 1);
      const limit = Math.min(Number(query.get('limit')) > 0 ? Number(query.get('limit')) : 20, 50);
      // `cancelled` is an expired link its owner closed; `expired` is the rest.
      const statusMatches = (p: Payment) => !status || (status === 'cancelled' ? Boolean(p.cancelled) : p.status === status && !p.cancelled);
      const rows = api.payments.filter((p) => p.mode === mode && (!source || p.source === source) && statusMatches(p)
        && (!q || p.reference.toLowerCase().includes(q) || p.description.toLowerCase().includes(q) || p.id.startsWith(q)));
      return ok({ items: rows.slice((page - 1) * limit, page * limit), total: rows.length, page, limit }, 'Payments');
    }
    if (route === 'POST /api/pay/merchant/open-links') {
      if (mode === 'live' && !m.liveAllowed) {
        return fail(403, "Live payments aren't switched on for your business yet. Try a test link first.", 'live_not_enabled');
      }
      if (!m.settlementMode) return fail(409, 'Set where you want to be paid before you request a payment', 'settlement_not_set');
      const refused = openLinkRefused(body);
      if (refused) return refused;
      const slug = newSlug();
      const made: OpenLink = {
        id: `0be11111-0000-4000-8000-${String(api.openLinks.length).padStart(12, '0')}`, mode, slug, url: openUrl(slug),
        label: String(body.label).trim(), minAmount: Number(body.minAmount ?? 0), maxAmount: Number(body.maxAmount ?? 0),
        active: true, version: 1, paidCount: 0, paidTotal: 0, receivedTotal: 0, paymentExpiresIn: Number(body.paymentExpiresIn ?? 0),
        createdAt: new Date().toISOString(),
      };
      api.openLinks.unshift(made);
      return ok(openView(made), 'Payment link created', 201);
    }
    if (route === 'GET /api/pay/merchant/open-links') {
      const page = Math.max(Number(query.get('page')) || 1, 1);
      const limit = Math.min(Number(query.get('limit')) > 0 ? Number(query.get('limit')) : 20, 50);
      const rows = api.openLinks.filter((l) => l.mode === mode);
      return ok({ items: rows.slice((page - 1) * limit, page * limit).map(openView), total: rows.length, page, limit }, 'Payment links');
    }
    const openLink = /^\/api\/pay\/merchant\/open-links\/([^/]+)(\/replace)?$/.exec(path);
    if (openLink) {
      const l = api.openLinks.find((x) => x.id === openLink[1] && x.mode === mode);
      if (!l) return fail(404, 'Link not found', 'link_not_found');
      if (method === 'GET' && !openLink[2]) {
        return ok({ openLink: openView(l), payments: api.payments.filter((p) => p.openLinkId === l.id).slice(0, 10) }, 'Payment link');
      }
      if (method === 'PUT' && !openLink[2]) {
        if (body.expectedVersion !== l.version) return fail(409, 'This link changed since you opened it — reload and try again', 'link_stale');
        const refused = openLinkRefused(body);
        if (refused) return refused;
        Object.assign(l, {
          label: String(body.label).trim(), minAmount: Number(body.minAmount ?? 0), maxAmount: Number(body.maxAmount ?? 0),
          active: Boolean(body.active), version: l.version + 1,
          // Left out, it stays as it is; 0 clears it.
          ...(body.paymentExpiresIn !== undefined ? { paymentExpiresIn: Number(body.paymentExpiresIn) } : {}),
        });
        return ok(openView(l), 'Payment link saved');
      }
      if (method === 'POST' && openLink[2]) {
        const slug = newSlug();
        Object.assign(l, { slug, url: openUrl(slug), version: l.version + 1 });
        return ok(openView(l), 'Payment link replaced');
      }
    }
    if (route === 'POST /api/pay/merchant/links') {
      if (m.status === 'suspended') return fail(403, 'Your merchant account is suspended — contact support', 'merchant_suspended');
      if (mode === 'live' && !m.liveAllowed) {
        return fail(403, "Live payments aren't switched on for your business yet. Try a test link first.", 'live_not_enabled');
      }
      if (!m.settlementMode) return fail(409, 'Set where you want to be paid before you request a payment', 'settlement_not_set');
      const amount = Number(body.amount);
      const description = String(body.description ?? '').trim();
      // No expiresInDays (or 0): the link never expires.
      const days = Number(body.expiresInDays ?? 0);
      if (!Number.isInteger(amount) || amount < 100_000 || amount > 500_000_000) return fail(422, 'Enter an amount between ₦1,000.00 and ₦5,000,000.00');
      if (!description || description.length > 255) return fail(422, "Say what the payment is for (up to 255 characters)");
      if (days && ![1, 3, 7].includes(days)) return fail(422, 'A link can be open for 1, 3 or 7 days');
      let reference = String(body.reference ?? '').trim();
      if (reference && api.payments.some((p) => p.mode === mode && p.reference === reference)) {
        return fail(409, "You've already used this reference — use another or leave it blank", 'reference_taken');
      }
      reference ||= `link_${String(api.payments.length).padStart(12, 'k')}`;
      const now = new Date();
      const id = `c0ffee00-0000-4000-8000-${String(api.payments.length).padStart(12, '0')}`;
      const made = payment(id, reference, mode, 'pending', amount, now.toISOString(), {
        description, source: 'link', ...(body.customerName ? { customerName: String(body.customerName).trim() } : {}),
        expiresAt: days ? new Date(now.getTime() + days * 24 * 60 * 60_000).toISOString() : null,
      });
      api.payments.unshift(made);
      return ok(made, 'Payment link created', 201);
    }
    const cancel = /^\/api\/pay\/merchant\/links\/([^/]+)\/cancel$/.exec(path);
    if (method === 'POST' && cancel) {
      const p = api.payments.find((x) => x.id === cancel[1] && x.mode === mode && x.source === 'link');
      if (!p) return fail(404, 'Payment not found');
      if (p.status !== 'pending') return fail(409, "This link can't be cancelled — it's already being paid, paid, or closed", 'not_cancellable');
      Object.assign(p, { status: 'expired', failureReason: MERCHANT_CANCELLED, cancelled: true });
      return ok(p, 'Payment link cancelled');
    }
    const one = /^\/api\/pay\/merchant\/payments\/([^/]+)$/.exec(path);
    if (method === 'GET' && one) {
      const p = api.payments.find((x) => x.id === one[1] && x.mode === mode);
      if (!p) return fail(404, 'Payment not found');
      const deliveries = p.status === 'succeeded' ? [{
        id: 'b7e0c1d2-5a4f-4c3e-8d2b-9f1a6e7c3d40', event: 'payment.succeeded', attempts: 1, lastStatusCode: 200,
        deliveredAt: p.succeededAt, createdAt: p.succeededAt!,
      }] : [];
      return ok({ payment: p, payer: p.status === 'pending' || p.status === 'expired' ? null : { name: 'Ada Obi' }, deliveries }, 'Payment');
    }
    if (route === 'GET /api/pay/merchant/banks') return ok([{ code: '044', name: 'Access Bank' }, { code: '058', name: 'GTBank' }], 'Banks');
    if (route === 'PUT /api/pay/merchant/business-name') {
      const refused = passwordRefused();
      if (refused) return refused;
      if (body.expectedVersion !== undefined && body.expectedVersion !== m.version) {
        return fail(409, 'Your payment settings changed since you opened this screen — reload and try again', 'merchant_stale');
      }
      // normalizeBusinessName: hidden characters, 2–100 characters (code points), reserved words — each
      // refused with the business_name_invalid code and the bare reason.
      const raw = String(body.name ?? '');
      const refuse = (why: string) => fail(422, why, 'business_name_invalid');
      if (/[\p{Cc}\p{Cf}]/u.test(raw)) return refuse("a business name can't contain hidden or control characters");
      const name = raw.trim().split(/\s+/).filter(Boolean).join(' ');
      if ([...name].length < 2 || [...name].length > 100) return refuse('a business name must be 2 to 100 characters');
      const folded = name.toLowerCase().replace(/\s+/g, '');
      const reserved = ['TSA Connect', 'TSAConnect', 'TSA Pay', 'TSAPay', 'support', 'refund'].find((w) => folded.includes(w.toLowerCase().replace(/\s+/g, '')));
      if (reserved) return refuse(`a business name can't contain "${reserved}"`);
      // The current name back files nothing, needs no code, and withdraws a rename still waiting.
      if (name === m.businessName) {
        delete m.pendingBusinessName;
        delete m.pendingSince;
        return ok({ merchant: m }, 'Business name unchanged');
      }
      return confirmation({ id: actionId(), kind: 'business_name', name });
    }
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
      if (action.kind === 'business_name') {
        // Files the request only: the name stays until an admin approves it.
        Object.assign(m, { pendingBusinessName: action.name, pendingSince: new Date().toISOString() });
        return ok({ merchant: m }, 'Change confirmed');
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
    const key = req.headers()['idempotency-key'];
    api.calls.push({ method, path: url.pathname, body, ...(key ? { idempotencyKey: key } : {}) });
    // The web app's CSRF header (webAuthGuard / middleware.Auth): every /auth/web call and every cookie write.
    if ((url.pathname.startsWith('/api/auth/web/') || method !== 'GET') && req.headers()['x-tsa-web'] !== '1') {
      await route.fulfill(fail(403, 'Missing request header', 'csrf'));
      return;
    }
    const replayKey = key && `${method} ${url.pathname} ${req.postData() ?? ''} ${key}`;
    const res = (replayKey && replays.get(replayKey)) || answer(method, url.pathname, url.searchParams, body);
    if (replayKey && res && res.status < 300) replays.set(replayKey, res);
    if (!res) {
      api.unhandled.push(`${method} ${url.pathname}`);
      await route.fulfill(fail(404, 'Not found'));
      return;
    }
    await route.fulfill(res);
  });
  return api;
}
