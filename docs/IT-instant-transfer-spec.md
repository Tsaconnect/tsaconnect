# SPEC — IT (Instant Transfer): buy/sell MCGP with Naira via the company PM

## 1. Objective & users

Let users **buy and sell MCGP with Naira automatically**, exactly like any other token in the
existing Payment Merchant (PM) flow — **except** the MCGP obtained this way lands in **Contract B's
premium balance** (spendable in the marketplace + peer pay), not in the user's raw wallet. This is
how TSA "knows which MCGP came through this path" and can make only that MCGP spendable.

- **Users:** TSA Connect app users with a wallet + KYC (same gate as P2P/PM today).
- **Counterparty:** a single **company-owned MCGP Payment Merchant** ("IT PM"), Nomba-settled.
- **Name:** user-facing feature is **IT — Instant Transfer**.

**Non-goal / out of scope:** changing the USDC-settled `buyPremium` path (PR #50) — it coexists;
the marketplace "Pay with MCGP" checkout (already deferred); any change to Contract A.

### Why this is mostly a *redirect*, not a new system
- The PM/Nomba **fiat automation already exists** (`pm_automation_service.go`: `HandleInbound`
  buy-match, `InitiateSellPayout`/`HandlePayoutStatus` sell, NGN ledger, caps, manual queue,
  refund-on-failure). Reused as-is.
- MCGP **already trades on Sonic P2P** (`config.go:408` Assets includes MCGP; the escrow whitelist
  is runtime, not hardcoded). Today that path releases MCGP **to the wallet**.
- The **only** change is the crypto leg's destination: **credit Contract B premium** instead of
  releasing to the wallet. Everything else is reuse.

## 2. Core features & acceptance criteria

| # | Feature | Acceptance criteria |
|---|---------|--------------------|
| F1 | **Buy MCGP with ₦** | User pays NGN to the IT PM's Nomba VA; on settled+matched inbound, the operator credits `premiumBalance[user]` on Contract B for the quoted MCGP; the user's IT balance (premium) increases; no auto-credit on ambiguous/mismatched inbound (manual queue). |
| F2 | **Sell MCGP for ₦** | User with premium balance ≥ amount initiates a sell; operator debits `premiumBalance[user]`, then Nomba pays NGN to the user's verified bank; on payout failure premium is **re-credited** (single-shot). |
| F3 | **Marketplace spendable** | MCGP bought via IT is premium balance → already spendable via the existing user-signed `spend` (peer pay today; marketplace checkout later). Wallet MCGP remains non-spendable. |
| F4 | **Pricing** | Admin sets the IT **NGN buy & sell rate** for MCGP (spread = TSA margin); the rate is locked into each intent at creation. |
| F5 | **Dedicated IT PM** | A single company PM row configured for MCGP/IT; dormant-by-default (feature off unless the IT PM + operator are configured). |
| F6 | **Fully backed** | `premiumBalance` total stays ≤ on-chain MCGP held by Contract B at all times; the operator can never create unbacked premium. |
| F7 | **Idempotent** | Every credit/debit/refund is single-shot under retried Nomba webhooks and dropped confirms (on-chain `usedRef` + DB unique key). |

## 3. Design

### 3.1 Contract B v2 changes (`McgpEntitlementSwap`, sibling repo)

> **Hardened after independent review (findings C1/C2/H1/M1/L2).** The naive
> "operator credit + existing user-signed exit" design is a **treasury drain**: a stolen operator
> key credits an attacker wallet, then the attacker's own `withdrawPremiumToWallet`/`sellPremium`
> realizes it as MCGP/USDC. The fix is **premium provenance** — operator-credited premium has **no
> on-chain cash-out**; its only exit is an operator-initiated **Nomba payout to a KYC bank account**.

**Provenance split (closes C1).** Replace the single `premiumBalance` with two buckets:
```solidity
mapping(address => uint256) public premiumBought;     // from user-signed buyPremium (USDC) — self-exitable
mapping(address => uint256) public premiumCredited;   // from operator creditPremium (IT/NGN) — SPEND-ONLY
mapping(address => uint256) public pendingSettle;      // debited-but-not-yet-paid (two-phase, closes C2)
// premiumBalanceOf(u) = premiumBought[u] + premiumCredited[u]  (spendable view)
// accountedMcgp == Σ (premiumBought + premiumCredited + pendingSettle)   // backing target

// Exits restricted by provenance:
//   withdrawPremiumToWallet / sellPremium  -> draw ONLY from premiumBought  (unchanged self-custody)
//   creditPremium  -> premiumCredited only;  debitPremium -> premiumCredited only
//   spend(to, amount) -> draws credited-first then bought; recipient receives the SAME bucket type
//      (so operator-credited value can never become self-exitable downstream)
```

**Operator role + caps (M1/L2):**
```solidity
mapping(address => bool) public operators;            // PM automation signer(s); onlyOwner add/remove (events)
mapping(address => bool) public guardians;            // low-threshold: removeOperator + pause ONLY (fast IR)
uint256 public maxCreditPerTx;                        // per-tx bug-guard
uint256 public creditWindowCap;     uint256 public windowStart;   uint256 public creditedInWindow;
// rolling on-chain ceiling: a stolen key can loop txs, so per-tx is not enough — bound aggregate/window
```

**Buy leg — credit (operator):**
```solidity
function creditPremium(address user, uint256 amount, bytes32 ref) external onlyOperator nonReentrant whenNotPaused;
//   require(ref != 0 && !usedRef[ref]); usedRef[ref]=true;
//   require(amount >= minCredit && amount <= maxCreditPerTx);            // L1 min + per-tx
//   _rollWindow(); require(creditedInWindow + amount <= creditWindowCap); creditedInWindow += amount;
//   premiumCredited[user] += amount; accountedMcgp += amount;
//   require(mcgpToken.balanceOf(this) >= accountedMcgp, "Underbacked");  // treasury must fund first
//   emit PremiumCredited(user, amount, ref)
```

**Sell leg — two-phase debit (closes C2):**
```solidity
function debitPremium(address user, uint256 amount, bytes32 ref) external onlyOperator nonReentrant whenNotPaused;
//   require(ref != 0 && !usedRef[ref]); usedRef[ref]=true;
//   require(premiumCredited[user] >= amount);
//   premiumCredited[user] -= amount; pendingSettle[user] += amount;     // STILL in accountedMcgp -> no withdraw-able excess
//   emit PremiumDebitPending(user, amount, ref)

function settleDebit(address user, uint256 amount, bytes32 ref) external onlyOperator nonReentrant;   // after Nomba SUCCESS
//   require(!usedRef[ref]); usedRef[ref]=true; require(pendingSettle[user] >= amount);
//   pendingSettle[user] -= amount; accountedMcgp -= amount;             // NOW the MCGP becomes TSA excess
//   emit PremiumDebitSettled(user, amount, ref)

function reverseDebit(address user, uint256 amount, bytes32 ref) external onlyOperator nonReentrant;   // Nomba FAILED/timeout
//   require(!usedRef[ref]); usedRef[ref]=true; require(pendingSettle[user] >= amount);
//   pendingSettle[user] -= amount; premiumCredited[user] += amount;     // user made whole; accountedMcgp unchanged
//   emit PremiumDebitReversed(user, amount, ref)
```

**Invariants:** `accountedMcgp == Σ(premiumBought + premiumCredited + pendingSettle)` and
`held >= accountedMcgp` at every step; `withdraw()` removes only true excess; a debit **cannot**
free backing until `settleDebit` (so an unknown/dropped payout never leaves withdraw-able excess —
C2). Operator EOA, owner multisig, and guardian are **distinct keys in distinct custody** (enforced
operationally + documented). `usedRef` is shared but every ref is **domain-separated** (H1, §3.2).

### 3.2 Backend (`tsa-api-go`)
- **Service** (`mcgp_swap_service.go`): `OperatorCreditPremium`, `OperatorDebitPremium`,
  `OperatorSettleDebit`, `OperatorReverseDebit` — build calldata, **sign with the operator signer**
  (KMS in prod; never the in-process `LocalKeyReleaseSigner` on mainnet), broadcast, **confirm by
  verifying the event before any DB write** (mirror the existing verify-then-write rule).
- **Domain-separated refs (H1).** Every ref is `keccak(domain || id)` with a distinct domain so
  paths can never collide or replay across each other:
  `IT:credit:<intentID>`, `IT:debit:<tradeID>`, `IT:settle:<tradeID>`, `IT:reverse:<tradeID>`,
  `spend:<orderID>`. Domain separation is a stated, tested invariant.
- **IT intent model** (`it_intent.go`): `{id, userID, pmID, direction, mcgpAmount, ngnAmount(kobo),
  rate, status, ref, txHash, paymentRef}`. Unique `ref`. `paymentRef` is the **strong attribution
  key** (see Automation). `MCGPSwapLedgerEntry` holds the audit row at confirm.
- **Automation:**
  - **Buy:** a settled NGN inbound attributed to the IT PM is matched to its open intent **by the
    strong attribution key first** (dedicated per-intent VA `accountRef` or a unique payment
    reference in the narration — **NOT** name+amount, see H2/§5). On a unique match → cap gates →
    `OperatorCreditPremium`. Name+amount may only *route to manual*, never auto-credit.
  - **Sell:** `OperatorDebitPremium` (→ `pendingSettle`) → reserve NGN → `InitiateSellPayout`. On
    Nomba **success** → `OperatorSettleDebit` (frees backing) + finalize. On **failed** →
    `OperatorReverseDebit` (user made whole) + release the NGN reservation (reuse the existing
    `ngn_reservation_released` marker). On **unknown** status → leave in `pendingSettle`, route to
    manual (never auto-settle or auto-reverse on uncertainty). Each step is single-shot via its
    domain-separated ref.
- **Pricing:** admin-set IT NGN buy/sell rate (a small `ITRate` config row); locked into the intent
  at creation. Integer kobo math; MCGP 18dp / NGN 2dp; credit rounds MCGP **down**, NGN payout
  **down**; `minCredit` floor (L1).
- **Routes** (`/api/it/*`, JWT): `POST /buy` (create intent → return Nomba VA details),
  `GET /buy/:id` (poll), `POST /sell`, `GET /balance` (alias of premium), `GET /rate`,
  `GET /ledger`. Admin: `POST /admin/it/rate`, IT PM seed/status. **Dormant** unless the IT PM +
  operator are configured (gate like `pmh != nil`).
- **Reconciler / monitoring (M3):** the `held >= owed` reconciler **cannot detect a C1-style drain**
  (the invariant stays true as funds leave), so it is necessary but not sufficient. Add alert-only
  signals on: operator-credit **volume vs window cap**; **credit→same-address-exit** correlation
  (a credited address that then withdraws/sells); and stale `pendingSettle`/IT-intents past a TTL.

### 3.3 App (`tsa-app`)
- IT screens under `app/wallet/it/` (or fold into the existing `mcgp` hub): **Buy** (enter ₦ or
  MCGP → quote at IT rate → show VA pay-in → poll until credited; **no client signing** — operator
  credits on Nomba settle), **Sell** (enter amount → debit + NGN payout → result), **balance** =
  premium. Reuse the PM/Nomba pay-in UI + the TSA Signature design system.
- The marketplace/peer **spend** of IT-bought MCGP is the already-built user-signed `spend`.

### 3.4 Pricing model (F4)
Admin-set NGN rate (buy & sell, spread = margin), locked per intent. MCGP has no liquid external
price, so admin-set is the right fit (same rationale as the USDC premium phases). Behind a service
interface so a feed/peg can replace it later.

## 4. Flows

**Buy (₦ → premiumCredited):** app creates IT buy intent (MCGP@rate, expected ₦, **paymentRef**) →
user pays the IT PM's Nomba VA including the payment reference → Nomba webhook → settled credit
attributed to IT PM → match the unique open intent **by paymentRef/dedicated-VA** (name+amount only
routes to manual) → cap + window checks → `OperatorCreditPremium(user, mcgp, IT:credit:<intentID>)`
→ confirm event → intent `confirmed`, audit row, **premiumCredited** up (spend-only).

**Sell (premiumCredited → ₦), two-phase:** app sell (amount ≤ premiumCredited) →
`OperatorDebitPremium` (moves to `pendingSettle`, still backed) → confirm → reserve NGN →
`InitiateSellPayout` → Nomba **success**: `OperatorSettleDebit` (frees backing) + finalize;
**failed**: `OperatorReverseDebit` (back to premiumCredited) + release NGN reservation;
**unknown**: stays in `pendingSettle`, route to manual (never auto-settle/auto-reverse). Each step
single-shot via its domain-separated ref.

## 5. Security & threat model (rewritten after review)

**The operator role is trusted, and its blast radius is bounded by design — not zero.** A
compromised operator key can:
- **Mis-attribute credits** to any wallet, up to `maxCreditPerTx` per tx and `creditWindowCap` per
  rolling window, never exceeding treasury-funded MCGP (`held >= accountedMcgp`).
- **Grief** by debiting a user's `premiumCredited` into `pendingSettle`.

It **cannot** turn credited premium into MCGP/USDC: `premiumCredited` is **spend-only** —
`withdrawPremiumToWallet`/`sellPremium` draw only from `premiumBought` (user-signed, USDC-backed),
which the operator never touches. The **only** cash-out of credited premium is an
operator-initiated **Nomba payout to a KYC bank account** — traceable, rate-limited, reversible —
so the C1 "credit → self-withdraw real MCGP" drain is structurally removed.

**Mitigations (all required for mainnet):**
- Owner = **multisig**; **separate** low-threshold **guardian** role with only `pause` +
  `removeOperator` for fast incident response (the credit/debit path is pausable; rotate the key
  before the window cap is hit).
- Operator key in **KMS** (never the in-process `LocalKeyReleaseSigner`); operator/owner/guardian
  are **distinct keys in distinct custody**.
- On-chain **rolling** credit ceiling (per-tx alone can't stop a looping key) + `minCredit` floor.
- **Strong NGN attribution** (per-intent paymentRef / dedicated VA) before any auto-credit — name+
  amount only routes to manual. **Blocked on** confirming the real Nomba inbound key (see
  `pm-inbound-attribution-held`); IT auto-credit must not ship before that is proven.
- Monitoring on credit volume + credit→same-address-exit correlation (the reconciler can't see the
  drain — M3).

**Always:** verify the on-chain event before writing any ledger row; fail closed on idempotency
errors; integer money math; dormant-by-default; domain-separate every ref.

**Ask first:** operator-key custody (KMS mandatory for mainnet); IT rate values + who sets them;
keep the USDC `buyPremium` path alongside IT (default: yes); on-chain cap + window values.

**Never:** let the backend/ledger move money; let `creditPremium` exceed backing or the window cap;
make `premiumCredited` self-exitable to wallet/USDC; free a debit's backing before Nomba confirms;
auto-credit on name+amount; auto-reverse on *unknown* payout status; reuse a `ref`; deliver
IT-bought MCGP to the wallet.

## 6. Testing strategy

- **Contract:** `creditPremium` raises `premiumCredited`+accounted, reverts when underbacked / over
  per-tx / over window; **C1 regression — `creditPremium(attacker,X)` then attacker
  `withdrawPremiumToWallet`/`sellPremium` MUST revert** (credited is not self-exitable); two-phase
  debit: `debitPremium`→`pendingSettle` keeps `accountedMcgp` (no new withdraw-able excess — C2
  regression), `settleDebit` frees it, `reverseDebit` restores; only-operator + guardian negatives;
  domain-separated `usedRef` reuse reverts and cross-path refs don't collide; property test
  `Σ(premiumBought+premiumCredited+pendingSettle) == accountedMcgp <= held` across mixed ops.
- **Backend:** SQLite-in-memory + fake operator signer + fake Nomba gateway; buy-match
  (exactly-one/ambiguous→manual), cap gates, idempotent webhook redelivery, sell payout
  success/failure (reversal single-shot), rate lock, kobo/wei rounding direction.
- **E2E (testnet):** ₦ buy → premium credited → peer `spend` → ₦ sell → payout; assert wallet MCGP
  is never credited and premium stays fully backed throughout.

## 7. Open questions (confirm before/at build)
1. **Strong NGN attribution key (BLOCKER):** IT auto-credit needs a confirmed per-intent attribution
   key from a real Nomba inbound (per-intent VA `accountRef` or narration reference). This is the
   same item held in `pm-inbound-attribution-held`. Until confirmed, IT buy can only run
   manual-review, not auto-credit.
2. **IT rate:** initial buy/sell spread + who sets it (admin role).
3. **Caps:** `maxCreditPerTx`, `creditWindowCap` + window length, `minCredit`, and how much MCGP to
   keep funded as backing (the float that bounds worst-case mis-attribution).
4. **Guardian set:** who holds the guardian (pause/removeOperator) keys + threshold.
5. **Naming surface:** "Instant Transfer (IT)" everywhere user-facing; internal contract stays
   `McgpEntitlementSwap`.

**Resolved by the review:** operator key = **KMS on mainnet** (not optional); USDC `buyPremium`
**coexists** (it's the `premiumBought`, self-exitable bucket) — provenance keeps the two safe side
by side.

## 8. Review status
Independent security review (round 1) returned **C1+C2 Critical, H1+H2 High** on the naive design
and a "do not implement as-is" verdict. This revision resolves them: C1 → provenance (credited is
spend-only, no on-chain cash-out); C2 → two-phase debit (`pendingSettle` keeps backing until Nomba
confirms); H1 → domain-separated refs; H2 → strong attribution + the §7.1 blocker; M1/M2/M3/L1/L2 →
rolling window cap, guardian role, correlation monitoring, min-credit, emergency rotation. **Re-run
the independent review on this revision, and a full external audit of Contract B v2, before code.**

## 9. Rollout
Contract B v2 (provenance buckets + operator/guardian + two-phase debit + window cap) → **re-run
independent review + full external audit** → deploy (multisig owner) → `addOperator(PM EOA)` +
set guardians → fund MCGP backing → seed IT PM + IT rate → **confirm the §7.1 attribution key** →
enable `/api/it/*` (manual-credit first, auto-credit only once attribution is proven) → app reveals
IT → testnet E2E → mainnet. USDC `buyPremium` path and legacy redeem unaffected.
