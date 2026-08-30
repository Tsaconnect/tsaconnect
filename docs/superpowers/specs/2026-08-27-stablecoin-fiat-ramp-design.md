# Stablecoin ⇄ fiat ramp — backend design (Phase A: sell)

**Date:** 2026-08-27
**Status:** Phase A shipped and merged (`8172697`, `32a137b`, `cbf105c`), **dormant by default**
**Repo:** `tsa-dev/tsa-api-go`
**Sandbox evidence:** `2026-08-27-stablecoin-ramp-sandbox-findings.md`
**App layer:** `2026-08-29-stablecoin-sell-app-design.md`

> **Reconstruction note (2026-08-30).** The original was deleted before it was ever committed and
> cannot be recovered from git. This is a reconstruction from the surviving SDD ledger
> (`.superpowers/sdd/2026-08-27-stablecoin-fiat-ramp-phase-a/`) and from the merged source, which
> together pin every decision below. Where the ledger cites the original by section (§6.1, §6.4,
> §10) those numbers are preserved, but the surrounding numbering may not match the original.
> Phase B (§11) was an outline in the original and is superseded — see the note there.

---

## 1. Objective

Let a user sell USDC/USDT for fiat, and later buy stablecoin with fiat, brokered through
Flutterwave. Phase A is **sell only**.

### 1.1 The model: broker, not counterparty

This is the decision everything else follows from, and it was a correction to an earlier draft.

For **MCGP** the platform is the counterparty — it is our own token and we buy and sell it from our
own supply. For **USDC/USDT we are a broker**: the user transacts with Flutterwave, we route and
price the trade, and we take a spread.

Concretely:

- the user's stablecoin goes from **their own wallet straight to Flutterwave's address**;
- it never passes through an address the platform controls;
- **no float, no reserve, no custody** on our side;
- our revenue is a spread on the rate (default **155 bps**), not inventory margin.

Any design element implying we hold user funds is out of scope by construction. This is why there
is no treasury, no reserve check, and no float sizing anywhere in Phase A.

---

## 2. Sell flow

```
user picks asset + amount + bank
  → quote      (spread applied to a plausibility-checked FW rate)
  → intent     (pending_deposit; user is handed FW's Polygon address)
  → user sends stablecoin from their own wallet  ← the only irreversible step
  → deposit_reported     (user reports the source tx hash)
  → deposit_confirmed    (LiFi DONE + verified on Polygon + FW balance visible)
  → converting           (FW stablecoin→fiat conversion accepted)
  → debited              (conversion reached SUCCESSFUL)
  → settled              (fiat paid to the user's bank)
```

Statuses, as shipped in `it_models.go`:

| Status | Meaning |
|---|---|
| `pending_deposit` | intent exists, awaiting the user's on-chain send |
| `deposit_reported` | user reported a source tx hash |
| `deposit_confirmed` | LiFi DONE + verified on Polygon |
| `converting` | FW stablecoin→fiat conversion in flight |
| `debited` | conversion settled; fiat payout in flight |
| `settled` | complete |
| `reversed` / `failed` | terminal failure |

### 2.1 The central invariant

> **Never advance money before the user's funds are provably present.**

Every gate in the flow exists to serve this, and the sandbox probes caught one place where the
plan broke it — see §5.

---

## 3. Pricing

### 3.1 Spread, not fee legs

The company margin is a single spread on the rate (`IT_STABLE_SPREAD_BPS`, default 155), applied by
`ApplySellSpread`, which **floors against the user** so rounding never pays out more than intended.
This replaced an earlier draft's separate bps legs — one spread is what the business actually
quotes, and one number is auditable.

Platform and provider fees still apply on top via the existing fee engine
(`applyStableSellFees`), and both `SpreadMinor` and `FeeMinor` are returned as itemised lines so the
app can show a breakdown (§6.1 of the original).

### 3.2 The rate source, and why it fails closed

Flutterwave is the pricing source — an explicit product decision, not silently overridden. But
`DeriveFiatPerUnit` deliberately **ignores `FWRate.Rate`** and recomputes the rate from the returned
source and destination amounts, because the sandbox proved the `rate` field can be internally
consistent yet detached from reality.

On top of that, `checkRatePlausible` validates the derived figure against an **independent** NGN/USD
feed (`s.fx`). A stablecoin is ~1 USD by construction, so anything outside a configurable band
(default ±25%) returns `ErrStableRateImplausible` and quotes **nothing**.

This exists because the sandbox returned a USDC→NGN rate wrong by ~six orders of magnitude. See the
findings note, Gate 2. It mirrors existing codebase discipline (`ErrITFxUnavailable`, the `fx.Stale`
guard in `CreateBuyIntent`): refuse rather than transact on a bad rate.

> A later fix added `stablePlausibilityRateFor` because the general-purpose `ratePerUsdFor` discards
> staleness — the guard was reading a feed that could itself be stale, i.e. backwards.

### 3.3 Paying on what arrived, not what was quoted

Bridges deliver less than they are given. The payout is therefore scaled by **`received / quoted`**:
the user is paid on what actually reached Flutterwave.

