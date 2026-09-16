# Revision 2 — what this pass changed in `2026-08-29-stablecoin-sell-app.md`

Sources: `2026-08-29-app-plan-rereview.md` (N1–N19) and the still-open minors M1–M13 from
`2026-08-29-app-plan-review.md`. Only the plan file was edited.

## Critical

**N1 — `reviewIdempotencyKey` defined by nothing; reprice path had no exit.**
Task 6 Step 5's `enterReview()` now explicitly mints `setReviewIdempotencyKey(makeIdempotencyKey())`
as part of entering `review`, alongside the rest of the snapshot. Task 7 Step 1 states that accepting
a reprice sets `setReviewNetKobo(createdNet)` **and** `setReviewIdempotencyKey(makeIdempotencyKey())`,
with the reason spelled out: the middleware caches and replays 2xx for the same key, so reusing it
replays the original create verbatim and the comparison fails identically forever — an infinite loop
on the confirm button. The cost of a *rejected* reprice — an orphaned `pending_deposit` intent with
no cancel endpoint — is stated in Task 7 Step 1 and repeated in "Known gaps", with
`DELETE /it/stable/sell/:id` named as the follow-up.

**N2 — reprice guard was a no-op setting state nothing rendered.**
`reviewNetKobo` is declared in Task 6 Step 1 and set from `quote.net` in Step 5, with the note that
`StableQuote.net` and `ITIntent.netNgnKobo` are both `q.NetMinor.String()`
(`it_handler.go:720` / `it_stable_sell.go:174`), which is what makes the comparison like-for-like.
The comparison is guarded: `if (!createdNet || !reviewNetKobo)` fails to the form rather than letting
an absent field invert into always-reprice or never-reprice. `repricedTo` is declared, and Task 6
Step 5's review-rendering list now opens with a prominent "the rate moved" banner.

**N3 — Polygon destination token contract.**
Confirmed the parallel backend change in the working tree: `StableDepositInstructions` now carries
`TokenAddress string \`json:"tokenAddress"\`` populated from `s.stableTokens[asset]`, with
`ErrStableNoTokenAddress` failing the create closed when unconfigured. The plan now uses
`created.data.deposit.tokenAddress` for both the Polygon transfer's `tokenAddress` and the execution
LiFi quote's `toToken`. All app-side `polygonTokenAddress` resolution is deleted. A new Global
Constraint explains why (native USDC vs bridged USDC.e; a wrong pick leaves the deposit unverifiable
and the funds unattributed), `StableDeposit.tokenAddress` is added to the type in Task 3 with the
same reasoning, and the Task 3 test fixture carries it. Task 6's quote-time LiFi call passes the
asset **symbol** as `toToken` — pricing only, never a fund destination — and says so; the residual
(the estimate may be priced against a different variant) is in "Known gaps". Task 7 gained an
explicit "Ordering is load-bearing" note: the execution quote is built after create because only
then is the contract known.

**N4 — status-enumeration test no longer guarded.**
Added `expect(hops.some((h) => h.state !== 'pending')).toBe(true)` inside the enumeration loop, with
a comment naming the defeat (the unknown fallback returns all four labelled hops, all `pending`).
Verified it holds for all nine mapped statuses.

## Important

- **N5** — the `created` guard moved to the top of Task 7 Step 1, immediately after the call and
  before any dereference. Task 7 Step 4's duplicate guard removed; `intentId`, `depositAddress`,
  `depositTokenAddress` and `depositAmount` are derived at the guard.
- **N6** — `return; // nothing broadcast — safe to fail back` replaced with a comment naming both
  ways it was false (`walletApi.ts:475-478` catch-all, and `:471-474` returning success-without-hash)
  plus a real recovery route: `setRecoveryHint`, the `/wallet/submit-tx` record, and
  `getTransactionHistory(1, 10, 137)`.
- **N7** — `ErrArrivalUnpriceable` is now `class ErrArrivalUnpriceable extends Error` with
  `readonly code = 'arrival_unpriceable'` and an explicit `Object.setPrototypeOf`. The throw site is
  `throw new ErrArrivalUnpriceable()`. The test asserts `toBeInstanceOf` and `.code`.
