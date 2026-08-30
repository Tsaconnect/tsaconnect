# Stablecoin Buy UI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user buy USDC/USDT with naira from inside Instant Transfer and watch the purchase through to delivery.

**Architecture:** `buy.tsx` gains an asset picker that routes USDC/USDT to a new `buy-stable.tsx`, exactly as `sell.tsx` routes to `sell-stable.tsx`. The new screen resumes any unpaid purchase, quotes, creates, and hands off to the existing `intent/[id].tsx` progress screen. **The app never touches a key on this path** — no signing, no LiFi, no gas, no chain interaction.

**Tech Stack:** React Native 0.81, Expo 54, expo-router v6, TypeScript, Jest.

**Spec:** `docs/superpowers/specs/2026-08-31-stablecoin-buy-app-design.md`

## Global Constraints

- **Fiat crosses the wire as minor-unit integer strings** and stays a string until formatted. Never parse naira into a JS number for arithmetic.
- **Asset amounts are 6-decimal base-unit strings.** `sell.tsx` hardcodes 18 decimals in two places; using that here is wrong by 10¹².
- **The app never re-derives the price.** It displays what the backend quoted and what the backend settled.
- **Branch on the backend's machine codes**, never on message text: `stable_buy_below_minimum`, `stable_buy_already_open`, `stable_buy_no_va`, `stable_buy_no_wallet`, `stable_unsupported_chain_asset`, `stable_buy_unsupported_currency`, `stable_not_configured`.
- **The quote is indicative.** Display `≈` before the asset amount. No explanatory paragraph — the `≈` carries it.
- **The destination address is shown on the confirm step for every chain**, with an irreversibility warning. It is the only control on the Solana path, which the server cannot verify.
- **`buy-stable.tsx` target: under ~500 lines.** Money math and status mapping live in `utils/stableBuyQuote.ts` from the first commit, not extracted later.
- Run `npx tsc --noEmit` and `npm test` before every commit.

---

### Task 1: API client — types and the four buy endpoints

**Files:**
- Modify: `services/itApi.ts`
- Test: `services/__tests__/itApi.stableBuy.test.ts` (create)

**Interfaces:**
- Consumes: the existing `req<T>()` wrapper, `ApiResult<T>`, `makeIdempotencyKey()`
- Produces:
  - `interface ITStableBuyQuote { asset; currency; fiatMinor; grossUnits; assetUnits; ratePerUnit; expiresAt; indicative }` (all `string` except `indicative: boolean`)
  - `interface ITStableBuyPayInto { accountNumber; bankName; accountName; reference }` (all `string`)
  - `interface ITStableBuyResult { intent: ITIntent; payInto: ITStableBuyPayInto }`
  - `quoteStableBuy(asset, amountMinor, currency)`, `createStableBuy(asset, amountMinor, currency, destChain, key?)`, `getStableBuyChains(asset)`, `cancelStableBuy(intentId)`
  - `ITIntent` gains optional `destChain?: string` and `destAddress?: string`

**The trap in this task:** the existing `ITPayTo` (used by the MCGP buy) has a field named **`bank`**. The stablecoin buy endpoint returns **`bankName`** (`StableBuyInstructions` in `internal/services/it_stable_buy.go:153-158`). Reusing `ITPayTo` would compile and render an empty bank name at runtime. Define a separate type.

- [ ] **Step 1: Write the failing tests**

Create `services/__tests__/itApi.stableBuy.test.ts`:

```ts
import { createStableBuy, getStableBuyChains, quoteStableBuy } from '../itApi';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
});

describe('stablecoin buy API', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('quotes with minor units and reads the indicative flag', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({
        asset: 'USDC',
        currency: 'NGN',
        fiatMinor: '16502500',
        grossUnits: '100000000',
        assetUnits: '98450000',
        ratePerUnit: '167623',
        expiresAt: '2026-08-31T00:00:00Z',
        indicative: true,
      })
    );

    const res = await quoteStableBuy('USDC', '16502500', 'NGN');
    expect(res.success).toBe(true);
    expect(res.data?.assetUnits).toBe('98450000');
    // The screen keys off this rather than assuming the quote is soft.
    expect(res.data?.indicative).toBe(true);

    const [, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(JSON.parse(init.body)).toEqual({
      asset: 'USDC',
      amount: '16502500',
      currency: 'NGN',
    });
  });

  it('reads bankName, not bank, from payInto', async () => {
    // ITPayTo (the MCGP type) calls this field `bank`. The stablecoin buy
    // endpoint returns `bankName`. Reusing the wrong type renders blank.
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({
        intent: { id: 'i1', direction: 'buy', status: 'pending_payment' },
        payInto: {
          accountNumber: '1234567890',
          bankName: 'Wema Bank',
          accountName: 'TSA / Ada Okafor',
          reference: 'ITVA-x',
        },
      })
    );

    const res = await createStableBuy('USDC', '16502500', 'NGN', 'POLYGON');
    expect(res.data?.payInto.bankName).toBe('Wema Bank');
    expect(res.data?.payInto.accountNumber).toBe('1234567890');
  });

  it('sends an Idempotency-Key on create and reuses a caller-supplied one', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({ intent: { id: 'i1' }, payInto: {} })
    );

    await createStableBuy('USDC', '16502500', 'NGN', 'POLYGON', 'fixed-key-1');
    await createStableBuy('USDC', '16502500', 'NGN', 'POLYGON', 'fixed-key-1');

    const [, a] = (global.fetch as jest.Mock).mock.calls[0];
    const [, b] = (global.fetch as jest.Mock).mock.calls[1];
    // A retried create must not mint a second purchase.
    expect(a.headers['Idempotency-Key']).toBe('fixed-key-1');
    expect(b.headers['Idempotency-Key']).toBe('fixed-key-1');
  });

  it('asks for chains by asset', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ chains: ['POLYGON', 'SOLANA'] }));

    const res = await getStableBuyChains('USDT');
    expect(res.data?.chains).toEqual(['POLYGON', 'SOLANA']);
    const [url] = (global.fetch as jest.Mock).mock.calls[0];
    expect(String(url)).toContain('/it/stable/buy/chains?asset=USDT');
  });

  it('surfaces the backend machine code on failure', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: false,
      status: 400,
      json: async () => ({
        success: false,
        message: 'this user already has a purchase awaiting payment',
        code: 'stable_buy_already_open',
      }),
    });

    const res = await createStableBuy('USDC', '16502500', 'NGN', 'POLYGON');
    expect(res.success).toBe(false);
    expect(res.code).toBe('stable_buy_already_open');
    expect(res.status).toBe(400);
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx jest services/__tests__/itApi.stableBuy.test.ts`
Expected: FAIL — `quoteStableBuy is not a function`

- [ ] **Step 3: Implement**

Append to `services/itApi.ts`, after the stablecoin sell block:

```ts
// ──────────────────────────── stablecoin buy (brokered) ───────────────────
//
// Users buy USDT/USDC with naira. They pay into their own standing virtual
// account; Flutterwave converts and delivers to an address WE resolve from the
// database — never one the caller sends. See
// docs/superpowers/specs/2026-08-31-stablecoin-buy-app-design.md.

/** Machine codes the buy endpoints return. Branch on these, not messages. */
export const IT_STABLE_BUY_ALREADY_OPEN = 'stable_buy_already_open';
export const IT_STABLE_BUY_NO_VA = 'stable_buy_no_va';
export const IT_STABLE_BUY_NO_WALLET = 'stable_buy_no_wallet';
export const IT_STABLE_NOT_CONFIGURED = 'stable_not_configured';

export interface ITStableBuyQuote {
  asset: string;
  currency: string;
  /** What the user pays, in fiat MINOR units. */
  fiatMinor: string;
  /** Asset base units (6dp) before the company spread. */
  grossUnits: string;
  /** Asset base units (6dp) the user receives — what to display. */
  assetUnits: string;
  /** Fiat minor units per one whole asset unit, spread applied. */
  ratePerUnit: string;
  expiresAt: string;
  /** Always true today: the binding price is struck when the naira lands.
   *  The screen keys off this rather than assuming. */
  indicative: boolean;
}

/** Where the user sends their naira: their own standing virtual account.
 *
 *  NOT ITPayTo. That type names the field `bank`; this endpoint returns
 *  `bankName` (StableBuyInstructions, it_stable_buy.go). Reusing ITPayTo
 *  compiles and renders an empty bank at runtime. */
export interface ITStableBuyPayInto {
  accountNumber: string;
  bankName: string;
  accountName: string;
  reference: string;
}

export interface ITStableBuyResult {
  intent: ITIntent;
  payInto: ITStableBuyPayInto;
}

/** Indicative price. amountMinor is fiat MINOR units as a string. */
export const quoteStableBuy = (asset: string, amountMinor: string, currency: string) =>
  req<ITStableBuyQuote>('POST', '/it/stable/buy/quote', {
    asset,
    amount: amountMinor,
    currency,
  });

/** Opens a purchase. Pass a stable key so a retry of the SAME create is
 *  deduped server-side rather than minting a second purchase. */
export const createStableBuy = (
  asset: string,
  amountMinor: string,
  currency: string,
  destChain: string,
  idempotencyKey?: string
) =>
  req<ITStableBuyResult>(
    'POST',
    '/it/stable/buy',
    { asset, amount: amountMinor, currency, destChain },
    { idempotencyKey }
  );

/** Chains this user can actually receive `asset` on. A chain they hold no
 *  address for is never returned, so it can never be offered. */
export const getStableBuyChains = (asset: string) =>
  req<{ chains: string[] }>('GET', `/it/stable/buy/chains?asset=${encodeURIComponent(asset)}`);

/** Abandon an unpaid purchase. Without this the user is blocked from buying
 *  until the intent expires 24h later. */
export const cancelStableBuy = (intentId: string) =>
  req<{ cancelled: boolean }>('POST', `/it/stable/buy/${intentId}/cancel`);
```

Then extend `ITIntent` (around line 44, in the stablecoin block) with the buy-side delivery target:

```ts
  /** Buy-side delivery target. Resolved server-side from the user's wallet —
   *  never sent by the client. */
  destChain?: string;
  destAddress?: string;
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npx jest services/__tests__/itApi.stableBuy.test.ts && npx tsc --noEmit`
Expected: PASS, no type errors

- [ ] **Step 5: Commit**

```bash
git add services/itApi.ts services/__tests__/itApi.stableBuy.test.ts
git commit -m "feat(it): add the stablecoin buy endpoints to the API client"
```

---

### Task 2: Pure money math and status mapping

**Files:**
- Create: `utils/stableBuyQuote.ts`
- Test: `utils/__tests__/stableBuyQuote.test.ts`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `STABLE_BUY_STATUSES: readonly string[]`
  - `formatAssetUnits(units: string): string` — 6dp base units → display, trimmed
  - `formatMinorAsNaira(minor: string): string` — e.g. `"16502500"` → `"165,025.00"`
  - `buyStatusToHops(status: string): { label: string; state: 'done' | 'active' | 'pending' | 'failed' }[]`
  - `isTerminalBuyStatus(status: string): boolean`
  - `buyStatusLabel(status: string): string`

**Why `buy_awaiting_rate` is NOT terminal:** the money has landed but could not be priced, and an operator can resume it via `RepriceParkedStableBuy`, which returns the intent to `payment_received`. Treating it as terminal would stop polling and leave the user staring at a frozen screen after an operator fixed it.

- [ ] **Step 1: Write the failing tests**

Create `utils/__tests__/stableBuyQuote.test.ts`:

```ts
import {
  STABLE_BUY_STATUSES,
  buyStatusLabel,
  buyStatusToHops,
  formatAssetUnits,
  formatMinorAsNaira,
  isTerminalBuyStatus,
} from '../stableBuyQuote';

describe('formatAssetUnits', () => {
  it('converts 6-decimal base units to display', () => {
    // 18 decimals would be wrong here by 10^12 — sell.tsx hardcodes 18.
    expect(formatAssetUnits('98450000')).toBe('98.45');
    expect(formatAssetUnits('100000000')).toBe('100');
    expect(formatAssetUnits('1')).toBe('0.000001');
    expect(formatAssetUnits('0')).toBe('0');
  });

  it('is defensive about unusable input', () => {
    expect(formatAssetUnits('')).toBe('0');
    expect(formatAssetUnits('abc')).toBe('0');
  });
});

describe('formatMinorAsNaira', () => {
  it('groups thousands and always shows two decimals', () => {
    expect(formatMinorAsNaira('16502500')).toBe('165,025.00');
    expect(formatMinorAsNaira('100')).toBe('1.00');
    expect(formatMinorAsNaira('5')).toBe('0.05');
  });

  it('is defensive about unusable input', () => {
    expect(formatMinorAsNaira('')).toBe('0.00');
    expect(formatMinorAsNaira('abc')).toBe('0.00');
  });
});

describe('buyStatusToHops', () => {
  it('maps EVERY backend buy status', () => {
    // An unmapped status renders a blank progress screen. buy_awaiting_rate
    // is the worst case: the user's naira is held pending an operator.
    for (const s of STABLE_BUY_STATUSES) {
      const hops = buyStatusToHops(s);
      expect(hops.length).toBeGreaterThan(0);
      expect(buyStatusLabel(s)).not.toBe('');
    }
  });

  it('advances as the purchase progresses', () => {
    const paid = buyStatusToHops('payment_received');
    const delivering = buyStatusToHops('delivering');
    const doneCount = (hs: ReturnType<typeof buyStatusToHops>) =>
      hs.filter((h) => h.state === 'done').length;
    expect(doneCount(delivering)).toBeGreaterThan(doneCount(paid));
  });

  it('marks every hop done when settled', () => {
    expect(buyStatusToHops('settled').every((h) => h.state === 'done')).toBe(true);
  });

  it('flags the parked state as failed so it reads as needing attention', () => {
    expect(buyStatusToHops('buy_awaiting_rate').some((h) => h.state === 'failed')).toBe(true);
  });
});

describe('isTerminalBuyStatus', () => {
  it('treats only genuinely finished states as terminal', () => {
    expect(isTerminalBuyStatus('settled')).toBe(true);
    expect(isTerminalBuyStatus('cancelled')).toBe(true);
    expect(isTerminalBuyStatus('failed')).toBe(true);
  });

  it('does NOT treat the parked state as terminal', () => {
    // An operator can reprice it back to payment_received. Stopping the poll
    // would freeze the screen after they did.
    expect(isTerminalBuyStatus('buy_awaiting_rate')).toBe(false);
    expect(isTerminalBuyStatus('converting')).toBe(false);
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `npx jest utils/__tests__/stableBuyQuote.test.ts`
Expected: FAIL — cannot find module `../stableBuyQuote`

- [ ] **Step 3: Implement**

Create `utils/stableBuyQuote.ts`:

```ts
/**
 * Pure money math and status mapping for the stablecoin buy flow.
 *
 * Lives outside the screen for the reason utils/sellQuote.ts and
 * utils/stableSellQuote.ts do: the repo has no React Native test harness, so
 * anything that must be TESTED has to be free of React. Keeping it here from
 * the first commit is also what stops buy-stable.tsx growing the way
 * sell-stable.tsx did.
 */

/** Stablecoin base units are 6 decimals on every chain Flutterwave supports. */
const ASSET_DECIMALS = 6;

/** Every status a stablecoin BUY intent can hold, per the backend model.
 *  The mapping test enumerates this, so adding one there without a hop here
 *  fails CI rather than shipping a blank screen. */
export const STABLE_BUY_STATUSES = [
  'pending_payment',
  'payment_received',
  'awaiting_funds',
  'converting',
  'delivering',
  'settled',
  'buy_awaiting_rate',
  'cancelled',
  'failed',
] as const;

/** Finished for good. `buy_awaiting_rate` is deliberately absent: the money
 *  landed but could not be priced, and an operator can resume it back to
 *  payment_received — so the screen must keep polling. */
const TERMINAL = new Set(['settled', 'cancelled', 'failed']);

export function isTerminalBuyStatus(status: string): boolean {
  return TERMINAL.has((status || '').trim());
}

function digitsOnly(v: string): string {
  const t = (v ?? '').trim();
  return /^\d+$/.test(t) ? t : '';
}

