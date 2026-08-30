# Review — `2026-08-29-stablecoin-sell-app.md`

**Verdict: REVISE.** The plan's shape, task ordering and money-math instincts are right, and most
of its codebase citations check out. But three of its load-bearing safety claims are false against
the merged backend, one task is not implementable at all, and its highest-value test asserts a
contract the server does not hold.

Everything below was verified by opening the file. Line numbers are as of this branch.

---

## Verification of the plan's codebase claims

| Claim | Verdict |
|---|---|
| `formatTokenAmount(base, decimals, displayDecimals=4)` — `p2p.ts:824` | ✅ 3-arg call valid; `formatTokenAmount(b, d, d)` trims trailing zeros, pure string math |
| `parseTokenAmount(input, decimals)` — `p2p.ts:874` | ✅ throws on junk/scientific notation |
| `p2pTokenDecimals(chainId, symbol)` — `p2p.ts:946` | ⚠️ exists, but returns **18** for USDT/USDC on BSC (56/97) and only knows Sonic/BSC — see C3 |
| `itApi.ts` `req` at line 42 | ✅ quoted accurately; 4th `opts` param breaks no existing caller (all call sites pass ≤3 args) |
| `getAuthHeaders(): Promise<Record<string,string>>` — `walletApi.ts:283` | ✅ mutable object, `headers['Idempotency-Key'] = …` typechecks |
| `getLiFiQuote({toAddress})` → `.toAmount` — `lifi.ts:49, 185` | ✅ accepts `toAddress`, returns `.toAmount`; **throws** on failure and defaults to `'0'` — see I3 |
| `executeLiFiSwap({quote, fromSymbol, fromDecimals, fromAmount, walletAddress, fromChainId, onStage})` → `Promise<string>` — `lifiFlow.ts:19-44` | ✅ exact match |
| `prepareSendTransaction(sym, to, amount, chainId, opts)` — `walletApi.ts:407` | ✅ match |
| `submitTransaction(signed, type, sym, to, amount, chainId, fromAddress)` — `walletApi.ts:451` | ✅ match (7 args) |
| `sendPreparedTransaction(ops)` — `walletApi.ts:509` | ❌ returns `ApiResponse<{txHash}>`, **not** a string — see C5 |
| `signTransaction(tx, expectedAddress?)` — `wallet.ts:300` | ✅ match |
| `ensureGas(chainKey, address, onAlert?)` — `wallet.ts:394` | ✅ signature matches; behaviour caveats in M3 |
| `getActiveWallet()` — `wallet.ts:41`, `readErc20BalanceOf(addr, owner, chainKey)` — `wallet.ts:504` | ✅ match (`readErc20BalanceOf` **throws** when all RPCs fail) |
| `AuthorizationModal {visible,title,onAuthorized,onCancel}` — `components/common/AuthorizationModal.tsx:34-40` | ✅ exact |
| `PickerSheet` / `PickerItem` — `components/ui/PickerSheet.tsx:14-32`; `SelectRow` — `SelectRow.tsx:9-20`; `SummaryCard`/`SummaryRow` — `components/ui/SummaryRow.tsx:8`, `Card.tsx` | ✅ all exist, props as used |
| Quote fields `gross, afterSpread, fee, net, rate, expiresAt` | ✅ exact — `it_handler.go:711-722` |
| `POST /it/stable/sell` → `{intent, deposit}` | ✅ `it_handler.go:763`; deposit JSON is `{chain, address, asset, amount}` (`it_stable_sell.go:76-82`) |
| Deposit endpoint takes `{txHash}` at `/it/stable/sell/:id/deposit` | ✅ `it_handler.go:693-695`, `routes.go:910` |
| `STABLE_SELL_STATUSES` matches `it_models.go` | ✅ exact — `pending_deposit`, `deposit_reported`, `deposit_confirmed`, `converting` (`it_models.go:42-45`) + `debited`, `settled`, `reversed`, `failed`, `cancelled` (`:23-27`). `awaiting_rate`/`processing`/`pending_payment`/`credited` are MCGP-only and correctly excluded |
| `sell.tsx` line refs (46, 175-226, 189-220, 274-289, 522-528, 541-603, 661-678) | ✅ all land where claimed |
| `_layout.tsx` screens at 76-83 | ✅ (76-82) |
| `all-swap.tsx:50` / `:215-253`, `spot-trade.tsx:88-101`, P2P poll `181-221` | ✅ line refs correct — but `all-swap.tsx` lives at **`screens/all-swap.tsx`**, not `app/wallet/` |

---

## CRITICAL

