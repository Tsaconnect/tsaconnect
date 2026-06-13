# TSA Connect P2P — Master Roadmap

**Date:** 2026-05-28
**Purpose:** One consolidated view of everything planned for the P2P feature — what's done, the cross-cutting decisions, and every phase with scope, rationale, dependencies, and impact. Each phase becomes its own brainstorm → spec → plan when we build it.
**Backed by:** `2026-05-28-p2p-telegram-wallet-architecture-research.md`, `2026-05-28-p2p-onchain-architecture-recommendation.md`.

---

## North-star

A **non-custodial**, **gasless-feeling**, **two-sided** P2P crypto↔fiat marketplace with exchange-grade trust UX (Telegram Wallet / Binance P2P quality) — without taking on custody. Users only ever *sign*; the platform sponsors gas. Buying and selling are first-class and symmetric.

## Cross-cutting decisions (locked)
1. **Non-custodial / on-chain escrow** — not custodial. (Sonic's <$0.01 gas + ~90% FeeM rebate make on-chain viable; custody's regulatory cost isn't worth the marginal UX.)
2. **Gas fully sponsored** (relayer / meta-tx + Permit2; AA later). No per-tx fees to users; recoup in the merchant fee + FeeM.
3. **Two-sided, role-parameterized escrow** — one contract, buy & sell share a state machine; only the crypto side is escrowed.
4. **Merchant-gated ads + collateral**; off-chain reputation with on-chain settlement.
5. **Fiat leg stays off-chain** with structured proof + dispute rails.

---

## Current state (shipped)
- **Multi-currency buy-side browse (Spec 1):** P2P-scoped currency picker (geo default), "All Payment Methods" filter, privacy-safe payment chips, de-NGN copy, currency-aware empty state, provider catalog (`bank_transfer/opay/palmpay/kuda/interac/paypal/revolut/wise/skrill`), per-currency `floatAnchor` float-gating, ad-detail/trade-open de-NGN.
- **Escrow today:** `P2PEscrow` pool/voucher model on Sonic — sellers deposit; EIP-712 voucher ad creation (gasless); on-chain `openTrade` (buyer) + `release` (seller); `cancelByTimeout`; admin dispute resolve. **Sell-only.**

---

## Phase 0 — Trust & UX layer (NO contract change)
**Goal:** exchange-grade polish on the *current* escrow. Highest visible ROI, shippable now, fully on the buy side. Design these so they carry into Phase 1 (don't build twice).

- **Merchant reputation & trust signals (foundational — many features depend on it):** per-merchant **completion rate (30d), avg release time, avg pay time, completed-order count, online/last-seen, verified badge, ⚡ auto-accept**. Surface on every ad row + ad detail. → backend reputation service (timestamps `paid→released`, rolling stats).
- **Order state machine + live countdown timer + push notifications:** explicit states (`Pending → Paid → Releasing → Completed / Disputed / Cancelled`), per-order payment deadline, server-authoritative auto-cancel, push at each transition. (Carries directly into Phase 1.)
- **Express auto-match (default buyer flow):** one form (asset + fiat + amount + method) → rank eligible ads by price×reputation → broadcast to top merchants → **fastest-accept-wins (<1 min)**. Browse becomes the power-user fallback. → matching/routing engine + short accept-lock.
- **Region-scoped payment methods:** drive the method picker off the fiat selector (NG→OPay/PalmPay/bank; CA→Interac; EU→SEPA/Revolut/Wise), with per-method **speed labels**; pre-saved payment instruments; cap N/ad. → extend provider catalog to `(fiat/country → method)` + localized names + speed attribute.
- **Self-serve dispute wizard:** reason picker → evidence upload → counterparty negotiation window → support SLA; escrow frozen throughout; hardened in-order chat (chat-only comms, receipt pipeline). → dispute entity + evidence storage.
- **Merchant onboarding & collateral:** merchant status, **security deposit** (frozen, slashable on fraud), tiers (Merchant / Trusted), KYC gating for posting & for taking P2P. → collateral accounting + slashing path.
- **Maker ergonomics:** auto-accept toggle, templated auto-reply, hide-ad-without-ranking-penalty, auto-hide price outliers vs index.
- **Ratings/feedback** post-trade.
- **Two-sided browse scaffolding (display only):** Buy/Sell tabs + buy-ad listing UI prepared — but real sell-side *trading* lights up in Phase 1.

## Phase 1 — Escrow v2: two-sided + gasless (contract + backend + UI; RE-AUDIT)
**Goal:** kill the per-tx friction and make buying *and* selling first-class. This is the architectural core.

- **Role-parameterized escrow contract** (`cryptoDepositor / cryptoRecipient / fiatPayer / fiatReceiver`): one state machine for both directions; single shared contract, trades keyed by hash.
- **Gasless settlement:** relayer + **EIP-2771** trusted forwarder; **Permit2** for token pulls (USDT-safe); batched `permit→deposit` and `release→fee`; **direct-call fallback**; register for **Sonic FeeM**.
- **~2-tx happy path** + keep EIP-712 maker-order (gasless ad creation), extended to both directions.
- **Optimistic auto-release window** + auto-refund timeouts (no stranded funds; release isn't a hard manual blocker).
- **Dispute v2:** loser-pays dispute fee; non-custodial arbitrator (winner-flag, can only pay a trade party). **Prereq: tsa-admin wallet signing.**
- **Migration** from the current pool/voucher contract; deprecate sell-only `Side` hardcode.
- **True user-sells flow** wired end-to-end (the deferred "Sell side").

## Phase 2 — Advanced (optional / later)
- **Account abstraction:** EIP-7702 (native batching/sponsorship) and/or ERC-4337 + **session keys** ("sign once per session"), pay-gas-in-USDT.
- **Decentralized disputes:** bonded/staked arbitrators; **Kleros** appeal tier.
- **On-chain reputation anchoring** (e.g., hash commitments; Bisq-style reputation-as-collateral).
- **Multi-chain escrow:** deploy on BNB/Base/Polygon for native USDT etc. (the previously-deferred multi-chain spec); per-chain routing; users receive on the chain the trader funded.
- **Fiat automation:** Nomba Phase-2 auto-payouts / on-off-ramp; name-enquiry beyond NG.

---

## Sequencing & dependencies
```
Spec 1 (done) ──► Phase 0 (UX on current escrow)
                     │  reputation + state-machine + dispute wizard + payment model
                     │  (designed to carry forward)
                     ▼
                  Phase 1 (escrow v2: two-sided + gasless) ──► lights up sell-side trading
                                                              + removes gas/signing friction
                     ▼
                  Phase 2 (AA / decentralized disputes / multi-chain / fiat automation)
```
- Phase 0 ships independently and fast; its reputation/state-machine/dispute/payment work is **reused** by Phase 1 (build once).
- Phase 1 is the big lift (contract rewrite + re-audit) and is what actually resolves the two concerns you raised (per-tx fees, one-sidedness).
- Phase 2 items are independent add-ons, prioritized by traction.

## Prerequisites & open decisions (resolve before/inside Phase 1)
- **Sonic capabilities:** EIP-7702 (Pectra) + ERC-4337 bundler/paymaster availability (EIP-2771 relayer works regardless); **FeeM registration terms**.
- **Stablecoin approvals:** USDT lacks `permit` → use **Permit2**.
- **tsa-admin wallet signing** for on-chain dispute resolution (hard prereq).
- **Relayer infrastructure:** build vs Biconomy/Pimlico/ZeroDev; cost model + recoup.
- **Compliance posture:** even non-custodial fiat-P2P typically needs KYC/AML on the fiat leg and merchant verification — confirm per target jurisdictions (NG-first). Gate features by KYC tier.
- **Fee model — DEFERRED (decided 2026-05-28):** *get a functional two-sided system working first; wire up the final fee model later.* Keep whatever the current escrow charges (0.5% on release) so nothing blocks; don't redesign fees as part of Phase 0/1 core. **Reference to revisit:** Telegram Wallet uses **maker-only, taker-zero, success-only** — fee reserved at order time, charged only on release, refunded if the trade doesn't complete; **0.9% Trusted / 1.2% regular merchant**; **zero-fee in many emerging-market fiats** (ZAR, KES, UGX, PKR, ARS, EGP, NGN-adjacent, etc.); no platform spread ([help.wallet.tg/27](https://help.wallet.tg/article/27-p2p-market-fees)). When we revisit: decide rates, who pays, when charged, the zero-fee currency list, and fold the sponsored-gas cost into the single maker fee rather than a separate gas line.

## Consolidated feature checklist
**Discovery/UX:** currency picker ✓ · payment filter ✓ · chips ✓ · de-NGN ✓ · empty states ✓ · Express auto-match · trust signals (completion %, release time, online, ⚡, verified) · region-scoped payment picker + speed labels · sorting.
**Trade flow:** order state machine · countdown timer · push · in-order chat · proof-of-payment upload · "I paid"/"confirm" gating · optimistic auto-release · auto-cancel/refund.
**Two-sided:** buy ads ✓(buy) · sell ads (Phase 1) · role-parameterized escrow.
**Gas/UX:** relayer + EIP-2771 · Permit2 · FeeM · (later) 7702/4337 + session keys.
**Trust & safety:** merchant status + collateral/slashing · tiers · KYC gating · ratings/completion-rate · self-serve dispute wizard + evidence · dispute fee · arbitrator (non-custodial) · (later) bonded/Kleros.
**Maker tools:** EIP-712 ad vouchers ✓ · fixed+float pricing ✓ · auto-accept · auto-reply · hide/auto-hide · pre-saved payment instruments.
**Infra/back-office:** reputation service · matching engine · proof-of-reserves/ledger integrity (if ever pooled) · admin dispute console (needs signing).
**Later:** multi-chain escrow · fiat auto-payouts · on-chain reputation.