The app's corresponding obligation is to quote fiat against LiFi's *estimated arrival* rather than
the typed amount, so the final figure does not always shrink. That is documented in the app design.

Over-delivery is bounded by `IT_STABLE_OVER_DELIVERY_TOL_BPS` (default 200); beyond it,
`ErrStableOverDelivered`.

---

## 4. Decimals — the error this design is built to avoid

There are **two different decimal counts** in play and conflating them is a 10¹² error:

- **source decimals** — the token's decimals on the *source* chain (BSC stablecoins are **18**);
- **destination decimals** — Polygon USDC/USDT, which are **6**.

`StableAssetDecimals = 6` covers every chain Flutterwave supports, which is what lets one constant
serve the ramp. `StableToBaseUnits` **errors** (`ErrStablePrecision`) on more than 6 decimal places
rather than truncating — silently dropping a digit on a money path is worse than refusing.

---

## 5. Conversion is asynchronous — the defect the sandbox caught

Flutterwave returns `201 {"status":"NEW"}` and settles later. The plan had assumed the conversion
was synchronous and set the intent to `debited` as soon as `ConvertBalance` returned — and `debited`
is what fires the fiat payout. The payout would therefore have fired against fiat that did not yet
exist: §2.1 violated one hop earlier than the balance check protects.

As shipped:

- the FW transfer id is persisted on the intent (`conversion_id`);
- `ConvertAndQueuePayout` **stops at `converting`** and never sets `debited`;
- `FinalizeConversion` polls `GET /transfers/{id}` — `SUCCESSFUL` ⇒ `debited`, `FAILED` ⇒ back to
  `deposit_confirmed` with the reason recorded so the next tick retries;
- the watcher carries a third bucket for `converting` intents.

This is also what makes the production canary safe to run: **the failure mode is stuck, not lost.**

---

## 6. Delivery matrix (constrains Phase B)

Flutterwave's supported outbound (chain, token) pairs, verified against
developer.flutterwave.com on 2026-08-27:

| Chain | USDC | USDT |
|---|---|---|
| Polygon | ✅ | ✅ |
| Ethereum | ✅ | ✅ |
| Solana | ✅ | ✅ |
| Base | ✅ | ❌ |

**Tron, Stellar, BSC and Arbitrum are rejected.** A rejected transfer to a live address is not
recoverable, so `SupportsDelivery` and `ValidateDeliveryAddress` gate *before* an intent exists.
Address validation is deliberately strict — EVM chains require `0x` + 40 hex; Solana requires
32–44 base58 — because a malformed or wrong-chain address means the funds are gone.

---

## 7. API surface

| Route | Purpose |
|---|---|
| `POST /api/it/stable/quote` | price a sell — gross, afterSpread, fee, net, rate, expiresAt |
| `POST /api/it/stable/sell` | create the intent → `{intent, deposit}` |
| `POST /api/it/stable/sell/:id/deposit` | report the broadcast tx hash |
| `GET /api/it/intents/:id` | poll status |
| `GET /api/it/admin/stable/stuck` | operator view of stalled sells |

All IT **write** routes carry the `itIdem` idempotency middleware. A stablecoin sell intent is a
claim on a deposit, so a retried create must not mint a second claim.

---

## 8. Operational safety

- **Dormant by default.** Absent configuration the ramp is off and routes return a clear error. This
  follows the codebase's existing dormant-integration convention and keeps dev environments bootable.
- **Fail-closed payout gate.** No payout without a visible Flutterwave balance. An absent asset line
  logs a distinct `it_stable_balance_line_missing` warning so "FW isn't surfacing the balance" is
  distinguishable from "the deposit hasn't landed" — the two look identical otherwise.
- **Stuck-intent surfacing.** `ListStuckStableSells` covers `converting`, so a stalled conversion is
  visible rather than silent.
- **Bounded retries.** `maxStableConversionAttempts = 10`. This is a judgment call, not a documented
  Flutterwave limit — trivially promotable to config if operations wants to tune it.

### 8.1 Recovery (§10 of the original)

The recovery path for a stalled conversion is a manual dashboard conversion plus a manual payout.
This is the path the production canary would exercise if the conversion never settles.

---

## 9. Known-open risks

| Risk | State |
|---|---|
| **Conversion never observed reaching `SUCCESSFUL`** | Sandbox cannot settle conversions at all. Closes only via a **production canary**. |
| **Front-running** | Nothing binds the reporting user to the transaction's sender. Surfaces to a victim as `ErrStableDepositAlreadyClaimed`. Escalated; inert while dormant. Decision outstanding. |
| ~~Minimum trade sizes unenforced (I5)~~ | **Closed 2026-08-30.** `QuoteStableSell` enforces a provider floor (`IT_STABLE_MIN_SELL`, default 10), with a defence-in-depth check in `CreateStableSellIntent`. The handler re-quotes server-side, so both `/stable/quote` and `/stable/sell` pass through the one chokepoint. A malformed config keeps the default rather than removing the floor. |
| **`ProviderFallbackBps` uncalibrated** | `/fees` is 501 in sandbox. Observed 1% flat, n=2, one tier. |
| **`FLUTTERWAVE_BASE_URL` unset in production** | Would silently route **all** FW traffic — including existing NGN payouts — to sandbox. User to confirm. |

