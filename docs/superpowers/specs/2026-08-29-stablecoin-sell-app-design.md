# Stablecoin Sell — Mobile App (tsa-app)

**Date:** 2026-08-29
**Status:** Draft — pending review
**Repo:** `tsa-dev/tsa-app` (React Native 0.81 + Expo 54 + expo-router v6)
**Backend:** merged and dormant — see `2026-08-27-stablecoin-fiat-ramp-design.md`

---

## 1. Objective

Let a user sell USDT/USDC for fiat from inside Instant Transfer. The backend for this
shipped in commit `8172697` and is fully dormant behind config; there is currently no app
code that calls it, so the IT screen shows MCGP only.

### 1.1 What the backend already provides

| Endpoint | Purpose |
|---|---|
| `POST /api/it/stable/quote` | price a sell — `gross`, `afterSpread`, `fee`, `net`, `rate`, `expiresAt` |
| `POST /api/it/stable/sell` | create the intent → `{intent, deposit{chain, address, asset, amount}}` |
| `POST /api/it/stable/sell/:id/deposit` | report the transaction the user broadcast |
| `GET /api/it/intents/:id` | poll the intent's status |

The platform is a **broker**: the user's stablecoin goes from their own wallet straight to
Flutterwave's Polygon address. The app never sends funds to an address the platform
controls, and the backend holds no float.

### 1.2 Scope

**In:** sell (off-ramp) only, from any LiFi-supported source chain, to a Nigerian bank
payout, with a live progress view.

**Out:** buy (on-ramp) — that is Phase B on the backend and does not exist yet. Also out:
mobile-money payout rails, and saved payout destinations.

---

## 2. Decisions taken

| Decision | Choice | Why |
|---|---|---|
| Placement | Asset picker inside IT sell | One entry point; users look for selling under IT |
| Wait UX | Live progress screen, leavable | The flow takes 3–10 minutes; a spinner would hold the app hostage and a silent redirect invites "did it work?" tickets |
| Source chains | All LiFi-supported | Widest reach. Safe here because the destination is chosen by us, never typed by the user — extra chains only mean more no-route quotes, not fund loss |
| Screen structure | New route, one shared extraction | `sell.tsx` is already 681 lines on a live money path |

---

## 3. Structure

`sell.tsx` gains an asset picker. Choosing USDC/USDT pushes a new route which owns its own
flow; the MCGP path is otherwise untouched.

```
app/wallet/it/
  sell.tsx           (modified) asset picker → routes USDC/USDT away; MCGP unchanged
  sell-stable.tsx    (new)      form → review → signing → handoff
  intent/[id].tsx    (modified) renders hop progress + polls when asset ≠ MCGP
components/it/
  BankDestination.tsx (new)     extracted from sell.tsx, shared by both flows
utils/
  stableSellQuote.ts  (new)     pure money math + status→hops. Unit-tested.
services/
  itApi.ts            (modified) the three stable endpoints + Idempotency-Key
```

Only the bank-destination block is extracted — bank picker, 400 ms debounced NUBAN name
enquiry, resolve/verified/offline states, derived country. It is identical between flows and
the fiddliest thing to duplicate correctly. Everything else in `sell.tsx` stays where it is,
so the live MCGP path changes as little as possible.

---

## 4. Quote composition — the central subtlety

The backend scales the payout by `received / quoted`: a bridge delivers less than it was
given, and the user is paid on what actually arrived. So the app must quote the fiat against
LiFi's **estimated arrival**, not the amount typed.

```
user types 100 USDC on BSC
  → LiFi quote      ≈ 99.88 arrives on Polygon
  → POST /it/stable/quote with 99.88
  → "You send 100 · arrives ≈99.88 · you receive ₦X"
```

Quoting the typed 100 instead would make the final figure always shrink, which reads as a
bait-and-switch. On Polygon there is no bridge, so sent equals arrives and the two lines
collapse into one.

---

## 5. Flow

