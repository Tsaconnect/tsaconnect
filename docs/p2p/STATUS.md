# P2P — Status & Handover

**As of:** 2026-05-28 · **Branch:** `feat/p2p-trading`

Pick-up note for anyone continuing the P2P work. High-level docs: [README.md](./README.md), [architecture.md](./architecture.md), [merchant-reputation.md](./merchant-reputation.md). Full design rationale + implementation plans: [`design/`](./design/) (see "Design docs" below).

## Where we are

Shipped and verified on this branch (backend `go build` clean + P2P services/handlers/jobs tests pass; app jest + `tsc` clean on touched files):

- **Buy-side multi-currency browse** — P2P-scoped currency selector (geo default), "All Payment Methods" filter, privacy-safe payment chips, de-NGN copy, currency-aware empty states. Ad-detail/trade-open screen is also de-NGN'd.
- **Payment-method providers** — normalized `provider` slug + catalog (`bank_transfer/opay/palmpay/kuda/interac/paypal/revolut/wise/skrill`); `GET /p2p/payment-providers`; provider-aware add-payment-method screen (NG bank + NUBAN path preserved).
- **Float-rate gating** — per-currency `floatAnchor` flag; float ads only where a market anchor exists.
- **Merchant reputation & trust** — `p2p_merchant_stats` + `p2p_ratings`, `MerchantStatsService` (event-driven + nightly reconcile), Verified/Trusted tiers, presence, `sellerStats` on ads, `GET /p2p/sellers/:id`, `POST /p2p/trades/:id/rate`, `SellerTrustBadges`, seller profile screen.
- **Live payment-window countdown** + post-trade 👍/👎 rating prompt + my-trades nudge.

## What's next

Per the roadmap (`design/2026-05-28-p2p-master-roadmap.md`):

- **Rest of Phase 0 (no contract change):** Express auto-match (default buyer flow), merchant onboarding + security-deposit collateral, region-scoped payment-method picker (drive methods off the fiat selector + speed labels), self-serve dispute wizard. Mobile **push** (Expo) is its own app-wide spec.
- **Phase 1 — Escrow v2 (contract + re-audit):** role-parameterized **two-sided** escrow (enables the user-*sells* flow), **gasless** via relayer + EIP-2771 + Permit2, ~2-tx happy path, optimistic auto-release, dispute fee + non-custodial arbitrator, migration from the current pool/voucher model. Rationale + blueprint: `design/2026-05-28-p2p-onchain-architecture-recommendation.md`.
- **Phase 2:** account abstraction (EIP-7702 / ERC-4337 + session keys), bonded/Kleros disputes, on-chain reputation anchoring, multi-chain escrow, fiat auto-payouts.

## Open decisions / prerequisites (before Phase 1)
- Sonic EIP-7702 (Pectra) + ERC-4337 bundler/paymaster availability (EIP-2771 relayer works regardless).
- Use **Permit2** for approvals (USDT has no `permit`).
- Sonic **FeeM** registration terms.
- **tsa-admin wallet signing** — hard prereq for on-chain dispute resolution.
- KYC/AML posture for the fiat leg (NG-first).
- **Fee model — DEFERRED:** get a functional two-sided system first, then wire maker-only / taker-zero / success-only (current escrow charges 0.5% on release). See architecture.md.

## How to continue
Each phase goes through **brainstorm → spec → plan → subagent-driven execution** (the workflow used here). Start from the relevant doc in `design/`:
- Two-sided / gasless escrow → `2026-05-28-p2p-onchain-architecture-recommendation.md` + the roadmap.
- More Phase-0 UX → the roadmap's Phase 0 list (Express, collateral, dispute wizard, region-scoped payments).

## Gotchas (learned the hard way)
- **GORM table naming:** the app opens GORM with a plain config (default naming → `p2_p_*`); every P2P model MUST pin `TableName()` to `p2p_*`, and raw SQL depends on it.
- **Double-fired events:** `P2PTradeReleased/Cancelled/Resolved` are emitted to BOTH trade parties; event handlers must dedup (process only the seller-addressed copy) or counts double.
- **SQLite test rigs** use raw DDL (not `AutoMigrate`, which rejects `gen_random_uuid()` defaults) and `SetMaxOpenConns(1)` (in-memory SQLite is per-connection).
- **Reputation is advisory** — never gate money on it; the nightly reconcile is the correctness backstop.
- App **typed routes**: new dynamic routes need a `<Stack.Screen>` entry in `app/p2p/_layout.tsx`; `router.push(... as any)` is the interim until typed routes regenerate.

## Design docs (`design/`)
Authored via brainstorm→spec→plan; copied here from the out-of-repo workspace so they survive in the repo.
- `2026-05-28-p2p-telegram-wallet-architecture-research.md` — custody-model research (Telegram Wallet, Binance/Bybit/OKX).
- `2026-05-28-p2p-onchain-architecture-recommendation.md` — the non-custodial + gasless + two-sided recommendation (blueprint).
- `2026-05-28-p2p-master-roadmap.md` — full feature roadmap (Phase 0/1/2) + fee deferral.
- `2026-05-28-p2p-merchant-reputation-trust-design.md` + `…-merchant-reputation-trust.md` — the reputation feature spec + implementation plan (just shipped).
- `2026-05-27-p2p-multi-currency-payment-methods-design.md` + `…-payment-methods.md` — the multi-currency spec + plan (shipped).

## Build / test
- Backend (`tsa-api-go/`): `go build ./...`; P2P tests `go test ./internal/services ./internal/handlers ./internal/jobs` (note: some unrelated suites — wallet/cart/orders/checkout — have pre-existing failures; P2P suites are green).
- App (`tsa-app/`): `npx jest services/p2p.test.ts hooks/useCountdown.test.ts hooks/useP2pCurrency.test.ts`; type-check via `npx tsc --noEmit` (filter to touched files — the project has pre-existing unrelated TS errors).
