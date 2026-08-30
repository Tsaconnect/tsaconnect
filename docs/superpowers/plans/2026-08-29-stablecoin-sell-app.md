# Stablecoin Sell (mobile app) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a user sell USDT/USDC for fiat from inside Instant Transfer, against the backend that shipped in `8172697`.

**Architecture:** `sell.tsx` gains an asset picker; USDC/USDT routes to a new `sell-stable` screen that quotes, creates the intent, gates on PIN, signs (a direct ERC-20 transfer on Polygon, LiFi elsewhere), reports the transaction hash, then hands off to a live progress view on the existing `intent/[id]` route. All money math lives in pure, unit-tested modules because this repo has no React Native test harness.

**Tech Stack:** React Native 0.81, Expo 54, expo-router v6, ethers v6, hand-rolled `fetch` services, jest. No react-query, no form library, no state library — plain `useState` and Context.

**Spec:** `docs/superpowers/specs/2026-08-29-stablecoin-sell-app-design.md`

**Plan review:** `docs/superpowers/plans/2026-08-29-app-plan-review.md` — this plan was revised
against it. Five Criticals and nine Importants were found and fixed before any code was written.

**Re-review:** `docs/superpowers/plans/2026-08-29-app-plan-rereview.md` — revision 1 closed the hard
ones and opened a new cluster in Tasks 6–7. See the **Revision 2** note at the bottom for what this
pass closed.

**Backend prerequisites: LANDED** (merge `32a137b`, plus the `tokenAddress` change below). The
reviews found four gaps that made parts of this plan unimplementable or dishonest:
- `Idempotency-Key` is now enforced on every IT write route (it was wired only to P2P), so a retried
  create genuinely cannot mint a second intent. Note the middleware returns **409 when a key is
  replayed with a different payload**, and **caches 2xx responses for replay of the same key** —
  both facts drive the per-review-snapshot key in Task 7 Step 1.
- The stable handlers now emit a machine-readable `code`, so the app can branch on
  `stable_deposit_already_claimed` rather than matching error strings.
- `ITIntent.depositAddress` is persisted and returned, so an abandoned sell is resumable.
- `StableDepositInstructions` now carries **`tokenAddress`** — the exact ERC-20 contract
  `ConfirmStableDeposit` verifies the arrival against (`s.stableTokens[asset]`), and
  `CreateStableSellIntent` fails closed with `ErrStableNoTokenAddress` when it is unconfigured.
  The app must use it rather than resolving a Polygon contract itself; see Task 7 Step 4.

## Global Constraints

- **The platform is a broker.** The user's stablecoin goes from their wallet straight to Flutterwave's Polygon address. The app must NEVER send funds to an address the platform controls, and must never hardcode Flutterwave's address — it comes from the create-intent response.
- **The destination TOKEN CONTRACT also comes from the create-intent response**, never from an app-side symbol lookup. Polygon carries two live USDC contracts — native USDC (`0x3c499c54…`) and bridged USDC.e (`0x2791Bca1…`) — and the backend verifies the arrival against exactly one of them (`s.stableTokens[asset]`, `it_stable_sell.go:361`). Delivering the other one means `VerifyTransfer` finds no matching transfer, the deposit is never confirmed, and the user's funds sit at Flutterwave unattributed. Use `created.data.deposit.tokenAddress`.
- **Quote against the ARRIVAL, not the amount typed.** The backend pays `received/quoted`. Quoting the typed amount makes the final figure always shrink.
- **Stablecoins are 6 decimals, MCGP is 18.** Use `p2pTokenDecimals(chainId, symbol)` from `services/p2p.ts` — never a hardcoded `18`. `sell.tsx` hardcodes 18 in two places; copying that for USDC is wrong by 10¹².
- **Money crosses the wire as minor-unit integer STRINGS.** Never parse fiat into a JS number for arithmetic; format only at the display edge.
- **The app never re-derives a payout.** It displays what the backend quoted and what the backend settled.
- **Never lose a broadcast transaction hash.** Persist it before reporting it.
- **Test command:** `npx jest --watchAll=false <path>` from `tsa-app/`. The `test` script is `jest --watchAll`, which does not exit — always pass `--watchAll=false` in CI-style runs.
- Screens are not unit-tested (no `@testing-library/react-native`). Logic goes in `utils/` and is tested there. This is the repo's stated convention — see the header of `utils/__tests__/sellQuote.test.ts`.
- Do not commit anything under `docs/`.

---

## File Structure

| File | Responsibility |
|---|---|
| `utils/stableSellQuote.ts` (create) | Pure money math: arrival estimate, fiat composition, minimum floor, status→hops. No React, no I/O. |
| `utils/__tests__/stableSellQuote.test.ts` (create) | Unit tests for the above. |
| `services/itApi.ts` (modify) | `status` on `ApiResult`, `Idempotency-Key` on non-GET, plus the three stable endpoints. |
| `services/__tests__/itStableApi.test.ts` (create) | Wrapper + endpoint tests. |
| `components/it/BankDestination.tsx` (create) | Bank picker + debounced NUBAN name enquiry, extracted from `sell.tsx` and shared by both sell flows. |
| `app/wallet/it/sell.tsx` (modify) | Asset picker; USDC/USDT routes away. MCGP path otherwise untouched. |
| `app/wallet/it/sell-stable.tsx` (create) | `form → review → signing → handoff`. |
| `app/wallet/it/intent/[id].tsx` (modify) | Hop progress + polling when asset ≠ MCGP. |

**Task order is dependency order.** Tasks 1–3 are pure logic and transport with no UI; Task 4 extracts the shared destination block; Tasks 5–7 build the sell screen through signing; Task 8 is the progress view.

---

## Task 1: Pure money math for the stablecoin sell

**Files:**
- Create: `tsa-app/utils/stableSellQuote.ts`
- Test: `tsa-app/utils/__tests__/stableSellQuote.test.ts`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `STABLE_MIN_SEND: Record<'USDC'|'USDT', string>` — major-unit floor strings
  - `belowMinimum(asset: 'USDC'|'USDT', amountMajor: string): boolean`
  - `STABLE_MAX_DECIMALS = 6` and `exceedsStablePrecision(amountMajor: string): boolean`
  - `POLYGON_STABLE_DECIMALS = 6` — USDC/USDT on the DESTINATION chain
  - `estimateArrival(sentMajor: string, lifiToAmountBase: string | null, destDecimals: number): string` — throws `ErrArrivalUnpriceable` when LiFi returned nothing usable
  - `ErrArrivalUnpriceable` — an error **class** carrying `code === 'arrival_unpriceable'`, so the screen blocks instead of silently quoting the typed amount
  - `formatSendVsArrive(sentMajor: string, arriveMajor: string): string | null`

- [ ] **Step 1: Write the failing test**

Create `tsa-app/utils/__tests__/stableSellQuote.test.ts`:

```ts
// utils/__tests__/stableSellQuote.test.ts
//
// Pure logic for the stablecoin sell screen. Tested here rather than by
// mounting the screen: this repo has no @testing-library/react-native, so
// money math is extracted to be testable at all (same convention as
// buyQuote.ts / sellQuote.ts).

import {
  STABLE_MIN_SEND,
  STABLE_MAX_DECIMALS,
  POLYGON_STABLE_DECIMALS,
  ErrArrivalUnpriceable,
  belowMinimum,
  exceedsStablePrecision,
  estimateArrival,
  formatSendVsArrive,
} from '../stableSellQuote';

describe('belowMinimum', () => {
  it('rejects amounts under the provider floor', () => {
    // Flutterwave's documented outbound floor is 10 USDC/USDT. Quoting under
    // it lets a user broadcast irreversibly and only then be rejected, with
    // the funds already at Flutterwave.
    expect(belowMinimum('USDC', '1')).toBe(true);
    expect(belowMinimum('USDC', '9.999999')).toBe(true);
    expect(belowMinimum('USDT', '0')).toBe(true);
  });

  it('accepts the floor exactly and above', () => {
    expect(belowMinimum('USDC', STABLE_MIN_SEND.USDC)).toBe(false);
    expect(belowMinimum('USDC', '10')).toBe(false);
    expect(belowMinimum('USDT', '100.5')).toBe(false);
  });

  it('treats junk as below the minimum rather than throwing', () => {
    // The amount comes from a text input; a half-typed "1." must not crash
    // the screen or, worse, pass the guard.
    expect(belowMinimum('USDC', '')).toBe(true);
    expect(belowMinimum('USDC', '.')).toBe(true);
    expect(belowMinimum('USDC', 'abc')).toBe(true);
  });

  it('rejects scientific notation, which the very next call would throw on', () => {
    // Number('1e1') is 10 and clears the floor, but parseTokenAmount
    // (p2p.ts:874-880) THROWS on scientific notation. A guard that admits an
    // input the next call rejects turns a typo into a crashed quote effect.
    // Both use the same /^\d+(\.\d+)?$/ shape.
    expect(belowMinimum('USDC', '1e1')).toBe(true);
    expect(belowMinimum('USDC', '1e3')).toBe(true);
    expect(belowMinimum('USDC', '0x10')).toBe(true);
  });
});

describe('exceedsStablePrecision', () => {
  it('accepts up to six fractional digits', () => {
    expect(exceedsStablePrecision('10')).toBe(false);
    expect(exceedsStablePrecision('10.1')).toBe(false);
    expect(exceedsStablePrecision('10.123456')).toBe(false);
    expect(STABLE_MAX_DECIMALS).toBe(6);
  });

  it('rejects more than six, which the backend 400s on', () => {
    // StableToBaseUnits rejects anything with more than 6 fractional digits
    // (stable_assets.go:89-92) → ErrStablePrecision → 400. Caught at the input
    // instead, a user typing 10.1234567 gets "at most 6 decimal places"
    // rather than an unexplained failure at the quote.
    expect(exceedsStablePrecision('10.1234567')).toBe(true);
    expect(exceedsStablePrecision('0.0000001')).toBe(true);
  });
});

describe('estimateArrival', () => {
  it('returns the sent amount unchanged when there is no bridge', () => {
    // Polygon source: no LiFi, so what is sent is what arrives.
    expect(estimateArrival('100', null, 6)).toBe('100');
  });

  it('converts LiFi base units to a major-unit string', () => {
    // 99.884217 USDC delivered against 100 sent.
    expect(estimateArrival('100', '99884217', 6)).toBe('99.884217');
  });

  it('does not confuse 6-decimal and 18-decimal scaling', () => {
    // The whole point of threading decimals through: 99884217 at 18 decimals
    // is dust, not 99.88. Pin the EXACT wrong answer rather than asserting
    // "something changed" — `.not.toBe(...)` passes against any bug at all.
    expect(estimateArrival('100', '99884217', 18)).toBe('0.000000000099884217');
  });

  it('THROWS when LiFi returned nothing usable, rather than quoting the typed amount', () => {
    // The bug this replaces: treating '0' as "no bridge" made the app quote
    // against what the user TYPED, which is exactly the bait-and-switch the
    // arrival estimate exists to prevent — and silently. `null` means "no
    // bridge" (Polygon only); an unusable LiFi answer must block the quote.
    //
    // Assert the CODE, not the message. toThrow(instance) compares message
    // equality, so a message-only assertion also passes for an unrelated
    // error a later refactor happens to throw with the same text.
    expect(() => estimateArrival('100', '', 6)).toThrow(ErrArrivalUnpriceable);
    expect(() => estimateArrival('100', '0', 6)).toThrow(ErrArrivalUnpriceable);
    try {
      estimateArrival('100', 'not-a-number', 6);
      throw new Error('expected estimateArrival to throw');
    } catch (e) {
      expect(e).toBeInstanceOf(ErrArrivalUnpriceable);
      expect((e as ErrArrivalUnpriceable).code).toBe('arrival_unpriceable');
    }
  });

  it('uses DESTINATION decimals, never the source chain\'s', () => {
    // LiFi's toAmount is always denominated on Polygon (toChain 137), where
    // USDC/USDT are 6 decimals. BSC stablecoins are genuinely 18. Using the
    // source's decimals here yields 0.000000000099884217, which the backend
    // rejects outright as over-precise.
    expect(estimateArrival('100', '99884217', POLYGON_STABLE_DECIMALS)).toBe('99.884217');
  });
});

describe('formatSendVsArrive', () => {
  it('returns null when nothing is lost in transit', () => {
    // Same-chain: showing "send 100 · arrives 100" is noise.
    expect(formatSendVsArrive('100', '100')).toBeNull();
  });

  it('describes the shortfall when a bridge takes a cut', () => {
    expect(formatSendVsArrive('100', '99.884217')).toBe('You send 100 · about 99.884217 arrives');
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-app && npx jest --watchAll=false utils/__tests__/stableSellQuote.test.ts`
Expected: FAIL — `Cannot find module '../stableSellQuote'`

