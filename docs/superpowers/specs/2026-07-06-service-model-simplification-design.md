# Service Model Simplification — Design

**Date:** 2026-07-06
**Status:** Approved (design), pending implementation plan
**Supersedes:** the deferred Phases 3–4 of the unified booking engine (quotes, deposits, dynamic pricing)

## Context

The PM's service spec defined four parallel configuration axes — Booking Type,
Pricing Method, Workflow, Payment Timing — plus deposits and merchant-configured
dynamic-pricing formulas. That spec was written as if for a traditional-payments
marketplace. TSA Connect is **escrow-based**, and escrow collapses most of those
dimensions:

- **Deposits are redundant.** A deposit protects a provider from no-shows when
  they don't yet hold the money. Here the buyer's full payment is locked in
  escrow — the provider already has payment certainty and the buyer already has
  refund certainty. Full-payment-into-escrow *is* the deposit.
- **Dynamic-pricing formulas are config nobody uses.** Real providers won't set
  up `base + count×perUnit + surcharge` rules. The flexible-and-simple version is
  a **human quote**: the provider sees the request and names a price.
- **"Quote Before Payment" is not a separate flow.** It's just approval with a
  price attached. The Request & Approval flow (already shipped) subsumes it.

We already shipped (merged to `main`): contact-fee drop, Phase 1 (configurable
instant booking), Phase 2 (request & approval). This design **simplifies the
model** — trimming the excess config that was added forward-compat for Phases 3–4
— and adds the **one** genuinely useful missing piece: **provider names the price
when approving a request**. The feature is not yet live and has no production
data, so trimming now is safe.

## The model — two dimensions, three valid combinations

A service listing has exactly two pricing/engagement choices:

|                    | **Fixed price**                                   | **Price on request**                                     |
| ------------------ | ------------------------------------------------- | -------------------------------------------------------- |
| **Buy now**        | Pay the listing price into escrow immediately.    | *(invalid — cannot prepay a price nobody has named)*     |
| **Request first**  | Provider approves → buyer pays the listing price. | Provider approves **and names the price** → buyer pays it. |

Alongside these sits the existing **Unlock contact** service type (pay to reveal
provider contact and engage directly) — unchanged.

The `instant + on_request` cell is invalid and is hidden in the merchant UI.

## Enum changes (trim the excess)

`ServiceProfile` config is reduced to two meaningful fields:

| Field           | Before (shipped)                                            | After                    |
| --------------- | ---------------------------------------------------------- | ------------------------ |
| `BookingType`   | `instant \| request_approval \| quote_before_payment`      | `instant \| request`     |
| `PricingMethod` | `fixed \| starting_from \| dynamic \| quote_later`         | `fixed \| on_request`    |
| `Workflow`      | `auto_accept \| manual_accept \| quote_required`           | **removed** (implied by BookingType) |
| `PaymentTiming` | `pay_before \| pay_after \| deposit`                       | **removed** (always escrow / pay-before) |
| `DepositPercent`| int 0–100                                                  | **removed**              |
| `BookingFeeUSD` | float                                                      | kept (merchant "booking fee")            |
| `FieldConfig`   | JSONB                                                      | kept (auto-generated form)               |