1. **Balances** — `getDiscoveredTokens()` for which chains hold USDC/USDT, then
   `readErc20BalanceOf` on the selected chain for the authoritative figure. (The backend
   balance endpoint zeroes imported tokens, which is why All Swap already reads the chain
   directly.)
2. **Token address** — `getLiFiToken(chainId, symbol)`, falling back to the per-chain list.
3. **Crypto quote** — LiFi with `toChain: 137` and `toAddress` = Flutterwave's address.
   **Skipped entirely when the source is Polygon.**
4. **Fiat quote** — `POST /it/stable/quote` with the expected arrival (§4).
5. **Create** — `POST /it/stable/sell` → `{intent, deposit}`.
6. **Sign + broadcast** (§6) → source transaction hash.
7. **Report** — `POST /it/stable/sell/:id/deposit`, then
   `router.replace('/wallet/it/intent/<id>')`.

Create must precede signing: the app learns the deposit address from the response rather
than hardcoding Flutterwave's address. The cost is that a user who creates and then abandons
leaves an intent in `pending_deposit` with no funds sent — so the progress screen offers
**"Continue — send your USDC"** in that state, making it resumable rather than orphaned.

---

## 6. The signing leg

Two paths, chosen by source chain:

- **Polygon** — a plain ERC-20 transfer to the deposit address via
  `sendPreparedTransaction`. No LiFi, no approval, no bridge fee, ~30 seconds. The backend's
  same-chain branch skips the LiFi status lookup, so nothing downstream needs a route.
- **Any other chain** — `getLiFiQuote({ toAddress })` then `executeLiFiSwap`, which already
  handles approve → wait-for-receipt → swap and returns the source hash.

Before either, in order:

1. **`AuthorizationModal`** (PIN/biometric). Spot Trade gates on this; All Swap does not.
   This is a money path, so it gates.
2. **`ensureGas`** on the source chain — fail before signing, not after.
3. **Sign with a pinned address.** `signLiFiTransaction` takes no expected address and
   always uses the *active* wallet. We quote and create against one specific address, so a
   wallet switch mid-flow would sign with the wrong key. The address is captured at quote
   time and asserted before signing.

---

## 7. Progress screen

`intent/[id]` renders hops when the asset is not MCGP:

| Backend status | Shown as |
|---|---|
| `pending_deposit` | Waiting for your transfer · **Continue** |
| `deposit_reported` | Confirming your transfer |
| `deposit_confirmed` | Arrived at Flutterwave ✓ |
| `converting` | Converting to ₦ |
| `debited` | Paying your bank |
| `settled` | Paid ✓ |
| `reversed` / `failed` | Failed — with the reason |

Polling follows the P2P trade cockpit: fetch on focus, ~10 s interval while focused and
non-terminal, stop on terminal. No websocket — the backend emits no events for this flow.

---

## 8. Error handling

Most failures are safe because nothing has moved: a failed or no-route quote, a rejected
create, a cancelled PIN, insufficient gas. Each surfaces a message and returns to the form.
The auth prompt and gas check run before signing specifically to keep them in this category.

**The one dangerous case — broadcast succeeded, reporting the hash failed.** Funds are in
flight to Flutterwave and the backend does not know which intent they belong to. Therefore:

- **Persist the hash the instant it exists**, keyed by intent id, before attempting the
  report. It must never live only in component state.
- **Retry with backoff**; if it still fails, show the hash with a Retry action and retry
  again whenever the progress screen opens.
- Safe to retry: the endpoint is idempotent server-side — the same hash on the same intent
  is a no-op.

Remaining states map to plain language, never internals:

| Condition | Message |
|---|---|
| `ErrStableRateImplausible` | "Pricing is temporarily unavailable — try again shortly" |
| `ErrStableQuoteExpired` | auto re-quote, show the new figure (never swap it silently) |
| `ErrStableDepositAlreadyClaimed` | "This transaction has already been reported" — loud; contact support |
| conversion attempts exhausted | "Needs review — we've been notified" |
| bridge still pending past a threshold | "Taking longer than usual — we're tracking it" |