- [ ] **Step 3: Write the implementation**

Create `tsa-app/utils/stableSellQuote.ts`:

```ts
// utils/stableSellQuote.ts
//
// Pure money logic for the stablecoin sell flow. No React, no I/O — the
// screen is untestable in this repo (no @testing-library/react-native), so
// anything that touches an amount lives here and is unit-tested.

import { formatTokenAmount } from '@/services/p2p';

export type StableAsset = 'USDC' | 'USDT';

/** USDC/USDT decimals on the DESTINATION chain. LiFi's toAmount is always
 *  denominated on Polygon (toChain 137) regardless of where the user sends
 *  from — BSC stablecoins are 18 decimals, and using that here is wrong by
 *  10^12. Source-side parsing uses the SOURCE token's decimals instead. */
export const POLYGON_STABLE_DECIMALS = 6;

/**
 * LiFi could not price the bridge. Blocks the quote rather than letting the
 * screen fall back to the amount the user typed.
 *
 * A CLASS, not a shared module-level `new Error(...)`. One shared instance
 * carries a single stack captured at module load, so every report points at
 * module init instead of the failing call; and `e instanceof <instance>` — the
 * obvious next thing someone writes — throws `TypeError: Right-hand side of
 * 'instanceof' is not callable`. `code` is the stable thing to branch on,
 * because Jest's toThrow(err) matches on MESSAGE equality alone.
 */
export class ErrArrivalUnpriceable extends Error {
  readonly code = 'arrival_unpriceable';
  constructor(message = 'Could not price this route — try another chain') {
    super(message);
    this.name = 'ErrArrivalUnpriceable';
    // Required for `instanceof` to survive TS's ES5 class-extends-builtin
    // downlevelling; harmless when targeting ES2015+.
    Object.setPrototypeOf(this, ErrArrivalUnpriceable.prototype);
  }
}

/**
 * Fractional digits the backend accepts on a stablecoin amount.
 * `StableToBaseUnits` rejects anything longer (stable_assets.go:89-92) with
 * `ErrStablePrecision` → 400, so this is checked at the input rather than
 * discovered as an unexplained failure after a round trip.
 */
export const STABLE_MAX_DECIMALS = 6;

/** Strict decimal shape. Deliberately the SAME grammar `parseTokenAmount`
 *  enforces (p2p.ts:874-880) so no guard here admits an input the next call
 *  throws on. Rejects "1e1", "1.2.3", "0x10", "abc", "" and ".". */
const DECIMAL_RE = /^\d+(\.\d+)?$/;

/** True when the typed amount carries more precision than the backend accepts. */
export function exceedsStablePrecision(amountMajor: string): boolean {
  const frac = amountMajor.trim().split('.')[1] ?? '';
  return frac.length > STABLE_MAX_DECIMALS;
}

/**
 * Flutterwave's documented outbound floor is 10 USDC/USDT.
 *
 * This is a USABILITY guard, not the enforcement point: a client-side check
 * is bypassable and does not protect a direct API caller. The backend's own
 * minimum (its review finding I5) is the real fix and is still open. Without
 * either, a 1 USDC sell quotes cleanly, the user broadcasts irreversibly, and
 * the provider rejects the conversion with the funds already at Flutterwave.
 */
export const STABLE_MIN_SEND: Record<StableAsset, string> = {
  USDC: '10',
  USDT: '10',
};

/** True when the amount is unusable or under the provider floor. */
export function belowMinimum(asset: StableAsset, amountMajor: string): boolean {
  const trimmed = amountMajor.trim();
  // Shape first, then value. Number('1e1') is 10 and would clear the floor,
  // but parseTokenAmount throws on scientific notation — so a Number()-only
  // guard passes an input the next call rejects.
  if (!DECIMAL_RE.test(trimmed)) return true;
  const n = Number(trimmed);
  if (!Number.isFinite(n) || n <= 0) return true;
  return n < Number(STABLE_MIN_SEND[asset]);
}

/**
 * What we expect to ARRIVE at Flutterwave, in major units.
 *
 * The backend pays out `received / quoted`, so the fiat quote must be composed
 * against this rather than against the amount typed — otherwise the figure the
 * user was shown always shrinks, which reads as a bait-and-switch.
 *
 * `lifiToAmountBase` is LiFi's estimated destination amount in BASE units, or
 * null on a same-chain (Polygon) send where there is no bridge at all.
 */
export function estimateArrival(
  sentMajor: string,
  lifiToAmountBase: string | null,
  destDecimals: number
): string {
  // null means there is genuinely no bridge (Polygon source): what is sent is
  // what arrives. Anything else falsy means we could NOT price the bridge, and
  // those two cases must never be conflated — quoting the typed amount because
  // LiFi failed is the silent bait-and-switch this function exists to prevent.
  if (lifiToAmountBase === null) return sentMajor;
  const asNumber = Number(lifiToAmountBase);
  if (!lifiToAmountBase || !Number.isFinite(asNumber) || asNumber <= 0) {
    throw new ErrArrivalUnpriceable();
  }
  // destDecimals is the DESTINATION chain's (Polygon = 6), never the source's.
  // formatTokenAmount is pure string math; displayDecimals is pinned so we do
  // not silently round away real value.
  return formatTokenAmount(lifiToAmountBase, destDecimals, destDecimals);
}

/** A "you send X · about Y arrives" line, or null when they are equal. */
export function formatSendVsArrive(sentMajor: string, arriveMajor: string): string | null {
  if (Number(sentMajor) === Number(arriveMajor)) return null;
  return `You send ${sentMajor} · about ${arriveMajor} arrives`;
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-app && npx jest --watchAll=false utils/__tests__/stableSellQuote.test.ts`
Expected: PASS (13 tests — 4 `belowMinimum`, 2 `exceedsStablePrecision`, 5 `estimateArrival`,
2 `formatSendVsArrive`)

- [ ] **Step 5: Commit**

```bash
git add tsa-app/utils/stableSellQuote.ts tsa-app/utils/__tests__/stableSellQuote.test.ts
git commit -m "feat(it): pure money math for the stablecoin sell flow"
```

---

## Task 2: Status → progress hops

Kept separate from Task 1 because it is the guard against a whole class of silent failure: the backend added four statuses on its branch, and an unmapped fifth must fail a test rather than render a blank screen.

**Files:**
- Modify: `tsa-app/utils/stableSellQuote.ts`
- Test: `tsa-app/utils/__tests__/stableSellQuote.test.ts`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `STABLE_SELL_STATUSES: readonly string[]`
  - `type HopState = 'done' | 'active' | 'pending' | 'failed'`
  - `type Hop = { key: string; label: string; state: HopState }`
  - `statusToHops(status: string): Hop[]`
  - `isTerminalStableStatus(status: string): boolean`

- [ ] **Step 1: Write the failing test**

Append to `tsa-app/utils/__tests__/stableSellQuote.test.ts`:

```ts
import {
  STABLE_SELL_STATUSES,
  statusToHops,
  isTerminalStableStatus,
} from '../stableSellQuote';

describe('statusToHops', () => {
  it('maps every status the backend can emit', () => {
    // The guard that matters: the backend added four statuses in one branch.
    // If a fifth arrives and nothing maps it, this fails here rather than
    // rendering an empty progress screen to someone whose money is in flight.
    //
    // `length > 0` and "labels are non-empty" are NOT enough: the unknown
    // fallback returns all four hops with their labels, all `pending`, so an
    // unmapped status would sail through. Assert something only a MAPPED
    // status can satisfy — at least one hop is not `pending`. True for all
    // nine below (settled is all-done; reversed/failed/cancelled carry a
    // `failed`; the rest carry an `active`) and false for a tenth nobody
    // mapped.
    for (const status of STABLE_SELL_STATUSES) {
      const hops = statusToHops(status);
      expect(hops.length).toBeGreaterThan(0);
      expect(hops.every((h) => h.label.length > 0)).toBe(true);
      expect(hops.some((h) => h.state !== 'pending')).toBe(true);
    }
  });

  it('advances the active hop as the sell progresses', () => {
    const reported = statusToHops('deposit_reported');
    expect(reported.find((h) => h.key === 'sent')?.state).toBe('done');
    expect(reported.find((h) => h.key === 'arrived')?.state).toBe('active');

    const converting = statusToHops('converting');
    expect(converting.find((h) => h.key === 'arrived')?.state).toBe('done');
    expect(converting.find((h) => h.key === 'converted')?.state).toBe('active');
  });

  it('marks every hop done when settled', () => {
    expect(statusToHops('settled').every((h) => h.state === 'done')).toBe(true);
  });

  it('fails the PAYOUT hop on a reversal, not the wallet transfer', () => {
    // `reversed` means the funds DID send, DID arrive and WERE converted — the
    // payout is what failed. Marking hop 0 failed would tell a user their
    // wallet transfer failed when it succeeded and their money is sitting at
    // Flutterwave. Assert WHICH hop failed, not merely that one did.
    const hops = statusToHops('reversed');
    expect(hops.find((h) => h.key === 'sent')?.state).toBe('done');
    expect(hops.find((h) => h.key === 'arrived')?.state).toBe('done');
    expect(hops.find((h) => h.key === 'converted')?.state).toBe('done');
    expect(hops.find((h) => h.key === 'paid')?.state).toBe('failed');
  });

  it('fails the FIRST hop when the sell was cancelled before anything moved', () => {
    // cancelled/failed can occur before a deposit ever lands, so nothing may
    // be claimed as done. Assert WHICH hop failed — `every(h => h.state !==
    // 'done')` is the same too-weak shape this suite already rejected for
    // `reversed`, and passes against an all-`pending` implementation that
    // shows no failure at all.
    const hops = statusToHops('cancelled');
    expect(hops[0].state).toBe('failed');
    expect(hops.every((h) => h.state !== 'done')).toBe(true);
  });

  it('never claims progress for an unknown status', () => {
    // Fail visibly-neutral, not falsely-complete. Assert the exact fallback
    // shape: `!== 'done'` alone also passes an implementation that returns
    // `active` for every hop, which reads as four things happening at once.
    const hops = statusToHops('something_new');
    expect(hops.every((h) => h.state === 'pending')).toBe(true);
  });
});

