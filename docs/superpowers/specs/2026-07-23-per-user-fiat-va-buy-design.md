# Per-User Fiat Virtual Account — Bank-Transfer Buy → MCGP

**Date:** 2026-07-23
**Status:** Implementing (spike skipped — test against live Flutterwave after ship)
**Scope:** Spec 1 of 3 toward the PM's global on-ramp vision (see "Roadmap context")

## Problem

Buying MCGP with fiat today provisions a **per-intent dynamic** Flutterwave virtual
account (`ProvisionBuyVA` → `CreateDynamicVA`): amount-locked and 1-hour-expiring.
Two live failure modes result:

- **"Invalid Amount"** — the dynamic VA rejects any transfer that isn't the exact
  locked naira figure; the user's own bank surfaces this as a hard failure.
- **Silent expiry** — the VA dies after 3600s and the buy screen never shows a
  countdown, so a user who waits then transfers into a dead account.

Attribution also relies on the user manually copying a payment reference into their
transfer narration — fragile, and the reference the buy screen shows may not be the
one Flutterwave echoes on the webhook (see "Attribution" below).

## Solution

Give each user a **permanent, dedicated static virtual account** keyed by
`(user, currency)`. Static VAs have `amount: 0` (any amount accepted) and no
expiry — both failure modes disappear structurally. The user's own BVN/NIN gates
issuance, matching Flutterwave's documented static-VA flow. Payment attribution
moves to the Flutterwave **customer id** (one customer per user), with the VA
reference as fallback — no user-typed reference.

Rate is determined **when the money lands**, not when the user taps Buy: the
`charge.completed` webhook resolves the user from the VA, prices at the live rate,
creates a credited `ITIntent`, and credits MCGP on-chain.

### Verified against Flutterwave PWBT docs (2026-07-23)

- Static VA: `amount` must be `0`, expiry ~1000 years, "assigned to a single
  customer for multiple transactions" → **one customer per user is required**, not
  a workaround.
- NGN static VA **requires** the account holder's BVN or NIN, used to create the
  customer object; `customer_id` then creates the VA.
- `charge.completed` webhook carries `data.customer.id` and `data.reference`. In
  the docs' own example the VA-create *response* reference (`deac44b2…`) — not the
  *request* reference (`1ca9e18f…`) — is what the webhook echoes. **Persist the
  reference from the create response.**
- Bank-transfer/VA collection is **NGN + GHS only**. Other currencies require
  mobile money or card (a different rail — deferred to Spec 2).
- `GET /charges` returns a `fees` array (`vat`, `stamp_duty`, `merchant`, `app`)
  separate from `amount`. We credit MCGP on the gross transferred `amount`; FW
  fees come out of TSA margin. **Needs business sign-off — not a code branch.**

### Not verifiable from docs — validate on live after ship

Live mode accepting many customers each with a distinct real BVN under one merchant
account. Sandbox does not enforce identity. Implementation ships against the documented
pattern; identity failures surface on `POST /api/it/va/activate` for retry.

## Architecture

### 1. Provisioning

New model `FiatVirtualAccount`:

```
id
user_id            uuid, index
currency           varchar(8)   -- "NGN"
provider           varchar(16)  -- "flutterwave"
fw_customer_id     varchar(64)  -- cus_…  (per-user)
fw_va_id           varchar(64)  -- van_…
reference          varchar(128) -- from the CREATE RESPONSE, not what we sent
account_number     varchar(20)
bank_name          varchar(64)
account_name       varchar(128) -- Flutterwave's real value (fixes name mismatch)
status             varchar(16)  -- active | inactive
created_at, updated_at
unique(user_id, currency)
```

One row per user per currency. GHS later = a row, not a schema change.

Each user gets **their own** Flutterwave customer (not the shared company customer
that `ResolveFlutterwaveCompanyCustomer` mints). Required by the static-VA model and
makes the user's own BVN meaningful.

Reference format: `ITVA-<user-id-hex>` on the create request; but attribution keys
on the persisted **response** reference and on `fw_customer_id`.

### 2. Identity gate — one-time at first buy

`POST /api/it/va/activate {idType, idNumber}`:
- Validate 11 digits (`idType` ∈ {BVN, NIN}) — lift the existing PM validator
  (`pm_handler.go:378-409`) into a shared helper rather than copy it.
- Require the user has a wallet address (buy target).
- Create FW customer → create static VA → persist row → return account details.
- BVN/NIN forwarded to Flutterwave, **never stored, never logged** (as PM does).
- On FW 400, surface `validation_errors[].message` to the user; nothing persisted;
  retryable.

