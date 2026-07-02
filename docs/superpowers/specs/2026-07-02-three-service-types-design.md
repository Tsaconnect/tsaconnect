# Three Service Types (Booking / Order / Contact) — Design Spec

**Date:** 2026-07-02
**Branch:** `feat/three-service-types`
**Status:** Draft (pending independent review)
**Builds on:** PR #61 (`2026-06-30-service-pricing-checkout-design.md`) — service escrow checkout, `Order.Kind`, `Category.ServiceFeeMode`, `CategoryBookingFee`, `ServiceContactPayment`.

## 1. Goal

The PM wants the marketplace "Services" tab to be **three first-class service types**, each
with its own merchant form and buyer flow (the sub-tabs already drawn in the app design —
"Contact service", "Ordering service", "Booking service"):

| Type | Buyer verb | New capability vs. today |
|------|-----------|--------------------------|
| **Booking** | Reserve a date/time | availability (days/slots/capacity/duration), policies, slot reservation |
| **Order** | Select menu items, place order | a **menu** of items (name/desc/price/image), delivery-fee tiers |
| **Contact** | Pay access fee to unlock & engage | (mostly exists: pay-to-reveal contact) + merchant access/booking/call-out fees |

Today services are a **flat** model: a `Product{Type:"Service"}` whose behaviour is *inferred at
runtime* from `Category.ServiceFeeMode` (standard/booking) + the existence of a
`ServiceContactPayment` row. There is **no explicit service-type**, **no menu**, and **no
availability/slot** concept. This spec makes the three types structural.

## 2. Locked decisions

| # | Decision | Rationale |
|---|----------|-----------|
| **D1** | **`ServiceType` is a property of `Category`** (`booking`\|`order`\|`contact`), not of each listing. | Matches the marketplace UI (three sub-tabs filter the *category* grid — Bakery→order, Barbing→booking, Plumber→contact). Extends the existing category-driven form pattern (`ServiceFeeMode` is already category-level and already toggles the merchant form). One category = one type keeps the taxonomy and discovery simple. **Trade-off** (flagged for review): a merchant cannot offer, say, a contact *and* a booking listing under one category. Accepted for Phase 1; per-listing override is a documented follow-up. |
| **D2** | **Tiered distance fees are merchant-set**, reusing the existing per-zone `Product.Shipping*` columns + a new `ShippingResidential`. | The PM's new form has the *merchant* enter the tiered fee (checkbox rows with amounts) — a shift from PR #61's admin-set `CategoryBookingFee`. Reusing the shipping columns means the **escrow money path is unchanged** (checkout already reads them via `GetCalloutRate`/`GetShippingRate`). |
| **D3** | **All new service-specific config lives in a new 1:1 `ServiceProfile` table**, not on the overloaded `products` table. | `products` already carries product + service + shipping concerns. Isolating access/call-out fees, slot config, duration, policies, coverage, address, operating-company keeps the change surgical and only materialises for services. |
| **D4** | **Reuse the existing product/service escrow for all charges. No smart-contract change.** | Repo invariant. Order & Booking charges settle through the same `Kind:"service"` escrow that PR #61 built (buyer-confirm release, `markDelivered`/"rendered" protection, on-chain 30-day `autoRefund` backstop). |
| **D5** | **Contact-unlock in Phase 1 keeps the existing on-chain `$0.10` contact-reveal.** The merchant-set **Access / Booking / Call-out fees** are stored + displayed and charged via the **existing service-escrow** ("View / Pay now") path *after* unlock — they are NOT wired into the on-chain contact contract. | The `ServiceContact` contract's fee is a **global fixed amount**; making it merchant-set needs a contract change → paid audit + the independent-review process, out of scope for an autonomous build. This keeps the MR safe while delivering merchant monetisation via escrow. **Phase 2** (flagged for the user): make the access fee the on-chain unlock. |

### Derived defaults