describe('isTerminalStableStatus', () => {
  it('stops polling only on terminal states', () => {
    expect(isTerminalStableStatus('settled')).toBe(true);
    expect(isTerminalStableStatus('reversed')).toBe(true);
    expect(isTerminalStableStatus('failed')).toBe(true);
    expect(isTerminalStableStatus('cancelled')).toBe(true);

    expect(isTerminalStableStatus('pending_deposit')).toBe(false);
    expect(isTerminalStableStatus('deposit_reported')).toBe(false);
    expect(isTerminalStableStatus('deposit_confirmed')).toBe(false);
    expect(isTerminalStableStatus('converting')).toBe(false);
    // `debited` means the payout is in flight but the money has NOT reached
    // the user's bank — polling must continue.
    expect(isTerminalStableStatus('debited')).toBe(false);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-app && npx jest --watchAll=false utils/__tests__/stableSellQuote.test.ts`
Expected: FAIL — `statusToHops is not a function`

- [ ] **Step 3: Write the implementation**

Append to `tsa-app/utils/stableSellQuote.ts`:

```ts
export type HopState = 'done' | 'active' | 'pending' | 'failed';
export type Hop = { key: string; label: string; state: HopState };

/**
 * Every status the backend's stablecoin sell can be in.
 *
 * Mirrors the ITStatus* constants in internal/models/it_models.go. Kept as an
 * explicit list so the "every status maps to hops" test can enumerate it.
 */
export const STABLE_SELL_STATUSES = [
  'pending_deposit',
  'deposit_reported',
  'deposit_confirmed',
  'converting',
  'debited',
  'settled',
  'reversed',
  // `failed` and `cancelled` are very likely UNREACHABLE for a stablecoin
  // sell: they are set only on the MCGP paths (it_service.go:373, 436, 441,
  // 621, 625; it_fiat_va.go:220), and the stable chain deliberately ends at
  // the `debited → settled` pair (it_models.go:39-41). They are listed and
  // mapped anyway because they are terminal, cheap to map, and shared with
  // MCGP on the same intent row — but do not read this list as evidence that
  // a stable sell can reach them.
  'failed',
  'cancelled',
] as const;

const TERMINAL = new Set(['settled', 'reversed', 'failed', 'cancelled']);

/** Terminal states stop the progress screen's polling. */
export function isTerminalStableStatus(status: string): boolean {
  return TERMINAL.has(status);
}

// Ordered hops the user sees. `debited` is deliberately its own hop: the fiat
// exists but has not reached the bank, and that gap is the one users ask about.
const HOPS: { key: string; label: string }[] = [
  { key: 'sent', label: 'Sent from your wallet' },
  { key: 'arrived', label: 'Arrived at Flutterwave' },
  { key: 'converted', label: 'Converted to ₦' },
  { key: 'paid', label: 'Paid to your bank' },
];

// How far along each status is: the index of the hop currently in flight.
const PROGRESS: Record<string, number> = {
  pending_deposit: 0,
  deposit_reported: 1,
  deposit_confirmed: 2,
  converting: 2,
  debited: 3,
  settled: 4,
};

export function statusToHops(status: string): Hop[] {
  // `reversed` is NOT a generic failure: it means the deposit arrived and was
  // converted, and only the bank payout failed. Telling that user their wallet
  // transfer failed would be flatly untrue while their money sits at
  // Flutterwave. cancelled/failed can occur before anything moved, so those
  // claim nothing.
  if (status === 'reversed') {
    return HOPS.map((h) => ({
      ...h,
      state: h.key === 'paid' ? ('failed' as HopState) : ('done' as HopState),
    }));
  }
  if (status === 'failed' || status === 'cancelled') {
    return HOPS.map((h, i) => ({ ...h, state: i === 0 ? 'failed' : 'pending' }));
  }
  const reached = PROGRESS[status];
  if (reached === undefined) {
    // Unknown status: claim nothing. Better a blank-looking screen than a
    // false "paid" for someone whose money is still in flight.
    return HOPS.map((h) => ({ ...h, state: 'pending' }));
  }
  return HOPS.map((h, i) => ({
    ...h,
    state: i < reached ? 'done' : i === reached ? 'active' : 'pending',
  }));
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-app && npx jest --watchAll=false utils/__tests__/stableSellQuote.test.ts`
Expected: PASS (20 tests — Task 1's 13, plus 6 `statusToHops` and 1 `isTerminalStableStatus`)

- [ ] **Step 5: Commit**

```bash
git add tsa-app/utils/stableSellQuote.ts tsa-app/utils/__tests__/stableSellQuote.test.ts
git commit -m "feat(it): map stablecoin sell statuses to progress hops"
```

---

## Task 3: API client — status, idempotency, and the three endpoints

**Files:**
- Modify: `tsa-app/services/itApi.ts`
- Test: `tsa-app/services/__tests__/itStableApi.test.ts`

**Interfaces:**
- Consumes: `estimateArrival` is NOT used here — this task is transport only.
- Produces:
  - `ApiResult<T>` gains `status?: number`
  - `StableQuote = { asset, currency, amount, gross, afterSpread, fee, net, rate, expiresAt }` (all strings except `expiresAt`)
  - `StableDeposit = { chain: string; address: string; asset: string; tokenAddress: string; amount: string }`
  - `quoteStableSell(body: { asset: StableAsset; amount: string; currency?: string }): Promise<ApiResult<StableQuote>>`
  - `createStableSell(body: {...}): Promise<ApiResult<{ intent: ITIntent; deposit: StableDeposit }>>`
  - `reportStableDeposit(intentId: string, txHash: string): Promise<ApiResult<{ recorded: boolean }>>`
  - `IT_STABLE_DEPOSIT_CLAIMED = 'stable_deposit_already_claimed'`
  - `ITIntent` extended with the stablecoin fields (below) — Task 8 reads them and they are absent today
  - `makeIdempotencyKey(): string` (exported — Task 7 holds one key across retries)

- [ ] **Step 1: Write the failing test**

Create `tsa-app/services/__tests__/itStableApi.test.ts`:

```ts
// services/__tests__/itStableApi.test.ts
//
// Transport-level tests for the stablecoin sell endpoints. Mirrors the
// approach in services/__tests__/lifiFlow.test.ts: stub global.fetch, assert
// on what we sent and how we interpreted the reply.
//
// The AsyncStorage mock is NOT optional. itApi imports getAuthHeaders from
// walletApi, which calls AsyncStorage.getItem; there is no `setupFiles` in
// this repo's jest config, so without the mock getAuthHeaders throws, req's
// catch swallows it, fetch is never called, and every assertion fails on
// mock.calls[0] being undefined — a TypeError masking whatever the test meant
// to prove. Same line as services/__tests__/sendRetry.test.ts:1-3.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import {
  quoteStableSell,
  createStableSell,
  reportStableDeposit,
} from '../itApi';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
  text: async () => JSON.stringify({ success: true, data }),
});

const errJson = (status: number, message: string, code?: string) => ({
  ok: false,
  status,
  json: async () => ({ success: false, message, code }),
  text: async () => JSON.stringify({ success: false, message, code }),
});

describe('stablecoin sell transport', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('sends an Idempotency-Key when creating a sell', async () => {
    // Every intent is a claim on a deposit, so a retried create must not
    // silently make a second one.
    (global.fetch as jest.Mock).mockResolvedValue(
      okJson({
        intent: { id: 'i1' },
        deposit: {
          chain: 'POLYGON',
          address: '0xabc',
          asset: 'USDC',
          // The contract the backend will verify the arrival against. The app
          // never resolves this itself — see StableDeposit.tokenAddress.
          tokenAddress: '0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359',
          amount: '100.000000',
        },
      })
    );

    await createStableSell({
      asset: 'USDC',
      amount: '100',
      currency: 'NGN',
      sourceChain: 'BSC',
      bank: { code: '044', number: '0690000031', name: 'Ada Okafor' },
    });

    const [, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(init.headers['Idempotency-Key']).toBeTruthy();
  });

  it('reuses one idempotency key across retries of the same create', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ intent: { id: 'i1' }, deposit: {} }));
    const body = {
      asset: 'USDC' as const,
      amount: '100',
      currency: 'NGN',
      sourceChain: 'BSC',
      bank: { code: '044', number: '0690000031', name: 'Ada Okafor' },
      idempotencyKey: 'fixed-key-1',
    };

    await createStableSell(body);
    await createStableSell(body);

    const first = (global.fetch as jest.Mock).mock.calls[0][1].headers['Idempotency-Key'];
    const second = (global.fetch as jest.Mock).mock.calls[1][1].headers['Idempotency-Key'];
    expect(first).toBe('fixed-key-1');
    expect(second).toBe('fixed-key-1');
  });

  it('surfaces the HTTP status so callers can tell 400 from 503', async () => {
    // A 400 is the user's to fix; a 503 is worth retrying. Without the status
    // the screen cannot tell them apart.
    (global.fetch as jest.Mock).mockResolvedValue(errJson(503, 'pricing unavailable'));
    const r = await quoteStableSell({ asset: 'USDC', amount: '100' });
    expect(r.success).toBe(false);
    expect(r.status).toBe(503);
  });

  it('surfaces the machine code for an already-claimed deposit', async () => {
    // This is the front-running case reaching the UI; the screen branches on
    // the code rather than matching message text.
    (global.fetch as jest.Mock).mockResolvedValue(
      errJson(400, 'this deposit has already been reported', 'stable_deposit_already_claimed')
    );
    const r = await reportStableDeposit('i1', '0xdeadbeef');
    expect(r.success).toBe(false);
    expect(r.code).toBe('stable_deposit_already_claimed');
  });

  it('posts the deposit hash to the intent-scoped path', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ recorded: true }));
    await reportStableDeposit('abc-123', '0xfeed');
    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(String(url)).toContain('/it/stable/sell/abc-123/deposit');
    expect(JSON.parse(init.body)).toEqual({ txHash: '0xfeed' });
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-app && npx jest --watchAll=false services/__tests__/itStableApi.test.ts`
Expected: FAIL — `quoteStableSell is not a function`

- [ ] **Step 3: Add status + idempotency to the shared wrapper**

In `tsa-app/services/itApi.ts`, change `ApiResult` and `req`. The existing `req` is at line 42; keep its behaviour identical apart from these two additions.

```ts
export type ApiResult<T> = {
  success: boolean;
  data?: T;
  message?: string;
  code?: string;
  /** HTTP status, so callers can distinguish a user-fixable 400 from a
   *  retryable 503. Absent when the request never reached the server. */
  status?: number;
};

