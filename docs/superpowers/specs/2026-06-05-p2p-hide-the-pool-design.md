# P2P — Hide the Pool (fold seller funding into ad creation)

**Status:** Draft for review · **Date:** 2026-06-05 · **Branch:** `feat/p2p-trading`

**Decision origin:** User asked whether the seller "pool" can be removed. A grounded
contract/backend/app investigation (workflow `p2p-pool-removal-analysis`) found the pool is
**load-bearing**: it is the only mechanism by which a buyer can open a sell-ad trade **instantly
while the seller is offline** — the buyer broadcasts `openTrade`, and `P2PEscrowV2` atomically
moves the seller's pre-deposited `available → locked` using the seller's **pre-signed EIP-712
voucher** (`P2PEscrowV2.sol:306-311`). JIT funding cannot reproduce this (the JIT pull always
draws from whoever signs+broadcasts; a voucher is *permission to fill*, not an approval). So
full removal would force sellers online at every open, add per-trade gas, and drop on-chain
oversell safety onto the race-prone off-chain counters.

The user chose **Option B: hide the pool** — keep the on-chain mechanism unchanged, remove the
standalone *Pool* concept from the UX, and fold the seller's deposit into ad creation so funding
+ voucher-signing happen in one gesture.

---

## Goal

A seller publishes a sell ad in a **single flow** that funds the escrow as needed and signs the
voucher — never visiting a separate "Pool" screen. Withdrawing/reclaiming unsold escrow funds
moves into ad management. **Zero smart-contract changes; zero backend money-model changes.**

## Non-goals (explicitly out of scope)

- **No contract changes.** `available`/`locked`, `deposit()`/`withdraw()`, the voucher model,
  and the buy-ad JIT path (`openTradeWithDeposit`) are untouched.
- **No backend money-model changes.** `PrepareApprove`/`GetAllowance`/`PrepareDeposit`/
  `ConfirmDeposit`/`PrepareWithdraw`/`ConfirmWithdraw`/`GetPoolBalance` endpoints stay exactly as
  they are (just called from a new place in the app). The voucher create→sign→activate flow is
  unchanged.
- **No per-ad full-backing / overcommit prevention.** Today the on-chain `available` balance is
  *shared* across a seller's ads, and `create-ad` gates a single ad's amount against `available`
  (not the sum of all active ads). We preserve that exact semantic — funding tops up to cover
  **this** ad, not the seller's total outstanding commitment. Tightening to "every active ad is
  fully and independently backed" is a separate future enhancement.
- **No MCGP-specific funding mode.** MCGP behaves like the stablecoins under this design.

## Architecture (what changes, all in `tsa-app`)

### 1. `create-ad.tsx` — funding folded into publish

Today (sell ad): `adsApi.create` (pending + typedData) → `signTypedData` (voucher) →
`adsApi.sign` (activate). The seller's funding is a wholly separate trip to `/p2p/pool`.

New flow inserts an **ensure-funding** step that reuses the *existing* pool API + the existing
approve/deposit/broadcast/confirm logic (ported from `pool.tsx`), on the ad's selected chain:

```
validate inputs (existing)
  → read available = poolApi.balance(token, sellerAddr, chainId)   [already fetched for the hint]
  → shortfall = max(0, adAmount − available)
  → if shortfall > 0:
        ensureGas(chainKey, sellerAddr)                            [existing helper]
        approve escrow for `shortfall` if allowance insufficient   [poolApi.prepareApprove → sign → broadcast → poll mined]
        deposit: poolApi.prepareDeposit(token, shortfall, sellerAddr, chainId) → sign → broadcast
        poolApi.confirmDeposit(token, shortfall, txHash, sellerAddr, chainId)  [+ recovery stash, see §3]
  → adsApi.create(...)            [voucher: pending + typedData]   (existing)
  → signTypedData(...)            [off-chain voucher signature]    (existing)
  → adsApi.sign(ad.id, sig, ...)  [activate]                       (existing)
```

- **Ordering:** fund first, then create+sign+activate, so an `active` ad is always backed. If
  create/sign fails *after* a successful deposit, the deposited funds are safe in the seller's
  `available` balance (reusable for the retry or withdrawable) — surface this in the error copy.
- **`shortfall ≤ 0` → no deposit, no gas.** Repeat ads (or ads after a refund credited
  `available`) publish with only the gasless voucher signature. Amortization is preserved as a
  natural consequence of the shared on-chain balance.