- **DV1 — Backward compatible.** Existing Service categories default `ServiceType="contact"` (their
  current standard/contact behaviour is preserved); existing `ServiceFeeMode="booking"` categories are
  backfilled to `ServiceType="booking"`. Existing listings keep working with no data change.
- **DV2 — `ServiceFeeMode` is superseded but retained.** New flows key off `ServiceType`.
  `ServiceFeeMode`/`CategoryBookingFee` remain in the schema and continue to drive any legacy
  admin-fare categories, but the new booking type uses **merchant-set** tiers (D2). No data is deleted.
- **DV3 — Residential is now priceable.** PR #61 hard-coded residential = free. For booking/order the
  merchant may set a residential fee (`ShippingResidential`, default 0 → still free unless set).

## 3. Data model

### 3.1 `Category` (`models/category.go`)

```go
const (
    ServiceTypeContact = "contact" // pay access fee → unlock/engage (default for Service categories)
    ServiceTypeOrder   = "order"   // select menu items → order + delivery
    ServiceTypeBooking = "booking" // reserve a date/time slot
)

// added field
ServiceType string `gorm:"default:'contact'" json:"serviceType"`
```

Migration backfill (idempotent, in `config/database.go` after AutoMigrate):
`UPDATE categories SET service_type='booking' WHERE service_fee_mode='booking' AND (service_type IS NULL OR service_type='contact')`.

### 3.2 `Product` (`models/product.go`)

One new column — the residential tier for merchant-set distance fees:

```go
ShippingResidential float64 `gorm:"default:0" json:"shippingResidential"`
```