/** RFC 4122 v4 where available; same approach as services/p2p.ts.
 *  Exported because a caller may need to hold ONE key across retries of the
 *  same logical create (see the stablecoin sell screen). */
export function makeIdempotencyKey(): string {
  const g: any = globalThis as any;
  if (g.crypto?.randomUUID) return g.crypto.randomUUID();
  return `it-${Date.now()}-${Math.random().toString(36).slice(2, 11)}`;
}

async function req<T>(
  method: string,
  path: string,
  body?: unknown,
  opts: { idempotencyKey?: string } = {}
): Promise<ApiResult<T>> {
  try {
    const headers = await getAuthHeaders();
    // Non-GET calls create or move money; a network-level retry must not
    // produce a second intent. Mirrors services/p2p.ts.
    if (method !== 'GET') {
      headers['Idempotency-Key'] = opts.idempotencyKey ?? makeIdempotencyKey();
    }
    const res = await fetch(`${API_BASE_URL}${path}`, {
      method,
      headers,
      body: body == null ? undefined : JSON.stringify(body),
    });
    let json: any = null;
    try {
      json = await res.json();
    } catch {
      /* empty */
    }
    if (json?.success) return { success: true, data: json.data as T, status: res.status };
    return {
      success: false,
      message: json?.message || `Request failed (${res.status})`,
      code: json?.code,
      status: res.status,
    };
  } catch (e: any) {
    return { success: false, message: e?.message || 'Network error' };
  }
}
```

- [ ] **Step 4: Extend the `ITIntent` type**

`ITIntent` (itApi.ts:21-36) carries only the MCGP-era fields. Task 8 branches on `intent.asset`,
which does not exist — a compile error. The Go model already emits all of these; add them as
optional so existing MCGP intents (which omit them) still typecheck:

```ts
export interface ITIntent {
  // ... existing fields unchanged ...

  /** Stablecoin ramp. Absent on MCGP intents, which is why every field is
   *  optional — the same row type serves both flows. */
  asset?: string;
  assetAmount?: string;
  currency?: string;
  sourceChain?: string;
  depositTxHash?: string;
  receivedAmount?: string;
  netMinor?: string;
  /** Flutterwave's address this sell was quoted against. Persisted server-side
   *  so an abandoned sell can be resumed without minting a second intent. */
  depositAddress?: string;
  conversionAttempts?: number;
}
```

- [ ] **Step 5: Add the three endpoints**

Append to `tsa-app/services/itApi.ts`:

```ts
export type StableAsset = 'USDC' | 'USDT';

/** Machine code: this deposit hash is already claimed by another intent. */
export const IT_STABLE_DEPOSIT_CLAIMED = 'stable_deposit_already_claimed';

// All money fields are minor-unit integer STRINGS — they stay strings until
// they are formatted for display.
export interface StableQuote {
  asset: string;
  currency: string;
  /** The asset amount the quote was priced for, as a STRING — the handler
   *  emits fmt.Sprintf("%.6f", …) (it_handler.go:716), not a JSON number.
   *  Typing it `number` would make `q.amount.toFixed(...)` a runtime crash. */
  amount: string;
  gross: string;
  afterSpread: string;
  fee: string;
  net: string;
  rate: string;
  expiresAt: string;
}

/** Where the user must send their stablecoin. This is FLUTTERWAVE's address —
 *  never one the platform controls, and never hardcoded in the app. */
export interface StableDeposit {
  chain: string;
  address: string;
  asset: string;
  /** The exact ERC-20 contract on Polygon that ConfirmStableDeposit will
   *  verify the arrival against (s.stableTokens[asset], it_stable_sell.go:361).
   *  NOT optional and NOT resolvable app-side: Polygon carries both native
   *  USDC and bridged USDC.e, and delivering the other one leaves the deposit
   *  unverifiable with the funds already at Flutterwave. The backend fails
   *  closed (ErrStableNoTokenAddress) rather than return this empty, so if a
   *  create succeeded this field is populated. */
  tokenAddress: string;
  amount: string;
}

export interface StableSellBank {
  code: string;
  number: string;
  name: string;
}

// `currency` is binding:"required" on both requests (it_handler.go:675).
// `{ currency: 'NGN', ...body }` does NOT default it: an explicit
// `currency: undefined` in `body` overrides the default back to undefined,
// JSON.stringify drops the key, and the server 400s. `?? 'NGN'` defaults it
// for real.
export const quoteStableSell = (body: {
  asset: StableAsset;
  amount: string;
  currency?: string;
}) =>
  req<StableQuote>('POST', '/it/stable/quote', {
    ...body,
    currency: body.currency ?? 'NGN',
  });

export const createStableSell = (body: {
  asset: StableAsset;
  amount: string;
  currency?: string;
  sourceChain: string;
  bank: StableSellBank;
  /** Pass a stable key so a retry of the SAME create is deduped server-side. */
  idempotencyKey?: string;
}) => {
  const { idempotencyKey, ...payload } = body;
  return req<{ intent: ITIntent; deposit: StableDeposit }>(
    'POST',
    '/it/stable/sell',
    { ...payload, currency: payload.currency ?? 'NGN' },
    { idempotencyKey }
  );
};

export const reportStableDeposit = (intentId: string, txHash: string) =>
  req<{ recorded: boolean }>('POST', `/it/stable/sell/${intentId}/deposit`, { txHash });
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd tsa-app && npx jest --watchAll=false services/__tests__/itStableApi.test.ts`
Expected: PASS (5 tests)

- [ ] **Step 7: Run the whole suite for regressions**

Run: `cd tsa-app && npx jest --watchAll=false`
Expected: PASS — `req` is shared by every IT call, so any existing IT test failing here means the wrapper change broke something. Fix rather than update the test.

- [ ] **Step 8: Commit**

```bash
git add tsa-app/services/itApi.ts tsa-app/services/__tests__/itStableApi.test.ts
git commit -m "feat(it): stablecoin sell endpoints, request status and idempotency"
```

---

## Task 4: Extract the bank destination block

Pure refactor of existing behaviour, done before the new screen so both flows consume one component. No behaviour change — the MCGP sell must look and act identically afterwards, **provided both callers memoise `onChange`**. That caveat is where the "no behaviour change" claim breaks: the component is fully controlled, and its debounce and bank-load effects list `onChange` in their dependency arrays. An inline arrow re-created on every parent render re-runs them on every keystroke elsewhere on the screen — restarting the 400 ms name-enquiry debounce and re-fetching the bank list. Both call sites pass a `useCallback`-wrapped handler (`setDest` from `useState` is already stable and may be passed directly).

**Files:**
- Create: `tsa-app/components/it/BankDestination.tsx`
- Modify: `tsa-app/app/wallet/it/sell.tsx` (replace the inline block with the component)

**Interfaces:**
- Consumes: `getBanksForCountry`, `resolveAccountName` from `services/ngBanks.ts`
- Produces:

```ts
export interface BankDestinationValue {
  bankCode: string;
  bankName: string;
  accountNumber: string;
  accountName: string;
  ready: boolean;
}
export interface BankDestinationProps {
  currency: string;
  value: BankDestinationValue;
  onChange: (next: BankDestinationValue) => void;
}
```

- [ ] **Step 1: Read the block being extracted**

Read `tsa-app/app/wallet/it/sell.tsx` lines 175–226 (bank list load, `bankItems`, `bankReady`), 189–220 (the debounced resolve effect), and 541–603 (the bank `SelectRow`, account-number `Input`, account-name `Input` with its adornments) plus the picker sheet at 661–678. These move as a unit.

- [ ] **Step 2: Create the component**

Create `tsa-app/components/it/BankDestination.tsx` containing exactly that logic, parameterised by `currency` and reporting upward through `onChange`. Preserve every existing behaviour:
- bank list from `getBanksForCountry(countryForCurrency(currency))`, never a hardcoded list;
- 400 ms debounced `resolveAccountName`, gated on `currency === 'NGN' && bank && accountNumber.length === 10`, cancelled on unmount;
- the three resolve states — spinner while resolving, green check + "Account verified" + tap-to-clear when resolved, inline error on `not_found`, and the "Account verification is offline. You can type the name manually." helper on `unavailable`;
- `ready` computed exactly as `bankReady` is today (bank code + account length + name ≥ 2 chars + not resolving + not `not_found`);
- and document on `BankDestinationProps.onChange` that it MUST be referentially stable, for the reason above.

- [ ] **Step 3: Swap it into the MCGP sell screen**

Replace the inline block in `sell.tsx` with `<BankDestination currency={payoutCurrency} value={dest} onChange={setDest} />`, mapping `dest.ready` onto the existing `bankReady` checks and `dest.*` onto the existing `reviewBank*` snapshot fields. Do not change the review snapshot logic — it exists so a late fee quote cannot pair with an edited amount.

- [ ] **Step 4: Verify no behaviour changed**

Run: `cd tsa-app && npx jest --watchAll=false && npx tsc --noEmit`
Expected: PASS, no type errors.

Then run the app and walk an MCGP sell to the review screen: pick a bank, type a 10-digit account, confirm the name resolves and "Account verified" appears, and confirm Review is still blocked until the destination is ready. This is a refactor of a live money path — the manual check is the point.

- [ ] **Step 5: Commit**

```bash
git add tsa-app/components/it/BankDestination.tsx tsa-app/app/wallet/it/sell.tsx
git commit -m "refactor(it): extract the bank destination block for reuse"
```

---

## Task 5: Asset picker on the sell screen

**Files:**
- Modify: `tsa-app/app/wallet/it/sell.tsx`

**Interfaces:**
- Consumes: nothing
- Produces: navigation to `/wallet/it/sell-stable?asset=USDC|USDT`

- [ ] **Step 1: Add the picker**

At the top of the form, above the amount input, add a `SelectRow` + `PickerSheet` (the components already used for payout currency at `sell.tsx:523-528`) with three options: `MCGP`, `USDC`, `USDT`.

- [ ] **Step 2: Route the stablecoin choices away**

On selecting USDC or USDT, immediately `router.push({ pathname: '/wallet/it/sell-stable', params: { asset } })` and leave the MCGP form state untouched. Selecting MCGP keeps the current screen exactly as it is.

`sell.tsx` has no asset state today — the only occurrence of `asset` in the file is the literal
`asset: 'MCGP'` at line 297. Introduce the state first, then the handler:

```tsx
// New: the picker's selection. MCGP is the only value this screen ever holds;
// the stablecoins navigate away rather than being rendered here.
const [asset, setAsset] = useState<'MCGP'>('MCGP');

