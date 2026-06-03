# P2P Multi-Chain — Plan 7: App (RN/Expo)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development / executing-plans. Checkbox steps.

**Goal:** Make the app correctly handle P2P ads/trades on any chain — route every on-chain action to the ad/trade's chain, show amounts with the right per-(chain,token) decimals, let a user create on the right chain, and filter the marketplace by chain.

**Tech Stack:** React Native 0.81 / Expo 54, `tsa-app/`. tsc + eslint gates.

**Depends on:** Plans 1–5 (backend) + Plan 6 (BSC deploy, for end-to-end exercise). The app changes are **inert until BSC is deployed** — everything defaults to Sonic today.

**Execution split:**
- **Core (do now — mechanical, verifiable without a live BSC):** Task 1 (per-chain decimals) + Task 2 (broadcast routing). These make the app *safe* for a BSC ad/trade the moment one exists.
- **Deploy-coupled (the remainder — pairs with the BSC testnet deploy so it's testable):** Task 3 (`GET /p2p/chains` backend endpoint), Task 4 (create-ad chain/asset selection), Task 5 (marketplace chain filter / asset-first), Task 6 (trade-time receive-chain confirm), Task 7 (BNB gas guard), Task 8 (pool per-chain). Specified below; build alongside the deploy.

---

## Task 1 (CORE): Per-(chain,token) decimals helper

**Problem:** `TOKEN_DECIMALS = { USDT:6, USDC:6, MCGP:18 }` is hardcoded in `app/p2p/index.tsx:37`, `app/p2p/my-trades.tsx:27`, and `app/p2p/trade/[tradeId].tsx:72` — BSC USDT/USDC are **18**, so amounts would be off by 10¹².

**Files:** new helper in `services/p2p.ts`; consumers `index.tsx`, `my-trades.tsx`, `trade/[tradeId].tsx`.

- [ ] **Step 1** — add to `services/p2p.ts` (mirrors backend `tokenDecimalsFor`):
```ts
/** On-chain decimals for a token on a chain. BSC (56/97) USDT/USDC are 18;
 *  Sonic (146/14601) USDT/USDC are 6; MCGP is 18. */
export function p2pTokenDecimals(chainId: number | undefined, symbol: string): number {
  const sym = (symbol || '').toUpperCase();
  if (chainId === 56 || chainId === 97) {
    if (sym === 'USDT' || sym === 'USDC') return 18;
  }
  if (sym === 'USDT' || sym === 'USDC') return 6;
  return 18; // MCGP + default
}
```
- [ ] **Step 2** — replace each consumer's `TOKEN_DECIMALS[token] ?? 18` with `p2pTokenDecimals(entity.chainId, token)`:
  - `index.tsx:265` (AdCard): `p2pTokenDecimals(ad.chainId, ad.token)` — `ad.chainId` exists on `P2PAd`.
  - `my-trades.tsx:177`: `p2pTokenDecimals(trade.chainId, trade.token)`.
  - `trade/[tradeId].tsx` (the `TOKEN_DECIMALS[trade.token]` use): `p2pTokenDecimals(trade.chainId, trade.token)`.
  - Delete the now-unused module-level `TOKEN_DECIMALS` consts.
- [ ] **Step 3** — `npx tsc --noEmit` shows no new errors in these files; `npx eslint <files>` clean.
- [ ] **Step 4** — commit: `git commit -m "fix(p2p-app): per-(chain,token) decimals (BSC stables = 18)"`

## Task 2 (CORE): Route broadcasts to the entity's chain

**Problem:** 10 sites hardcode `broadcastTransaction('sonic' as ChainKey, …)` — a BSC ad/trade would broadcast to Sonic and revert.

**Files:** `app/p2p/ad/[adId].tsx` (213, 301, 356), `app/p2p/trade/[tradeId].tsx` (378, 409), `app/p2p/my-ads.tsx` (125), `app/p2p/dispute/[tradeId].tsx` (227), `app/admin/p2p-disputes/[tradeId].tsx` (130), `app/p2p/pool.tsx` (272, 310).

- [ ] **Step 1** — add a tiny resolver to `services/p2p.ts` (or `constants/chains.ts`):
```ts
import { getChainByChainId, type ChainKey } from '../constants/chains';
/** ChainKey for a P2P entity's chainId; falls back to 'sonic'. */
export function p2pChainKey(chainId: number | undefined): ChainKey {
  return (chainId && getChainByChainId(chainId)?.key) || 'sonic';
}
```
- [ ] **Step 2** — at each broadcast site, replace `'sonic' as ChainKey` with `p2pChainKey(<entity>.chainId)`:
  - `ad/[adId].tsx`: the ad is loaded → `p2pChainKey(ad.chainId)` for all 3 sites (approve + open).
  - `trade/[tradeId].tsx`: `p2pChainKey(trade.chainId)` (release + cancel).
  - `my-ads.tsx`: the ad being cancelled → `p2pChainKey(ad.chainId)`.
  - `dispute/[tradeId].tsx`: the trade → `p2pChainKey(trade.chainId)`.
  - `admin/p2p-disputes/[tradeId].tsx`: the dispute's trade → `p2pChainKey(trade.chainId)`.
  - `pool.tsx`: pool ops have no ad/trade entity yet → keep Sonic for now via `p2pChainKey(undefined)` (≡ 'sonic'); Task 8 makes the pool chain-selectable. Leave a `// TODO(Plan 7 Task 8): pool chain` comment.
- [ ] **Step 3** — `tsc --noEmit` clean for these files; `eslint` clean. Add a lint/grep guard: no remaining `broadcastTransaction('sonic'` in `app/p2p/` except the documented pool default.
- [ ] **Step 4** — commit: `git commit -m "fix(p2p-app): broadcast on-chain actions to the ad/trade's chain"`

---

## Task 3–8 (DEPLOY-COUPLED remainder — specified; build alongside the BSC deploy)

**Task 3 — `GET /api/p2p/chains` backend endpoint.** The app must learn the *live* enabled (chain,asset) matrix (config-driven; changes at BSC deploy + the §6 curation flip). Add a handler returning, per enabled chain: `{chainId, chainKey, assets[]}` from `cfg.P2PChains` (or the escrow registry's `ChainIDs()` + `AssetAllowed`). Route `p2pGroup.GET("/chains", …)` (auth, no escrow needed). App: `chainsApi.list()` in `services/p2p.ts`.

**Task 4 — create-ad chain/asset selection.** In `create-ad.tsx` / `create-buy-ad.tsx`, fetch `/p2p/chains`; present **asset-first** (user picks USDT/USDC/MCGP); derive the chain from the matrix (the asset's enabled chain) and pass `chainId` to `adsApi.create` (`CreateAdInput.chainId` already exists). When an asset is enabled on >1 chain later, show a chain chooser; today it's 1:1.

**Task 5 — marketplace chain filter** (`index.tsx`): default to the user's preferred/last-used chain; pass `chainId` to `adsApi.list`; show a chain badge per ad. Don't intermix chains in one list.

**Task 6 — trade-time receive-chain confirm** (`ad/[adId].tsx` open flow): before signing, show "You'll receive {token} as {standard} on {network} — make sure your destination supports it." (`standard` = BEP-20 for BSC, native for Sonic.)

**Task 7 — BNB gas guard:** before a broadcast on a chain whose native token the user may lack, check native balance (wallet service) and prompt "fund your wallet with {BNB/S}" rather than letting the tx fail mid-flow with "insufficient gas".

**Task 8 — pool per-chain** (`pool.tsx`): a chain selector; show per-chain balances (so existing Sonic pool funds stay visible); route deposit/withdraw/approve broadcasts to the selected chain.

---

## Self-review (core)
- **Spec coverage:** §7.1 (10 broadcast sites incl. admin), §7.5 (per-(chain,token) decimals) — the correctness core. §7.2–7.8 are the deploy-coupled remainder (Tasks 3–8), specified.
- **Inert-until-deploy:** every entity defaults to Sonic chainId today, so `p2pChainKey`/`p2pTokenDecimals` return Sonic values until a BSC ad exists — zero behavior change pre-deploy.
- **No placeholder:** Tasks 1–2 have complete code; Tasks 3–8 are design specs for the deploy-coupled phase (each its own small change).
