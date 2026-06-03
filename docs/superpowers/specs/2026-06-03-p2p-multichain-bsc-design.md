# P2P Multi-Chain Model + BSC Support (Design)

**Date:** 2026-06-03
**Status:** Design — brainstormed, **independently reviewed 2026-06-03 (3-lens panel,
verdict: ready-with-fixes; all confirmed fixes folded in below)**. NOT implemented. The
backend refactor + BSC deploy are gated on owner sign-off of the §10 decisions; BSC
**mainnet** is additionally gated on the P2PEscrowV2 re-audit (Sonic mainnet itself is
still v1 awaiting that audit — see `2026-05-29-p2p-escrow-v2-two-sided-design.md`).
**Phase:** P2P Phase 1 follow-on — extends the Sonic-only P2P stack to a second chain.
**Backed by:** `2026-05-29-p2p-escrow-v2-two-sided-design.md`; a 5-agent cross-project
BSC scoping map + a 3-lens spec review (2026-06-03).

---

## 1. Purpose & scope

Extend P2P trading from Sonic-only to a second chain (**BSC**) and — the load-bearing
part — **decide how "chain" fits the non-custodial P2P model.** The plumbing is
mechanical; the model decision determines whether the feature is honest, safe, and
bounded in scope.

**In scope:** the chain model (how buyers/sellers relate to chain), the backend
per-chain refactor, the BSC contract deploy, the app UX.
**Out of scope (non-goals, §9):** in-escrow cross-chain bridging, any custodial
"hide the chain" abstraction, and MCGP on BSC.

---

## 2. The core problem: non-custodial makes the chain intrinsic

In a **non-custodial** escrow, the chain the seller deposits on **is** the asset the
buyer receives. "USDT on Sonic" and "USDT on BSC" are genuinely different goods —
different contract, different gas token, different ecosystem, **different decimals**.
You cannot abstract that away without either (a) becoming custodial, or (b)
bridging/swapping at settlement. TSA is non-custodial, so **chain is intrinsic.**

The concern that motivated this design — *"buyers may not want the chain the seller is
selling"* — is therefore not a UX bug to hide; it is a real fork in the model.

---

## 3. How other platforms treat "chain" (why our decision follows)

| Platform | Custody | How chain works in P2P |
|---|---|---|
| Binance / Bybit / OKX P2P | **Custodial** (internal ledger) | Chain is **invisible** in the trade. You trade abstract "USDT". The blockchain enters only at **deposit/withdrawal** (network chosen *then*). |
| Paxful / LocalBitcoins | Custodial-ish | Single chain (BTC). N/A. |
| LocalCryptos | Non-custodial on-chain escrow | **Single chain** (ETH/ERC-20). |
| Hodl Hodl / Bisq | Non-custodial | **Single chain** settlement (BTC). |
| **TSA** | **Non-custodial**, per-chain escrow contract | **Chain is intrinsic** — buyer receives the exact on-chain asset the seller escrowed. |

**Takeaway:** the only platforms that make chain invisible are *custodial*; every
non-custodial P2P has historically been *single-chain*, precisely to avoid the
buyer-chain-mismatch. Multi-chain **non-custodial** P2P is somewhat novel — and the way
to do it honestly is to make chain an **explicit choice**, not a hidden one.

---

## 4. Locked decisions (brainstorming 2026-06-03 + spec review)

1. **Chain is a first-class, explicit per-ad attribute.** Ads are effectively
   "USDT@Sonic" vs "USDT@BSC"; matching happens within a chain; buyers filter/choose.
2. **No in-escrow bridging.** Settlement never crosses chains — keeps the audited
   money-path free of an async, partially-trusted bridge hop.
3. **The wallet's existing LiFi swap is the post-trade cross-chain escape valve.** A
   buyer who needs a different chain receives on the seller's chain, then bridges it as a
   normal, separate wallet action. Cross-chain flexibility, zero new escrow risk.
4. **Explicit chain choice is a safety surface.** In this market "chain" usually means
   *destination compatibility* (will Binance / my recipient accept it — e.g. BEP-20).
   The trade flow shows a **trade-time confirmation** of the receive-chain/standard.