const onAssetChange = (next: string) => {
  if (next === 'USDC' || next === 'USDT') {
    router.push({ pathname: '/wallet/it/sell-stable', params: { asset: next } });
    return;
  }
  setAsset('MCGP');
};
```

Then replace the hardcoded `asset: 'MCGP'` at line 297 with `asset` so the fee quote reads from
state rather than a literal.

- [ ] **Step 3: Register the route**

In `tsa-app/app/wallet/it/_layout.tsx`, add alongside the existing screens (which sit at lines 76–83):

```tsx
<Stack.Screen name="sell-stable" options={{ title: 'Withdraw · Sell' }} />
```

**Why the new screen needs no `it_agreement_required` handling.** `_layout.tsx:64-73` renders
`<ITAgreementGate />` *instead of* the whole `<Stack>` until the user has accepted, so every route
in the `/wallet/it` group — this one included — is unreachable before acceptance. The create
endpoint's `it_agreement_required` 403 therefore cannot be produced from this screen. Correct as-is;
stated here so the next reader does not add dead error handling for it, and so that anyone who later
moves this screen out of the group knows what they are removing.

- [ ] **Step 4: Verify**

Run: `cd tsa-app && npx tsc --noEmit`
Then run the app: the picker appears, MCGP behaves exactly as before, and USDC/USDT navigates to a (currently empty) screen with a working back button.

- [ ] **Step 5: Commit**

```bash
git add tsa-app/app/wallet/it/sell.tsx tsa-app/app/wallet/it/_layout.tsx
git commit -m "feat(it): asset picker on the sell screen"
```

---

## Task 6: Stablecoin sell form — chains, balances, and the quote

**Files:**
- Create: `tsa-app/app/wallet/it/sell-stable.tsx`

**Interfaces:**
- Consumes: `belowMinimum`, `exceedsStablePrecision`, `estimateArrival`, `formatSendVsArrive`, `POLYGON_STABLE_DECIMALS` (Task 1); `quoteStableSell`, `makeIdempotencyKey` (Task 3); `BankDestination` (Task 4)
- Produces: the `form` and `review` states of the screen, and the **review snapshot** Task 7 consumes; Task 7 adds signing.

- [ ] **Step 1: Build the form state**

Create the screen with `type Screen = 'form' | 'review' | 'signing' | 'error'` — the same local-state-machine idiom as `sell.tsx:46` and `all-swap.tsx:50`.

Declare the screen's state up front. Task 7 extends this same component, so every symbol it uses is
introduced here rather than appearing from nowhere three tasks later. (The imports, `EMPTY_DEST` and
the `Screen` type are module scope; everything from `useLocalSearchParams` down is the component
body.)

```tsx
import { useCallback, useEffect, useState } from 'react';
import { ethers } from 'ethers';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useLocalSearchParams, router } from 'expo-router';
import { getChain, type ChainKey } from '@/constants/chains';
import { getLiFiQuote, getLiFiToken, getLiFiTokens, type LiFiToken } from '@/services/lifi';
import { executeLiFiSwap } from '@/services/lifiFlow';
import { mapSubmitError } from '@/services/swapErrors';
import { parseTokenAmount } from '@/services/p2p';
import { ensureGas, getActiveWallet, readErc20BalanceOf, signTransaction } from '@/services/wallet';
import {
  getDiscoveredTokens, prepareSendTransaction, sendPreparedTransaction, submitTransaction,
} from '@/services/walletApi';
import { BankDestination, type BankDestinationValue } from '@/components/it/BankDestination';
import { CountdownBar, SummaryCard, SummaryRow } from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import {
  createStableSell, quoteStableSell, reportStableDeposit, makeIdempotencyKey,
  IT_STABLE_DEPOSIT_CLAIMED, type StableAsset, type StableQuote,
} from '@/services/itApi';
import {
  POLYGON_STABLE_DECIMALS, belowMinimum, estimateArrival, exceedsStablePrecision,
  formatSendVsArrive,
} from '@/utils/stableSellQuote';

const EMPTY_DEST: BankDestinationValue = {
  bankCode: '', bankName: '', accountNumber: '', accountName: '', ready: false,
};

const { asset: assetParam } = useLocalSearchParams<{ asset: string }>();
const asset: StableAsset = assetParam === 'USDT' ? 'USDT' : 'USDC';

const [screen, setScreen] = useState<Screen>('form');

// --- form inputs -----------------------------------------------------------
const [chainKey, setChainKey] = useState<ChainKey>('polygon');
const [amount, setAmount] = useState('');
const [dest, setDest] = useState<BankDestinationValue>(EMPTY_DEST);
const [sourceToken, setSourceToken] = useState<LiFiToken | null>(null);
const [balance, setBalance] = useState<string | null>(null); // null = UNKNOWN, not zero
const [quote, setQuote] = useState<StableQuote | null>(null);
const [arrival, setArrival] = useState<string | null>(null);
const chainId = getChain(chainKey).chainId;
const chainLabel = getChain(chainKey).name; // "BNB Smart Chain", "Polygon", …

// --- two DISTINCT error surfaces, deliberately -----------------------------
// quoteError belongs to the amount/chain block and clears on every re-quote;
// error belongs to the screen and survives until the user acts. Rendering one
// string for both would let a stale "no route" sit under a signing failure.
const [quoteError, setQuoteError] = useState<string | null>(null);
const [error, setError] = useState<string | null>(null);

// --- the review SNAPSHOT (Step 5 fills it; Task 7 reads ONLY these) --------
// Frozen copies of the inputs, so a late quote or an edited field cannot pair
// with a create that is already in flight.
const [reviewAmount, setReviewAmount] = useState('');
const [reviewArrival, setReviewArrival] = useState('');
const [reviewSourceDecimals, setReviewSourceDecimals] = useState(6);
const [reviewSourceTokenAddress, setReviewSourceTokenAddress] = useState('');
const [reviewNetKobo, setReviewNetKobo] = useState<string | null>(null);
const [reviewRateKobo, setReviewRateKobo] = useState<string | null>(null);
const [reviewBankCode, setReviewBankCode] = useState('');
const [reviewBankName, setReviewBankName] = useState('');
const [reviewAccountNumber, setReviewAccountNumber] = useState('');
const [reviewAccountName, setReviewAccountName] = useState('');
const [reviewIdempotencyKey, setReviewIdempotencyKey] = useState('');
// Non-null when the server re-quoted to a different net at create time; drives
// the "the rate moved" banner in Step 5. Task 7 Step 1 sets it.
const [repricedTo, setRepricedTo] = useState<string | null>(null);

// --- signing (used by Task 7) ---------------------------------------------
const [showAuth, setShowAuth] = useState(false);
const [signingStatus, setSigningStatus] = useState<string | null>(null);
// Set when a Polygon submit returned no hash: we cannot prove nothing was
// broadcast, so the error screen offers a recovery route rather than a
// dead end. Task 7 Step 4.
const [recoveryHint, setRecoveryHint] = useState<string | null>(null);
// The active EVM address, resolved the way all-swap.tsx:102-106 does — there
// is no useWalletAddress hook in this repo. Everything downstream (the
// balance read, the LiFi quotes, the signature pin) is against THIS address.
const [walletAddress, setWalletAddress] = useState('');
useEffect(() => {
  getActiveWallet().then((addr) => { if (addr) setWalletAddress(addr); });
}, []);
// ensureGas alerts via Alert.alert when no handler is passed. We pass one so
// the message lands in THIS screen's error surface instead of a second,
// competing system dialog — see Step 2 of Task 7.
const alertFn = useCallback(
  (title: string, message: string) => setError(`${title}: ${message}`),
  []
);
// `dest` comes back from a controlled child; setState setters are already
// referentially stable, so this satisfies BankDestination's memoised-onChange
// requirement without a useCallback wrapper.
```

`runSigning` is the async function defined in Task 7 and passed to `AuthorizationModal`'s
`onAuthorized`; it is declared here as `const runSigning = useCallback(async () => { /* Task 7 */ }, [...])` so
the modal has something to bind to before Task 7 fills it in.

Form fields: asset (from route params), source chain, amount, and `<BankDestination currency="NGN" value={dest} onChange={setDest} />`.

- [ ] **Step 2: Load per-chain balances**

Use `getDiscoveredTokens()` to find which chains hold the chosen asset, then `readErc20BalanceOf(sourceToken.address, walletAddress, chainKey)` for the selected chain's authoritative figure — the pattern `all-swap.tsx:215-253` uses, because the backend balance endpoint zeroes imported tokens. Show each chain's balance in the picker so chains the user holds nothing on are visibly empty rather than mysteriously unquotable.

Resolve the source token per chain with `getLiFiToken(chainId, asset)`, falling back to the per-chain list — the `spot-trade.tsx:88-101` pattern. **Both halves can miss**, and neither failure may be swallowed:

```ts
// getLiFiToken returns LiFiToken | null (lifi.ts:158) and the list fallback
// can miss too. `sourceToken.decimals` on a null token is a TypeError that
// reads to the user as a broken screen, so resolve into state and gate the
// form on it — exactly what spot-trade.tsx:104-106 throws for.
// The spot-trade.tsx:88-101 resolver, verbatim in shape: the single-token
// lookup accepts a SYMBOL and returns a tiny payload; the full per-chain list
// is multi-MB and flaky on mobile, so it is only the fallback.
const resolveToken = async (id: number, sym: string): Promise<LiFiToken | null> => {
  try {
    const t = await getLiFiToken(id, sym);
    if (t) return t;
  } catch { /* fall through to the list */ }
  try {
    const tokens = await getLiFiTokens(id);
    return tokens.find((t) => t.symbol?.toUpperCase() === sym) ?? null;
  } catch {
    return null;
  }
};

const token = await resolveToken(chainId, asset);
if (!token) {
  setSourceToken(null);
  setQuoteError(`Couldn't load ${asset} on ${chainLabel}. Check your connection and try again.`);
  return;
}
setSourceToken(token);