/** 6-decimal base units → a display string with trailing zeros trimmed. */
export function formatAssetUnits(units: string): string {
  const d = digitsOnly(units);
  if (!d) return '0';
  const padded = d.padStart(ASSET_DECIMALS + 1, '0');
  const whole = padded.slice(0, -ASSET_DECIMALS);
  const frac = padded.slice(-ASSET_DECIMALS).replace(/0+$/, '');
  return frac ? `${whole}.${frac}` : whole;
}

/** Fiat minor units → grouped display with exactly two decimals. */
export function formatMinorAsNaira(minor: string): string {
  const d = digitsOnly(minor);
  if (!d) return '0.00';
  const padded = d.padStart(3, '0');
  const whole = padded.slice(0, -2);
  const frac = padded.slice(-2);
  return `${whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',')}.${frac}`;
}

export type HopState = 'done' | 'active' | 'pending' | 'failed';
export interface Hop {
  label: string;
  state: HopState;
}

const ORDER = ['payment_received', 'awaiting_funds', 'converting', 'delivering', 'settled'];
const LABELS: Record<string, string> = {
  pending_payment: 'Waiting for your transfer',
  payment_received: 'Payment received',
  awaiting_funds: 'Confirming your payment',
  converting: 'Buying',
  delivering: 'Sending to your wallet',
  settled: 'Delivered',
  buy_awaiting_rate: 'Needs review — we’ve been notified',
  cancelled: 'Cancelled',
  failed: 'Failed',
};

export function buyStatusLabel(status: string): string {
  return LABELS[(status || '').trim()] ?? 'In progress';
}

