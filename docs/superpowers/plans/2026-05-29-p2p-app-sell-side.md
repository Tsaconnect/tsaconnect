# P2P App — Sell-Side / Two-Sided Integration Plan (#6)

> Implement with TDD where unit-testable. Verification in this environment: `npx tsc --noEmit` (type-check; filter to touched files — the project has pre-existing unrelated TS errors), `npx jest <files>` (unit tests), `npm run lint`. **No simulator/browser is available**, so screens are type-check-only; pure logic (helpers, client) is unit-tested.

**Goal:** Let a user **sell crypto to a merchant** in the app: browse merchant **buy-ads** (Buy/Sell tabs), open a sell-trade by approving + depositing crypto just-in-time (`approve` → `openTradeWithDeposit`), and run the role-flipped trade lifecycle (merchant marks fiat paid; user releases). Merchants can post buy-ads.

**Branch:** `feat/p2p-two-sided` (shared with the backend work). Dir: `tsa-app/`.

**Tech:** React Native 0.81 + Expo 54 + expo-router v6, TS. Wallet signing in `services/wallet.ts`; P2P client in `services/p2p.ts`.

## Design decisions (locked)
1. **Amount direction (avoids approve-mismatch):** On the SELL side the user enters the **crypto amount** to sell (`tokenAmount`). The app approves exactly that, then opens. The backend treats the app-provided `tokenAmount` as authoritative and derives fiat = tokenAmount × rate. (Backend tweak handled separately; the app always sends `tokenAmount` + `sellerAddress` on the buy-ad open.)
2. **Minimize new screens:** Buy/Sell tabs live in `index.tsx`. The sell-trade open is a **mode** of `ad/[adId].tsx` (keyed on `ad.side`), not a new screen. The trade cockpit `trade/[tradeId].tsx` is already role-aware — just fix hardcoded currency + role copy. New screen: `create-buy-ad.tsx` (merchant posts a buy-ad — different capital model, no pool).
3. **Reuse the existing approve→act pattern** from the pool screen (`poolApi.prepareApprove` + `signTransaction` + `broadcastTransaction`) and the existing open→confirm pattern.

## Task 1: Client types + methods + unit-testable helpers
**File:** `tsa-app/services/p2p.ts` (+ `services/p2p.test.ts`).
- [ ] Extend `OpenTradeInput`: add optional `sellerAddress?: string` and `tokenAmount?: string` (base units). Sell-ad path keeps using `buyerAddress`/`fiatAmount`; buy-ad path sends `sellerAddress` + `tokenAmount` (+ `fiatAmount` for display).
- [ ] Add `CreateAdInput.side?: 'sell' | 'buy'` (default 'sell').
- [ ] Export a pure helper `hasSufficientAllowance(allowance: string, needed: string): boolean` (BigInt compare) and `previewFiatFromToken(tokenAmount: string, rate: string, decimals: number): string` (token×rate). Keep them pure + exported for tests.
- [ ] `tradesApi.open(adId, input)` already returns `{ trade, unsignedTx }` — no signature change (the backend returns `openTradeWithDeposit` calldata for buy-ads). Confirm `poolApi.prepareApprove(token, amount, owner)` and `poolApi.allowance(token, owner)` exist (the map says prepareApprove L498); if no `allowance` getter exists, add `poolApi.allowance` calling the backend `GET /p2p/allowance`.
- [ ] **Tests** (`services/p2p.test.ts`, follow existing pattern): `hasSufficientAllowance` (equal/less/greater, big values beyond Number precision), `previewFiatFromToken` (rounding), and `distinctAdProviders` with a buy-ad fixture. 
- [ ] `npx tsc --noEmit` on touched files clean; `npx jest services/p2p.test.ts` green. Commit.

## Task 2: Browse — Buy/Sell tabs
**File:** `tsa-app/app/p2p/index.tsx`.
- [ ] Add `adSide: 'sell' | 'buy'` state (default 'sell') + a segmented Buy/Sell control. Pass `side: adSide` to `adsApi.list` (replace the hardcoded `side:'sell'`).
- [ ] Side-aware copy: heading "Buy {token}" (sell tab, user buys) vs "Sell {token}" (buy tab, user sells); `AdCard` sub-label "Sells {token}" vs "Buys {token}". Tapping a buy-ad routes to `ad/[adId]` (mode-aware).
- [ ] The merchant-only "Want to sell?" banner: hide on the buy tab (any user can respond to a buy-ad).
- [ ] `npx tsc --noEmit` touched files clean. Commit. (Screen = type-check only.)