// readErc20BalanceOf THROWS when every configured RPC fails (wallet.ts:504-513)
// — deliberately, so "unknown" stays distinguishable from "zero". Treating
// unknown as zero would block the form with no explanation, so keep it null
// and say so.
try {
  const raw = await readErc20BalanceOf(token.address, walletAddress, chainKey);
  setBalance(ethers.formatUnits(raw, token.decimals));
} catch {
  setBalance(null);
  setQuoteError(`Couldn't read your ${asset} balance on ${chainLabel}. Try again shortly.`);
}
```

The `amount ≤ balance` guard in Step 4 must therefore be skipped — not failed — when `balance` is
`null`, with the unknown-balance message left visible. The chain rejects an over-send anyway; a
transient RPC blip must not become an unexplained locked form.

- [ ] **Step 3: Compose the quote**

Debounce 500 ms (as `all-swap.tsx` does), then:

```ts
// TWO different decimals — conflating them is a 10^12 error. `sourceDecimals`
// is the token on the chain the user sends FROM (BSC stablecoins are 18);
// POLYGON_STABLE_DECIMALS is the destination, where LiFi's toAmount is always
// denominated because toChain is pinned to 137.
//
// Take decimals from the resolved LiFi token rather than p2pTokenDecimals:
// that helper is a Sonic/BSC whitelist and silently returns 6 for Arbitrum,
// Base, Linea and Avalanche — correct today, but by accident.
if (!sourceToken) return;               // Step 2 already surfaced the reason
const sourceDecimals = sourceToken.decimals;

let lifiToAmount: string | null = null;
if (chainKey !== 'polygon') {
  try {
    // getLiFiQuote THROWS on a non-2xx (lifi.ts:180) — a no-route response
    // must surface as a message, not crash the effect.
    const routed = await getLiFiQuote({
      fromChain: chainId,
      toChain: 137,
      fromToken: sourceToken.address,
      // PRICING ONLY. `toToken` is passed through verbatim to LiFi
      // (swap_lifi_handler.go:656), which accepts a symbol as well as an
      // address — the same way the existing handler test passes
      // `fromToken=SOL`. Using the SYMBOL here is deliberate: the app must
      // never pick a Polygon USDC contract itself, because Polygon carries
      // both native USDC and bridged USDC.e and the backend confirms against
      // exactly one of them. The authoritative contract arrives with the
      // create response and is used for the EXECUTION quote and the transfer
      // in Task 7 Step 4. A symbol that resolves to the other variant can
      // therefore only skew this ESTIMATE — it can never misroute funds.
      toToken: asset,
      fromAmount: parseTokenAmount(amount, sourceDecimals),
      fromAddress: walletAddress,
      // The real deposit address only exists after create (Task 7). LiFi just
      // needs a well-formed destination to PRICE a route, so the user's own
      // address stands in. Task 7's execution quote uses the real one. Looks
      // wrong without this comment; it is deliberate.
      toAddress: walletAddress,
    });
    lifiToAmount = routed.toAmount;
  } catch {
    setQuoteError(`No route from ${chainLabel} — try another chain`);
    return;
  }
}

let arrivalMajor: string;
try {
  arrivalMajor = estimateArrival(amount, lifiToAmount, POLYGON_STABLE_DECIMALS);
} catch {
  // LiFi answered but gave us nothing usable. Block rather than quote the
  // typed amount — see estimateArrival's contract.
  setQuoteError('Could not price this route — try another chain');
  return;
}
const q = await quoteStableSell({ asset, amount: arrivalMajor, currency: 'NGN' });
if (!q.success || !q.data) {
  setQuoteError(q.message || 'Could not price this amount — try again shortly');
  return;
}
setQuoteError(null);
setArrival(arrivalMajor);
setQuote(q.data);
```

**On the destination at quote time:** neither the deposit ADDRESS nor the deposit TOKEN CONTRACT is
known until create returns, and the app must hardcode neither. For the *quote* LiFi only needs a
well-formed destination to price the route, so the user's own wallet address and the plain asset
symbol stand in. Task 7 Step 4 re-quotes against the real address and the real contract before a
single transaction is signed. Add these comments, because both lines look wrong otherwise.

- [ ] **Step 4: Block the review on the guards**

Review is unavailable unless **all** of:
- the amount parses as a plain decimal — `!belowMinimum(asset, amount)` already enforces the shape
  (it shares `parseTokenAmount`'s grammar), so `'1e1'` is rejected here rather than throwing later;
- `!exceedsStablePrecision(amount)` — more than 6 fractional digits is a guaranteed 400 from
  `StableToBaseUnits` (`stable_assets.go:89-92`). Show **"USDC amounts can have at most 6 decimal
  places"**, not a generic failure;
- `balance === null || Number(amount) <= Number(balance)` — see Step 2: an unknown balance skips the
  comparison rather than failing it;
- a quote exists (`quote !== null`) and `quoteError === null`;
- `dest.ready`.

Show `belowMinimum`'s failure as "Minimum sell is 10 USDC" rather than a generic error.

- [ ] **Step 5: Build the review screen**

Snapshot every input into `review*` state on leaving the form — the same guard `sell.tsx:274-289` uses so a late quote cannot pair with an edited amount. **This is also where the idempotency key is minted**, because a key is a property of the snapshot, not of the mount:

```ts
// The snapshot Task 7 creates the intent from. NOTHING in Task 7 reads live
// form state: that is the entire point — create must not pair a stale quote
// with edited inputs.
const enterReview = () => {
  setReviewAmount(amount);                       // typed, major units
  setReviewArrival(arrival!);                    // what we expect to LAND
  // Both halves of the source token are part of the snapshot: Task 7 parses
  // reviewAmount with these decimals and builds the execution quote's
  // `fromToken` from this address. One name for one value — `sourceToken` is
  // live form state and must not be read after this point.
  setReviewSourceDecimals(sourceToken!.decimals);
  setReviewSourceTokenAddress(sourceToken!.address);
  setReviewNetKobo(quote!.net);                  // the payout figure being shown
  setReviewRateKobo(quote!.rate);
  setReviewBankCode(dest.bankCode);
  setReviewBankName(dest.bankName);
  setReviewAccountNumber(dest.accountNumber);
  setReviewAccountName(dest.accountName);
  // One key per confirmed snapshot. Minted HERE, not at mount: the middleware
  // 409s a key replayed with a DIFFERENT payload, so a mount-scoped key would
  // wedge a user who backs out, edits the amount and confirms again.
  setReviewIdempotencyKey(makeIdempotencyKey());
  setRepricedTo(null);                           // clear any prior reprice banner
  setScreen('review');
};
```

`reviewNetKobo` is the quote's `net` verbatim — a minor-unit integer string. It is the same value
`CreateStableSellIntent` writes to `NetNgnKobo` (`it_stable_sell.go:174` stores `q.NetMinor.String()`,
and `StableQuote.net` is that same `q.NetMinor.String()` at `it_handler.go:720`), which is what makes
Task 7's create-time comparison a like-for-like one.

Render, using `SummaryCard`/`SummaryRow`:
- **the reprice banner, first and prominent, when `repricedTo !== null`**: "The rate moved. Your
  payout is now ₦X (was ₦Y). Confirm to accept." Without this the user is bounced back to a review
  screen showing the *old* figure with no explanation — worse than proceeding. Task 7 Step 1 sets it;
- `formatSendVsArrive(reviewAmount, reviewArrival)` when it returns non-null (bridged sends only);
- on a bridged sell, label the ₦ figure an **estimate**, not a fact. The payout is `received/quoted`,
  and LiFi's `toAmount` is itself an estimate — under-delivery scales the payout down. On Polygon
  there is no bridge, so the figure is firm and needs no qualifier;
- a one-line note on a bridged sell that a delivery **more than 2% above** the quote is held for
  review rather than paid automatically (`stableOverDeliveryCeiling`, it_stable_sell.go:47-59). An
  over-conservative LiFi estimate on a volatile route can trip this, and a user whose sell silently
  enters manual review with no explanation will assume it is lost;
- rate, then `gross` → `afterSpread` → `fee` → `net` as separate lines, since the spread and the fee
  are distinct charges;
- destination bank and account, from `reviewBank*` — never live `dest.*`;
- the quote's expiry as a countdown. **Use the existing `CountdownBar` from `components/ui/`** rather
  than a new ticker: it already takes `expiresAt` as an ISO string, renders an urgent state under
  5 minutes, and fires `onExpire` once. Wire `onExpire` to re-run the quote and re-enter review
  through `enterReview` so the new figure is shown (and a fresh key minted) rather than swapped in
  silently.

- [ ] **Step 6: Verify**

Run: `cd tsa-app && npx tsc --noEmit && npx jest --watchAll=false`
Then in the app: pick USDC, see chains with balances, enter an amount, watch the quote resolve, and confirm the review shows send-vs-arrive on a non-Polygon chain and omits it on Polygon. Type `10.1234567` and confirm the decimal-places message appears instead of a failed quote.

- [ ] **Step 7: Commit**

```bash
git add tsa-app/app/wallet/it/sell-stable.tsx
git commit -m "feat(it): stablecoin sell form and quote"
```

---

## Task 7: Create, authorize, sign, broadcast

The riskiest task in the plan: everything from here moves real user funds irreversibly.

**Files:**
- Modify: `tsa-app/app/wallet/it/sell-stable.tsx`

**Interfaces:**
- Consumes: the `review*` snapshot from Task 6 Step 5 (`reviewAmount`, `reviewArrival`,
  `reviewSourceDecimals`, `reviewSourceTokenAddress`, `reviewNetKobo`, `reviewBankCode`,
  `reviewAccountNumber`, `reviewAccountName`, `reviewIdempotencyKey`); `createStableSell`, `reportStableDeposit`,
  `makeIdempotencyKey`, `IT_STABLE_DEPOSIT_CLAIMED` (Task 3)
- Produces: navigation to `/wallet/it/intent/<id>`

**Ordering is load-bearing in this task.** Create comes first because *both* the deposit address and
the deposit token contract come out of its response, and neither may be resolved app-side. That is
why the execution LiFi quote in Step 4 is built after Step 1 and not alongside Task 6's pricing
quote: until create returns, the destination contract is simply not known.

- [ ] **Step 1: Create the intent on confirm, then reconcile it against the review**

```ts
// One key per CONFIRMED REVIEW SNAPSHOT — minted in Task 6 Step 5's
// enterReview(), alongside the other review* state, not per screen mount.
//
// A network retry of THIS create must not mint a second intent (every intent
// is a claim on a deposit). But the key must ALSO change when the order
// changes: the middleware returns 409 when a key is replayed with a different
// payload, so a mount-scoped key would wedge a user who backs out, edits the
// amount and confirms again.
const created = await createStableSell({
  asset,
  amount: reviewArrival,
  currency: 'NGN',
  sourceChain: chainKey.toUpperCase(),
  // reviewBank*, never live dest.* — the snapshot exists precisely so create
  // cannot pair a stale quote with edited inputs, and the bank is an input.
  bank: { code: reviewBankCode, number: reviewAccountNumber, name: reviewAccountName },
  idempotencyKey: reviewIdempotencyKey,
});