/** The four visible hops, with the current one active. */
export function buyStatusToHops(status: string): Hop[] {
  const s = (status || '').trim();
  const shown = ['awaiting_funds', 'converting', 'delivering', 'settled'];

  if (s === 'cancelled' || s === 'failed') {
    return shown.map((k) => ({ label: LABELS[k], state: 'pending' as HopState }));
  }
  if (s === 'buy_awaiting_rate') {
    // Money landed, unpriced, parked for an operator. Flagged rather than
    // merely stalled so it does not read as ordinary progress.
    return [
      { label: LABELS.payment_received, state: 'done' },
      { label: LABELS.buy_awaiting_rate, state: 'failed' },
    ];
  }

  const idx = ORDER.indexOf(s);
  return shown.map((k) => {
    const ki = ORDER.indexOf(k);
    if (idx < 0) return { label: LABELS[k], state: 'pending' as HopState };
    if (ki < idx) return { label: LABELS[k], state: 'done' as HopState };
    if (ki === idx) {
      return { label: LABELS[k], state: (s === 'settled' ? 'done' : 'active') as HopState };
    }
    return { label: LABELS[k], state: 'pending' as HopState };
  });
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `npx jest utils/__tests__/stableBuyQuote.test.ts && npx tsc --noEmit`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add utils/stableBuyQuote.ts utils/__tests__/stableBuyQuote.test.ts
git commit -m "feat(it): add pure money math and status mapping for stablecoin buys"
```

---

### Task 3: The buy screen and the asset picker

**Files:**
- Create: `app/wallet/it/buy-stable.tsx`
- Modify: `app/wallet/it/buy.tsx`

**Interfaces:**
- Consumes: Task 1's `quoteStableBuy`, `createStableBuy`, `getStableBuyChains`, `cancelStableBuy`, `ITStableBuyQuote`, `ITStableBuyPayInto`, the machine-code constants; Task 2's `formatAssetUnits`, `formatMinorAsNaira`
- Produces: the route `/wallet/it/buy-stable`, accepting a `?asset=USDC|USDT` param

Batched deliberately: the picker routes to a screen that must already exist, so the branch is never in a state where tapping USDC leads nowhere.

**Read `app/wallet/it/sell.tsx:170-190` first** — that is the picker pattern to mirror exactly. And read `app/wallet/it/buy.tsx` around the `CopyableField` / `SummaryRow` usage: the pay-into rendering already exists there and should be reused, not rebuilt.

- [ ] **Step 1: Add the asset picker to `buy.tsx`**

Mirror `sell.tsx`'s handler. Where `buy.tsx` currently assumes MCGP, add the same three-option picker and route the stablecoins away:

```tsx
const onAssetChange = (next: string) => {
  if (next === 'USDC' || next === 'USDT') {
    router.push({ pathname: '/wallet/it/buy-stable', params: { asset: next } });
    return;
  }
  setAsset('MCGP');
};
```

The MCGP path below it must not change — it is live.

- [ ] **Step 2: Create `buy-stable.tsx` with the resume check FIRST**

The screen has three states: `resume`, `form`, `payInto`. On mount it looks for an unpaid purchase before rendering the form, because the backend permits only one and an abandoned one blocks buying for 24 hours.

```tsx
// Step 0 of the flow, and it must come before the form renders.
// The backend allows one unpaid stablecoin buy per user
// (stable_buy_already_open). An abandoned one blocks buying until it expires
// 24h later — the cancel action below is the only self-service way out.
useEffect(() => {
  let cancelled = false;
  (async () => {
    const res = await listITIntents();
    if (cancelled || !res.success) return;
    const open = (res.data?.intents ?? []).find(
      (i) =>
        i.direction === 'buy' &&
        (i.asset === 'USDC' || i.asset === 'USDT') &&
        i.status === 'pending_payment'
    );
    if (open) setResuming(open);
    setChecked(true);
  })();
  return () => {
    cancelled = true;
  };
}, []);
```

- [ ] **Step 3: The form**

Asset (from the route param), naira amount, and a chain picker populated from `getStableBuyChains(asset)`. Render only the chains it returns — a chain the user holds no address for is never in the list, so it can never be chosen.

Handle the empty case explicitly: no chains means the user has no wallet address at all. Show a message directing them to wallet setup, not a raw error.

- [ ] **Step 4: The confirm step — show the address**

```tsx
<SummaryRow label="You'll receive" value={`≈ ${formatAssetUnits(quote.assetUnits)} ${quote.asset}`} />
<SummaryRow label="On" value={destChain} />
{/* FULL address, never truncated, and never inside SummaryRow (which
    ellipsises). Truncation defeats the only purpose this line has:
    address-poisoning attacks work by generating an address whose first
    and last characters match the victim's, so "0x742d…9f2A" verifies as
    correct against a completely different address. Wrap it instead. */}
<Text style={styles.addressLabel}>Sent to your wallet</Text>
<Text style={styles.addressValue} selectable numberOfLines={0}>
  {destAddress}
</Text>
<Text style={styles.warning}>
  Delivery is irreversible. Check this is your address.
</Text>
<SummaryRow label="You pay" value={`₦${formatMinorAsNaira(quote.fiatMinor)}`} />
```

The `≈` is required and carries the "this is an estimate" signal on its own — do not add an explanatory paragraph. Gate it on `quote.indicative` so it drops automatically if the backend ever returns a firm quote.

`addressValue` needs its own style: monospace where the platform has one, a small size that fits a 44-character Solana address across two lines, and `selectable` so the user can copy it into their wallet to compare. Do NOT put it in `SummaryRow` — that component lays out label and value on one line and will ellipsise a long value, silently reintroducing the truncation this step exists to avoid.

The address comes from the created intent's `destAddress`. If it is only available after create, show the confirm step post-create and before the user pays — it must appear before any money moves.

- [ ] **Step 5: Create and show the pay-into account**

Mint ONE idempotency key when the user enters the confirm step and reuse it for every retry of that same create, mirroring `sell-stable.tsx`. A retried create must not mint a second purchase.

Render `payInto` with the existing `CopyableField` for the account number and `SummaryRow` for bank and account name. **Use `payInto.bankName`** — not `bank`.

The "I've sent the money" button performs NO API call. It navigates to `/wallet/it/intent/<id>`. It must not imply the app has been told anything; the webhook drives everything.

- [ ] **Step 6: Error handling**

Branch on codes, never messages:

```tsx
switch (res.code) {
  case IT_STABLE_BUY_ALREADY_OPEN:
    // Step 0 should have caught this; fall back to the resume view.
    return showResume();
  case IT_STABLE_BUY_NO_WALLET:
  case IT_STABLE_BUY_NO_VA:
    return setError('Finish setting up your wallet to buy.');
  case IT_STABLE_NOT_CONFIGURED:
    return setError('Buying is temporarily unavailable.');
  default:
    return setError(res.message ?? 'Something went wrong.');
}
```

- [ ] **Step 7: Verify**

Run: `npx tsc --noEmit && npm test && npx expo export --platform ios 2>&1 | tail -5`
Expected: no type errors, tests pass, bundle resolves the new route.

Then check the file length: `wc -l app/wallet/it/buy-stable.tsx`. If it is over ~500 lines, move logic into `utils/stableBuyQuote.ts` rather than letting it grow — that is what happened to `sell-stable.tsx` (1,574 lines).

- [ ] **Step 8: Commit**

```bash
git add app/wallet/it/buy-stable.tsx app/wallet/it/buy.tsx
git commit -m "feat(it): add the stablecoin buy screen and asset picker"
```

---

### Task 4: Buy hops on the progress screen

**Files:**
- Modify: `app/wallet/it/intent/[id].tsx`

**Interfaces:**
- Consumes: Task 2's `buyStatusToHops`, `buyStatusLabel`, `isTerminalBuyStatus`; Task 1's `ITIntent.destChain` / `destAddress`

**Read the file first.** It already distinguishes the flows: `isStableAsset` at ~:136, `pollingActive` at ~:158, `isBuy = intent.direction === 'buy'` at ~:284. It currently imports `statusToHops` and `isTerminalStableStatus` from `stableSellQuote` — those are the SELL mappings and are simply the wrong functions for a buy intent, not merely unused ones.

- [ ] **Step 1: Route to the right mapping by direction**

Where the screen chooses hops, select the buy functions when `intent.direction === 'buy'` and the asset is a stablecoin. Do not widen the sell mapping to cover buy — the statuses differ (`buy_awaiting_rate` and `delivering` do not exist on the sell side).

- [ ] **Step 2: Fix the polling predicate for buy**

`pollingActive` currently uses `isTerminalStableStatus`, whose set is `{settled, reversed, failed, cancelled}`. For a buy intent use `isTerminalBuyStatus`. The difference that matters: `buy_awaiting_rate` must keep polling, because an operator can reprice it back to `payment_received` and the screen would otherwise stay frozen after they did.

- [ ] **Step 3: Show the delivery target on a buy**

Add `destChain` and the FULL `destAddress` for stablecoin buys, so the user can see where it went after the fact and reconcile it against their wallet. Render it the same way as the confirm step — selectable, wrapped, not truncated and not inside a component that ellipsises. Same reasoning: a truncated address cannot be verified against, which is the only thing displaying it achieves.

- [ ] **Step 4: Offer Cancel while unpaid**

When `direction === 'buy'`, the asset is a stablecoin, and status is `pending_payment`, show a Cancel action calling `cancelStableBuy(intent.id)`. Refresh on success. This is the same escape hatch as the buy screen's resume view — a user who reaches the intent from their history needs it too.

- [ ] **Step 5: Verify the MCGP path is untouched**

Run: `npx tsc --noEmit && npm test`

Then read the MCGP branches you did not change and confirm the buy additions are gated on `direction === 'buy' && isStableAsset`. An MCGP buy is also `direction === 'buy'` — gating on direction ALONE would apply stablecoin hops to every MCGP purchase and break a live screen.

- [ ] **Step 6: Commit**

```bash
git add app/wallet/it/intent/\[id\].tsx
git commit -m "feat(it): render stablecoin buy progress and cancel"
```

---

## Verification before merge

- `npx tsc --noEmit` clean
- `npm test` green
- Route resolves: open Buy → pick USDC → form renders with chains
- The MCGP buy path is unchanged end to end
- `buy-stable.tsx` is under ~500 lines

## Not in scope

The app cannot fix these; they are backend or operational:

- `type:"crypto"` delivery has never run on any chain, and no conversion has reached `SUCCESSFUL`. The first real buy is the canary.
- `buy_awaiting_rate` has no user-facing recovery by design — only an operator can resume it.
- The Solana delivery address is user-registered and unverifiable server-side. The confirm screen is the only control, which is why Task 3 Step 4 is not optional.
