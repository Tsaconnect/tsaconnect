# Flutterwave v4 sandbox — stablecoin ramp gate findings

**Probe date:** 2026-08-27
**Status:** Gate 1 PARTIAL — unprovable in sandbox; carried to a production canary
**Environment:** `https://developersandbox-api.flutterwave.com` (confirmed: `FLUTTERWAVE_BASE_URL`
unset in `tsa-api-go/.env`, and `config.go` defaults to sandbox — probes could not have hit
production)

> **Reconstruction note (2026-08-30).** The original of this file was deleted before it was ever
> committed and is unrecoverable from git. This is a faithful reconstruction from the surviving
> SDD ledger (`.superpowers/sdd/2026-08-27-stablecoin-fiat-ramp-phase-a/progress.md`), which
> quotes the probe results directly. Every figure, transfer id and status below is from that
> record. What is *not* reconstructable: the full raw request/response bodies, which the original
> captured verbatim. Section numbering may not match the original.

---

## Why these gates existed

The spec named four gates. Gate 1 was **blocking**: if Flutterwave cannot convert a stablecoin
balance to fiat through the API, the sell direction has no path and the entire Phase A plan is
void. Tasks 1–14 were not to start until Gate 1 resolved.

Gate 1 did not require a funded balance to answer. A zero-balance conversion attempt still
distinguishes the two outcomes that matter:

- `"We do not currently support this conversion"` → **FAIL**, the pair is unsupported, plan is void
- an insufficient-balance error → **PASS**, the direction is supported and the plan proceeds

Funding was a dashboard-only action unavailable to the implementer, so this two-outcome test was
substituted deliberately (ledger Ruling 4).

---

## Gate 1 (blocking) — stablecoin → fiat conversion: **PASS, with a hard caveat**

`type:"wallet"` conversions were accepted for **both** USDC→NGN and USDT→NGN:

```
HTTP 201  {"status": "NEW"}
transfer id: trf_BLlychqFAfOTeb   (USDC→NGN probe)
fee: NGN 10.0 on a NGN 1000 destination
```

Neither pair was rejected as unsupported. The strongest positive signal was not the 201 — it was
that **Flutterwave computed a real fee**. Pricing a conversion means the orchestrator understands
the pair.

### What this proved, and what it did not

- **PROVEN:** the USDC→NGN and USDT→NGN pairs are supported, accepted, and priced by the v4
  orchestrator.
- **NOT PROVEN:** that a funded stablecoin→fiat conversion settles end to end.

Both transfers sat at `PENDING` and were never observed reaching terminal `SUCCESSFUL`.

This is enough to justify **building** Phase A — the fatal outcome is excluded — but it is not
enough to **ship** it. Recorded explicitly because a bare "PASS" in a summary table reads as
stronger evidence than this is.

### The re-probe that changed the gate's nature

The user reported that sandbox funding was not possible on this account. Before accepting that as
a dead end, `trf_BLlychqFAfOTeb` was re-checked after **~7 hours**:

```
status: PENDING     fee: {NGN 10.0}     debit_information: null
```

Still PENDING. Not FAILED, not SUCCESSFUL — and that distinction is the finding. Had Flutterwave
validated the (empty) balance, it would have gone FAILED within seconds. Instead it accepted the
request, priced it, and never settled.

**Conclusion: the sandbox does not process wallet conversions to a terminal state at all. Funding
would not have closed this gate either.** The blocker is the environment, not the balance.

Gate 1 therefore converts from "a funded sandbox conversion reaching SUCCESSFUL" into a
**production canary** — see §Release gate below.

---

## Gate 2 — `POST /transfers/rates`: **PARTIAL / UNRELIABLE**

For USDC→NGN, a `destination.amount` of 1,000 NGN returned:

```
source.amount = 1,406,790.278235 USDC
```

Real-world that should be roughly **0.65 USDC**. The `rate` field agrees with the returned amounts
internally, but neither agrees with reality — wrong by about six orders of magnitude.