- **N8** — `STABLE_MAX_DECIMALS = 6` and `exceedsStablePrecision()` added to Task 1 with two tests;
  Task 6 Step 4's guard list includes it with the message "at most 6 decimal places". M7 is subsumed:
  `belowMinimum` now shape-checks with `/^\d+(\.\d+)?$/` before `Number()`, with its own test.
- **N9** — counts recounted from the `it(...)` blocks: Task 1 **13**, Task 2 **20** cumulative,
  Task 3 **5**. All three "Expected: PASS" lines corrected and itemised.

## Minor

N10 "Task 9" → "Task 8 Step 3". N11 `sourceToken` null: Task 6 Step 2 resolves into state, surfaces
a message, and Step 3 returns early on null. N12 one name per value: `reviewSourceTokenAddress` and
`reviewSourceDecimals` are both snapshot fields, stated as such; `tokenAddress` now only ever means
the *deposit* contract. N13 every symbol declared — `setQuoteError`/`setError` (with why they are two
surfaces), `chainLabel`, `alertFn`, `setSigningStatus`, `showAuth`, `runSigning`, `recoveryHint`,
`EMPTY_DEST`, `resolveToken`, and a complete import block including `AsyncStorage` and
`mapSubmitError`. N14 Task 7 uses `reviewBankCode`/`reviewAccountNumber`/`reviewAccountName`.
N15 the `cancelled` test asserts `hops[0].state === 'failed'`. N16 a comment on `STABLE_SELL_STATUSES`
says `failed`/`cancelled` are very likely unreachable for a stable sell and cites where they are set.
N17 Task 8 Step 3 states it runs once per screen open in its own `useEffect`, not inside `load()`.
N18 the retry's safety is attributed to server-side idempotency, not the middleware.

Also strengthened beyond the findings: the "unknown status" test now asserts the exact all-`pending`
shape instead of merely `!== 'done'`.

## Original review's minors

M1 `StableQuote.amount: string`. M2 `currency: body.currency ?? 'NGN'` on both endpoints.
M3 `ensureGas` described as advisory, `alertFn` passed explicitly, the competing `setError` removed.
M4 `readErc20BalanceOf` wrapped; unknown balance stays `null` and *skips* the guard rather than
failing it. M5 `assertActiveWallet` extracted and called again immediately before `executeLiFiSwap`.
M6 pinned to `'0.000000000099884217'`. M7 see N8. M8 the Polygon transfer sends
`created.data.deposit.amount`. M10 `CountdownBar` from `components/ui/` named and wired to
`onExpire`. M11 Task 4's `onChange` memoisation requirement stated. M12 the `ITAgreementGate` note
added to Task 5 Step 3. M13 Task 8 Step 5 renders `failureReason` as-is.

## Signatures verified against source before writing

`formatTokenAmount` / `parseTokenAmount` (`p2p.ts:824, 874`), `getLiFiToken` → `LiFiToken | null`
and `getLiFiTokens` → `LiFiToken[]` (`lifi.ts:93, 158`), `ensureGas` returning `true` on RPC failure
(`wallet.ts:399-404`) and self-alerting (`:412-416`), `readErc20BalanceOf` throwing
(`wallet.ts:504-513`), `signTransaction(tx, expectedAddress?)`, `getActiveWallet()`,
`sendPreparedTransaction → ApiResponse<{txHash}>` and `submitTransaction` returning `result` as-is
without a hash (`walletApi.ts:471-474, 509`), `prepareSendTransaction`'s options object,
`executeLiFiSwap`'s params, `AuthorizationModal`'s props, `mapSubmitError` (`swapErrors.ts:13`),
the `components/ui` barrel exporting `CountdownBar`/`SummaryCard`/`SummaryRow`,
`getChain`/`ChainKey` (`constants/chains.ts:43, 520`), `it_handler.go:716` emitting `amount` as a
string, and the backend's new `StableDepositInstructions.TokenAddress`.

Corrections made against the plan's own prior text: there is **no** `useWalletAddress` hook in this
repo — replaced with the `getActiveWallet()` effect used at `all-swap.tsx:102-106`.

## Final count

- **8 tasks**, **50 steps** (5, 5, 8, 5, 5, 7, 8, 7), contiguous within each task.
- `utils/__tests__/stableSellQuote.test.ts` — **20** `it(...)` blocks (13 after Task 1, +7 in Task 2).
- `services/__tests__/itStableApi.test.ts` — **5** `it(...)` blocks.