`ErrStableDepositAlreadyClaimed` is the front-running case surfacing in the UI. It should be
prominent rather than a toast.

---

## 9. Two service-layer fixes this flow forces

1. **Idempotency.** `itApi`'s `req` wrapper sends no `Idempotency-Key` (unlike `p2p.ts`'s
   `request`), so a retried `POST /it/stable/sell` creates a second intent — and every intent
   is a claim on a deposit. Add the header for non-GET calls, mirroring `p2p.ts:313-315`.
2. **HTTP status.** `itApi`'s `ApiResult` drops the status code, so the screen cannot
   distinguish a 400 the user can fix from a 503 it should retry. Add `status`, again
   mirroring `p2p.ts`.

Both are small, both are in the shared wrapper, and both are load-bearing for a money path.

---

## 10. Money handling

- **Reuse `p2p.ts`'s token math** — `parseTokenAmount`, `formatTokenAmount`,
  `p2pTokenDecimals`. These are 6-decimal-safe and pure-string. `sell.tsx` hardcodes 18
  decimals in two places; using that for USDC would be wrong by a factor of 10¹².
- Fiat amounts arrive as **minor-unit integer strings** and stay strings until formatted.
- The app never re-derives the payout. It displays what the backend quoted, and the progress
  screen displays what the backend settled.

---

## 11. Testing

The repo has no `@testing-library/react-native`; its stated convention (see the headers of
`utils/buyQuote.ts` and `utils/sellQuote.ts`) is that money math is extracted into pure
modules so it can be tested at all. Following that:

- **`utils/stableSellQuote.ts`** — arrival estimation, fiat composition, minimum checks
  (§12), display strings. Pure, unit-tested, no React.
- **`statusToHops(status)`** — a pure mapping with a test enumerating **every** backend
  `ITStatus*` constant, so an unmapped status fails a test rather than rendering a blank
  progress screen. The backend added four statuses on this branch; this is the guard against
  a fifth arriving silently.
- **Service tests** for the new endpoints and the wrapper changes, mirroring
  `services/__tests__/lifiFlow.test.ts`.
- Screen components are not unit-tested (no harness exists); their logic lives in the pure
  modules above, which is the point of extracting it.

---

## 12. Risks

- **The backend is unproven end to end.** No funded Flutterwave conversion has been observed
  reaching `SUCCESSFUL` — the sandbox does not settle them. A production canary is required
  before this feature is enabled for users. The app should not ship to stores ahead of that.
- **Front-running is open.** Nothing binds the reporting user to the transaction's sender.
  `ErrStableDepositAlreadyClaimed` is how it surfaces to a victim, which is why §8 treats it
  prominently rather than as a generic error.
- **`ensureGas` is advisory.** It checks the native balance but cannot predict the exact
  bridge gas cost, so a signed transaction can still fail for gas on a volatile chain.
- **All chains means many no-route quotes.** Chains where the user holds nothing will fail to
  quote. Showing per-chain balances in the picker keeps that from reading as a bug.

- **Minimum trade sizes are not enforced anywhere yet.** The backend's own review raised this
  (finding I5) and triaged it as must-fix-before-launch rather than before-merge, so it is
  still open: `ErrStableBelowMinimum` exists but only fires for a non-positive amount.
  Flutterwave's documented floors — $5 inbound, 10 USDC/USDT outbound — are unchecked on both
  sides today. Without a floor, a 1 USDC sell quotes cleanly, the user broadcasts
  irreversibly, and the provider then rejects the conversion with the funds already at
  Flutterwave. That is precisely the failure the backend spec's §6.4 was written to prevent.

  The app will enforce a client-side floor so the flow is not obviously broken, but **that is
  belt-and-braces, not the fix** — a client-side-only check on a money constraint is bypassable
  and does not protect an API caller. Backend I5 should land before this feature is enabled.
  Treat the app-side floor as a usability guard, and the backend's as the real one.