---

## 10. What the sandbox taught about testing

Stubbed tests hid every serious defect found in review: a ~100× fee error (minor units passed where
major were expected), a missing `FxMinorPerUSD` that would have failed **every** non-USD sell, and a
retry path with no exit. Each surfaced only when a **real** engine was wired into the test.

The rule that came out of it: **money-path tests wire the real fee engine.** A stub that agrees with
the implementation proves the implementation agrees with itself.

Two further defects came from environment differences — a PostgreSQL insert failure (`''` into
numeric columns) that SQLite fixtures hid, and an IDOR plus non-unique tx hashes (fixed in
`e2d2a98`).

---

## 11. Phase B (buy) — blocked on one empirical question

The original sketched buy as the mirror of sell: collect fiat, convert, deliver stablecoin to a
user-supplied address on one of the four chains in §6. **That outline is superseded** — as of
2026-08-30 the conversion leg has a precondition the original did not account for.

### 11.1 The finding: collected fiat is not automatically spendable

Flutterwave separates **Collection Balance** (money received from customers) from **Payout
Balance** / `available_balance` (money usable for transfers). `GET /wallets/balances` — the balance
`/direct-transfers` draws from — reflects only the latter. The charge object carries `settled` and
`settlement_id[]` as fields *separate* from `status`, so `charge.completed` firing says nothing
about spendability. Default NGN settlement is **T+1** (Friday settles Monday).

Buy's conversion leg is wallet-to-wallet NGN→USDC, so it needs *spendable* fiat. That makes
settlement a precondition the sell flow never had.

> Good news for the conversion itself: NGN→USDC is the **documented** direction
> (`developer.flutterwave.com/docs/stablecoins`), whereas sell's USDC→NGN was undocumented and
> needed a blocking spike. See the `ConvertBalance` doc comment.

### 11.2 Virtual accounts — researched, contradicted, not yet closed

The natural objection is that VA (bank-transfer) collections might bypass settlement, since a bank
transfer carries no chargeback risk. Primary sources say otherwise:

- *"Bank transfer payments are processed instantly to your Flutterwave collection balance, which is
  then settled to your preferred settlement destination within 24 business hours."*
  (support/payment-methods/pay-with-bank-transfer-pwbt)
- *"The payment is processed instantly, and you will be settled in line with our settlement
  schedule."* (support/payments/fixed-virtual-accounts)
- *"Settlements are supported for all payment methods."* (developer.flutterwave.com/docs/settlements)

Instant **processing**, scheduled **settlement**. Two caveats keep this open rather than closed:

1. **"Sweeping" is absent from Flutterwave's documentation entirely** — not in the v3/v4 VA
   references, blog, or support. The mechanism under discussion may simply be undocumented, or an
   account-level arrangement.
2. **A documented exception exists with the right shape.** The "top up your available balance" page
   describes a dedicated **MFB virtual account** and states *"The funds will be added to your payout
   balance"* — a direct credit with no settlement wait. It is documented as merchant self-funding
   rather than customer collection, but it is a VA that credits payout balance directly.

### 11.3 The decisive test (open)

`models.WebhookEvent.Payload` is `jsonb` and stores every FW webhook verbatim, including fields the
parser ignores (`fwChargeData` reads neither `settled` nor `settlement_id`). Real VA credits are
already in production. A read-only query therefore settles this against real money:

```sql
SELECT payload->'data'->>'id'   AS charge_id,
       payload->'data'->>'settled' AS settled,
       payload->'data'->'settlement_id' AS settlement_id,
       created_at
FROM webhook_events
WHERE payload->>'type' = 'charge.completed'
ORDER BY created_at DESC LIMIT 20;
```

Equivalently: check whether a recent VA payment sits under Collection or Payout balance in the
dashboard.

### 11.4 Design fork, once answered

| Outcome | Consequence for buy |
|---|---|
| VA credits arrive **settled** | Buy is a minutes-long flow. Design proceeds as the mirror of sell. |
| Credits await settlement | Buy is next-day, **or** needs a manual Express Settlement step (dashboard-only, no API endpoint found; NGN-only; 0.1–1% fee), **or** requires holding NGN — which is the counterparty model deliberately rejected in §1.1 and must be an explicit decision, not a default. |

### 11.5 Gates still unrun

- `type:"crypto"` payout on each of the four chains in §6.
- Charge → settlement linkage (`GET /settlements/{id}` carries `charges[]`; the charge carries
  `settlement_id[]`). No settlement-completion webhook was found, so polling is likely required.

Phase B should not be planned in detail until §11.3 resolves.

The existing IT buy is unaffected: it credits MCGP from company supply on `charge.completed` and
never needed spendable fiat — which is why this precondition has stayed invisible.