5. **Primary buyer-facing chain at launch = BSC** (RECOMMENDED; config-reversible, §10.1).
   BEP-20 demand in the NG market; Sonic retained for MCGP/native. Default/seeding only,
   **not** architectural.
6. **Dormant-by-default.** A chain is P2P-enabled only when its escrow address is
   configured. Unset BSC = BSC P2P unavailable; nothing breaks.
7. **Backend shape is LOCKED to per-chain service instances** (review-confirmed, §5.1):
   `map[int64]*P2PTradeService` + `map[int64]*P2PEscrowService`, multiplexed in the
   handler on ad/trade `ChainID` — **not** threading `chainID` through ~30 call sites
   (any missed site silently falls back to Sonic). This is the **gating prerequisite**,
   sequenced before config/app work.

> Still needing owner sign-off before implementation: §10 (primary chain, mainnet-vs-
> testnet, gas-token strategy, key custody, marketplace default).

---

## 5. Architecture — backend (`tsa-api-go`)

The data model is **already chain-aware** and needs no migration: `P2PAd.ChainID`
(`models/p2p_ad.go:83`, gorm default 146) and `P2PTrade.ChainID` (`p2p_trade.go:56`)
exist, and the trade inherits the ad's chain at open (`p2p_trade_service.go:902`).

### 5.1 The crux (review-confirmed)
Today there is **one** `P2PTradeService` (`main.go:184`) with scalar `chainID` + scalar
`eip712Version` + **one** `P2PEscrowService`, wired into a single `P2PHandler.trade`
(`p2p_handler.go:29`). `buildVoucherTypedData` (`p2p_trade_service.go:3238-3242`) stamps
`s.chainID`/`s.eip712Version` unconditionally; `CreateAd` sets `ad.ChainID = s.chainID`
(`:381`); `P2PEscrowService` binds one client + `contractAddress` at construction
(`p2p_escrow_service.go:220-236`).

> ⚠️ **EIP-712 version is already non-uniform.** Sonic **mainnet** `"1"` (`config.go:46`),
> Sonic **testnet** `"2"`, BSC will be `"2"`. A single service instance **cannot** serve
> two chains — every BSC voucher would be stamped with Sonic's chainId/version and fail
> `ecrecover` with "recovered != expected". Chain → `{escrowAddr, eip712Version, client,
> chainID}` must move together as a unit, like `sonicPresets`.

### 5.2 Changes
1. **`bscPresets` bundle** parallel to `sonicPresets` (`config.go:36-59`): per network
   `{chainID, USDT/USDC addrs, p2pEscrow, eip712Version}`. BSC USDT `0x55d398…` / USDC
   `0x8AC76a…` already exist in `config.Networks` (`config.go:302-303`).
2. **Promote the P2P config scalars to a per-chain map** (`config.go:103-107`):
   `P2PChains map[int64]{RPCNetworkKey, EscrowAddress, EIP712Version}`, selected via
   `SONIC_NETWORK` / a new `BSC_NETWORK`; keep `P2P_ESCROW_ADDRESS[_BSC]` env overrides.
3. **Per-chain `P2PEscrowService` registry** — `map[int64]*P2PEscrowService`, each built
   from `blockchainService.ClientForChainID(id)` (registry already returns a BSC client;
   `main.go:69` already uses `ClientForNetwork`). Method bodies unchanged.
4. **Per-chain `P2PTradeService` (the gating refactor, decision §4.7).** Resolve escrow +
   chainID + version by ad/trade `ChainID`; multiplex in `P2PHandler` — including the
   **admin dispute path** (`AdminResolveDispute`/`ConfirmAdminResolveDispute`,
   `p2p_handler.go:1389,1417`), which today routes through the same single service and
   would land a BSC `adminResolve` tx on Sonic. **Test:** sign + `ecrecover` a voucher on
   *each* chain's domain (catches the version/chainId-mismatch class).
5. **Build a per-chain `eth_chainId` boot-probe LOOP** (net-new — today's probe at
   `main.go:172-184` is Sonic-only, no loop). Probe each active chain's RPC, feed the
   **RPC-reported** chainID into *its* domain (preserve the CLAUDE.md invariant), warn on
   config/RPC mismatch. Extend the bytecode-presence check (`main.go:91-101`) per chain
   with the chain name in the warning. Log one startup line listing P2P-enabled chains +
   their escrow addresses so operators can verify at boot.