`BookingRequest.QuotedAmountUSD` and `QuoteExpiresAt` already exist. We keep
`QuotedAmountUSD` (now actively used); `QuoteExpiresAt` is dropped (no expiry —
an approval-with-price is just an approval, it doesn't time out).

Removed struct fields leave orphan columns in any existing dev DB — harmless
(GORM AutoMigrate never drops columns) and there is no production data.

Validation rule (in `UpsertServiceProfile`): reject `instant + on_request` as an
invalid combination, with a clear message.

## What gets built

The only new behavior is **"provider names the price at approval."** It reuses
the existing Phase-2 endpoints:

1. **`ApproveBookingRequest`** accepts an optional `amountUsd`.
   - Listing is `fixed`: `amountUsd` ignored; price resolved as today.
   - Listing is `on_request`: `amountUsd` **required** (> 0); stored in
     `BookingRequest.QuotedAmountUSD`. Reject approval without it.
2. **`PayBookingRequest`** / `placeServiceOrder`: when `QuotedAmountUSD` is set,
   use it as the service price (bypass the tier/answer computation); otherwise
   compute as today. The atomic status-claim, escrow amount conversion, and
   escrow flow are all unchanged.
3. **Merchant UI (`AddService` / `BookingConfigBuilder`):**
   - Pricing toggle: **Fixed price ($__)** vs **Price on request**.
   - Engagement toggle: **Buy now** vs **Request first** (Buy-now disabled/hidden
     when Pricing = On request).
4. **Buyer UI:**
   - `ServicePricingSheet`: on-request booking submits a request (no price shown
     up front); the submit label reflects "Request booking."
   - `booking-requests` screen: an approved on-request row shows the **quoted
     price** before the Pay button.
5. **Merchant approval UI (`booking-requests`, seller view):** when the listing
   is on-request, the Approve action prompts for a price (a small amount input)
   before sending.

## Components touched

Backend (`tsa-api-go`):
- `internal/models/service_profile.go` — trim enums + validators.
- `internal/models/booking_request.go` — drop `QuoteExpiresAt`; keep `QuotedAmountUSD`.
- `internal/handlers/service_profile_handler.go` — validate the reduced enums + invalid-combo rule.
- `internal/handlers/booking_request_handler.go` — `amountUsd` on approve; quoted-price path.
- `internal/handlers/checkout_handler.go` — `placeServiceOrder` accepts an optional price override.
- `internal/handlers/booking_form_handler.go` — schema reflects reduced enums.

App (`tsa-app`):
- `services/serviceListingApi.ts` — trim the exported enum unions.
- `services/bookingRequestApi.ts` — `approveBookingRequest(reqId, amountUsd?)`.
- `components/services/AddService.tsx` + `BookingConfigBuilder.tsx` — pricing/engagement toggles.
- `components/services/ServicePricingSheet.tsx` — on-request submit path.
- `app/(dashboard)/booking-requests.tsx` — show quoted price (buyer) + price prompt on approve (seller).

## Error handling

- Approve without a price on an on-request listing → `400`, clear message.
- `instant + on_request` config on save → `400`.
- Quoted price ≤ 0 → `400` (same as the existing zero-amount escrow guard).
- Paying a request whose price is unresolved (no quote, no computable price) →
  the existing "resolves to a zero on-chain amount" `400`.

## Testing

- Model: reduced enum validators; invalid-combo rejection.
- Handler: approve-with-price stores `QuotedAmountUSD`; approve-without-price on
  an on-request listing is rejected; pay uses the quoted price (assert the
  escrowed `ProductAmount` equals the quote, not the computed tier); fixed-price
  request flow unchanged (existing lifecycle test still green).
- App: `tsc` + `eslint` clean; drive the on-request flow end-to-end (merchant
  sets on-request → buyer requests → merchant approves with price → buyer sees
  price → pays → escrow).

## Explicitly dropped (YAGNI)

Deposits, partial payment + balance collection, dynamic-pricing formula engine,
`starting_from` pricing, quote expiry/re-quote state machine, and the separate
Workflow/PaymentTiming axes. If a real need for any of these emerges later, each
is additive on top of this model.

## Out of scope (unchanged from today)

- Slotted bookings (`slotDurationMinutes > 0`) remain **instant-only** — the
  request/quote flow does not reserve a specific time slot. This constraint is
  already enforced (`UpsertServiceProfile` rejects request-mode + slot duration).
- Native date/time picker for the auto-generated form (still a formatted text
  input) — a separate follow-up.
- Cancellation/refund policy enforcement — still display-only text.