For services these five columns (`ShippingResidential`, `ShippingSameCity`, `ShippingSameState`,
`ShippingSameCountry`, `ShippingInternational`) hold the **tiered fee** — the "Booking/Route fee"
for booking listings, the "Delivery fee" for order listings. (`same_lga` maps onto `same_city`;
we keep the coarse geo tiers the detector can actually resolve, plus buyer-declared refinement per PR #61.)

### 3.3 `ServiceProfile` (new — `models/service_profile.go`), 1:1 with a service Product

```go
type ServiceProfile struct {
    ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    ProductID uuid.UUID `gorm:"type:uuid;not null;uniqueIndex" json:"productId"`

    // Fees (merchant-set, USD). Tiered fees live on Product (§3.2); these are the flat extras.
    AccessFeeUSD  float64 `gorm:"default:0" json:"accessFeeUsd"`  // contact: engagement/unlock price; booking: optional
    CalloutFeeUSD float64 `gorm:"default:0" json:"calloutFeeUsd"` // optional flat mobilisation fee

    // Booking config (booking type)
    SlotDurationMinutes int `gorm:"default:0" json:"slotDurationMinutes"` // service duration / slot length
    MaxCapacityPerSlot  int `gorm:"default:1" json:"maxCapacityPerSlot"`  // 0 = unlimited

    // Location / coverage (all types)
    ServiceAddress  string `json:"serviceAddress,omitempty"`
    CoverageArea    string `json:"coverageArea,omitempty"`
    OperatingCompany string `json:"operatingCompany,omitempty"` // flight/transport only

    // Policies (booking type) — free text, shown to buyer before reserving
    CancellationPolicy string `json:"cancellationPolicy,omitempty"`
    ReschedulePolicy   string `json:"reschedulePolicy,omitempty"`
    RefundPolicy       string `json:"refundPolicy,omitempty"`
    BookingRules       string `json:"bookingRules,omitempty"`

    CreatedAt time.Time `json:"createdAt"`
    UpdatedAt time.Time `json:"updatedAt"`
}
```

### 3.4 `ServiceMenuItem` (new — `models/service_menu_item.go`), for order type

```go
type ServiceMenuItem struct {
    ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    ProductID   uuid.UUID `gorm:"type:uuid;not null;index" json:"productId"`
    Name        string    `gorm:"not null" json:"name"`
    Description string    `json:"description,omitempty"`
    PriceUSD    float64   `gorm:"not null;default:0" json:"priceUsd"`
    ImageURL    string    `json:"imageUrl,omitempty"`
    IsAvailable bool      `gorm:"default:true" json:"isAvailable"`
    SortOrder   int       `gorm:"default:0" json:"sortOrder"`
    CreatedAt   time.Time `json:"createdAt"`
    UpdatedAt   time.Time `json:"updatedAt"`
}
```

### 3.5 `ServiceAvailability` (new — `models/service_availability.go`), booking + order operating hours

```go
type ServiceAvailability struct {
    ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    ProductID uuid.UUID `gorm:"type:uuid;not null;index" json:"productId"`
    DayOfWeek int       `gorm:"not null" json:"dayOfWeek"` // 0=Sun … 6=Sat
    StartTime string    `gorm:"size:5;not null" json:"startTime"` // "HH:MM" 24h
    EndTime   string    `gorm:"size:5;not null" json:"endTime"`
    CreatedAt time.Time `json:"createdAt"`
    UpdatedAt time.Time `json:"updatedAt"`
}
```

### 3.6 `ServiceBooking` (new — `models/service_booking.go`), a reserved slot tied to an order

```go
type ServiceBooking struct {
    ID        uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
    OrderID   uuid.UUID  `gorm:"type:uuid;not null;uniqueIndex" json:"orderId"`
    ProductID uuid.UUID  `gorm:"type:uuid;not null;index:idx_booking_slot" json:"productId"`
    BuyerID   uuid.UUID  `gorm:"type:uuid;not null;index" json:"buyerId"`
    SlotStart time.Time  `gorm:"not null;index:idx_booking_slot" json:"slotStart"`
    SlotEnd   time.Time  `gorm:"not null" json:"slotEnd"`
    PartySize int        `gorm:"not null;default:1" json:"partySize"`
    Status    string     `gorm:"not null;default:'reserved';index" json:"status"` // reserved|cancelled|completed
    CreatedAt time.Time  `json:"createdAt"`
    UpdatedAt time.Time  `json:"updatedAt"`
}
```

All additions are `AutoMigrate`-safe (new nullable/defaulted columns + new tables). Register the
four new models in `config/database.go`.

## 4. Capacity model (booking)

A slot is `(product_id, slot_start)`. Capacity is enforced **inside the checkout transaction**:

```
SELECT COALESCE(SUM(party_size),0) FROM service_bookings
  WHERE product_id=? AND slot_start=? AND status IN ('reserved','completed')
```
Reject if `existing + requested > MaxCapacityPerSlot` (unless `MaxCapacityPerSlot=0` → unlimited).
The slot must fall inside a `ServiceAvailability` window for that `DayOfWeek`. A cancelled/refunded
order flips its `ServiceBooking.Status='cancelled'`, freeing capacity. **Phase-1 honesty:** this is
last-writer-wins under the checkout tx; a unique partial index can't express a SUM, so two
simultaneous opens *could* over-book by one at the capacity boundary — acceptable for Phase 1, noted
for a follow-up (advisory lock / serialised slot rows).

## 5. Pricing per type (checkout)

`CreateOrderFromCart` (already service-aware) branches on the **category's `ServiceType`**:

- **Contact:** unchanged from PR #61 — buyer must have paid the `$0.10` contact fee (gate), then the
  escrowed engagement charge = `AccessFeeUSD` (+ optional call-out / declared-tier fee), placed in
  `ProductAmount` per PR #61's §3.3 mapping. (Access fee is the sole charge if no tier fee set.)
- **Order:** `ProductAmount` = Σ(selected menu items × qty); `ShippingAmount` = delivery fee for the
  resolved tier (from `Product.Shipping*`/`ShippingResidential`). Requires ≥1 in-stock (`IsAvailable`)
  menu item; server recomputes the item prices from the DB (never trusts client totals). Selected
  items are recorded in `Order.Notes` (JSON) for the merchant.
- **Booking:** `ProductAmount` = tiered booking fee for the resolved tier (+ optional
  `AccessFeeUSD`); `ShippingAmount` = 0 (or `CalloutFeeUSD` as pass-through). Requires a valid
  `slotStart` with capacity; a `ServiceBooking` row is created in the same tx.

Platform fee stays `service_other` (0.5%) on `ProductAmount` (PR #61). Zero-total service orders are
still rejected (no free escrow).

## 6. Backend surface

- `models/`: `ServiceType` const+field; `ShippingResidential`; four new models.
- `config/database.go`: AutoMigrate the four models; backfill booking categories.
- `category_handler.go`: persist `ServiceType` on create/update; expose it in the category tree.
- **`service_profile_handler.go`** (new): GET (public) / PUT (merchant-owned) the profile.
- **`service_menu_handler.go`** (new): list (public) / create / update / delete menu items (merchant-owned).
- **`service_availability_handler.go`** (new): list (public) / replace (merchant-owned) availability rows; GET computed open slots for a date range (availability windows minus full slots).
- `checkout_handler.go`: order & booking pricing branches (§5); menu-item recompute; capacity check + `ServiceBooking` create; keep the contact gate.
- `routes.go`: register the new route groups (profile/menu/availability/slots), merchant-auth on writes.
- `product_handler.go`: on service create/update, upsert the `ServiceProfile` (fees/config) from the request.

## 7. App surface

- **Marketplace** (`screens/marketplace.tsx`): under Services, three sub-tabs (Contact / Order /
  Booking) filtering the category grid by `serviceType`. Type badge on service cards.
- **Merchant `AddService.tsx`**: branch the form by the chosen category's `serviceType`:
  - *Contact* — portfolio images, access fee, optional booking/call-out fee, availability (days/hours), coverage, contact fields (existing).
  - *Order* — a **menu builder** (add/remove item: name, desc, price, image), delivery-fee tiers, operating days/hours, coverage.
  - *Booking* — tiered booking fee + optional access/call-out, availability (days, time windows, slot duration, capacity), policies (cancel/reschedule/refund/rules), address, coverage, operating company.
- **Buyer**:
  - *Order* — a **menu picker** (quantities) → cart line item with selected items; total = items + delivery.
  - *Booking* — a **slot picker** (pick a day from availability → pick an open time slot → party size) → pricing sheet (tiered booking fee) → escrow.
  - *Contact* — existing unlock, then the access/booking/call-out engagement charge via the pricing sheet.
- **Admin category form** (`AddCategory.tsx`/`EditCategory.tsx`): a `Service type` picker
  (Contact / Order / Booking) shown when category type is Service/Both.
- All UI uses the TSA Signature design system (`@/theme`, `@/components/ui`).

## 8. Out of scope (follow-ups)

- Making the merchant access fee the on-chain contact unlock (Phase 2; needs contract change/audit) — D5.
- Per-listing service-type override (D1).
- Hard concurrency guarantee on slot capacity (§4).
- Calendar/ICS invites, provider-side booking calendar management, reschedule flow UI.
- Neighbourhood/LGA geodata (still buyer-declared per PR #61).
- Platform cut on delivery/call-out/booking fee (pass-through, per PR #61).

## 9. Testing

Backend (Go, table-driven, SQLite in-memory):
- `ServiceType` persisted + returned in the category tree; backfill maps booking.
- Menu CRUD scoped to owner; order checkout recomputes item total from DB, rejects unavailable items.
- Availability replace; slot computation (windows minus full slots).
- Booking checkout: creates `ServiceBooking`, enforces capacity (accept up to cap, reject over), rejects slot outside availability, frees capacity on cancel.
- Order/booking pricing → correct `ProductAmount`/`ShippingAmount`/`Kind`/zone.

App (jest): sub-tab filtering by type; `AddService` branches by type; menu builder add/remove; slot picker disables full slots; buyer totals.

## 10. Implementation order

1. Backend models + migration + backfill.
2. Category `ServiceType` CRUD.
3. `ServiceProfile` + `ServiceMenuItem` + `ServiceAvailability` handlers/routes (TDD).
4. Checkout order + booking branches + capacity (TDD).
5. Admin category type picker (app).
6. Merchant `AddService` type-branched form (app).
7. Buyer menu picker + slot picker + marketplace sub-tabs (app).
8. Smoke + docs + PR.

## Appendix A — decisions flagged for the user / reviewer

- **D1** category-level type (vs per-listing).
- **D5** contact unlock stays `$0.10` on-chain; merchant access fee via escrow, not the contact contract (the one place I chose *not* to change a money path autonomously).
- **§4** Phase-1 slot capacity is not hard-concurrent.
- **F7** the `service_other` 0.5% carved split now applies to full Order menu totals / Booking fares
  (previously only small service amounts) → larger cashback/TP outflow per order. Mechanism is sound;
  confirm the economic intent.

## Appendix B — independent review resolutions (security-auditor, 2026-07-02)

An adversarial review verified the spec against the live code before implementation. Resolutions
(baked into the sections above and the implementation):

| ID | Sev | Finding | Resolution |
|----|-----|---------|-----------|
| **F1** | BLOCKER | The contact-fee gate in `CreateOrderFromCart` fires for **every** `Type=="Service"` product → Order/Booking checkout always 403s (they never have a `ServiceContactPayment`). | **Gate is now `ServiceType`-aware:** required only when the resolved category `ServiceType == contact`; skipped for `order`/`booking`. Reuse the `cat` already loaded in the branch. Table-driven test per type. |
| **F2** | HIGH | No channel carries the buyer's menu selection to the server; `Order.Notes` is shared plain text overwritten by ship/refund handlers. | Add `MenuSelections map[string][]{itemId,qty}` to `createOrderRequest`; persist to a **new** `Order.MenuSelectionsJSON` column (not `Notes`). |
| **F3** | HIGH | Checkout must not trust client prices / cross-product item ids. | Order pricing loads `ServiceMenuItem WHERE product_id=? AND id IN(?) AND is_available=true`, sums `PriceUSD×qty` server-side; rejects if any requested id is missing/unavailable. Test: item from another merchant's product → rejected. |
| **F4** | HIGH | Capacity can't depend on order-terminal events (scattered; escrowed self-refund never writes DB). | **Capacity = `SUM(party_size)` over `service_bookings JOIN orders` where `orders.status NOT IN (cancelled, refunded)`.** Auto-frees on cancel/refund; conservative (a stuck order holds its slot, never over-books). Abandoned-`pending_payment` slot leak documented as a follow-up (same class as the missing product order-watchdog). |
| **F5** | MED | Backfilling standard Service categories to `contact` would break their `price+call-out` flow (they have no access fee). | **Pricing fallbacks preserve PR #61:** contact type charges `AccessFeeUSD` **else** legacy `price+call-out`; booking type charges merchant tiered fee **else** admin `CategoryBookingFee`. Test: a pre-existing standard priced service still checks out. |
| **F6** | MED | Adding `ShippingResidential` must not silently start charging residential for contact/legacy-standard. | Residential fee is read **only** for `order`/`booking` types; `GetCalloutRate` keeps residential hard-zero for contact/standard. Test: existing standard service, residential tier → 0. |
| **F7** | LOW | 0.5% now on full menu totals is a business change. | Flagged to PM (Appendix A). Mechanism unchanged. |
| **F8** | LOW | `GetCategoryByID` serializes an explicit `gin.H` that omits new fields; product preview needs `serviceType` for the type badge. | Add `serviceType` to the hand-rolled category serializer and the product preview payload. |
| **F9** | LOW | 1:1 `ServiceBooking`↔order relies on the per-item order split. | Verified (inner loop creates one order per item). Test: multi-booking cart → one order + one booking each. |
</content>
</invoke>