// Guard BEFORE the first dereference. On a failed create the shape is
// {success:false, data: undefined}, so reading created.data.intent below
// would be a TypeError on a money screen. Nothing has been broadcast yet, so
// failing back to the form here is genuinely safe.
if (!created.success || !created.data?.intent?.id || !created.data?.deposit?.address
    || !created.data?.deposit?.tokenAddress) {
  setError(created.message || 'Could not start this sell');
  setScreen('form');
  return;
}
const intentId = created.data.intent.id;
const depositAddress = created.data.deposit.address;
// The contract the backend will verify the arrival against. Never resolved
// app-side — see the Global Constraints. The backend fails closed
// (ErrStableNoTokenAddress) rather than return this empty, so a successful
// create always carries it; the guard above is belt-and-braces.
const depositTokenAddress = created.data.deposit.tokenAddress;
// The amount the backend will reconcile the arrival against
// (it_stable_sell.go:195, "%.6f"). Prefer it over reviewAmount: on Polygon
// they are equal by construction, but this is the authoritative figure.
const depositAmount = created.data.deposit.amount;
```

Create MUST precede signing: the deposit address **and the deposit token contract** come from this
response, and the app must hardcode neither.

**Then compare the created intent against what the user reviewed, BEFORE the PIN prompt.**
`PostStableSell` calls `QuoteStableSell` again server-side (it_handler.go:743-747) and creates the
intent against *that* quote, not the one shown at Review — the rate can move between the two calls.
Authorizing against a stale figure is the one place money is actually committed:

```ts
const createdNet = created.data.intent.netNgnKobo;
// Both sides must be present for the comparison to mean anything. netNgnKobo
// is `string | undefined` on the ITIntent type, and reviewNetKobo is only set
// by enterReview() — if either were missing, `a !== b` would invert into
// "always reprice" (or, when both are undefined, "never reprice"). Neither
// silent mode is acceptable on a money path, so treat a missing figure as an
// error rather than as agreement.
if (!createdNet || !reviewNetKobo) {
  setError('Could not confirm your payout figure. Start the sell again.');
  setScreen('form');
  return;
}
if (createdNet !== reviewNetKobo) {
  // Re-present rather than proceed. Same principle the quote countdown already
  // applies, at the moment it matters most.
  setRepricedTo(createdNet);
  setReviewNetKobo(createdNet);          // the figure the banner is now showing
  setReviewIdempotencyKey(makeIdempotencyKey()); // see below — MANDATORY
  setScreen('review');
  return;
}
```

**Why accepting a reprice MUST re-mint the key.** The middleware caches 2xx responses and replays
them for the same key (`idempotency.go:57-59`). If the user accepts the new figure and confirms
again with the *same* key, the server replays the original create verbatim — the same intent, the
same `netNgnKobo` — so the comparison above fails identically every time and **the user can never
proceed**. An infinite loop, on the confirm button of a sell. Re-minting makes the second confirm a
genuinely new request against the current rate.

**What this costs, stated plainly.** A reprice the user *rejects* — they back out instead of
confirming — leaves the first intent sitting in `pending_deposit` with nothing ever sent to it. There
is no cancel endpoint, so it is orphaned: it will sit until an operator or a sweeper clears it. It is
harmless (an intent is a record, not a claim on any balance of ours, and the deposit address is
Flutterwave's) but it is real, and accepting a reprice mints a second one for the same reason. Task 8
Step 4's "Continue — send your USDC" is the only user-facing exit from such an intent. A
`DELETE /it/stable/sell/:id` for an untouched `pending_deposit` is the right backend follow-up; it is
not a blocker for this plan.

- [ ] **Step 2: Gate on authorization, then gas**

```tsx
<AuthorizationModal
  visible={showAuth}
  title="Authorize sell"
  onAuthorized={runSigning}
  onCancel={() => setShowAuth(false)}
/>
```

Inside `runSigning`, before touching a key:

```ts
// ADVISORY, not a gate. ensureGas returns TRUE when the RPC read fails
// (wallet.ts:399-404) — deliberately, so an RPC blip does not block a send the
// chain would have accepted. So this catches the common "wallet has zero
// native token" case and nothing else; a swap can still die for gas after an
// approval has already cost the user a transaction. Do not describe this as
// "fail before signing" — it is "fail before signing WHEN WE CAN TELL".
//
// alertFn (Task 6 Step 1) is passed explicitly because ensureGas fires its own
// Alert.alert when onAlert is omitted (wallet.ts:412-416); with a handler the
// message lands in this screen's single error surface instead of a competing
// system dialog. Do NOT also call setError here — alertFn already did.
const gasOk = await ensureGas(chainKey, walletAddress, alertFn);
if (!gasOk) return;
```

- [ ] **Step 3: Pin the signing wallet**

```ts
// signLiFiTransaction always signs with the ACTIVE wallet and takes no
// expected address. We quoted and created against one specific address, so a
// wallet switch mid-flow would sign with the wrong key and send someone
// else's funds. Refuse rather than sign.
const assertActiveWallet = async () => {
  const active = await getActiveWallet();
  if (!active || active.toLowerCase() !== walletAddress.toLowerCase()) {
    setError('Your active wallet changed. Start the sell again.');
    return false;
  }
  return true;
};
if (!(await assertActiveWallet())) return;
```

**Checking once is not enough on the LiFi path.** `executeLiFiSwap` signs **twice** — the ERC-20
approval, then the swap — with a receipt wait between them that can run for minutes
(`lifiFlow.ts:77-154`), and `signLiFiTransaction` takes no expected address (`wallet.ts:643`). A user
who switches wallets during that wait gets the swap signed by the new key. Hence
`assertActiveWallet` is a reusable function, not an inline block: Step 4 calls it again immediately
before `executeLiFiSwap`, and the Polygon path is unaffected because `signTransaction` takes
`walletAddress` explicitly.

- [ ] **Step 4: Branch the send by source chain**

```ts
let txHash: string;
if (chainKey === 'polygon') {
  // No bridge: a plain ERC-20 transfer to Flutterwave's address. The backend's
  // same-chain branch skips the LiFi status lookup entirely.
  //
  // sendPreparedTransaction returns ApiResponse<{txHash}>, NOT a string
  // (walletApi.ts:509). Assigning it straight to a string would persist and
  // report "[object Object]" — losing the real hash on the primary route, at
  // the exact moment the "never lose a hash" rule matters.
  //
  // tokenAddress comes from the create response, never from a symbol lookup:
  // Polygon carries both native USDC and bridged USDC.e, and the backend
  // confirms against exactly one of them.
  const sent = await sendPreparedTransaction({
    prepare: () => prepareSendTransaction(asset, depositAddress, depositAmount, 137, {
      tokenAddress: depositTokenAddress,
      decimals: POLYGON_STABLE_DECIMALS,
      fromAddress: walletAddress,
    }),
    sign: (unsigned) => signTransaction(unsigned, walletAddress),
    submit: (signed) => submitTransaction(signed, 'send', asset, depositAddress, depositAmount, 137, walletAddress),
  });
  if (!sent.success || !sent.data?.txHash) {
    // We have no hash to persist — which is NOT the same as "nothing was
    // broadcast", and must not be written down as if it were:
    //   * submitTransaction's catch returns {success:false} on ANY network
    //     error (walletApi.ts:475-478), including a timeout reading the
    //     response AFTER the backend broadcast the raw transaction;
    //   * it also returns the result as-is when the backend SUCCEEDED but the
    //     response carried no extractable hash (walletApi.ts:471-474), which
    //     lands in this same branch.
    // So this branch can be reached with the user's funds already in flight.
    setError(mapSubmitError(sent.message));
    setRecoveryHint(intentId); // see below
    setScreen('error');
    return;
  }
  txHash = sent.data.txHash;
} else {
  // Cross-chain: only NOW is the destination contract known, so the execution
  // quote is built here rather than reusing Task 6's pricing quote. Both the
  // toAddress and the toToken are the backend's own values.
  const execQuote = await getLiFiQuote({
    fromChain: chainId, toChain: 137,
    fromToken: reviewSourceTokenAddress,
    toToken: depositTokenAddress,
    fromAmount: parseTokenAmount(reviewAmount, reviewSourceDecimals),
    fromAddress: walletAddress,
    toAddress: depositAddress,
  });
  // Re-assert immediately before the two signatures — see Step 3.
  if (!(await assertActiveWallet())) return;
  txHash = await executeLiFiSwap({
    quote: execQuote, fromSymbol: asset, fromDecimals: reviewSourceDecimals,
    fromAmount: reviewAmount, walletAddress, fromChainId: chainId,
    onStage: setSigningStatus,
  });
}
```

`setRecoveryHint(intentId)` renders, on the error screen: *"If your wallet shows the transfer went
through, open this order and paste the transaction hash."* plus a link to the order. The recovery
route is real — the backend records every broadcast at `/wallet/submit-tx`, and
`getTransactionHistory(1, 10, 137)` (`walletApi.ts:539`) surfaces the user's recent Polygon sends —
so the screen offers "Find my transaction", lists those, and lets the user report one against the
intent via Task 8's retry path. Do not claim a safety property the code does not have.

- [ ] **Step 5: Persist the hash BEFORE reporting it**

```ts
// The one genuinely dangerous failure: broadcast succeeded, reporting failed.
// The funds are in flight to Flutterwave and the backend does not know which
// intent they belong to. The hash must survive a crash, a backgrounded app and
// a dead network, so it goes to storage before the network call is attempted.
await AsyncStorage.setItem(`it_stable_pending_tx_${intentId}`, txHash);
```

The window between the broadcast returning and this line completing is not closed, and no
app-side write can close it. It is small, and Step 4's recovery hint plus the `/wallet/submit-tx`
record are the fallback when it loses.

- [ ] **Step 6: Report with retry, then hand off**

Attempt `reportStableDeposit(intentId, txHash)` up to 3 times with backoff (1s, 3s). On success, clear the stored hash and `router.replace('/wallet/it/intent/' + intentId)`. On repeated failure, still navigate to the progress screen — **Task 8 Step 3** retries from there — and show the hash with a Retry action so the user has it. Never discard the hash.

**What makes the retry safe is the server, not the middleware.** `req` mints a fresh
`Idempotency-Key` per call, so the middleware sees three unrelated requests and dedups nothing.
The retry is safe because `ReportStableDeposit` is idempotent for the same (intent, hash): the same
hash on an already-reported intent returns early server-side. Do not pass a fixed key here to "make
it idempotent" — that would cache the first response, including a transient 5xx's *absence* from the
cache, and buys nothing the server does not already give.

Branch on `code === IT_STABLE_DEPOSIT_CLAIMED` with a distinct, prominent message: "This transaction has already been reported. Contact support." That is the front-running case surfacing, and it must not read as a generic error. Do not retry that one — it will never succeed.

- [ ] **Step 7: Verify**

Run: `cd tsa-app && npx tsc --noEmit && npx jest --watchAll=false`

Manual, on a testnet or with a trivial amount:
1. Polygon source → confirm one transaction, no approval, and the hash reported. Check the transfer's
   token contract against `s.stableTokens[asset]` on the block explorer — this is the check that
   proves the `deposit.tokenAddress` wiring works end to end.
2. Non-Polygon source → confirm the approve → swap sequence and the stage strings.
3. Kill the network right after broadcast → confirm the hash is in AsyncStorage and the screen offers Retry.
4. Force a reprice (create against a moved rate, or stub `netNgnKobo`) → confirm the banner appears
   with the new figure, and that confirming a SECOND time proceeds rather than looping.

- [ ] **Step 8: Commit**

```bash
git add tsa-app/app/wallet/it/sell-stable.tsx
git commit -m "feat(it): authorize, sign and broadcast a stablecoin sell"
```

---

## Task 8: Progress screen with hops and polling

**Files:**
- Modify: `tsa-app/app/wallet/it/intent/[id].tsx`

**Interfaces:**
- Consumes: `statusToHops`, `isTerminalStableStatus` (Task 2); `reportStableDeposit` (Task 3)

- [ ] **Step 1: Render hops for stablecoin intents**

When `intent.asset` is `USDC` or `USDT`, render `statusToHops(intent.status)` as a vertical list —
done rows ticked, the active row with a spinner, pending rows dimmed.

**The existing rendering is actively WRONG for this asset, not merely irrelevant, so it must be
branched rather than left alone:**

- The screen renders `fmtMcgp(intent.mcgpAmount)`, and `CreateStableSellIntent` writes
  `McgpAmount: "0"` for every stable sell (it_stable_sell.go:161) — so a user selling 100 USDC sees
  **"0 MCGP"**. Branch the amount row to show `assetAmount` formatted at 6 decimals with the asset
  symbol, and the rate row to the stablecoin rate.
- `statusLabel` (intent/[id].tsx:36-59) has no case for `pending_deposit`, `deposit_reported`,
  `deposit_confirmed` or `converting`, so it falls through to `default: return status` and renders
  raw snake_case at the user. Extend it with those four.

Keep the MCGP path byte-for-byte for every other asset.

- [ ] **Step 2: Poll while non-terminal**

Follow the P2P cockpit (`app/p2p/trade/[tradeId].tsx:181-221`):

```tsx
const isFocused = useIsFocused();
const pollingActive = isFocused && !isTerminalStableStatus(intent?.status ?? '');