### C1 — `Idempotency-Key` is a no-op on `/it/*`. The plan's central retry-safety claim is false, and its test certifies nothing.

`middleware.Idempotency(config.DB)` is instantiated at **`routes.go:702`, inside the P2P block**, and
applied only to P2P write paths (`routes.go:709` etc.). The IT group (`routes.go:892-911`) is:

```go
it := api.Group("/it")
it.Use(auth)          // ← auth only. No idempotency.
```

So `POST /it/stable/sell` is **not** idempotency-aware. Task 3's header does nothing; Task 7's
comment — *"a network retry of THIS create must not mint a second intent"* — is untrue as written.
A retry mints a second intent, and every intent is a claim on a deposit.

The test makes this worse rather than catching it:

```ts
expect(init.headers['Idempotency-Key']).toBeTruthy();
```

That passes against a server with zero idempotency. It asserts the app's half of a contract the
backend does not hold — precisely the class of stubbed pass-through this project has shipped before.

**Fix:** add a backend prerequisite task applying `middleware.Idempotency(config.DB)` to
`it.POST("/stable/sell", …)` (and, while there, `/it/buy`, `/it/sell`, `/it/va/activate`). Until it
lands, delete the safety claim from the comments rather than leave a false one in a money path.

**And a second defect that only appears once the middleware exists:** Task 7 Step 1 mints the key
into a ref that is never reset. If the user backs out of Review, edits the amount and confirms
again, the same key arrives with a different payload and the middleware returns
**409 `"Idempotency-Key reused with a different request payload"`** (`idempotency.go:110-116`) — the
user is wedged until they kill the screen. Mint the key per *confirmed review snapshot*, alongside
the existing `review*` snapshot in Task 6 Step 5, not per screen mount.

### C2 — The backend emits no `code` field on these endpoints, so every code-based branch in the plan is dead.

All three stable handlers report errors through
`utils.ErrorResponse(c, stableStatusFor(err), err.Error())` (`it_handler.go:704, 713, 745, 752, 762, 782, 787`),
and `utils/response.go:17-22` is:

```go
c.JSON(status, gin.H{"success": false, "message": message})
```

No `code`. The only IT endpoint that emits one is `requireITAgreement`, which hand-rolls the JSON
(`it_handler.go:848-853`). Therefore:

- `IT_STABLE_DEPOSIT_CLAIMED` / `r.code === 'stable_deposit_already_claimed'` **never fires**;
- the whole spec §8 error table (`ErrStableRateImplausible`, `ErrStableQuoteExpired`, conversion
  exhausted…) has nothing to match on.

Task 3's test fabricates `code` in its own mock response, so it stays green forever against a server
that never sends one. The front-running case — the one the spec calls out as needing to be
*prominent* — will render as a generic error.

**Fix:** backend prerequisite — an `ErrorResponseWithCode` and a `stableCodeFor(err)` alongside
`stableStatusFor` (`it_handler.go:814`). Matching on `err.Error()` strings is the fallback, and it is
a bad one: those strings are `"it stable: this deposit has already been reported against a sell"`.

### C3 — One `decimals` used for both the source parse and the Polygon arrival — wrong by 10¹² on BSC, the plan's own example chain.

Task 6 Step 3:

```ts
fromAmount: parseTokenAmount(amount, decimals),   // SOURCE token decimals
…
const arrival = estimateArrival(amount, lifiToAmount, decimals);  // DESTINATION (Polygon) decimals
```

These are not the same number. LiFi's `toAmount` is always denominated on Polygon (`toChain: 137`),
where USDC/USDT are 6 decimals. The source can be anything. `p2pTokenDecimals(56, 'USDT')` returns
**18** (`p2p.ts:947-950` — BSC stablecoins genuinely are 18), so on the BSC source the plan's own
Task 3 test uses:

- `estimateArrival('100', '99884217', 18)` → `"0.000000000099884217"`
- backend `StableToBaseUnits` rejects >6 fractional digits → `ErrStablePrecision` → **400**

Every non-6-decimal source chain fails at the quote. This is exactly the 10¹² error the plan's own
Global Constraints warn about, reproduced inside the plan.

**Fix:** two variables — `sourceDecimals` (for `parseTokenAmount`) and a constant
`POLYGON_STABLE_DECIMALS = 6` (for `estimateArrival`). Change `estimateArrival`'s parameter name to
`destDecimals` so the confusion cannot recur.

