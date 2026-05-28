# P2P Architecture & Roadmap

## Current architecture

### Escrow (on-chain, Sonic)
`P2PEscrow.sol` is a **pool/vault** model:
- A merchant **deposits** crypto into their per-seller pool balance (on-chain).
- Publishing a **sell ad** is **gasless**: the seller signs an EIP-712 "AdVoucher" off-chain authorizing trades up to a max amount; no on-chain tx at publish time.
- A buyer **opens a trade** on-chain (`openTrade`, voucher + signature) which moves `amount` from the seller's `available` → `locked`.
- The seller **releases** on-chain (`release`) after confirming fiat receipt; a platform fee (currently 50 bps, MCGP fee-free) is deducted.
- `cancelByTimeout` (30-min payment window), `raiseDispute`, and `adminResolve` (admin directs funds to buyer or seller — never to the platform) round out the lifecycle.

The trade engine is **one-directional today**: every ad is a merchant *selling* crypto. There is no flow for a user to *sell* crypto to a merchant (that's the deferred two-sided work).

### Trade state machine (backend)
`P2PTrade` has an explicit transition validator (`P2PTradeValidNextStatuses`):
`pending_payment → {paid, cancelled}`, `paid → {releasing, disputed}`, `releasing → {released, paid}`, `disputed → {resolved}`; terminal: `released`, `cancelled`, `resolved`. Timestamps (`MarkedPaidAt`, `ReleasedAt`, `ExpiresAt`, `CreatedAt`) drive the reputation metrics. Trade transitions publish events on the in-process event bus (`internal/events`), consumed by the notification service and the merchant-stats service.

### Multi-currency buy browse
- A **P2P-scoped currency selector** (defaults from the app's geolocation currency, persisted separately so it doesn't change the global display currency).
- Ads are listed by `fiatCurrency`; copy and amounts are currency-driven (no NGN hardcoding on the buy path).
- **Payment methods** carry a normalized `provider` slug from a backend catalog (`bank_transfer, opay, palmpay, kuda, interac, paypal, revolut, wise, skrill`); the ad list exposes a **privacy-safe** `paymentMethodSummary` (provider + label only — never account numbers), and the "All Payment Methods" filter is derived client-side from loaded ads.
- **Float ads** are gated per currency by a `floatAnchor` flag on the rates feed (true only where a Bybit market anchor exists); fixed-rate ads work for any currency.

### Fee model — DEFERRED
Get a functional two-sided system working first; wire the final fee model later. The current escrow charges 0.5% on release. The target (reference: Telegram Wallet) is **maker-only, taker-zero, success-only** (charged on release, refunded if the trade doesn't complete), with many emerging-market fiats zero-fee, and the sponsored-gas cost folded into the single maker fee. Not part of Phase 0/1 core.

## Forward direction (decided 2026-05-28)

After surveying Telegram Wallet (custodial), Binance/Bybit/OKX P2P, and non-custodial smart-contract P2P (Hodl Hodl, Bisq, LocalCryptos, LocalCoinSwap, OpenPeer), the direction is:

**Stay non-custodial / on-chain, and make it *feel* gasless.** Sonic gas is <$0.01/tx and its Fee Monetization rebates up to ~90% of app fees — so the friction is signing-steps + holding the gas token, not cost. Custody (the Telegram model) buys marginal smoothness at the price of money-transmission licensing, KYC/AML/proof-of-reserves, and geo-restrictions — not worth it here.

Concretely:
- **Two-sided, role-parameterized escrow** — one contract modeling a trade by roles (`cryptoDepositor / cryptoRecipient / fiatPayer / fiatReceiver`); buy and sell share one state machine and only the crypto side is ever escrowed.
- **Gas sponsorship** — relayer + EIP-2771 meta-transactions; **Permit2** for approvals (USDT has no `permit`); register the contract for Sonic FeeM; users only ever *sign*.
- **Optimistic auto-release** window + auto-refund timeouts so a trade never stalls on a manual step.
- Non-custodial arbitrator (winner-flag, can only pay a trade party) + loser-pays dispute fee.

The current pool/voucher model is ~70% of this — ad creation is already gasless; the gaps are gasless trade actions, two-sidedness, and optimistic release.

## Roadmap

- **Phase 0 — Trust & UX layer (no contract change):** multi-currency browse ✅, payment providers ✅, **merchant reputation & trust** ✅ (see [merchant-reputation.md](./merchant-reputation.md)), live countdown + rating prompt ✅. Remaining: Express auto-match, merchant onboarding + collateral, region-scoped payment picker, self-serve dispute wizard.
- **Phase 1 — Escrow v2 (contract + re-audit):** role-parameterized two-sided escrow, gasless via relayer + Permit2, ~2-tx happy path, optimistic auto-release, dispute fee + non-custodial arbitrator, migration. Lights up the user-sells flow.
- **Phase 2 — Advanced:** account abstraction (EIP-7702 / ERC-4337 + session keys), bonded/Kleros disputes, on-chain reputation anchoring, multi-chain escrow (deploy on BNB/Base/Polygon for native USDT etc.), fiat auto-payouts.

## Prerequisites / open decisions (before Phase 1)
- Sonic EIP-7702 (Pectra) + ERC-4337 bundler/paymaster availability (EIP-2771 relayer works regardless).
- Stablecoin approvals → use **Permit2** (USDT lacks `permit`).
- Sonic FeeM registration terms.
- **tsa-admin wallet signing** — hard prerequisite for on-chain dispute resolution (admin must be a signing key).
- KYC/AML posture for the fiat leg per target jurisdictions (NG-first).
- Reconcile the fee model (see "Fee model — DEFERRED").

## Multi-chain note
`P2PEscrow` is currently **Sonic-only**; `MCGP` is Sonic-only, `USDC` is on Sonic + others, `USDT` is not on Sonic. True multi-chain deposits (trader picks chain; buyer receives on that chain) require deploying the escrow on additional chains + backend routing — that's the Phase 2 multi-chain item.