### 3. Settlement — webhook creates the intent

`DispatchFlutterwave` already branches on `IT-` reference prefix
(`flutterwave_dispatch.go:153`). Add an `ITVA-` branch → new `AutoCreditFromVA`:

```
resolve VA by data.customer.id (primary) or reference (fallback) → user
verify charge against the FW API           (existing fail-closed path, unchanged)
price at the live buy rate                 (EffectiveKoboRates / RatePerUsd)
create ITIntent{
  direction: buy, minor_units, currency, rate_minor,
  status: credited,
  payment_ref: <fw charge id>              -- unique attribution + idempotency
}
OperatorCreditPremium(wallet, mcgp, "IT:va:"+chargeID)
```

Idempotency: unique index on `payment_ref` (webhook redelivery) + on-chain
`usedRef` backstop. `POST /api/it/buy` stops creating intents — becomes a quote
endpoint returning the indicative live rate + the user's standing account details.

### 4. Currency-generic schema (kobo retired)

On `ITIntent`, replace `NgnKobo` / `RateKobo` with `minor_units` + `currency` +
`rate_minor`. Migration backfills existing rows as `currency='NGN'` losslessly.
`usdMicrosToKobo` → `usdMicrosToMinor(currency)`. FX interface gains
`RatePerUsd(ctx, currency)`; NGN is the only implementation shipped.

### 5. Buy screen (matches PM sketch)

Full-page flow: select payment currency (NGN enabled; others greyed "coming soon")
→ choose method (Bank transfer enabled; Mobile money greyed) → enter amount →
Continue.

- VA exists → show standing account details immediately.
- No VA → one-time BVN/NIN prompt → issue → show details.

Details screen shows the real `account_name`, no expiry, no reference to type.
Balance updates when the webhook confirms payment.

## Error handling

| Case | Behaviour |
|---|---|
| Stale FX at settlement | Money already landed — cannot reject. Intent `status=awaiting_rate`, park for operator, alarm. Never credit at a stale rate. |
| No wallet at settlement | Gate requires a wallet before issue, so unreachable; if reached, park + alarm. |
| VA create 400 (live identity) | Surface FW `validation_errors` to the user on activate; nothing persisted; retryable. |
| Webhook redelivery | Unique index on `payment_ref` + on-chain `usedRef`; idempotent both layers. |
| FW fees | MCGP credited on gross `amount`; fees hit TSA margin. Business sign-off, not code. |

## Deprecation

Delete `ProvisionBuyVA` / `CreateDynamicVA` on the creation side. Keep the `IT-`
webhook branch for **one release** so dynamic-VA intents in flight at deploy still
settle; remove next release.

## Testing

- **VA provisioning:** new user (mint customer → VA → persist *response* ref),
  returning user (reuse row, no API call), bad BVN (400 surfaced, nothing persisted).
- **Webhook attribution:** resolve by `customer_id` primary, reference fallback;
  redelivery credits once; unknown ref parks for operator.
- **Rate:** credit at live rate on arrival; stale FX → `awaiting_rate`, no credit.
- **Migration:** existing NGN kobo rows backfill to `minor_units`/`currency`
  losslessly.
- **Currency-generic seam:** a GHS intent prices through `RatePerUsd` with a stub
  feed — proves the seam without shipping GHS.

## Roadmap context

The PM's hand-drawn design targets a global on-ramp (buy in any currency via bank
transfer **or** mobile money, delivering MCGP **or** stablecoins) and a global
off-ramp (sell — already shipped multi-currency in commit `c9347a0`). Decomposed:

- **Spec 1 (this doc):** per-user VA, bank-transfer buy → MCGP. NGN live, GHS-ready.
- **Spec 2:** mobile-money buy rail (charge-initiation API, not VAs; unlocks
  non-NGN/GHS currencies).
- **Spec 3:** stablecoin delivery (second settlement leg beside the MCGP credit).

Each is its own brainstorm → plan → build.

## Open items after ship

1. **Live BVN/NIN validation** — confirm activate succeeds with a real identity on live FW.
2. **Fee-on-margin sign-off** — confirm TSA absorbs Flutterwave's per-transaction
   collection fees.
3. **Currency-generic ITIntent columns** (`minor_units` / `currency` / `rate_minor`) —
   deferred; NGN kobo fields remain for Spec 1.