Related: `p2pTokenDecimals` is a **Sonic/BSC whitelist** and knows nothing about Arbitrum, Base,
Linea or Avalanche — it silently returns 6 for all of them. That happens to be correct today, but
"correct by accident on a whitelist that does not cover the chains we ship" is not a foundation for a
money path. Prefer `getLiFiToken(chainId, asset).decimals` — already the `spot-trade.tsx:88-101`
pattern this plan cites for token resolution, and it carries the decimals in the same response.

### C4 — Task 8 Step 4 is not implementable: nothing returns the deposit address after create.

`StableDepositInstructions` is **computed and discarded** — built in the return of
`CreateStableSellIntent` (`it_stable_sell.go:186-192`) and never persisted. `models.ITIntent` has no
deposit-address column (I read every field, `it_models.go`), and `GET /it/intents/:id`
(`it_handler.go` `GetIntent`) returns the intent alone.

So the progress screen cannot show *"Continue — send your USDC"* with an address. And recovering it
by re-calling `POST /it/stable/sell` creates a **second intent** (with C1, no idempotency to stop it).

This matters more than a missing screen state: spec §5 uses resumability as the *justification* for
"create must precede signing". That justification does not currently hold.

**Fix (backend prerequisite):** persist the address on the intent and include it in `GetIntent`, or
add `GET /it/stable/sell/:id/deposit`. Either is small. Without one, cut Task 8 Step 4 and say
plainly that an abandoned intent is orphaned.

### C5 — `sendPreparedTransaction` returns a result object, not a hash — the Polygon path loses the hash.

`walletApi.ts:509`:

```go
export async function sendPreparedTransaction(ops: SendTxOps): Promise<ApiResponse<{ txHash: string }>>
```

Task 7 Step 4 writes `txHash = await sendPreparedTransaction({...})` into `let txHash: string`. TS
will reject it; if anyone silences it, the Polygon path — the plan's *primary, no-bridge* route —
persists and reports `"[object Object]"`, losing the real hash and breaking the plan's own
"never lose a broadcast transaction hash" constraint at the exact moment it matters.

```ts
const res = await sendPreparedTransaction({ … });
if (!res.success || !res.data?.txHash) { /* nothing broadcast — safe to fail to the form */ }
txHash = res.data.txHash;
```

Same omission on the line above it: `created.data.deposit.address` is dereferenced with no
`created.success` / `created.data` check.

---

## IMPORTANT

### I1 — Review can display a payout the user will not receive: the create **re-quotes** server-side.

`PostStableSell` calls `QuoteStableSell` **again** (`it_handler.go:743-747`) and creates the intent
against *that* quote, not the one shown at Review. The rate can move between the two calls. Task 7
never compares `created.data.intent.netNgnKobo` / `rateKobo` against the reviewed figures before
gating on the PIN — so the user authorizes against a number that is already stale.