## Task 3: Sell-trade open — `ad/[adId].tsx` mode-aware
**File:** `tsa-app/app/p2p/ad/[adId].tsx`.
- [ ] Detect `ad.side`. For `'buy'` (user is the seller):
  - Input is **crypto amount to sell** (`tokenAmount`); show fiat-to-receive preview via `previewFiatFromToken` using the ad's rate.
  - Show the **user's own** payment methods (where the merchant will send fiat), not the ad poster's.
  - Flip copy: "You send {token}" / "You receive {fiat}".
  - Submit handler runs the sell sequence: `getActiveWallet()` → `poolApi.allowance(token, seller)`; if `!hasSufficientAllowance` → `poolApi.prepareApprove(token, tokenAmount, seller)` → `signTransaction` → `broadcastTransaction` (wait for hash); then `tradesApi.open(adId, { sellerAddress, tokenAmount, fiatAmount, paymentMethodId })` → sign+broadcast the returned `openTradeWithDeposit` tx → `tradesApi.confirmOpen(tradeId, txHash, sellerAddress)`. Add `submitStage` states: "Approving…", "Depositing & opening…".
  - Use the shared `unsignedTxToTxLike` from `services/p2p.ts` (remove the local `toTxLike` dup).
- [ ] Keep the sell-ad (buy) mode unchanged.
- [ ] `npx tsc --noEmit` touched clean. Commit.

## Task 4: Trade cockpit — currency + role copy fixes
**File:** `tsa-app/app/p2p/trade/[tradeId].tsx`.
- [ ] Replace the hardcoded `₦` fiat rendering with a `trade.fiatCurrency`-aware formatter (reuse the app's currency formatting; do NOT assume NGN).
- [ ] Make the dispute-modal copy role-aware: buyer sees "…seller releases your {token} or you get a refund"; seller sees "…funds are released to the buyer or returned to you."
- [ ] The role detection (buyer vs seller from `buyerId`/`sellerId` vs `user.id`) already works for sell-trades — verify `canMarkPaid`/`canRelease` gate correctly when the user is the seller. No logic change expected; add a note if the existing logic already handles it.
- [ ] `npx tsc --noEmit` touched clean. Commit.

## Task 5: `create-buy-ad.tsx` + routing
**Files:** new `tsa-app/app/p2p/create-buy-ad.tsx`, `tsa-app/app/p2p/_layout.tsx`, possibly `index.tsx`.
- [ ] New screen for a merchant to post a buy-ad: token, fiat currency, rate (fixed/float), min/max limits, payment methods (the merchant's — where they'll PAY fiat from / receive proof), and the published total they'll buy (`amount`). NO pool deposit, NO voucher signing. Calls `adsApi.create({ ...fields, side: 'buy', sellerAddress: merchantWallet })`.
- [ ] Gate behind the merchant role (reuse `useUserRole`).
- [ ] Register `<Stack.Screen name="create-buy-ad" options={{ title: 'Create Buy Ad' }} />` in `_layout.tsx`. Add an entry point (e.g. a "Post buy ad" action) on the buy tab / my-ads for merchants.
- [ ] `npx tsc --noEmit` touched clean. Commit.

## Task 6: Full type-check + jest + lint + self-review
- [ ] `npx tsc --noEmit` — confirm NO NEW errors vs the pre-existing baseline on touched files (capture the baseline error set first: `git stash` or compare against the diff's files). 
- [ ] `npx jest services/p2p.test.ts hooks/useCountdown.test.ts hooks/useP2pCurrency.test.ts` — all green.
- [ ] `npm run lint` on touched files — clean (or no new violations).
- [ ] Self-review against the app map's "role-awareness list": every spot that assumed the user is the buyer is now side-aware. Commit fixes.

## Out of scope
- Auto-release ⚡ UI, dispute-v2 wizard, Permit2/meta-tx (relayer) signing — later sub-projects. The sell flow uses the plain ERC-20 approve + direct openTradeWithDeposit tx (user pays gas), consistent with today's pool deposit flow.
- EIP-712 voucher domain version bump (cutover) — only affects sell-ad creation, not the sell side.

## Risks
- **tsc baseline:** the project has pre-existing TS errors; only assert NO NEW errors on touched files.
- **approve confirmation timing:** the open tx's backend gas-estimation reverts if the approve isn't mined yet — the submit flow must wait for the approve tx receipt (or poll allowance) before calling `tradesApi.open`. Implement an await-receipt step.
- **Screens are not runtime-verified** (no simulator) — call this out in the final report; only type-check + unit tests + lint gate them.