useEffect(() => {
  if (!pollingActive) return;
  load();
  const id = setInterval(load, 10_000);
  return () => clearInterval(id);
}, [load, pollingActive]);
```

Polling must stop on terminal states — otherwise a settled order polls forever in the background.

- [ ] **Step 3: Retry an unreported deposit on open**

**Placement matters: this runs ONCE PER SCREEN OPEN, not inside `load()`.** Step 2 calls `load()`
every 10 seconds while the intent is non-terminal; if this snippet lived inside `load()` it would
re-POST the deposit report on every tick until the key cleared. Put it in its own
`useEffect(..., [id])` that fires after the first successful load, and add
`import AsyncStorage from '@react-native-async-storage/async-storage';` plus
`import { reportStableDeposit, IT_STABLE_DEPOSIT_CLAIMED } from '@/services/itApi';` to the screen —
neither is imported there today.

```ts
// Task 7 stores the hash before reporting it, so a report that failed then
// (dead network, app killed) is recoverable here. Safe to retry: the endpoint
// is idempotent server-side — same hash, same intent, no-op.
const pending = await AsyncStorage.getItem(`it_stable_pending_tx_${id}`);
// Gate on NON-TERMINAL, not on `pending_deposit`. The case this exists for —
// the report succeeded server-side but the response was lost — leaves the
// intent at `deposit_reported`, so a pending_deposit-only guard would never
// fire and the key would accumulate forever, once per abandoned intent.
//
// Safe to re-report: the same hash on an already-reported intent returns early
// server-side. That server-side idempotency — NOT the Idempotency-Key
// middleware — is what makes this safe; `req` mints a fresh key per call, so
// the middleware sees an unrelated request each time and dedups nothing.
// Clear the key on success OR on already-claimed, since neither will ever
// succeed later.
if (pending && intent && !isTerminalStableStatus(intent.status)) {
  const r = await reportStableDeposit(String(id), pending);
  if (r.success || r.code === IT_STABLE_DEPOSIT_CLAIMED) {
    await AsyncStorage.removeItem(`it_stable_pending_tx_${id}`);
  }
}
```

- [ ] **Step 4: Offer resume when nothing was ever sent**

When the status is `pending_deposit` and no stored hash exists, the user created an intent and abandoned it before sending. Show `intent.depositAddress` (persisted server-side and returned by `GetIntent`) and a **"Continue — send your USDC"** action rather than leaving the order looking stuck. This is also the only exit for the orphaned intent a rejected reprice leaves behind — see Task 7 Step 1.

- [ ] **Step 5: Show terminal outcomes plainly**

`settled` → all hops ticked plus the amount paid. `reversed` / `failed` → the failed hop plus `intent.failureReason`.

**Render `failureReason` as-is.** `GetIntent` already runs it through `UserFacingITFailure`
(`it_handler.go:461`), so the plain-language mapping is done server-side and a second app-side
mapping would only drift from it. If a message reads badly, fix `UserFacingITFailure` — one place,
both clients.

- [ ] **Step 6: Verify**

Run: `cd tsa-app && npx tsc --noEmit && npx jest --watchAll=false`
Then: open a live intent, confirm hops advance without a manual refresh, background and reopen the app to confirm polling resumes, and confirm a settled order stops polling (add a temporary log if needed, then remove it).

- [ ] **Step 7: Commit**

```bash
git add tsa-app/app/wallet/it/intent/[id].tsx
git commit -m "feat(it): live progress and polling for stablecoin sells"
```

---

## Self-Review Notes

**Spec coverage:** §3 structure → Tasks 4/5/6. §4 quote-against-arrival → Task 1 + Task 6 Step 3. §5 flow → Tasks 6/7. §6 signing (auth, gas, pinned signer, two paths) → Task 7 Steps 2–4. §7 progress → Task 8. §8 error handling, including the persist-before-report rule → Task 7 Steps 5–6 and Task 8 Step 3. §9 service fixes → Task 3. §10 money handling → Global Constraints + Task 1. §11 testing → Tasks 1–3. §12 minimums risk → Task 1.

**Revised after review.** The first draft contained five Critical defects, three of which needed
backend changes (now landed) and two of which were mine alone: one `decimals` value used for both the
source parse and the Polygon arrival — the exact 10^12 error this plan's own Global Constraints warn
about — and a `sendPreparedTransaction` return treated as a string, which would have persisted
`"[object Object]"` as the transaction hash on the primary route. Two tests would have passed against
broken code: the idempotency assertion certified the app's half of a contract the server did not
hold, and `estimateArrival('100','0',6) === '100'` codified the silent bait-and-switch it was meant
to prevent. Both are rewritten.

**Revision 2 — after the re-review.** Revision 1 closed the hard problems and opened a cluster of
new ones in the two tasks that move money. Four were Critical and all four were mine:

- **Three symbols the plan depended on were defined by nothing.** `reviewIdempotencyKey` was
  *referenced* in Task 7 and created by no task; `reviewNetKobo` was compared against and never set,
  which made the whole create-time reprice guard a literal no-op (`undefined !== undefined` is
  `false`); `setRepricedTo` set state that nothing rendered, so the user would have been bounced back
  to a review screen showing the **old** figure with no explanation.
- **The reprice path had no exit.** Even once the guard fired, confirming again with the same key
  would have had the middleware replay the cached 2xx verbatim — the same intent, the same net — so
  the comparison failed identically forever and the user could never complete the sell. Accepting a
  reprice now re-mints the key. The cost is stated rather than hidden: a rejected reprice orphans a
  `pending_deposit` intent, and there is no cancel endpoint.
- **The Polygon destination token contract was resolved nowhere.** Polygon carries both native USDC
  and bridged USDC.e; picking the wrong one leaves the deposit unverifiable and the user's funds at
  Flutterwave unattributed. Rather than have the app guess, a backend change now returns
  `deposit.tokenAddress` — the exact contract `ConfirmStableDeposit` verifies against — and fails
  closed when it is unconfigured. All app-side resolution of it is gone. The quote-time LiFi call
  passes the asset SYMBOL and is documented as pricing-only; the execution quote, built after create
  for exactly this reason, uses the returned contract.
- **The status-enumeration test — the plan's best test — no longer guarded anything.** The
  unknown-status fallback returns all four hops with labels, all `pending`, so an unmapped status
  passed. It now asserts `hops.some(h => h.state !== 'pending')`.

Also closed: the create guard ran three steps after the first dereference; C5's "nothing broadcast —
safe to fail back" was false in two distinct ways and is now a recovery route instead of a claim;
`ErrArrivalUnpriceable` is a class with a `code`; both "Expected: PASS (N tests)" counts were wrong;
and the thirteen minors the first review raised and revision 1 skipped — `StableQuote.amount` is a
string, `{currency:'NGN', ...body}` does not default an explicit `undefined`, `ensureGas` is advisory
and alerts by itself, `readErc20BalanceOf` throws, the LiFi path signs twice minutes apart, M6's
exact expected value, a ≤6-decimal input guard, `CountdownBar` already exists, Task 4's `onChange`
memoisation, the `ITAgreementGate` making `it_agreement_required` unreachable, and `GetIntent`
already applying `UserFacingITFailure`.

Tasks 6 and 7 are now self-contained the way Tasks 1–3 already were: every symbol they use is
declared in Task 6 Step 1 or created by a named step.

**Known gaps, deliberate:**
- Screens have no automated tests — no harness exists in this repo. Their logic lives in Tasks 1–2, which do.
- Task 7 can only be fully verified against a live chain. Its manual steps are therefore part of the task, not optional.
- The backend's minimum (its finding I5) is still open; Task 1's floor is a usability guard, not enforcement.
- A rejected reprice orphans a `pending_deposit` intent. Harmless, but real; a
  `DELETE /it/stable/sell/:id` for an untouched intent is the follow-up.
- The quote-time arrival estimate is priced against LiFi's own canonical Polygon USDC, which may not
  be the contract the backend confirms against. That can skew the estimate; it cannot misroute funds,
  because the execution quote and the transfer both use `deposit.tokenAddress`.
- The window between a successful broadcast and `AsyncStorage.setItem` is not closed and cannot be
  closed app-side. `/wallet/submit-tx` and `getTransactionHistory` are the recovery route.

**Not covered by this plan:** the buy/on-ramp direction, which does not exist on the backend either, and mobile-money payout rails.
