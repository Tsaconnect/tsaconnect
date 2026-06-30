# Service Pricing & Checkout — Design Spec

**Date:** 2026-06-30
**Branch:** `feat/service-pricing-checkout`
**Status:** Draft (pending independent review)

## 1. Goal

Today a "service" listing on TSA Connect is **contact-only**: a buyer pays a one-time
on-chain **contact-reveal fee** (~$0.10) to unlock the provider's phone/email, then
negotiates and pays off-app. There is no way to pay for the service *through the platform*.

The PM wants services to support **in-app payment** for two distinct charging models:

- **Standard services** (haircut, phone-screen repair, car wash, website design, delivery):
  an **optional merchant-set fixed price** plus a **call-out fee** (mobilisation/transport),
  priced by area zone — **free in the same residential area**, merchant-fixed beyond. Buyer
  pays **service price + call-out fee together** at checkout.
- **Booking services** (flight, taxi, bike, Keke): a single **merged call-out + booking fee**,
  defined by the **admin per category** across six area tiers, charged as the **sole** amount.

Payment settles through the **existing product escrow** (funds held, released when the buyer
confirms completion, dispute fallback). The **"Pricing services — View/PayNow"** button on the
service detail screen is gated behind the existing one-time contact-fee payment.

## 2. Locked decisions (from brainstorming)

| # | Decision |
|---|----------|
| D1 | **Settlement = escrow**, reusing the product escrow path **exactly**. Funds held on checkout; released on **buyer-confirms-completion** (`confirmReceipt`); **dispute** fallback via the same provider/admin path products use; the existing on-chain **30-day `autoRefund`** is the only time-based backstop (buyer-protective, same as products — there is **no** backend auto-release). Call-out/booking fee is escrowed **alongside** the service price and released together. |
| D2 | **Scope = both** charging models (standard + booking) in this spec. |
| D3 | **Cart = shared**, with service-aware line items. One cart holds products and services; each line item knows its kind and renders/charges accordingly. |
| D4 | **Booking fees = admin-set per-category schedule**, the **sole** charge for that category. No merchant override, no separate merchant price for booking listings. |

### Derived defaults (not separately asked; stated here)

- **DV1 — Optional pricing, backward compatible.** A standard service with **no price and no
  call-out** stays exactly as today (contact-only; no View/PayNow checkout). The checkout path
  appears only when a merchant has set a price and/or call-out fee.
- **DV2 — Contact-fee gate.** A buyer can only add a service to cart / checkout **after** the
  one-time `ServiceContactPayment` for that (buyer, service) exists. Enforced both in the app UI
  and on the backend (defense in depth).
- **DV3 — No escrow-contract change.** Services ride the same on-chain `Escrow` contract as
  products. The money invariant and chain reconciler are unaffected.

## 3. Data model

### 3.1 Category (`tsa-api-go/internal/models/category.go`)

Add service-fee configuration. Meaningful only for `Service`/`Both` categories; ignored for
`Product`.

```go
const (
    ServiceFeeModeStandard = "standard" // merchant sets price + zone call-out (default)
    ServiceFeeModeBooking  = "booking"  // admin sets merged per-area fee; sole charge
)

// added fields on Category
ServiceFeeMode string `gorm:"default:'standard'" json:"serviceFeeMode"`
```

New related table for booking schedules (one row per (category, tier)):

```go
// tsa-api-go/internal/models/category_booking_fee.go
type CategoryBookingFee struct {
    ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    CategoryID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_cat_booking_tier" json:"categoryId"`
    Tier       string    `gorm:"size:24;not null;uniqueIndex:idx_cat_booking_tier" json:"tier"`
    AmountUSD  float64   `gorm:"not null;default:0" json:"amountUsd"`
    CreatedAt  time.Time `json:"createdAt"`
    UpdatedAt  time.Time `json:"updatedAt"`
}
```

Tiers (the six area bands the PM named):
`residential`, `same_city`, `same_lga`, `same_state`, `same_country`, `outside_country`.

A related table (not a JSON column) keeps admin edits atomic and the schema queryable; it also
lets us add a tier later without a column migration.

### 3.2 Service listing (the `Product` row with `type="Service"`)

- **Stop hard-coding `price=0`** on the app side. `Product.Price` becomes the **optional fixed
  service price** for standard services. `0` (or empty) means "no fixed price".
- **Reuse existing shipping columns as the standard call-out fee**:
  `ShippingSameCity/State/Country/International` hold the call-out amounts. "Same residential
  area" is the **implicit free tier** (always 0). No new columns; the service UI relabels these
  "Call-out fee". Booking-mode listings ignore these (their fee comes from the category schedule).

No new columns on `Product`. This is deliberate reuse — a service is already a `Product` row with
`type="Service"`, and the escrow path already reads `Price` + shipping fields.

### 3.3 Order (`tsa-api-go/internal/models/order.go`)

```go
const (
    OrderKindProduct = "product" // default
    OrderKindService = "service"
)