This is very likely sandbox noise (the tenant's fake balances are similarly arbitrary). It is still
disqualifying as a basis for trust: the sell price was to be derived entirely from this call, and
pricing cannot be shipped on an endpoint proven to return nonsense in the only environment testable.

**Consequence (ledger Ruling 6):** Flutterwave stays the pricing source — that was an explicit
product decision and was not silently overridden — but the quote path **fails closed on an
implausible rate**. The FW-derived fiat-per-unit figure is checked against the independent NGN/USD
feed already held by `ITService` (`s.fx`, behind `EffectiveKoboRates`); since a stablecoin is ~1 USD
by construction, a result outside a configurable band (default ±25%) returns
`ErrStableRateImplausible` and quotes nothing.

This mirrors existing codebase discipline — `ErrITFxUnavailable` and the `fx.Stale` guard in
`CreateBuyIntent` both refuse rather than transact on a bad rate.

---

## Gate 3 — `GET /fees`: **501 in sandbox**

Returns 501 for every `payment_method`, including the exact shape this codebase already uses for
its live NGN payouts. Non-blocking — the fee engine carries `ProviderFallbackBps` — but it means
**the fee model cannot be sized from sandbox at all**.

One incidental real datapoint, from the Gate 1 probes: both conversions priced at exactly
**10.0 NGN on a 1,000 NGN destination** — a flat 1% of destination, with no separate VAT line.

> **n=2, at a single amount tier. Do not extrapolate across bands.** Calibrating
> `ProviderFallbackBps` needs real settled transfers.

---

## Gate 4 — inbound crediting visibility: **UNKNOWN**

`GET /wallets/balances` returned **26 fiat currencies and no USDC/USDT line at all**.

The payout gate reads that list for the asset's line. With no line, `available` stays 0 and every
sell would stall in `deposit_confirmed` indefinitely.

This fails in the **safe** direction — no payout without proven funds — so it is not a correctness
bug. But it is operationally opaque: a stall caused by "Flutterwave isn't surfacing our stablecoin
balance" is indistinguishable from "the user's deposit hasn't landed yet".

**Consequence (ledger Ruling 7):** the two are distinguished in logs. An absent asset line emits a
distinct `it_stable_balance_line_missing` warning naming the asset, versus a normal
not-yet-credited wait. The payout gate itself is unchanged and stays fail-closed.

Also found: `GET /wallets/statement` 404s with `TRANSFER_NOT_FOUND` even for NGN, so that path is
wrong for v4. Harmless here — the design uses `FetchBalances`, not statement — but it means the
v4 statement path is still unknown.

---

## The defect these probes exposed in the plan

Gate 1's `201 {"status":"NEW"}` proved conversion is **asynchronous**, and the plan had assumed
otherwise. As written, `ConvertAndQueuePayout` set the intent to `debited` the moment
`ConvertBalance` *returned* — and `debited` is what fires the fiat payout. Since the conversion had
only been *accepted* at that point, the fiat did not yet exist: the payout would fire against money
that had not arrived.

That is exactly the "never advance money before the funds are provably present" invariant the whole
design rests on, violated one hop earlier than the balance check protects.

**Fix (ledger Ruling 5), now shipped:**

- persist the conversion's FW transfer id on the intent (`conversion_id`)
- `ConvertAndQueuePayout` stops at `converting` and no longer sets `debited`
- `FinalizeConversion` polls `GET /transfers/{id}`: `SUCCESSFUL` ⇒ `debited`; `FAILED` ⇒ back to
  `deposit_confirmed` with the reason recorded, so the next tick retries
- the watcher gained a third bucket for `converting` intents

Cost had the conversion actually been synchronous: one poll cycle of latency and one column. A
trivial price against firing unfunded payouts.

---

## Release gate — the production canary

Because the sandbox cannot reach a terminal conversion state, Gate 1 closes only in production:

> With the ramp configured in production, fund a small **company** USDC balance and drive **one
> staff-account sell end to end**, observing a conversion reach `SUCCESSFUL`, before any real user
> is admitted.

### Why carrying this risk is acceptable — the failure mode is stuck, not lost

- the user's USDC is provably at Flutterwave before anything advances;
- if the conversion never settles the intent holds at `converting` and **no payout fires** —
  Ruling 5 guarantees this, since `debited` is the only trigger and only a `SUCCESSFUL` conversion
  reaches it;
- `converting` is included in `ListStuckStableSells`, so the stall is visible to operators;
- recovery is a manual dashboard conversion plus a manual payout, exactly as the design's recovery
  table anticipates.

### What the canary forced earlier in the schedule

Finding **I6 (the retry cliff)** was promoted from "before launch" to **must-fix before the
canary**, and was fixed in `f0e7b9a`. If `ConvertBalance` errors repeatedly rather than hanging,
`ConversionAttempts` climbs, and at 10 the generated reference exceeds Flutterwave's 42-character
limit — wedging the intent permanently with no terminal state and no alert. That is the one path
that could fail invisibly, and the canary is precisely when it would first be exercised.

---

## Open items carried out of these probes

| # | Item | Owner |
|---|---|---|
| 1 | Confirm `FLUTTERWAVE_BASE_URL` is set in `/srv/tsa-api/shared/env` — unset means **all** Flutterwave traffic, including existing NGN payouts, silently uses sandbox | user |
| 2 | Decide the front-running risk before enabling the ramp (inert while dormant) | user |
| 3 | Production canary: one staff sell end to end, observing `SUCCESSFUL` — the Gate 1 sandbox cannot close | user |
| 4 | Calibrate `ProviderFallbackBps` against real settled transfers (observed 1%, n=2) | user |
| 5 | Obtain the correct v4 wallet-statement path from Flutterwave support | user |
| 6 | Confirm whether a USDC/USDT balance line appears once a stablecoin balance is provisioned | user |
