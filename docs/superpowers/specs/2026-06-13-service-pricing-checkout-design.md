# Service Pricing + Call-out Fee + Checkout (Project C)

**Date:** 2026-06-13
**Branch:** `feat/fee-tp-overhaul`
**Status:** Design — awaiting review
**Origin:** PM services review. Parts A (marketplace leak) and B (in-repo "Both"
category option) already shipped in commit `7b50dfd`. This spec covers **C**.
Part **D** (merged call-out + booking fee for travel services, finer area
taxonomy) is a follow-on and is explicitly out of scope here.

---

## 1. Problem

Services today are **contact-fee-only**: a user pays a one-time per-provider
contact fee to unlock the provider's phone/email, and that's the end of the
in-app flow. There is no way to *pay for the service itself* in-app.

The PM wants services that have a fixed price (haircut, phone-screen
replacement, car wash, website-design package, delivery, …) to be payable
in-app — "copy what we have in the product listing page / checkout and
replicate to the service," with two service-specific adjustments:

- **"Call-out fee" replaces "Shipping fee"** for services (it covers the
  provider's mobilisation/transport to reach the user).
- **A fixed service price is optional** — only services that have a set price
  become payable; the rest stay contact-fee-only.

The call-out fee is **free within the same residential area** and otherwise set
by the merchant per area, exactly as product shipping is set per zone.

## 2. Goals / Non-goals

**Goals**
- A merchant can set an **optional** fixed service price + per-area call-out fee
  on a service listing.
- After the contact fee is paid, a user can **pay for a priced service** in-app:
  service price + call-out fee, in one escrowed payment, with release on
  completion.
- Reuse the product escrow, fee engine, zone math, and checkout summary UI.
- Cashback + TP accrue on the service-sale fee via the existing v2 engine.
- Fully backwards-compatible: a service with no price behaves exactly as today.

**Non-goals (this spec)**
- No cart / multi-provider batching for services (see §4).
- No new escrow contract (reuse the product escrow).
- D's merged call-out+booking fee and finer travel zones
  (residential → LGA → … → outside country). Deferred.

## 3. Key decisions (settled with the user)

1. **Single-provider service order, not a cart.** A service is bound to one
   provider the user already unlocked via the contact fee. Payment is a
   single-provider "service order," not a product-style multi-seller cart. We
   reuse the escrow contract, the fee split, the zone→fee math, and the summary
   UI components — but **not** the cart or shipping-address-as-parcel-destination
   semantics.
2. **Call-out fee reuses the 4 existing product zones** (Same City / Same State /
   Same Country / International). "Free within the same residential area" =
   merchant sets the Same City tier to $0 (its default). The finer
   residential/LGA taxonomy is a D concern, not built here.
3. **No new columns where the model already has them.** Services are `Product`
   rows (`type="Service"`). The price field and the four shipping-zone fields
   already exist on the model; services simply start *using* them (price is no
   longer hard-coded to 0), relabelled "Call-out fee" in service contexts.

## 4. Architecture

```
Service detail (already gated by contact fee)
   │  contact fee paid? ──no──> existing "Reveal contact" flow (unchanged)
   │  yes
   ▼
[Pricing — view / pay now]   (shown only when the service has a price)
   ▼
Service-order screen (single provider)
   service price + call-out fee (by buyer area) + total
   ▼
POST create service order ──> POST /orders/:id/prepare-escrow ──> sign+broadcast ──> /submit-escrow
   ▼
Escrow held (reuses product escrow) ──> provider delivers ──> buyer releases ──> fee settles (v2 engine)
```

The order lifecycle, prepare/submit-escrow endpoints, and the escrow contract
are the **same** machinery products use; the service order is a single-item
order whose one line is the service. The product cart (`/cart/checkout`,
`ConvertToOrder`) is **not** involved.

## 5. Data model

No new columns. On `models.Product` (the shared product/service model):

| Field | Product meaning | Service meaning (this spec) |
|---|---|---|
| `Price` | product price | **optional** fixed service price; `0`/unset → contact-fee-only |
| `ShippingSameCity` | ship same city | **Call-out fee** same area (default `0` = free) |
| `ShippingSameState` | ship same state | Call-out fee same state |
| `ShippingSameCountry` | ship same country | Call-out fee same country |
| `ShippingInternational` | ship international | Call-out fee international |

App/UI relabels these to "Call-out fee" when `type === 'Service'`. The DB column
names stay (no migration); only the labels and the listing-form inputs change.

A service order needs to record which service + provider + buyer + the chosen
call-out zone + amounts. **Open decision (§8.1):** reuse the existing product
`Order` model with a single service line, or a thin `ServiceOrder` model. Lean:
reuse `Order` to inherit prepare/submit-escrow + release + dispute for free.

## 6. Fee treatment

The service **sale** (price + call-out) settles a fee through the v2 engine like
any other fee event. The contact fee is unchanged (`service_contact`, the
$0.01/$0.01/$0.01 split). The model already defines `FeatureServiceOther`
("other fixed service fee", 0.5%) but it has **no explicit split** in
`featureSplits`, so today it would fall back to the product carved split.

**Open decision (§8.2):** which fee + split applies to the priced-service sale.
Candidates:
- **`product_merchant` (2%, carved 0.50/0.15/0.15/0/0.20)** — treat a priced
  service like a product sale; buyer + upline each get 0.3% cashback, consistent
  with "copy the product checkout."
- **`service_other` (0.5%) with an explicit carved split added** — lighter fee
  intended for services; needs a split row defined so it still pays cashback+TP.

Recommendation: `service_other` with an explicit carved split (mirroring the
product carve so cashback+TP still flow), since the platform already earmarked a
lighter service-sale rate. Confirm with PM.

## 7. Surfaces to build

**Backend**
- Service-order create endpoint (single service line), gated on a confirmed
  contact-fee payment for that (buyer, provider, service).
- Reuse `/orders/:id/prepare-escrow` + `/submit-escrow` + release/dispute.
- Settle the sale fee under the chosen feature (§8.2).

**App**
- **Service listing form** (`AddService`): optional **Service price** + per-area
  **Call-out fee** inputs (mirroring the product price/shipping inputs).
- **Service detail** (`ServiceDetail`): a **"Pricing — view / pay now"** button,
  visible only when the contact fee is paid *and* the service has a price.
- **Service-order screen**: price + call-out (by buyer area) + total + pay;
  reuses the checkout summary components, with "Shipping" relabelled
  "Call-out fee."
- Order/escrow status + release reuses the product order screens (a service
  order is an `Order`).

## 8. Open decisions (resolve during review)

1. **Order model:** reuse product `Order` with a single service line
   (recommended — inherits escrow/release/dispute) vs a thin `ServiceOrder`.
2. **Sale fee feature/split:** `service_other` + explicit carved split
   (recommended) vs `product_merchant` (2%). PM to confirm the rate.
3. **Release trigger:** buyer taps "Release" after the service is delivered
   (mirrors product receipt confirmation) — assume yes unless PM wants a
   provider-side "mark delivered" first.

## 9. Testing

- Service with no price → unchanged contact-fee-only behaviour (regression).
- Service with price, contact fee unpaid → no pay-now button; create-order
  endpoint rejects.
- Service with price, contact fee paid → pay-now → escrow → release → fee
  settles → cashback+TP accrue (assert the chosen split).
- Call-out fee: same-area buyer → $0; other zones → merchant-set amount; total =
  price + call-out.
- Backwards-compat: existing product checkout untouched.

## 10. Deferred to D

Merged call-out + booking fee for travel services (flight/taxi/bike/keke), and
the finer area taxonomy (residential / same city / LGA / state / country /
outside country) configurable per category from admin. D extends C's call-out
model; it is not built here.