// added field
Kind string `gorm:"default:'product';index" json:"kind"`
```

- `Kind` lets escrow/UI/order-listing branch without re-joining the product to read `type`.
- **Reuse `ShippingZone`** to store the resolved **area tier** (`residential`, `same_city`, …).
- **Escrow amount mapping (critical — the on-chain `createOrder` requires `productAmount > 0`,
  ProductEscrowV2.sol:258):**
  - **Standard, price > 0:** `ProductAmount` = service price (platform fee applies here),
    `ShippingAmount` = call-out fee (pass-through). `productAmount > 0` ✓.
  - **Standard, price = 0 but call-out > 0:** `ProductAmount` = call-out fee, `ShippingAmount` = 0
    (so the require passes). The 0.5% `service_other` fee on a pass-through call-out is negligible
    and accepted for Phase 1.
  - **Booking:** `ProductAmount` = resolved booking fare, `ShippingAmount` = 0.
  - **Zero total (e.g. free residential booking tier, or a contact-only listing):** **no escrow
    order is created** — see §6. The checkout path is only reached when the resolved charge > 0.
- `BuyerConfirmedAt` already records completion. `EscrowExpiresAt` is informational only — **no
  backend job reads it** (see §6); settlement is buyer-confirm or on-chain `autoRefund`, same as
  products.

### 3.4 Migration

All additions are GORM `AutoMigrate`-safe (new nullable/defaulted columns + a new table).
`CategoryBookingFee` is added to the `AutoMigrate` list in `config/database.go`. No destructive
migration. Existing categories default to `serviceFeeMode='standard'`; existing orders default to
`kind='product'`.

## 4. Fee & zone model

### 4.1 Standard call-out zones

Reuse the existing `DetectShippingZone(buyer*, seller*)` which yields
`same_city | same_state | same_country | international` from country/state/city. For services we
add a **finer free tier**:

- **`residential`** — same city **and** buyer attests the provider is in their immediate/
  residential area. We do not capture neighbourhood geodata, so `residential` is a
  **buyer-declared refinement** of `same_city` at checkout (a "Provider is in my area — call-out
  free" affordance). Always **free** (call-out = 0).
- Beyond residential, the merchant's per-zone amount applies
  (`GetShippingRate` → `GetCalloutRate`, same columns).

`GetCalloutRate(service, zone)` returns 0 for `residential`, else the matching `Shipping*` column.

### 4.2 Booking tiers (merged call-out + booking fee)

For `serviceFeeMode='booking'` categories, the **category schedule** is the sole charge. Six tiers
(`residential` … `outside_country`). Detection:

- Coarse geographic tier auto-detected (city/state/country/international →
  `same_city/same_state/same_country/outside_country`).
- The two finer tiers the geo-detector can't resolve — **`residential`** and **`same_lga`** — are
  **buyer-declared** at checkout (defaulting to the auto-detected coarse tier). A provider who
  believes the buyer under-declared can **raise a dispute** (existing path) before release.

The booking fee = `CategoryBookingFee.AmountUSD` for the resolved tier, placed in `ProductAmount`
(§3.3). Service price is **not** charged for booking listings (D4). A resolved fare of 0 (e.g. a
free residential tier) means **no payment / no escrow** — the booking is recorded/confirmed
without an on-chain order.

> **Phase-1 honesty:** residential/LGA precision depends on buyer attestation because the app's
> location model is country/state/city only. Capturing neighbourhood/LGA geodata is **out of
> scope** (§9) and tracked as a follow-up. The dispute path is the integrity backstop.

### 4.3 Platform fee

The `service_other` fee_config feature (0.5%) applies to the escrowed `ProductAmount` at checkout,
mirroring how `product_merchant` (2%) applies to products. Two code changes are required (the
current `CalculatePlatformFee` hardcodes `product_merchant`, and `service_other` is absent from the
fee-distribution split map):
1. Branch fee calculation on `Kind=service` to read `FeatureServiceOther`.
2. Add a `FeatureServiceOther` entry to `featureSplits` in `fee_distribution_service.go` so
   settlement doesn't fall back to the product split.
Call-out fee (when carried in `ShippingAmount`) is a pass-through to the merchant (no platform cut),
matching product shipping. Quote == charge: the cart-summary computation and the escrow charge read
the same fee-config rows for the same resolved tier.

## 5. Merchant listing flow (app — `components/services/AddService.tsx`)

The service add/edit form gains an optional **Pricing** section, shown based on the selected
category's `serviceFeeMode`:

- **Standard category:**
  - **Service price ($)** — optional; placeholder "Leave blank if quoted per job".
  - **Call-out fee** section (relabelled "Shipping Rates"): "Same residential area" shown as a
    fixed **Free** chip; `Same City / Same State / Same Country / International` editable, with the
    same base-fee auto-fill helper products use. Optional — blank = free everywhere.
- **Booking category:** no pricing inputs. A read-only note: "Fares for this category are set by
  the admin and shown to the buyer at checkout." (`price=0`, no call-out.)

`AddService` stops sending `price=0` unconditionally; it sends the entered price (or omits it) and
the call-out amounts. The category picker already filters `type=Service`; we additionally read the
chosen category's `serviceFeeMode` to toggle the section.

## 6. Buyer flow

1. **Service detail** (`app/(servicegroup)/servicedetail.tsx`) — unchanged up top: pay one-time
   contact fee to reveal the provider (existing `ServiceContactPayment`).
2. The **"Pricing services — View/PayNow"** button becomes **actionable only after** the contact
   fee is paid (DV2). Before payment it shows locked, consistent with the contact card.
3. Tapping it opens a **service pricing sheet/screen** showing: service price (if any), the
   call-out/booking fee for the buyer's detected tier (with the residential/LGA declaration
   control), and the total. "Add to cart" / "Pay now".
4. **Shared cart** (D3): the service becomes a service-aware line item — renders "Call-out fee"
   (not "Shipping"), carries its resolved tier + fee. Products and services coexist; checkout
   groups by seller as today.
5. **Checkout** reuses `CreateOrderFromCart`: for a service line item it sets `Kind=service`,
   `ProductAmount`=price, `ShippingAmount`=call-out/booking fee, `ShippingZone`=tier, and runs the
   same approve → escrow → submit flow.
6. **Completion (mirrors products, minus the shipping leg):**
   - After escrow, the **provider performs the service and taps "Mark service rendered"** →
     reuses `MarkDelivered` (sets on-chain `sellerDelivered=true`). This is the provider's
     protection: once rendered, the buyer can no longer instant-self-refund — a refund needs admin
     adjudication (exactly the product post-delivery rule, ProductEscrowV2.sol:322/328).
   - The buyer taps **"Confirm service completed"** → `PrepareConfirm`/`SubmitConfirm` →
     `confirmReceipt` releases escrow to the provider. Allowed from `escrowed` **or** rendered
     (`confirmReceipt` has no delivered precondition on-chain).
   - **No backend auto-release.** If the buyer never confirms, settlement matches products: the
     on-chain **30-day `autoRefund`** is the only time-based path (buyer-protective — 100% if not
     yet rendered, else 90/7/3). This is an **accepted, product-consistent** posture (§10), not a
     new risk. Either party may dispute before release.

### Service status lifecycle

Services skip the **shipped** step but keep **delivered** (= "rendered", for refund protection):

```
pending_payment → escrowed → delivered(rendered) → completed
                     ↘ (confirm directly) ──────────→ completed
                     ↘ cancel_requested → cancelled
                     ↘ refund_requested → (adminResolve) → completed | refunded