6. **Reconciler + timeout watchdog must resolve escrow by `trade.ChainID` and FAIL LOUD.**
   `ChainReconciler.Run` (`chain_reconciler.go:48-62`) loops trades chain-agnostically and
   `ReconcileTrade` (`p2p_trade_service.go:2009`) reads `s.escrow` (Sonic). A BSC trade
   read against the Sonic contract returns `Exists=false`, and the **orphan-reclaim
   branch (~:2018-2023) would then cancel/restore a LIVE BSC trade once its window
   elapses — actively stranding funds**, not a benign no-op. So `escrowFor(trade.ChainID)`
   must **return an error** (never default to Sonic) for an unconfigured chain. The
   timeout watchdog (registered `main.go:209` with the same service) has the identical
   defect. **Test:** a trade on an unconfigured chainID must error out of reconciliation,
   not get orphan-reclaimed.
7. **Chain selection + filtering end-to-end** (review-confirmed all missing today):
   - `createAdRequest` (`p2p_handler.go:541-560`) gains optional `chainId` (validated
     against enabled chains, default 146 for back-compat); `CreateAd` stops hard-setting
     `ad.ChainID = s.chainID`.
   - `AdFilter` (`p2p_trade_service.go:2503`) + the `ListAds` handler (`p2p_handler.go:836`)
     gain a `chainId` filter so Sonic/BSC ads don't intermix.
   - **Pool + deposit/withdraw/balance endpoints must be chain-scoped** — pool balance is
     per-(seller, chain).
8. **Restructure token decimals to per-`(chain, token)`** (see §7.5; this is the
   fund-amount-correctness fix). Backend `tokenDecimalsFor` (`p2p_trade_service.go:3380`)
   is a chain-agnostic switch returning 6 for USDT/USDC — it must resolve from the
   ad/trade chainId (**BSC USDT = 18 decimals**). The parse/format *functions* already
   take a decimals arg; only the lookup is wrong.
9. **Money invariant is per `(chain, token)`** — `sum(active trades.token_amount) ==
   escrow.balanceOf(token) − ad-pool remaining`, against *that chain's* escrow. The drift
   metric must be per-chain; a global cross-chain sum is meaningless once BSC exists.

---

## 6. Architecture — contracts (`smart-contracts`)

`P2PEscrowV2.sol` is chain-agnostic; BSC Hardhat networks (56/97) are already configured.

1. **Deploy `P2PEscrowV2` to BSC testnet (97), then mainnet (56)** via
   `scripts/deploy-p2p-escrow-v2.js --network bscTestnet|bscMainnet`. Set
   OWNER/ADMIN/SYSTEM_WALLET; **curate `ACCEPTED_TOKENS` to exactly the BSC USDT
   `0x55d398…` / USDC `0x8AC76a…` addresses** (the script reads an arbitrary
   `ACCEPTED_TOKENS` env with no chain validation — wrong-chain token addresses are an
   operator footgun).
