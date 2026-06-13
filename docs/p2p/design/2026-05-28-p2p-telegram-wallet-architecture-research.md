# P2P UX Overhaul — Telegram Wallet Architecture Research & Forward Proposal

**Date:** 2026-05-28
**Purpose:** Study how Telegram Wallet (and Binance/Bybit/OKX) achieve great P2P UX, compare to TSA Connect's current P2P, and propose a forward architecture. UX is the priority; contract changes are on the table.

---

## TL;DR — the one thing that matters

**Telegram Wallet's smooth P2P UX is bought with custody.** Balances live in an internal ledger, so:
- Escrow = a **ledger freeze** (seller's balance reserved at ad-creation; release is an instant book entry).
- **No gas, no on-chain confirmation wait, no seed phrase, no addresses** for in-app actions.
- The blockchain is touched **only at deposit/withdraw edges**.

TSA Connect today is the opposite: **self-custody + on-chain per-trade escrow** (every `openTrade`/`release` is a signed, gas-paying, block-confirmed tx). That is the entire UX gap.

**Therefore the central decision is the custody model, not the UI.** You can have Telegram-grade instant/gasless P2P *or* pure self-custody-with-on-chain-per-trade, but not both. Everything else (Express auto-match, trust stats, timers, region-scoped payments) is a UX layer that helps either way.

---

## How Telegram Wallet works (condensed, sourced)

**Custody:** Custodial-by-default ("we create blockchain addresses for you and securely store the private keys" — [/113](https://help.wallet.tg/article/113-wallet-where-to-start)). An opt-in self-custodial "TON Wallet" exists for power users. Identity = the Telegram account; no separate signup, no seed for the custodial path. US is geofenced out of the custodial product — a tell that it's treated as regulated money transmission ([TechCrunch](https://techcrunch.com/2023/09/13/telegram-ton-crypto-wallet/)).

**Settlement:** In-app transfers are instant & free (internal ledger debit/credit); only external deposits/withdrawals hit chains and carry flat fees ([/50](https://help.wallet.tg/article/50-fee-rates-and-limits)). Cross-chain deposits auto-convert to a TON-side asset "behind the scenes" (MoonPay-powered).

**P2P specifics (authoritative numbers):**
- Escrow is a **custodial ledger hold**; sell-ad crypto is **reserved at ad-creation time** ([/5](https://help.wallet.tg/article/5-p2p-market-faq)).
- **Only Merchants post ads**; everyone else is a taker. Merchant = ID-verified + **50 USDT** frozen deposit; Trusted Merchant = **500 USDT**, ≥90% rating, ≥60-day account ([/130](https://help.wallet.tg/article/130-status-trusted-seller)).
- Pricing: fixed or floating, bounded **90–120%** of market. Up to **5 payment methods/ad**. Payment timeout 15 min–3 h ([/51](https://help.wallet.tg/article/51-questions-on-ad)).
- Fees: **maker-only, success-only** — 0.9% Trusted / 1.2% other; **takers pay 0%**; zero-fee in many emerging-market fiats ([/27](https://help.wallet.tg/article/27-p2p-market-fees)).
- **P2P Express**: no order book — user enters asset+fiat+amount+method, system auto-matches the best top-rated merchant and **fastest-to-accept wins (<1 min)** ([/142](https://help.wallet.tg/article/142-express-purchase)).
- KYC tiers Basic/Extended/Advanced; **Extended gates P2P** ([/239](https://help.wallet.tg/article/239-identity-verification)).
- Reputation = completed orders + completion-rate %; disputes freeze the crypto and are arbitrated by support ([/665](https://help.wallet.tg/article/665-rating-and-statistics), [/52](https://help.wallet.tg/article/52-questions-on-order)).

**Big-exchange deltas worth noting:** Binance/Bybit/OKX surface richer trust signals per ad — **avg release time, avg pay time, 30d completion %, verified badge, online status, ⚡fast/auto badge** — and run **region-scoped payment-method pickers** (the fiat selector drives which methods show, with per-method speed labels). Binance/Bybit lock escrow at **order** time with a **live countdown timer** and an explicit order state machine + self-serve appeal wizard.

---

## TSA Connect today vs Telegram Wallet

| Dimension | TSA Connect (now) | Telegram Wallet |
|---|---|---|
| Custody | Self-custody HD wallet (user holds seed) | Custodial internal ledger (default) |
| P2P escrow | **On-chain** P2PEscrow on Sonic; per-trade `openTrade`/`release` | **Ledger freeze**; reserve-at-ad-creation |
| Gas | User pays gas, signs every action | None for in-app actions |
| Settlement latency | Block confirmation per trade | Instant (book entry) |
| Onboarding | Seed phrase, wallet setup | Telegram identity, no seed |
| Ad sides | Merchant **sell-only** (buy-side browse) | Merchant buy & sell ads; takers either side |
| Pricing | Fixed + floating (✓ shipped) | Fixed + floating (90–120% band) |
| Payment methods | Provider catalog (✓ shipped), global list | Region-scoped catalog, ≤5/ad |
| Trust signals | Username + completion? minimal | Completion rate, verified, ⚡, ratings |
| Discovery | Browse list (✓ multi-currency shipped) | Browse **+ Express auto-match** |
| Merchant collateral | None | 50/500 USDT frozen, slashable |
| Disputes | Admin resolve (on-chain) | Support arbitration, frozen escrow |
| Fees | 2% merchant fee (marketplace), 0.5% escrow | Maker 0.9/1.2%, taker 0% |

You already have meaningful pieces: a **pool-model** escrow (sellers deposit into the vault — halfway to a ledger model), floating-rate ads, multi-currency browse, a provider catalog, and dispute/admin-resolve. The friction is concentrated in **per-trade on-chain open/release** and the **self-custody signing/gas** that surrounds it.

---

## The strategic fork: custody spectrum

| Option | What it is | UX | Cost / risk |
|---|---|---|---|
| **A. Full custodial ledger** (Telegram model) | All balances on an internal ledger; chain only at edges | Best — instant, gasless, seedless | Operator holds funds; money-transmission licensing, KYC/AML/KYT, proof-of-reserves, insolvency risk, likely geo-restrictions |
| **B. Hybrid pooled-custody ledger** (recommended target) | Funds custodied **on-chain in the existing P2PEscrow vault**; P2P trade lifecycle (lock/release) runs **off-chain on a backend ledger**; chain touched only at pool deposit/withdraw | Near-Telegram for P2P: instant, gasless trades | Platform controls release while funds are pooled → custodial *characteristics* during trades; needs ledger integrity + contract change |
| **C. Self-custody + gasless on-chain** | Keep on-chain per-trade escrow, but add **account abstraction / gas sponsorship / session keys** so users don't manage gas or sign raw txs | Smoother, but still block-latency; not instant | Preserves non-custodial integrity & lowest regulatory exposure; can't match "instant" feel |
| **D. Status quo** | On-chain per-trade, user-signed, gas-paid | Highest friction | None new — but loses the UX race |

**Recommendation:** a **phased path** that decouples the high-ROI UX work from the heavy custody decision:

- **Phase 0 (custody-agnostic UX layer) — do this regardless.** Biggest perceived-UX gains, works on the *current* on-chain escrow.
- **Phase 1 (custody decision).** Choose **B** (hybrid ledger) for Telegram-grade P2P feel while keeping on-chain custody at rest, *or* **C** if staying strictly non-custodial is a hard requirement. Avoid **A** unless you're prepared for full money-transmitter compliance.

This lets you ship a dramatically better P2P in weeks without betting the company on a custody/regulatory pivot first.

---

## Phase 0 — UX wins that DON'T depend on custody (steal these now)

Prioritized; each notes the backend/data it requires.

1. **Express auto-match (default buyer flow).** One form (asset + fiat + amount + method) → system ranks eligible ads by price×reputation, fans out to top merchants, **first-to-accept wins**. Browse becomes the power-user fallback. → *Matching/routing engine + short accept-window lock.* **(Highest UX leverage.)**
2. **Trust-at-a-glance merchant stats on every ad row:** verified badge, **completion rate (30d)**, **avg release time**, 30d order count, online/last-seen, ⚡auto-accept. → *Reputation service tracking per-merchant completion %, release latency (timestamp `paid→released`), order counts, online status.*
3. **Live countdown timer + explicit order state machine + push at each transition** (`Pending → Paid → Releasing → Completed/Disputed`). → *Per-order `payment_deadline`, server-authoritative auto-cancel job, push hooks.*
4. **Region-scoped payment-method picker** driven by the fiat selector, with per-method speed labels (Bank 5–30m, PayPal instant, etc.). Extends the provider catalog you just shipped. → *Catalog keyed by `(fiat/country → method)` + localized names + speed attribute.*
5. **Merchant collateral (skin-in-the-game):** frozen security deposit to post ads, slashable on fraud (Telegram's 50/500 USDT model). → *Collateral-freeze accounting + slashing path in disputes.*
6. **Self-serve dispute wizard:** reason picker → evidence upload → short counterparty negotiation window → support SLA, escrow frozen throughout. → *Dispute entity + evidence storage + hold-state.*
7. **Maker ergonomics:** pre-saved payment instruments, auto-accept toggle, templated auto-reply, hide-ad-without-ranking-penalty, auto-hide price outliers. → *Per-ad config + index-deviation job.*
8. **Two-sided ads (enables the deferred "Sell" side):** let merchants post buy ads and users sell to them — needed for parity and already on the Spec 2 list.

---

## Phase 1 — Proposed target architecture (Option B: hybrid pooled-custody ledger)

Your `P2PEscrow` is already a **pool vault** (sellers deposit). Evolve it so the **trade lifecycle moves off-chain**:

- **On-chain (edges only):** deposit into the vault, withdraw from the vault. The contract becomes a **custody vault + settlement authority**, not a per-trade state machine.
- **Off-chain ledger (backend, source of truth for P2P):** double-entry balances per user (`available` / `locked`), atomic reserve on ad creation, atomic release on confirm — **instant, gasless** to the user. Idempotent, append-only, auditable.
- **Reconciliation:** the contract holds aggregate custody; the ledger sums must equal the on-chain vault balance. Withdrawals settle on-chain against the ledger. Add **proof-of-reserves** style invariants and an admin/guardian settlement path.
- **Contract change (you flagged openness):** replace per-trade `openTrade`/`release` with vault `deposit`/`withdraw` + a guarded `settle`/`adminResolve`; the per-trade escrow accounting graduates to the backend ledger. This is a meaningful rewrite + re-audit.
- **Smooth the edges with account abstraction:** sponsor gas on deposits/withdrawals (or use smart accounts/session keys) so even on-chain steps feel one-tap.

**Custody caveat (be explicit):** while funds sit in the pooled vault and the backend controls release, this has **custodial characteristics** even though custody-at-rest is on-chain. That carries KYC/AML and licensing questions you must scope with legal before committing. Option C (stay non-custodial, just sponsor gas) avoids most of that at the cost of the "instant" feel.

---

## Open decisions & risks

- **Custody/regulatory:** Is custodial/hybrid acceptable for your jurisdictions (NG-first, expanding)? This gates A/B vs C. Needs legal input.
- **Proof-of-reserves & ledger integrity:** non-negotiable if you hold pooled funds.
- **Identity/onboarding:** you can't use Telegram identity, but you can cut wallet friction (gas sponsorship, smart-account recovery, optional managed keys) so P2P users aren't forced through seed-phrase management.
- **Contract re-audit cost/time** for the vault rewrite (Option B).
- **Two-sided trading** (user-sells) is a prerequisite for full parity and overlaps this work.

---

## Recommended next step

1. **Decide the custody direction** (B hybrid vs C non-custodial-gasless) — this is the fork everything hangs on; loop in legal.
2. **Greenlight Phase 0** (Express + trust stats + timers/state-machine + region-scoped payments + collateral + dispute wizard) — independently shippable on the current escrow, biggest UX ROI.
3. Then brainstorm → spec the chosen Phase 1 custody architecture (and fold in the deferred two-sided/multi-chain work).
