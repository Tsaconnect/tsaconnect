# Stablecoin buy (Phase B1) — backend design

**Date:** 2026-08-30
**Status:** Draft — pending review
**Repo:** `tsa-dev/tsa-api-go`
**Predecessor:** `2026-08-27-stablecoin-fiat-ramp-design.md` (Phase A, sell — shipped, dormant)
**Related:** `2026-07-23-per-user-fiat-va-buy-design.md` (the static-VA collection rail this reuses)

---

## 1. Objective

Let a user buy USDC/USDT with naira and receive it at an address they already control, on a
chain they choose. Brokered through Flutterwave; the platform carries no stablecoin inventory.

### 1.1 Scope

**In:** NGN → USDC/USDT, delivered **direct from Flutterwave** to the user's own wallet address
on one of Flutterwave's four supported chains.

**Out, deliberately:**

- **The bridge leg.** Chains Flutterwave will not deliver to (Sonic, BSC, Arbitrum, Optimism,
  Avalanche, Linea, Tron) are not offered. Covering them requires receiving at a platform-owned
  Polygon address and bridging onward — see §9.
- Currencies other than NGN. Flutterwave's bank-transfer collection is NGN + GHS only, and only
  NGN is wired today.
- Mobile-money collection.

### 1.2 The custody position, stated plainly

Sell is genuinely pass-through: the user's stablecoin goes from their wallet straight to
Flutterwave, touching nothing we control.

**Buy cannot be.** `ConvertBalance` is a wallet-to-wallet move between *our own* currency
balances — there is no Flutterwave operation that turns a user's naira into their USDC. The
naira must land in our merchant balance, be converted there, and the resulting stablecoin sent
out. Between those points we hold user funds.

Two things bound that:

1. **It is not new exposure.** The live MCGP buy already takes user naira into the same
   Flutterwave balance on `charge.completed`. This delivers a different asset over the same rail.
2. **There is still no float.** We do not stock stablecoin to sell from inventory. The user's
   naira buys the exact stablecoin that is then forwarded. Nothing is held between customers and
   we carry no price risk on a book.

The accurate description of Phase B1 is therefore **no float, and a bounded transit window** —
not "no custody". The window's length is dominated by settlement (§4).

---

## 2. Delivery — the only irreversible step

| Chain | USDC | USDT |
|---|---|---|
| Polygon | ✅ | ✅ |
| Ethereum | ✅ | ✅ |
| Solana | ✅ | ✅ |
| Base | ✅ | ❌ |

This is the existing `deliveryMatrix` in `stable_assets.go`, verified against Flutterwave's docs
on 2026-08-27. Everything else is rejected by Flutterwave, and **a rejected transfer to a live
address is not recoverable** — so the gate runs before an intent exists, exactly as it does on
the sell side.

Three rules make a wrong delivery structurally hard rather than merely unlikely:

- **The address is never typed — for EVM chains.** For Ethereum, Polygon and Base it is read
  straight from `users.wallet_address`, server-held. There is no field in which to make a typo
  and no paste to get wrong.

  **This does not hold for Solana.** Its address comes from `wallet_addresses`, which the user
  populates themselves through `POST /api/wallet/nonevm/register`. `RegisterNonEVMAddress`
  (`internal/handlers/wallet_handler.go:964-1007`) validates the value for **shape only**
  (`client.ValidateAddress`) and writes whatever well-formed address the caller sent. There is no
  server-side derivation to check it against — Solana keys are derived client-side from a mnemonic
  the server never sees — so a real ownership check is not available, and adding one that cannot
  actually verify would be worse than none. The endpoint is correctly scoped to the caller's own
  `user_id` (nobody can overwrite another user's row), but nothing stops a user from registering
  an address they do not control as their own delivery target. See the risk table (§10).
- **A chain is offered only if the user holds an address on it.** A chain the user cannot
  receive on never appears in the picker, so funds cannot be sent somewhere they cannot see.

  The addresses come from **two** sources, not one (corrected 2026-08-30 — an earlier draft of
  this spec named `Wallet.Addresses`, which is a **dead column**: `models.WalletAddrEntry`
  occurs exactly once in the repo, its own declaration, and nothing has written it since the
  Node/Mongo API was retired):

  | Chain | Source |
  |---|---|
  | Ethereum, Polygon, Base | `users.wallet_address` — one EVM address serves all three |
  | Solana | `wallet_addresses` where `chain_key = 'solana'` |

  Every chain-key producer in the repo is **lowercase** while `deliveryMatrix` keys are
  uppercase, so the lookup must normalise case or it silently finds nothing.
- **Asset and chain are validated together.** `SupportsDelivery(chain, asset)` rejects
  Base+USDT before an intent is created; the UI hides USDT when Base is selected.

`ValidateDeliveryAddress` runs as a final shape check (EVM `0x`+40 hex, Solana 32–44 base58).
It cannot catch a well-formed wrong address — which is precisely why the address is ours to
supply rather than the user's to enter.

---

## 3. Collection — reusing the static VA rail

The user pays into their **existing standing virtual account**, the one issued by
`POST /api/it/va/activate`. No new account is minted per purchase.

This is a deliberate rejection of per-intent dynamic VAs, which were removed in July after two
observed production failures (`2026-07-23-per-user-fiat-va-buy-design.md`):

- **"Invalid Amount"** — a dynamic VA is amount-locked and rejects any transfer that is not the
  exact figure; the user's own bank surfaces this as a hard failure.
- **Silent expiry** — the account dies after 3600s with no countdown on screen, so a user who
  waits transfers into a dead account.

Static VAs accept any amount and never expire, which removes both structurally. The user-facing
flow is unchanged from what a per-transaction account would give: create intent → see an account
→ pay → poll. The account simply happens to be the same one each time, which also lets a
returning user save it as a payee.

### 3.1 Routing an incoming payment

A static VA's reference identifies the **user**, not the **purchase**, so one rule decides what
an arriving payment means:

```
charge.completed for user X
  │
  ├─ intent for X in `pending_payment`?
  │     └─ yes → fund that intent
  │
  └─ no  → today's MCGP credit path (AutoCreditFromVA), unchanged
```

**"Open" means `pending_payment` specifically — awaiting the user's transfer.** That is the only
state which can claim an incoming charge, and a user may hold at most one. Creating a second
while one is awaiting payment is rejected.

An intent past that point (`converting`, `delivering`) does **not** block a new purchase: it has
already been funded and cannot claim further money. A user whose first purchase is still settling
can start another, and the incoming naira is unambiguously the new one's.

Falling through to MCGP preserves current behaviour exactly: a user with no open stablecoin
intent who transfers naira gets what they get today.

---

## 4. Pricing — at landing, not at intent

The rate is the **live rate when the money lands**, not a rate locked when the user taps Buy.
This matches the shipped MCGP buy, whose webhook prices on arrival.

The consequence worth stating: **there is no lock, therefore no expiry, therefore no
"you paid too late" state.** The whole class of problems around a late payment against a stale
quote does not arise. The intent still exists — it captures asset, chain, delivery address, and
an indicative rate — but none of those move with price.

The 155 bps spread (`IT_STABLE_SPREAD_BPS`) applies as on sell, floored against the user, and
the same plausibility guard applies: a Flutterwave rate outside a configurable band of the
independent FX feed returns `ErrStableRateImplausible` and prices nothing. The sandbox returned a
rate wrong by six orders of magnitude, and that guard is why it could not have reached a user.

---

## 5. Flow and state machine

```
user picks asset + chain + amount
  → intent (pending_payment), app shows their standing VA
  → user transfers NGN
  → charge.completed, matched to the intent      ← the naira is now ours
  → naira becomes spendable                      ← settlement gate, §4.1 below
  → ConvertBalance NGN → asset                   ← this IS the purchase
  → direct-transfers type:"crypto" to the user   ← now it is theirs
```

| Status | Meaning |
|---|---|
| `pending_payment` | intent created; the user is looking at their VA details |
| `payment_received` | `charge.completed` matched; naira in our collection balance |
| `awaiting_funds` | naira not yet spendable |
| `converting` | `ConvertBalance` accepted, not yet SUCCESSFUL |
| `delivering` | crypto transfer accepted, not yet SUCCESSFUL |
| `settled` | the user has the stablecoin |
| `failed` / `reversed` | terminal, with the reason recorded |

### 5.1 `awaiting_funds` and the settlement question

Flutterwave separates **Collection Balance** (money received) from **Payout Balance** /
`available_balance` (money usable for transfers). `GET /wallets/balances` reflects only the
latter, and `ConvertBalance` draws on it. Primary sources say bank-transfer collections follow
the standard settlement pipeline (NGN default T+1); whether a virtual-account credit is
nonetheless immediately spendable is **unresolved** and will be answered by observation.

`awaiting_funds` exists so the answer does not gate the build:

- if VA credits arrive spendable, the state passes through in seconds and is invisible;
- if they await settlement, the intent holds correctly and the user is told it is in progress.

Identical code either way. The state is the design's answer to an open empirical question, not a
placeholder for one.

**This is the state to watch during the canary.** How long an intent sits in `awaiting_funds` is
the measurement that settles the question, and it determines whether buy is a minutes-long
product or a next-day one.

### 5.2 The invariant, carried from sell

> Never advance money before the funds are provably present.

`converting` is entered only after the naira is spendable. `delivering` is entered only after the
conversion reaches SUCCESSFUL — never when it is merely accepted. Flutterwave returns
`201 {"status":"NEW"}` and settles later; treating acceptance as completion is exactly the defect
the sell leg's Ruling 5 fixed, and it would here mean sending crypto against stablecoin that does
not yet exist.

---

## 6. Failure handling

Every failure is **stuck, not lost**. This is the property that makes a production canary a
reasonable way to carry the remaining unknowns.

| Failure | Where the money is | Behaviour |
|---|---|---|
| Naira never becomes spendable | our collection balance | holds in `awaiting_funds`; surfaced to operators |
| Conversion fails | our collection balance | back to `awaiting_funds`, bounded retries, then parked |
| Conversion never settles | our balance, mid-conversion | holds in `converting`; recoverable via the dashboard |
| Delivery rejected by Flutterwave | our stablecoin balance | parked for an operator; the user has not been sent anything |
| Delivery accepted, never settles | in flight to the user | holds in `delivering`; the FW transfer id is recorded |
| Underpayment | our collection balance | price what actually landed (`received / quoted`, as sell) |
| Underpayment below the provider floor | our collection balance | park rather than attempt a conversion Flutterwave will refuse |
| Overpayment | our collection balance | deliver on the full amount received |

A stuck-intent list mirrors `ListStuckStableSells`, covering `awaiting_funds`, `converting` and
`delivering`, so a stall is visible rather than silent.

### 6.1 Minimums

Flutterwave documents a **10 USDC/USDT outbound floor**. Buy's delivery leg is outbound, so this
is the binding constraint — and unlike sell it is checkable *before* any money moves, because the
user has not yet paid when the intent is created.

The quote refuses below the floor. If a user nonetheless underpays past it, the intent parks
rather than attempting a delivery Flutterwave will reject. The enforcement pattern follows the
sell leg's `IT_STABLE_MIN_SELL`: config-driven, defaulting to the documented figure, and
impossible to disable by leaving config unset or malformed.

---

## 7. API surface

| Route | Purpose |
|---|---|
| `POST /api/it/stable/buy/quote` | indicative price for an amount + asset |
| `POST /api/it/stable/buy` | create the intent → `{intent, payInto}` |
| `GET /api/it/intents/:id` | poll (existing route, extended) |
| `GET /api/it/admin/stable/stuck` | extended to cover buy states |

`payInto` carries the user's standing VA — account number, bank, account name. If the user has no
VA yet, the create call returns a clear error directing them to `POST /api/it/va/activate`, which
already handles BVN/NIN issuance.

All IT write routes keep the existing `itIdem` idempotency middleware.

---

## 8. Testing

Following the discipline established by the sell leg, where stubbed tests concealed a ~100×
fee error, a missing FX rate that would have failed every non-USD trade, and a retry path with
no exit:

- **Money-path tests wire the real fee engine.** A stub that agrees with the implementation only
  proves the implementation agrees with itself.
- **Delivery matrix tests enumerate every (chain, asset) pair**, so Base+USDT stays rejected and
  a future matrix change cannot silently widen it.
- **A status test enumerates every buy status**, so an unmapped state fails a test rather than
  rendering a blank progress screen.
- **Attribution tests** cover the routing rule: open intent funds it; no open intent falls
  through to MCGP; a second concurrent intent is rejected.
- **PostgreSQL-backed tests for the intent insert.** SQLite's type affinity hid a numeric-column
  failure on the sell leg; the same shape exists here.

---

## 9. Deferred: the bridge leg (Phase B2)

Chains Flutterwave will not deliver to can be covered by receiving at a **platform-owned Polygon
address** and bridging onward with LiFi. Deferred deliberately, because it is the largest new
risk surface in the ramp and is separable from a working buy:

- a funded Polygon hot wallet holding user funds in transit;
- MATIC gas funding, monitoring, and top-up — if gas runs dry, purchases strand mid-flight;
- **server-side LiFi execution** (approve → swap → poll), which the backend has never done: LiFi
  runs client-side today, signed by the user;
- key management for an address holding real user money.

Phase B1 preserves the property that **the backend never holds keys for user funds**. B2 gives
that up, and should be designed with that stated rather than inherited.

---

## 10. Known-open risks

| Risk | State |
|---|---|
| **Whether VA credits are immediately spendable** | Unresolved. Absorbed by `awaiting_funds`; measured during the canary. Determines whether buy is minutes or next-day. |
| **`type:"crypto"` delivery has never been exercised** | The request shape exists in `DirectTransfer` but no transfer has been made on any chain. A gate before launch, per chain. |
| **Conversion has never reached SUCCESSFUL** | Inherited from Phase A. The sandbox does not settle wallet conversions at all; only a production canary closes it. |
| **`ProviderFallbackBps` uncalibrated** | `/fees` is 501 in sandbox; the only real datapoint is 1% flat at one amount tier. |
| **Solana delivery depends on wallet coverage** | Users without a row in `wallet_addresses` for `solana` are simply not offered Solana. Needs confirming against real wallet data — Solana addresses are written only by `RegisterNonEVMAddress`, so coverage may be thin. |
| **EVM delivery depends on `users.wallet_address`** | A user who never completed wallet registration has no EVM address and can be offered no chain at all. The buy endpoint must fail clearly rather than opaquely. |
| **Solana delivery address is user-registered and unverifiable** | Unlike the EVM chains, the Solana address is not server-held — it is whatever the user submitted to `POST /api/wallet/nonevm/register`, checked for format only. There is no server-side derivation to verify it against, so a user socially engineered into registering an attacker's address will have their purchased stablecoin delivered there, and nothing in the buy flow can detect it. Two mitigations exist short of that: registration is scoped to the registering user's own row (nobody can plant an address in someone else's), and the delivery allow-list keeps a `wallet_addresses` row from affecting the EVM chains (§2, Finding 2). The option not taken: dropping Solana from buy entirely, which would remove the exposure at the cost of a chain. |