2. **MCGP = `address(0)` on BSC** — do **not** set `MCGP_TOKEN_ADDRESS`. Review-confirmed
   safe: `_feeBpsForToken` (`P2PEscrowV2.sol:270`) charges the standard fee when
   `mcgpToken` is zero (no revert, discount simply doesn't apply). Optionally add a deploy
   guard that rejects an MCGP address on a BSC network.
3. ⚠️ **Delete or loudly rename `deploy-bsc-testnet-p2p-bundle.js`** — it deploys the
   deprecated **v1** `P2PEscrow` (`getContractFactory('P2PEscrow')`, line ~68), not V2. As-is
   it's a footgun that would deploy the wrong, sell-only contract to BSC.
4. **Record the V2 proxy address + its `eip712Version` (`"2"`)** into `bscPresets` and a
   deployments note (no formal registry exists today).

---

## 7. UX — app (`tsa-app`)

Primitives are already chain-agnostic: `broadcastTransaction(chainKey, …)`,
`signTypedData` (forwards the backend's domain), and `getChainByChainId` (`chains.ts:362`)
need no change; `chains.ts` defines BSC mainnet (56) + testnet (97). The work is the call
sites + chain-choice UI.

1. **Replace ALL hardcoded `broadcastTransaction('sonic')` P2P sites (10, not 9)** with a
   per-ad/trade chain lookup via `getChainByChainId`: `my-ads.tsx:125`;
   `ad/[adId].tsx:213,301,356`; `pool.tsx:272,310`; `dispute/[tradeId].tsx:227`;
   `trade/[tradeId].tsx:378,409`; **and `admin/p2p-disputes/[tradeId].tsx:130` (admin
   resolve — was missed)**. (`servicedetail.tsx:231,257` are the *product-escrow* flow, not
   P2P — correctly excluded.) **Use a grep + lint guard** for `'sonic'` in P2P files
   rather than a hand-counted list, so a future site can't regress.
2. **Chain selector in `create-ad.tsx` / `create-buy-ad.tsx`** → pass `chainId` to
   `CreateAd` (add `chainId` to `CreateAdInput`). Signing is backend-domain-driven, so no
   client-side EIP-712 change once `chainId` is plumbed.
3. **Marketplace chain filter + default** (`index.tsx`) → add `chainId` to
   `ListAdsFilter`; default to the user's preferred/last-used chain.
4. **Trade-time receive-chain confirmation** (decision §4.4) — a concrete app task, built
   into the open-trade flow in `ad/[adId].tsx` before the signing step (doesn't exist
   today): "You'll receive USDT as **BEP-20 on BSC** — make sure your destination
   supports it."
5. **Restructure token decimals to per-`(chain, token)`** — there is no per-(chain,token)
   decimals data anywhere today: `chains.ts` `DEFAULT_TOKENS` USDT/USDC = scalar `6`
   (lines 325/333) plus **three inline copies** (`app/p2p/index.tsx:37`,
   `my-trades.tsx:27`, `services/p2p.ts:844`). Replace these four maps with a
   `(chain,token)→decimals` model and resolve from ad/trade `chainId` at every parse +
   format site. **BSC USDT is 18, Sonic USDT is 6** — without this, BSC amounts are off by
   10¹² on both the deposit/parse and display/format paths. Regression test: BSC USDT → 18;
   1.0 USDT on BSC parses to 1e18 base units.
6. **Gas-token guard (BSC).** BSC needs **BNB / tBNB** for `openTrade`, `markPaid`,
   `release`, `deposit`, `dispute` (Sonic uses native S). Per §10.3, for the MVP add a
   **pre-broadcast native-balance check + "fund your wallet with BNB" prompt** so a user
   can't open a BSC trade and hit a cryptic mid-flow "insufficient gas" failure.
7. **`pool.tsx` per-chain balance breakdown.** Pool is per-(seller, chain). Show a
   per-chain breakdown (not just the selected chain) so a seller's existing **Sonic** pool
   funds stay visible after switching to BSC and don't appear to "disappear".
8. **Post-trade cross-chain hint** (optional) — on a released trade, "Need it on another
   chain? Swap in wallet" → the existing LiFi flow.

---

## 8. Invariants to preserve

1. **EIP-712 domain chainId == the chain's real `eth_chainId`** (probed per chain).
2. **EIP-712 version travels with the chain** (Sonic-mainnet `1`, Sonic-testnet `2`,
   BSC `2`) — never assumed uniform.
3. **Preset-bundle no-drift** — RPC + chainID + token addrs + escrow + version flip
   together. A half-set `bscPresets` is a live signature-rejection bug.
4. **Money invariant is per `(chain, token)`** against that chain's escrow.
5. **Reconciler/watchdog read each trade against its OWN chain's escrow; FAIL LOUD,
   never default to Sonic** — mis-dispatch is *destructive* (orphan-reclaims a live trade),
   not a no-op.
6. **Token identity is per-chain** — BSC USDT/USDC addresses **and 18 decimals** ≠ Sonic.

---

## 9. Phasing & non-goals

**Phasing (chain model):**
- **A (with BSC work):** per-ad chain + chain filter + default + trade-time confirmation +
  gas guard.
- **B:** "preferred receive chain" on the buyer profile, wired into the marketplace
  default and **Express auto-match (#22)**.
- **C (probably never):** in-escrow cross-chain delivery — only if data shows real
  abandonment from chain mismatch *and* the wallet-swap path is insufficient.

**Non-goals:** in-escrow bridging; custodial "invisible chain" abstraction; MCGP on BSC.

---

## 10. Open decisions (need owner sign-off)

1. **Primary buyer-facing chain at launch.** RECOMMENDED: **BSC primary** (BEP-20 demand),
   Sonic for MCGP/native. Architecture identical either way.
2. **BSC mainnet now, or testnet-only first?** RECOMMENDED: **testnet-only first** (mainnet
   needs external audit sign-off).
3. **Gas-token strategy on BSC.** RECOMMENDED for MVP: **users self-fund BNB** + the
   pre-broadcast balance guard (§7.6); deploy an **EIP-2771 relayer on BSC later** (the V2
   contract is relayer-ready). Decide before launch — this is a hard first-run blocker.
4. **MCGP on BSC.** RECOMMENDED: **no** — `mcgpAddress = address(0)`, USDT/USDC only.
5. **Marketplace default:** RECOMMENDED: one chain at a time, defaulting to the user's
   preferred/last-used (vs all chains with a per-row badge).
6. **Owner/admin key custody on BSC:** same multisig as Sonic vs chain-specific. Affects
   who can `adminResolve`/upgrade + deploy coordination.
7. **Chain-enablement mechanism:** RECOMMENDED: "enabled when escrow address configured"
   (matches dormant-by-default), vs an explicit `P2P_CHAINS` allowlist env.

---

## 11. Effort & sequencing

≈ **2 weeks for a careful testnet-only landing**; BSC mainnet adds external-audit lead
time. The chain-aware data model removes the riskiest migration.

Ordered (per-chain service multiplex is the gating prerequisite):
1. §10 decisions → BSC testnet V2 deploy (capture proxy + version).
2. `bscPresets` + per-chain config map.
3. **Per-chain `P2PEscrowService` + `P2PTradeService` registry, multiplexed in the handler
   (incl. admin path) + per-chain boot-probe loop + per-chain bytecode check.** ← gating.
4. Reconciler/watchdog fail-loud per-chain escrow resolution.
5. `CreateAd`/`ListAds` chainId + per-(chain,token) decimals (backend).
6. App: 10 broadcast sites + chain selector + list filter + decimals model + gas guard +
   trade-time confirmation + pool per-chain breakdown.
7. BSC-testnet E2E (deposit → open → markPaid → release, + dispute + timeout + a
   deliberately-dropped-confirm reconciler check) → mainnet gated on audit.

---

## 12. Risks

- **Silent signature rejection** if any per-chain domain (chainID/version) is wrong — #1
  failure mode; mitigated by per-chain `eth_chainId` probe + bundle discipline + the
  per-chain voucher sign/recover test.
- **Orphaned funds** if the reconciler reads a BSC trade against the Sonic escrow — the
  orphan-reclaim path makes this *destructive*; mitigated by fail-loud per-chain resolution.
- **10¹² fund-amount error** from chain-agnostic decimals (BSC USDT 18 vs Sonic 6) —
  mitigated by the per-(chain,token) decimals restructure + regression test.
- **Wrong-chain delivery** (asset on a chain the destination rejects) — mitigated by the
  trade-time confirmation; wallet swap is the recovery path.
- **Stuck mid-flow on gas** on BSC — mitigated by the BNB balance guard (or a relayer).
- **Liquidity fragmentation** across two books — mitigated by a single primary chain +
  default + Express matching (not by bridging).

---

## 13. Launch dependencies (non-code)

- **Liquidity seeding** — a BSC-primary launch with zero merchant pools has zero buy-side
  liquidity (the chicken-and-egg). Define a merchant-onboarding/incentive SOP for funding
  BSC pools before go-live.
- **Rate parity / arbitrage monitor** — confirm the NGN/USDT rate feed (Bybit P2P / OXR) is
  chain-independent (USDT is USDT), and add a monitor flagging same-pair ads whose rates
  diverge across chains, to prevent cross-chain rate arbitrage draining one book.
- **Operator boot verification** — the startup log line (§5.2.5) listing P2P-enabled
  chains + escrow addresses lets ops confirm config before taking trades.