```

`isValidTransition`/`ValidNextStatuses` gain a **kind-aware** branch (kind threaded through):
for services, `escrowed → {delivered, completed, cancelled, cancel_requested, refunded}` and
`delivered → {completed, refund_requested}`. **`MarkShipped` is rejected** for `Kind=service`;
**`MarkDelivered` is kept** (relabelled "rendered"); **confirm is allowed from `escrowed`**.

## 7. Admin portal (app — `components/category/AddCategory.tsx` + `EditCategory.tsx`)

When the category type is `Service` or `Both`, show a **Service fee mode** picker:
`Standard (merchant sets price + call-out)` | `Booking (admin sets merged per-area fare)`.

When `Booking` is chosen, reveal a **six-row fare editor** (residential → outside country), each a
USD amount. Saved via a new admin endpoint:

- `PUT /api/admin/categories/:id/booking-fees` — upsert the six-tier schedule.
- `GET /api/products/category/:id/booking-fees` — public read for the buyer checkout.

Category create/update already require admin; the booking-fee endpoints reuse `admin_auth`.

## 8. Backend surface (handlers/routes)

- `category_handler.go` — persist `ServiceFeeMode`; CRUD for `CategoryBookingFee`.
- `checkout_handler.go` — `CreateOrderFromCart` service branch:
  - **Skip** stock check, decrement, and cancel/refund stock-restore for `Kind=service` (S1 —
    services have `stock=0` and aren't inventory).
  - **Contact-fee gate (in-transaction):** for a service line item, require an existing
    `ServiceContactPayment{caller_id: user.ID, service_id: product.ID}` inside the checkout
    transaction before creating the order (S6).
  - Compute fee: standard `GetCalloutRate(service, tier)` (new, residential = 0) or booking
    (`CategoryBookingFee` for the tier); place per §3.3 escrow mapping; set `Kind`/`ShippingZone`.
  - **Reject** service orders whose resolved escrowable amount is 0 (no free-escrow; free bookings
    handled as no-payment, §6).
  - `CalculatePlatformFee`: branch to `FeatureServiceOther` for services; `SubmitConfirm` fee
    settlement passes `FeatureServiceOther` for service orders (S3).
  - `isValidTransition` → kind-aware (`ValidNextStatusesForKind(current, kind)`); `MarkShipped`
    rejects services; `MarkDelivered` accepts (relabel copy); `SubmitConfirm` allows from
    `escrowed` (S5).
  - `createOrderRequest`: add a per-line-item **declared tier** field so the buyer's
    residential/LGA declaration reaches the server; `CreateOrderFromCart` honors it for service
    items instead of always re-deriving the coarse zone (S4).
  - Branch escrow/order **notification copy** on `Kind=service` ("Payment escrowed — perform the
    service" / "Service confirmed — funds released") instead of ship/deliver wording (N2).
- `fee_distribution_service.go` — add `FeatureServiceOther` to `featureSplits` (S3).
- `service_contact_payment.go` / migration — add a unique index on `(caller_id, service_id)` as
  defense-in-depth for the gate + contact-fee dedup (S6).
- `routes.go` — register the two booking-fee routes.

## 9. Out of scope (future specs / follow-ups)

- Neighbourhood/LGA **geodata capture** (replacing buyer-declared residential/LGA). Tracked as a
  follow-up; dispute path is the Phase-1 backstop.
- Per-merchant booking overrides (explicitly excluded by D4).
- Scheduling/appointment slots, provider availability calendars.
- Splitting call-out from service price for separate/early release (Phase-1 escrows both together).
- Platform cut on the call-out/booking fee (Phase-1 treats it as merchant pass-through).

## 10. Backward compatibility & invariants

- **Contact-only services keep working** — no price/call-out ⇒ no checkout surface (DV1).
- **Money invariant** — services use the same escrow contract and the same `productAmount > 0`
  rule; no new on-chain money path. (Note: there is no order-level reconciler enforcing the
  invariant today — `p2p_reconciler_drift_total` is P2P-only — so this is aspirational for orders,
  product and service alike.)
- **Quote == charge (service quote = the pricing sheet).** The authoritative per-service quote is
  the **ServicePricingSheet**, which mirrors the server's `resolveServiceTier` plausibility exactly
  (residential offered only from same_city; booking offers only the honorable tiers) so the quoted
  total always equals the escrow charge, and the on-chain approval uses the server-computed order
  amount. **Known Phase-1 limitation:** the generic **cart summary** (`CalculateCartSummary`) is
  product-shaped — it does not re-resolve a service's tier/call-out/booking fare and labels the fee
  line with `product_merchant` (2%), not `service_other` (0.5%). That line is informational (the
  merchant fee is borne by the merchant, not added to the buyer), and the buyer has already seen the
  authoritative total in the pricing sheet, so this is a display gap, not a mischarge. Threading tier
  resolution into the cart summary is a follow-up (§9).
- **No contract change** — `ProductEscrowV2` is untouched; EIP-712/chain config untouched.
- **Accepted posture (documented):** a provider who renders a service can be 90%-refunded if the
  buyer stays silent for 30 days (on-chain `autoRefund`). This is **identical to the risk product
  merchants already carry** and follows directly from D1 ("reuse product escrow"). Flagged here and
  in the PR; not silently absorbed. A provider-favouring timeout would require a contract change
  (out of scope).

## 11. Testing strategy

Backend (Go, table-driven, SQLite in-memory per existing pattern):
- `GetCalloutRate` zone→amount incl. residential=free.
- Booking tier resolution incl. buyer-declared residential/LGA defaults.
- `CreateOrderFromCart` for a service item: sets `Kind/ProductAmount/ShippingAmount/zone`;
  **rejects** when no contact payment exists (gate); standard vs booking fee selection.
- Service status machine: `escrowed→completed` allowed; `MarkShipped/Delivered` rejected;
  confirm-from-escrowed allowed.
- `CategoryBookingFee` upsert (six tiers, idempotent).

App (jest):
- `AddService` shows/hides pricing by `serviceFeeMode`; omits price when blank.
- View/PayNow gated by contact payment.
- Cart renders "Call-out fee" for service line items; total = price + call-out.

## 12. Implementation order (high-level; full plan via writing-plans)

1. Backend data model + migration (`Category.ServiceFeeMode`, `CategoryBookingFee`, `Order.Kind`).
2. Backend fee/zone helpers + booking-fee CRUD + routes (TDD).
3. Backend `CreateOrderFromCart` service branch + gate + status machine (TDD).
4. Admin app: category fee-mode + booking fare editor.
5. Merchant app: `AddService` pricing section.
6. Buyer app: View/PayNow gate → pricing sheet → shared-cart line item → confirm-completion.
7. End-to-end smoke + docs.

## Appendix A — Independent review resolutions

An independent adversarial review (security-auditor agent) checked v1 against the live contract and
handlers. Findings and resolutions:

| ID | Finding | Resolution |
|----|---------|-----------|
| B1 | On-chain timeout `autoRefund` favours the buyer; services never "delivered" ⇒ 100% buyer refund | **Reframed, not a service-specific bug**: products behave identically. Dropped the false "auto-release" claim (D1, §6); services settle by buyer-confirm + the existing on-chain 30-day backstop. Provider protection via `markDelivered`. No contract change. |
| B2 | No order-level backend watchdog/reconciler exists (they're P2P-only); `EscrowExpiresAt` is read nowhere | Corrected §3.3/§6/§10 to stop claiming watchdog reuse. Services match product settlement (no backend auto-settle). |
| B3 | `createOrder` reverts on `productAmount == 0`; booking/price-less services un-escrowable | Escrow the charge in `ProductAmount` (§3.3); zero-charge services create no escrow (§6). |
| S1 | Services have `stock=0`; checkout rejects "Insufficient stock" + decrements inventory | Skip stock check/decrement/restore for `Kind=service` (§8). |
| S2 | Dispute backstop unreachable; pre-delivery `requestRefund` is instant 100% self-refund | `markDelivered` ("rendered") flips refund to admin-adjudicated, mirroring products (§6). |
| S3 | Fee hardcoded to `product_merchant` (2%); `service_other` absent from split map | Branch fee on kind → `service_other`; add to `featureSplits` (§4.3, §8). |
| S4 | Declared residential/LGA tier has no field in the create-order request | Add per-item declared-tier field; honor server-side (§8). |
| S5 | `ValidNextStatuses(escrowed)` lacks `completed` — DB blocks confirm-from-escrowed | Thread `Kind` into transition validation (§6, §8). |
| S6 | Gate key is `caller_id`/`service_id`; uniqueness not DB-enforced | Enforce gate in-transaction; add unique index (§8). |
| N1 | Priced services may surface in product price-range queries | Verify service-list queries filter `type`; covered by the existing type filter (commit `7b50dfd`). |
| N2 | Notifications are product-worded | Branch event copy on kind (§8). |
| N3 | Money invariant not enforced for orders | Stated honestly in §10 (aspirational; P2P-only reconciler today). |

**Deviation from the reviewer's recommendation.** The review concluded the design needs *either* a
`ProductEscrowV2` change *or* a narrowed Phase 1 (buyer-confirm-only, standard-priced services,
drop booking). We take a **third path**: mirror the product escrow flow *exactly* — which the PM
explicitly requested ("copy what we have in products") — keeping full scope (both models) with **no
contract change**. The only semantic the contract can't give services (a provider-favouring
timeout) is one products don't have either, so deferring it is consistent, not a regression.