- **Funding summary UI** replaces the current ambiguous `Pool: X USDT` hint
  (`create-ad.tsx:484-489`). Before submit, show the seller exactly what will happen, e.g.:
  - shortfall > 0: *"This ad locks **100 USDT** in escrow. You have **40 USDT** available — you'll
    deposit **60 USDT** now (one approval + one deposit on BEP20)."*
  - shortfall ≤ 0: *"This ad locks **100 USDT** in escrow, already covered by your **120 USDT**
    available — no deposit needed."*
- The existing `exceedsPool` gate (`create-ad.tsx:325`) is **removed** — the ad is no longer
  blocked for exceeding `available`; instead it deposits the shortfall.
- Stage labels feed the existing `submitStage` UI (`Approving…`, `Depositing…`,
  `Confirming deposit…`, then the existing `Building voucher… / Signing voucher… / Activating ad…`).

### 2. `P2PTabBar.tsx` — remove the Pool tab

Delete the `{ label: 'Pool', route: '/p2p/pool', … }` entry (`P2PTabBar.tsx:31`). Footer tabs
become Market · Orders · Ads · Banks.

### 3. `pool.tsx` → repurpose as "Escrow funds" (reclaim/withdraw), reached from My Ads

The route survives but its role narrows to **viewing balances + withdrawing** (reclaiming unsold
funds). Deposit becomes a secondary/auxiliary action (funding is now the create-ad job), or is
removed from this screen — TBD in review, but the default is: **keep withdraw, drop deposit as the
primary CTA** so the screen reads as "your escrow balance & reclaim", not "fund your pool".
- Entry point: a row/button in `my-ads.tsx` ("Escrow funds" / "Reclaim funds") → navigates to the
  balance screen with the chain selector (reusing the just-shipped per-chain pool work).
- The broadcast→confirm **recovery** (the `PENDING_KEY` AsyncStorage stash) now serves **two**
  call sites: the deposit-in-create-ad path (§1) and the withdraw-in-this-screen path. Use
  distinct storage keys (e.g. `p2p.pendingDeposit` vs `p2p.pendingWithdraw`) and tag each record
  with `{ mode, token, chainId, amount, txHash, walletAddress }` so a stale record is only ever
  retried against the chain it was broadcast on (the guard added in `4229634`).

### 4. `services/p2p.ts` — no API surface removed

All `poolApi` methods stay (deposit is now called from create-ad; withdraw from the reclaim
screen). No backend endpoints are deleted.

## Review fixes (folded in — independent review 2026-06-05, MUST implement)

The design passed an adversarial review (verdict: *ready-with-fixes*; the "zero contract/backend
change" claim was confirmed). All fixes below are app-local. The critical ones are ★.

1. **★ Per-chain decimals in ALL funding math.** The current create-ad code uses
   `token.decimals` from `FALLBACK_TOKENS` (`create-ad.tsx:58-62`) which **hardcodes USDT/USDC = 6**
   and is chain-blind. Feeding that into the deposit amount would **under-fund BSC (18-dec) by
   10¹²**. The folded flow MUST compute decimals via `p2pTokenDecimals(selectedChainId,
   token.symbol)` for the shortfall parse, the deposit amount, the `confirmDeposit` amount, and
   display — never `token.decimals`. This is the single most dangerous mis-funding vector.
2. **★ Resolve `selectedChainId` to a concrete chainId before any deposit broadcast.** In create-ad
   `selectedChainId` is legitimately `undefined` (single-chain token, or before `/p2p/chains`
   lands) — and the create call intentionally omits it to let the backend default to Sonic. But a
   *deposit* broadcasts a real tx: `poolApi.*(…, undefined)`, `p2pChainKey(undefined)`, and
   `ensureGas` all silently fall back to **Sonic** (`p2p.ts:515,915-917`). When funding is required,
   block submit until `chainsFetched && selectedChainId != null`, and fund on the SAME concrete
   chainId sent to `adsApi.create`, so funding chain and voucher domain can never disagree.
3. **★ Re-point the deposit push-notification route.** `hooks/notificationRouting.ts:24-25` routes
   `p2p.deposit.confirmed` → `/p2p/pool` (with a test at `notificationRouting.test.ts:27-28`). Since
   `/p2p/pool` becomes the *reclaim* screen, re-point `p2p.deposit.confirmed` → `/p2p/my-ads` (the
   new funding context) and keep `p2p.withdraw.confirmed` → `/p2p/pool`; update the test. (Still
   app-side — no Go/Solidity change, so the headline claim holds; the spec's file inventory just
   missed this.)
4. **★ Recovery banner keys off the deposit RECORD, not the live form; non-overwriting stash.**
   There is **no server-side deposit reconciler** (`pool.tsx:415-417`) — the AsyncStorage stash is
   the *only* safety net for a dropped deposit-confirm, so it must not lose records. (a) The
   create-ad recovery banner must surface whenever *any* `pendingDeposit` exists for the active
   wallet and confirm against the record's own `{token, chainId, amount, txHash}` — NOT gated on
   the current form selection (else a stranded deposit is invisible until the seller re-selects the
   exact token+chain). (b) Do not overwrite an existing `pendingDeposit` singleton — key the stash
   by `txHash` (a list) or confirm/clear the prior one first, so funding ad #2 can't orphan ad #1's
   stranded confirm forever.
5. **Consolidate wallet resolution.** Resolve `sellerAddress = getActiveWallet()` (with the
   existing `verifyWalletKey` pre-flight, `create-ad.tsx:351-376`) ONCE at the top of `handleSubmit`
   and thread that single value through funding (`ensureGas`/approve/deposit/confirm) AND voucher
   signing. A wallet switch mid-flow must not fund from wallet A then sign a voucher for wallet B
   (which would fail the backend `ReadBalance(sellerAddress)` gate or strand the deposit).
6. **Port the stale-read guard into the create-ad balance read.** `create-ad.tsx:254-275` has no
   `balReqRef` monotonic guard (unlike `pool.tsx:168`). Add it before the `available` value seeds
   the shortfall/deposit math, so a chain switch mid-fetch can't compute the shortfall against the
   wrong chain's balance.
7. **Funding-summary copy reflects the ACTUAL planned steps.** Approve is skipped when allowance
   already covers (`pool.tsx:319-329`). Check allowance first, then say "one deposit" vs "one
   approval + one deposit" — don't hard-promise an approval.
8. **Delete ALL stale "Pool screen" copy in create-ad**, not just the `Pool: X` hint
   (`create-ad.tsx:484-489`): also the "Funds are drawn from your pool balance (deposit first via
   the Pool screen)" note (`:504-508`), the `exceedsPool` error string (`:587`), and the per-chain
   "fund your … pool before publishing" caveat (`:555-560`).