Add an explicit comparison after create and before `AuthorizationModal`: if the created intent's net
differs from the reviewed net, re-present the new figure rather than proceeding. This is the same
principle the plan already applies to quote expiry ("show the new figure rather than swapping it
silently"), and it is missing at the one point where money is actually committed.

### I2 — On any bridged sell, the ₦ figure is an estimate presented as fact.

The payout is `received / quoted` (`it_models.go`, `ReceivedAmount` comment). `toAmount` is LiFi's
*estimate*; under-delivery scales the payout down. Task 6 Step 5 renders `net` as a firm line and
only qualifies the crypto side via `formatSendVsArrive`.

Also unhandled in either direction: `stableOverDeliveryCeiling` (`it_stable_sell.go:47-59`) parks the
intent for operator review if **more than 2% above** the quoted amount arrives. An over-conservative
LiFi estimate on a volatile route therefore strands the user's sell in manual review with no app-side
explanation. Nothing in Tasks 6-8 mentions this state.

### I3 — `getLiFiQuote` throws, and its `'0'` fallback silently defeats the whole of §4.

`lifi.ts:185`: `toAmount: data.estimate?.toAmount ?? data.toAmount ?? '0'`. `estimateArrival` treats
`'0'` as "no bridge" and returns `sentMajor`. So a routeless or malformed LiFi response makes the app
quote against the **typed** amount — exactly the bait-and-switch §4 exists to prevent, and now
silent. Task 1's test *codifies* the bug:

```ts
expect(estimateArrival('100', '0', 6)).toBe('100');   // asserts the wrong behaviour
```

"Same-chain, nothing lost" and "we could not price the bridge" must be distinguishable: `null` = no
bridge (Polygon only); `'0'`/absent/throw = block the quote with an error. Separately, Task 6 Step 3's
`(await getLiFiQuote({…})).toAmount` has no `try/catch` — `getLiFiQuote` throws on `!res.ok`
(`lifi.ts:180`), so a no-route response crashes the effect rather than surfacing a message.

### I4 — Task 3's test cannot run as written.

`itApi` imports `getAuthHeaders` from `walletApi`, which calls `AsyncStorage.getItem`
(`walletApi.ts:284`). Every existing test that reaches walletApi mocks it first —
`services/__tests__/sendRetry.test.ts:1-3`:

```ts
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
```

There is no `setupFiles` in the jest config (`package.json`), so without that mock `getAuthHeaders`
throws, `req`'s catch returns `{success:false}`, `fetch` is never called, and every assertion fails on
`mock.calls[0]` being `undefined` — an unrelated TypeError masking whatever the test meant to prove.

### I5 — `ITIntent` is missing every field Task 8 reads.

`itApi.ts:21-36` has `id, direction, status, mcgpAmount, ngnKobo, rateKobo, feeNgnKobo, netNgnKobo,
paymentRef, txHash, failureReason, createdAt`. Task 8 Step 1 branches on **`intent.asset`**, which is
not on the type — a compile error. Also absent and needed: `assetAmount`, `currency`, `sourceChain`,
`depositTxHash`, `receivedAmount`, `netMinor`. All are emitted by the Go model. No task adds them.

### I6 — The progress screen will render MCGP-shaped rows for a stablecoin intent.

`app/wallet/it/intent/[id].tsx` renders `fmtMcgp(intent.mcgpAmount)` — and `CreateStableSellIntent`
writes `McgpAmount: "0"` for every stable sell (`it_stable_sell.go:161`), so the screen shows "0
MCGP". `statusLabel` (lines 36-59) has no case for `pending_deposit`, `deposit_reported`,
`deposit_confirmed` or `converting`, so it falls through to `default: return status` and renders raw
snake_case. Task 8 Step 1 only adds hops and says "keep the existing MCGP rendering untouched" — the
existing rendering is *wrong* for this asset, not merely irrelevant. The task must branch the
amount/rate rows and extend `statusLabel`.

### I7 — Task 8 Step 3's retry gate leaks the stored hash forever.

```ts
if (pending && intent?.status === 'pending_deposit') { … }
```

The recovery case this exists for — report succeeded server-side, response lost — leaves the intent
at `deposit_reported`, so the guard never fires and the AsyncStorage key is never removed. It
accumulates per abandoned intent and is never revisited.

`ReportStableDeposit` is genuinely idempotent here — same hash on a non-`pending_deposit` intent
returns `nil` early (`it_stable_sell.go`, first branch), so re-reporting is safe. Retry whenever a
stored hash exists and the status is non-terminal, and clear the key on success **or** on
already-claimed.

### I8 — `setAsset` does not exist.

Task 5 Step 2's snippet calls `setAsset(asset)`. `sell.tsx` has no asset state — the only occurrence
of `asset` in the file is the literal `asset: 'MCGP'` at line 297. The task must introduce the state
before using its setter.

### I9 — Task 2's failure mapping is wrong, and its test is too weak to notice.

`statusToHops` marks hop 0 (`Sent from your wallet`) as `failed` for `reversed`. But `reversed` means
the funds *did* send, *did* arrive, *were* converted, and the **payout** failed — the user is shown a
screen claiming their wallet transfer failed when it succeeded and their money is at Flutterwave. The
test only asserts:

```ts
expect(hops.some((h) => h.state === 'failed')).toBe(true);
```

which passes against this mapping and against any other. Assert *which* hop failed per status.

Credit where due: the `STABLE_SELL_STATUSES` enumeration test (Task 2 Step 1) is the plan's best
test — it would genuinely catch a new backend status, and the list matches `it_models.go` exactly.

---

## MINOR

- **M1** `StableQuote.amount` is declared `number`, but the handler emits `fmt.Sprintf("%.6f", …)` —
  a string (`it_handler.go:714`). Contradicts the plan's own Interfaces line ("all strings except
  `expiresAt`").
- **M2** `currency` is `binding:"required"` on both requests. `{ currency: 'NGN', ...body }` with an
  explicit `currency: undefined` overrides the default to `undefined`, `JSON.stringify` drops it →
  400. Use `currency: body.currency ?? 'NGN'`.
- **M3** `ensureGas` fires its own `Alert.alert` when `onAlert` is omitted (`wallet.ts:420-424`) and
  returns **`true`** on RPC failure (`:399-403`). Task 7 Step 2 also calls `setError` → two error
  surfaces; and "fail before signing, not after" overstates an advisory check. The spec (§12) is
  honest about this; the plan is not.
- **M4** `readErc20BalanceOf` throws when every RPC fails (`wallet.ts:504-513`) — deliberately, so
  "unknown" is distinguishable from "zero". Task 6 Step 2 doesn't handle it, and treating unknown as
  zero would block the form with no explanation.
- **M5** The wallet pin (Task 7 Step 3) is check-then-use. The LiFi path signs **twice** — approve,
  then swap, potentially minutes apart across a receipt wait (`lifiFlow.ts:77-154`) — and
  `signLiFiTransaction` takes no address and always uses the active wallet (`wallet.ts:643`).
  Re-assert immediately before the swap signature, or accept and document the window.
- **M6** `expect(estimateArrival('100','99884217',18)).not.toBe('99.884217')` asserts only "something
  changed". Pin the exact string (`'0.000000000099884217'`).
- **M7** `belowMinimum` uses `Number()`, so `'1e1'` → 10 clears the floor, while `parseTokenAmount`
  (`p2p.ts:874`) throws on scientific notation — the guard passes an input the very next call
  rejects. Use the same `/^\d+(\.\d+)?$/`.
- **M8** Task 7's Polygon branch sends `reviewAmount` (typed) to `prepareSendTransaction`, while the
  intent was created for `reviewArrival`. Equal on Polygon by construction, but
  `created.data.deposit.amount` is the value the backend will reconcile against — prefer it.
- **M9** `all-swap.tsx` is `screens/all-swap.tsx`, not under `app/wallet/`. Line refs (`:50`,
  `:215-253`) are correct.
- **M10** `CountdownBar` already exists in `components/ui/` for Task 6 Step 5's expiry countdown.
- **M11** Task 4's `BankDestination` is fully controlled with a parent `onChange`; if `onChange` is
  not memoised the internal debounce/bank-load effects re-run on every parent render. Say so — the
  task claims "no behaviour change" and this is where that claim breaks.
- **M12** `_layout.tsx:64-73` gates the **whole** `/wallet/it` group behind `ITAgreementGate`, so the
  create endpoint's `it_agreement_required` 403 is unreachable from this screen. Correct as-is; the
  plan should state it rather than leave the reader to wonder.
- **M13** `GetIntent` already runs `failureReason` through `UserFacingITFailure`
  (`it_handler.go`), so Task 8 Step 5's "plain language, not raw backend text" is already satisfied
  server-side.

---

## Spec coverage

Walked every section. Covered: §3 (T4/5/6), §4 (T1 + T6.3 — but see C3/I3), §5 (T6/7), §6 (T7.2-4),
§7 (T8), §9 (T3 — but see C1/C2), §10 (Constraints + T1), §11 (T1-3), §12 (T1).

Gaps:

1. **§5 resumability** — not implementable (C4).
2. **§8 error table** — five of six rows depend on a `code` the backend never sends (C2).
3. **§8 "bridge still pending past a threshold"** — no task implements a stall threshold or its
   message. Task 8 renders `converting`/`deposit_reported` identically no matter how long they sit.
4. **Over-delivery parking** (`it_stable_sell.go:47-59`) — a real terminal-ish state with no spec
   section and no task (I2).
5. **Platform concerns** — the plan never mentions iOS vs Android. Relevant at least once: the app
   is backgrounded during a bridge, and Task 7 Step 5's `AsyncStorage.setItem` between broadcast and
   report is the crash window. Worth stating that the window exists and is not fully closed — the
   global constraint says "never lose a hash", which is stronger than what the plan delivers. The
   backend's own `/wallet/submit-tx` record is the recovery route if that gap needs closing.

## Task ordering and boundaries

Dependency order is correct as written, and Task 4 (extract before reuse) is the right call.

Two changes:

- The backend prerequisites — **C1** (idempotency wiring), **C2** (machine codes), **C4** (deposit
  address retrieval) — are blockers, and they belong as Task 0 before Task 3, not as caveats inside
  Tasks 3/7/8.
- **Task 6 is too large**: chain discovery, per-chain balances, LiFi token resolution, debounced
  quote composition, guard set, review snapshot, and an expiry countdown. Split at
  "form + balances" / "quote + review".

Task 2's separation from Task 1 is justified and should stay.

---

## Bottom line

REVISE. Fix C1-C5 (three need small backend changes that must land first), correct the two tests that
would pass against broken code (C1's idempotency assertion, I3's `'0'` fallback, I9's failure hop),
and add the create-time re-quote comparison in I1 before any of this touches a real wallet.