## Money-path safety / invariants

- **Funds never lost on partial failure.** A successful deposit credits the seller's on-chain
  `available` regardless of whether the subsequent ad create/sign succeeds. The reclaim screen +
  recovery stash make those funds visible and withdrawable.
- **Idempotent confirms.** `confirmDeposit` is idempotent server-side (same `txHash` → same
  result), so a retried confirm after a flaky network is safe (unchanged from today).
- **Chain correctness.** The deposit, approve, broadcast, and confirm all use the ad's
  `selectedChainId` (and `p2pChainKey(selectedChainId)` for the RPC), consistent with the
  multi-chain work just merged. No mainnet/testnet or wrong-chain drift.
- **Gas guard** (`ensureGas`) runs before any broadcast, same as the buy-ad and current pool flows.

## Edge cases

| Case | Behavior |
|---|---|
| `available` already covers the ad | Skip approve+deposit; publish with voucher only (no gas). |
| Allowance already sufficient | Skip approve; go straight to deposit. |
| Deposit broadcasts, confirm fails | Stash `pendingDeposit`; show recovery banner in create-ad with a "Retry confirm" that does not re-broadcast. Funds are already on-chain. |
| Deposit succeeds, ad create/sign fails | Funds safe in `available`; error copy tells the seller their funds are in escrow and the ad can be retried or funds reclaimed. |
| No native gas for the deposit | `ensureGas` alerts before broadcast; publish aborts cleanly, nothing signed. |
| User cancels at the wallet prompt | Abort cleanly; no ad created, no funds moved. |
| Withdrawing while an ad is active | Contract already prevents withdrawing `locked` funds; `available` is withdrawable. Reclaim screen reflects available vs locked. |

## Testing

- App: extend create-ad coverage — shortfall>0 funds then publishes; shortfall≤0 publishes with no
  deposit; deposit-confirm-fail stashes + recovers; deposit-ok/create-fail leaves funds safe.
  Reclaim screen withdraws on the selected chain. `tsc`, `eslint`, jest green.
- No backend/contract tests change (no code changed there). Re-run the backend P2P suite to prove
  the untouched endpoints still pass.
- Manual: on BSC testnet, create a sell ad with zero prior balance → one approve + one deposit +
  voucher → ad goes live → a taker opens instantly. Then create a *second* ad within `available`
  → publishes with no deposit.

## Open questions for review / user

1. On the reclaim screen: **drop deposit entirely**, or keep it as a secondary "top up" action?
   (Default: drop it as a primary CTA; funding lives in create-ad.)
2. Entry-point wording in My Ads: "Escrow funds" vs "Reclaim funds" vs "My escrow balance".
3. Should we *also* show the seller's current `available`/`locked` somewhere passive (e.g. My Ads
   header) now that the Pool tab is gone, so they're never surprised by locked capital?
