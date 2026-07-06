# Service Model Simplification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse the booking-engine config to two dimensions (engagement: `instant | request`; pricing: `fixed | on_request`), delete the unused deposit/quote/dynamic/workflow/payment config, and add the one new behavior — the provider names the price when approving a price-on-request booking.

**Architecture:** Trim enums on `ServiceProfile` and the `BookingRequest` model in one atomic refactor; add an optional price override to the shared `placeServiceOrder` path; let `ApproveBookingRequest` accept an `amountUsd` stored in the already-existing `QuotedAmountUSD` and consumed by `PayBookingRequest`. The app mirrors the reduced enums and adds pricing/engagement toggles plus a price prompt on approve.

**Tech Stack:** Go 1.24 + Gin + GORM (backend, SQLite in-memory for tests); React Native 0.81 + Expo + TypeScript (app).

## Global Constraints

- Escrow requires `productAmount > 0` on-chain — a quoted price ≤ 0 must be rejected before an order is created.
- Feature is not live and has no production data — value renames and struct-field removals are safe; orphan DB columns left by removed struct fields are harmless (GORM AutoMigrate never drops columns; the test DDL keeps them).
- **Symbol-removal reality:** Go compiles a whole package (incl. its `_test.go` files) before running any `-run` filter. Removing an enum used across `internal/handlers` means the package only compiles once *every* reference — source and test — is updated. Therefore the enum trim is done as **one atomic task (Task 1)** that ends with the tree green, not spread across tasks that would each leave a red build.
- App has no unit-test harness for these screens — verification is `npx tsc --noEmit` + `npx eslint` on touched files + manual drive. Never claim green without running these. Baseline: the app has 22 pre-existing `tsc` errors in unrelated files; success = the count does not rise.
- Work on branch `feat/service-model-simplification` (already created). Commit after every task.
- Backend commands run from `tsa-api-go/`; app commands from `tsa-app/`.

---

### Task 1: Reduce the booking-engine config to two dimensions

One atomic refactor: trim the enums everywhere they're referenced (model, handlers, and their tests) so the tree stays green. Adds the reduced-enum validators and the invalid-combo guard.

**Files:**
- Modify: `internal/models/service_profile.go` (struct fields, enum constants, validators)
- Modify: `internal/models/booking_request.go` (drop `QuoteExpiresAt`, `quoted` status)
- Modify: `internal/handlers/service_profile_handler.go` (`serviceProfileRequest`, `UpsertServiceProfile`, `GetServiceProfile`)
- Modify: `internal/handlers/booking_form_handler.go` (`bookingFormSchema`, `GetBookingFormSchema`)
- Modify: `internal/handlers/booking_request_handler.go` (`PayBookingRequest` atomic-claim references the deleted `BookingRequestQuoted` — must be updated in this task or the package won't compile)
- Modify: `internal/handlers/booking_request_handler_test.go` (`writeBookingProfile` helper, constant renames, existing tests)
- Modify: `internal/handlers/booking_form_handler_test.go` (`writeFieldConfig` helper)
- Create: `internal/models/service_profile_test.go`

**Interfaces:**
- Produces: `models.BookingTypeInstant="instant"`, `models.BookingTypeRequest="request"`, `models.PricingMethodFixed="fixed"`, `models.PricingMethodOnRequest="on_request"`, `models.ValidBookingType(s) bool`, `models.ValidPricingMethod(s) bool`.
- Removes: `BookingTypeRequestApproval`, `BookingTypeQuoteFirst`, `PricingMethodStartingFrom`, `PricingMethodDynamic`, `PricingMethodQuoteLater`, all `Workflow*`, all `PaymentTiming*`, `ValidWorkflow`, `ValidPaymentTiming`, `BookingRequestQuoted`, the `Workflow`/`PaymentTiming`/`DepositPercent` `ServiceProfile` fields, and `QuoteExpiresAt`.

- [ ] **Step 1: Write the validator test (new model test)**

Create `internal/models/service_profile_test.go`:

```go
package models

import "testing"

func TestValidBookingType(t *testing.T) {
	for _, s := range []string{BookingTypeInstant, BookingTypeRequest} {
		if !ValidBookingType(s) {
			t.Errorf("ValidBookingType(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"request_approval", "quote_before_payment", "bogus", ""} {
		if ValidBookingType(s) {
			t.Errorf("ValidBookingType(%q) = true, want false", s)
		}
	}
}

func TestValidPricingMethod(t *testing.T) {
	for _, s := range []string{PricingMethodFixed, PricingMethodOnRequest} {
		if !ValidPricingMethod(s) {
			t.Errorf("ValidPricingMethod(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"starting_from", "dynamic", "quote_later", "bogus", ""} {
		if ValidPricingMethod(s) {
			t.Errorf("ValidPricingMethod(%q) = true, want false", s)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/models/ -run 'TestValidBookingType|TestValidPricingMethod' -count=1`
Expected: FAIL — `BookingTypeRequest`, `PricingMethodOnRequest` undefined.

- [ ] **Step 3: Edit `service_profile.go`**

Replace the six config struct fields (`BookingType`…`BookingFeeUSD`) with:

```go
	// Booking-engine config. Two dimensions, three valid combinations:
	//   instant + fixed        — buy now, pay the listing price into escrow
	//   request + fixed        — buyer requests, provider approves, pays listing price
	//   request + on_request   — buyer requests, provider approves & names the price
	// (instant + on_request is invalid — can't prepay an unnamed price.)
	BookingType   string  `gorm:"default:'instant'" json:"bookingType"`   // instant | request
	PricingMethod string  `gorm:"default:'fixed'" json:"pricingMethod"`   // fixed | on_request
	BookingFeeUSD float64 `gorm:"default:0" json:"bookingFeeUsd"`         // merchant "booking fee"
```

Replace the entire enum `const (...)` block and all four `Valid*` funcs with:

```go
// Booking-engine enum constants (ServiceProfile config dimensions).
const (
	BookingTypeInstant = "instant"
	BookingTypeRequest = "request"

	PricingMethodFixed     = "fixed"
	PricingMethodOnRequest = "on_request"
)

// ValidBookingType reports whether s is a known booking type.
func ValidBookingType(s string) bool {
	return s == BookingTypeInstant || s == BookingTypeRequest
}

// ValidPricingMethod reports whether s is a known pricing method.
func ValidPricingMethod(s string) bool {
	return s == PricingMethodFixed || s == PricingMethodOnRequest
}
```

- [ ] **Step 4: Edit `booking_request.go`**

Delete the `QuoteExpiresAt *time.Time` field. Change the `QuotedAmountUSD` comment to `// set by the provider when approving a price-on-request booking`. Remove `BookingRequestQuoted` from the constants. Replace the transition table:

```go
// ValidNextBookingRequestStatuses returns the allowed transitions from current.
func ValidNextBookingRequestStatuses(current string) []string {
	switch current {
	case BookingRequestRequested:
		return []string{BookingRequestApproved, BookingRequestRejected, BookingRequestCancelled, BookingRequestExpired}
	case BookingRequestApproved:
		return []string{BookingRequestConverted, BookingRequestCancelled, BookingRequestExpired}
	default:
		return nil
	}
}
```

- [ ] **Step 4b: Fix the live `BookingRequestQuoted` reference in `booking_request_handler.go`**

`PayBookingRequest`'s atomic status-claim (currently line 361) reads `[]string{models.BookingRequestApproved, models.BookingRequestQuoted}`. `BookingRequestQuoted` is deleted in Step 4, so this line must change or the `internal/handlers` package won't compile. With `quoted` gone, `approved` is the only payable state:

```go
	claim := tx.Model(&models.BookingRequest{}).
		Where("id = ? AND status = ?", r.ID, models.BookingRequestApproved).
		Updates(map[string]interface{}{
```

(Change only the `Where(...)` line — the `IN ?` + two-element slice becomes `= ?` + the single `BookingRequestApproved`. Leave the rest of the `Updates(...)` call unchanged.)

- [ ] **Step 5: Edit `service_profile_handler.go`**

In `serviceProfileRequest`, remove `Workflow`, `PaymentTiming`, `DepositPercent` (keep `BookingType`, `PricingMethod`, `BookingFeeUSD`, `FieldConfig`).

In `UpsertServiceProfile`:
- Remove the `DepositPercent` bounds-check block.
- Remove the `Workflow` and `PaymentTiming` `Valid*` checks.
- Add the invalid-combo guard next to the existing slotted guard:

```go
	// instant + on_request is invalid: you can't pay a listing price up front
	// when the price is only named on approval.
	if req.BookingType == models.BookingTypeInstant && req.PricingMethod == models.PricingMethodOnRequest {
		utils.ErrorResponse(c, http.StatusBadRequest, "Instant booking requires a fixed price; use 'request' for price-on-request")
		return
	}
```

- In the assignment block, remove `profile.DepositPercent = ...`, the `if req.Workflow != ""` and `if req.PaymentTiming != ""` blocks, and the `Workflow`/`PaymentTiming` default-normalization blocks. Keep the `BookingType`/`PricingMethod` assignment + defaults:

```go
	if req.BookingType != "" {
		profile.BookingType = req.BookingType
	}
	if req.PricingMethod != "" {
		profile.PricingMethod = req.PricingMethod
	}
	if profile.BookingType == "" {
		profile.BookingType = models.BookingTypeInstant
	}
	if profile.PricingMethod == "" {
		profile.PricingMethod = models.PricingMethodFixed
	}
```

In `GetServiceProfile`, drop the `Workflow`/`PaymentTiming` fields from the zero-value fallback literal (keep `BookingType: models.BookingTypeInstant`, `PricingMethod: models.PricingMethodFixed`).

- [ ] **Step 6: Edit `booking_form_handler.go`**

In `bookingFormSchema`, remove the `Workflow` and `PaymentTiming` fields. In `GetBookingFormSchema`, remove the `Workflow:` and `PaymentTiming:` lines from the returned literal (keep `BookingType` + `PricingMethod`).

- [ ] **Step 7: Update the handler test helpers + renames**

In `booking_form_handler_test.go`, `writeFieldConfig` builds a `models.ServiceProfile{...}` with `Workflow`/`PaymentTiming` — remove those two fields.

In `booking_request_handler_test.go`:
- `writeBookingProfile(t, db, productID, bookingType string)` — change its signature to add a pricing method: `writeBookingProfile(t, db, productID, bookingType, pricingMethod string)`, drop the `Workflow`/`PaymentTiming` fields from the literal, and set `PricingMethod: pricingMethod`.
- Update **all three** callers to pass a pricing method (the new arity is required for every call — a missed caller fails the package compile):
  - `TestBookingRequestLifecycle` → `models.PricingMethodFixed`
  - `TestBookingRequestReject` → `models.PricingMethodFixed`
  - `TestCreateBookingRequest_RejectsInstant` (currently `writeBookingProfile(t, db, svc.ID, models.BookingTypeInstant)`) → `writeBookingProfile(t, db, svc.ID, models.BookingTypeInstant, models.PricingMethodFixed)`
- Replace every `models.BookingTypeRequestApproval` with `models.BookingTypeRequest` (in `TestBookingRequestLifecycle`, `TestBookingRequestReject`, `TestUpsertServiceProfile_RejectsSlottedRequestMode`).

- [ ] **Step 8: Add the invalid-combo handler test**

Append to `booking_request_handler_test.go`:

```go
// instant + on_request is an invalid combination (can't prepay an unnamed price).
func TestUpsertServiceProfile_RejectsInstantOnRequest(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: svc.ID.String()}}
	body, _ := json.Marshal(map[string]interface{}{
		"bookingType":   models.BookingTypeInstant,
		"pricingMethod": models.PricingMethodOnRequest,
	})
	c.Request, _ = http.NewRequest("PUT", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpsertServiceProfile(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for instant+on_request, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 9: Build, vet, test — verify green**

Run: `go build ./... && go vet ./internal/models/ ./internal/handlers/ 2>&1 | grep -v '^#' | head && go test ./internal/models/ ./internal/handlers/ -run 'ValidBookingType|ValidPricingMethod|Booking|Service|Contact|EffectiveServiceType' -count=1`
Expected: build clean, vet clean, all listed tests PASS (validators pass; existing lifecycle/reject/instant-reject/slotted-reject tests pass with the renamed constants; new invalid-combo test passes).

- [ ] **Step 10: Commit**

```bash
git add internal/models/service_profile.go internal/models/booking_request.go internal/models/service_profile_test.go internal/handlers/service_profile_handler.go internal/handlers/booking_form_handler.go internal/handlers/booking_request_handler.go internal/handlers/booking_request_handler_test.go internal/handlers/booking_form_handler_test.go
git commit -m "refactor(service): reduce booking-engine config to two dimensions"
```

---

### Task 2: Add a price override to placeServiceOrder

**Files:**
- Modify: `internal/handlers/checkout_handler.go` (`placeServiceOrder` signature + body; `CreateServiceOrder` call site)
- Modify: `internal/handlers/booking_request_handler.go` (`PayBookingRequest` call site — pass `nil` for now)

**Interfaces:**
- Produces: `placeServiceOrder(tx, buyer, seller, product, serviceType, profile, token, serviceTier, buyerCity, buyerState, buyerCountry, menuLines, slotReq, answers, priceOverrideUSD *float64) (*models.Order, string, int)`. When `priceOverrideUSD != nil`, service price = the override, call-out = 0, and pricing/slot resolution is skipped.

- [ ] **Step 1: Change the signature + branch**

Add `priceOverrideUSD *float64` as the final parameter to `placeServiceOrder`. Replace the block from `origin := resolveShippingOrigin(product, seller)` through the `resolveServicePricing` error check with:

```go
	origin := resolveShippingOrigin(product, seller)
	detectedZone := DetectShippingZone(buyerCity, buyerState, buyerCountry, origin.City, origin.State, origin.Country)

	var priceUSD, calloutUSD float64
	var zone string
	var menuSel []models.OrderMenuSelection
	var booking *models.ServiceBooking
	if priceOverrideUSD != nil {
		// Provider-quoted price (price-on-request): the quote is the all-in
		// service price; no tier call-out is added and there's no slot to reserve.
		priceUSD = *priceOverrideUSD
		zone = detectedZone
	} else {
		var perr string
		priceUSD, calloutUSD, zone, menuSel, booking, perr = ch.resolveServicePricing(
			tx, buyer.ID, product, serviceType, profile, detectedZone, serviceTier, menuLines, slotReq,
		)
		if perr != "" {
			return nil, perr, http.StatusBadRequest
		}
	}
```

The remainder (wei conversion, the `productAmountWei.Sign() <= 0` guard, order + booking creation) is unchanged.

- [ ] **Step 2: Update both call sites**

In `CreateServiceOrder` (checkout_handler.go), append `, nil` to the `ch.placeServiceOrder(...)` call. In `PayBookingRequest` (booking_request_handler.go), append `, nil` to its `ch.placeServiceOrder(...)` call (Task 3 changes this `nil` to the quote).

- [ ] **Step 3: Verify unchanged behavior**

Run: `go build ./... && go test ./internal/handlers/ -run 'TestCreateServiceOrder|TestCreateOrderFromCart_Service|TestBookingRequest' -count=1`
Expected: PASS — existing service-order and booking-request tests unaffected (override is nil everywhere).

- [ ] **Step 4: Commit**

```bash
git add internal/handlers/checkout_handler.go internal/handlers/booking_request_handler.go
git commit -m "feat(service): optional price override in placeServiceOrder"
```

---

### Task 3: Provider names the price on approve

**Files:**
- Modify: `internal/handlers/booking_request_handler.go` (`ApproveBookingRequest`; `PayBookingRequest` override arg)
- Test: `internal/handlers/booking_request_handler_test.go`

**Interfaces:**
- Consumes: `models.PricingMethodOnRequest` (Task 1); `placeServiceOrder(..., priceOverrideUSD)` (Task 2).
- Produces: `POST /services/requests/:reqId/approve` accepts `{ "amountUsd": number }` — required (>0) when the listing is `on_request`, ignored when `fixed`; stored in `BookingRequest.QuotedAmountUSD`. `PayBookingRequest` uses that quote.

- [ ] **Step 1: Write the failing test**

Append to `booking_request_handler_test.go`:

```go
// A price-on-request booking: approval must carry a price, which becomes the
// escrowed amount the buyer pays.
func TestBookingRequestOnRequestQuote(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	writeBookingProfile(t, db, svc.ID, models.BookingTypeRequest, models.PricingMethodOnRequest)

	w := postJSON(t, buyer, gin.Params{{Key: "id", Value: svc.ID.String()}}, map[string]interface{}{"token": "USDC"}, ch.CreateBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var reqRow models.BookingRequest
	db.Where("buyer_id = ?", buyer.ID).First(&reqRow)
	reqParams := gin.Params{{Key: "reqId", Value: reqRow.ID.String()}}

	// Approve without a price → 400.
	if w := postJSON(t, seller, reqParams, nil, ch.ApproveBookingRequest); w.Code != http.StatusBadRequest {
		t.Fatalf("approve without price: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Approve with a price → 200, stored.
	if w := postJSON(t, seller, reqParams, map[string]interface{}{"amountUsd": 42.0}, ch.ApproveBookingRequest); w.Code != http.StatusOK {
		t.Fatalf("approve with price: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	db.First(&reqRow, "id = ?", reqRow.ID)
	if reqRow.QuotedAmountUSD == nil || *reqRow.QuotedAmountUSD != 42.0 {
		t.Fatalf("expected quoted 42, got %v", reqRow.QuotedAmountUSD)
	}

	// Pay → order escrows the quoted price (42 USDC → 42000000 in 6 decimals).
	w = postJSON(t, buyer, reqParams, nil, ch.PayBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("pay: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var order models.Order
	db.Where("buyer_id = ?", buyer.ID).First(&order)
	if order.ProductAmount != "42000000" {
		t.Errorf("expected productAmount 42000000, got %s", order.ProductAmount)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/handlers/ -run 'TestBookingRequestOnRequestQuote' -count=1`
Expected: FAIL — approve ignores the body and doesn't require a price.

- [ ] **Step 3: Edit `ApproveBookingRequest`**

After the transition-check block and before `r.Status = models.BookingRequestApproved`, insert:

```go
	var body struct {
		AmountUSD *float64 `json:"amountUsd"`
	}
	_ = c.ShouldBindJSON(&body)

	// Price-on-request listings require the provider to name a price at approval.
	var profile models.ServiceProfile
	config.DB.Where("product_id = ?", r.ProductID).First(&profile)
	if profile.PricingMethod == models.PricingMethodOnRequest {
		if body.AmountUSD == nil || *body.AmountUSD <= 0 {
			utils.ErrorResponse(c, http.StatusBadRequest, "Set a price to approve this request")
			return
		}
		r.QuotedAmountUSD = body.AmountUSD
	}
```

- [ ] **Step 4: Point `PayBookingRequest` at the quote**

Change the final `nil` in `PayBookingRequest`'s `ch.placeServiceOrder(...)` call (added in Task 2) to `r.QuotedAmountUSD`.

- [ ] **Step 5: Run to verify it passes**

Run: `go build ./... && go test ./internal/handlers/ -run 'TestBookingRequest|TestCreateServiceOrder' -count=1`
Expected: PASS (on-request quote + all existing booking-request/service-order tests green).

- [ ] **Step 6: Commit**

```bash
git add internal/handlers/booking_request_handler.go internal/handlers/booking_request_handler_test.go
git commit -m "feat(service): provider names price when approving price-on-request booking"
```

---

### Task 4: App API client — trim unions + approve-with-price

**Files:**
- Modify: `tsa-app/services/serviceListingApi.ts`
- Modify: `tsa-app/services/bookingRequestApi.ts`

**Interfaces:**
- Produces: `BookingType = 'instant' | 'request'`; `PricingMethod = 'fixed' | 'on_request'`; `ServiceProfile` drops `workflow`/`paymentTiming`/`depositPercent`; `BookingRequest` gains `quotedAmountUsd?: number`; `approveBookingRequest(reqId: string, amountUsd?: number)`.

- [ ] **Step 1: Trim `serviceListingApi.ts` types**

```ts
export type BookingType = 'instant' | 'request';
export type PricingMethod = 'fixed' | 'on_request';
```

Remove the `Workflow` and `PaymentTiming` type exports. In `ServiceProfile`, remove `workflow?`, `paymentTiming?`, `depositPercent?`. In `BookingFormSchema`, remove `workflow` and `paymentTiming`.

- [ ] **Step 2: Edit `bookingRequestApi.ts`**

Add `quotedAmountUsd?: number;` to the `BookingRequest` interface. Remove `| 'quoted'` from the `BookingRequestStatus` union (the `quoted` state was dropped server-side). Note: this makes `STATUS_TONE` in `booking-requests.tsx` a transient `tsc` error until Task 7 removes its `quoted` entry — expected, like the other consumer errors below. Replace `approveBookingRequest`:

```ts
export async function approveBookingRequest(
  reqId: string,
  amountUsd?: number
): Promise<ApiResponse<BookingRequest>> {
  return mutate(`${API_BASE_URL}/services/requests/${reqId}/approve`, amountUsd != null ? { amountUsd } : undefined);
}
```

- [ ] **Step 3: Verify**

Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep -E "serviceListingApi|bookingRequestApi" | head`
Expected: empty (errors in consumer files are expected until Tasks 5–7).

- [ ] **Step 4: Commit**

```bash
git add services/serviceListingApi.ts services/bookingRequestApi.ts
git commit -m "refactor(service): app api types match reduced booking model"
```

---

### Task 5: Merchant pricing + engagement toggles (AddService)

**Files:**
- Modify: `tsa-app/components/services/AddService.tsx`

**Interfaces:**
- Consumes: `BookingType`, `PricingMethod` (Task 4).
- Produces: a Pricing `SegmentedControl` (Fixed / Price on request) and an Engagement `SegmentedControl` (Buy now / Request first — Buy-now hidden when pricing = on_request); persists `bookingType` + `pricingMethod`.

- [ ] **Step 1: Add pricingMethod state + persistence**

Import `PricingMethod` from `@/services/serviceListingApi`. Add:

```tsx
const [pricingMethod, setPricingMethod] = useState<PricingMethod>("fixed");
```

In the edit-prefetch block (next to `if (prof.bookingType) setBookingType(...)`): `if (prof.pricingMethod) setPricingMethod(prof.pricingMethod);`. In `persistServiceConfig`, next to `profile.bookingType = bookingType;`: `profile.pricingMethod = pricingMethod;`.

- [ ] **Step 2: Force the valid combo**

Add an effect so on_request implies request engagement:

```tsx
useEffect(() => {
  if (pricingMethod === "on_request" && bookingType === "instant") {
    setBookingType("request");
  }
}, [pricingMethod, bookingType]);
```

- [ ] **Step 3: Render both toggles**

Replace the existing "Booking workflow" `SegmentedControl` block with:

```tsx
{sectionTitle("Pricing")}
<Card padding="lg" style={{ gap: spacing.sm }}>
  <SegmentedControl
    segments={[
      { key: "fixed", label: "Fixed price" },
      { key: "on_request", label: "Price on request" },
    ]}
    value={pricingMethod}
    onChange={(k) => setPricingMethod(k as PricingMethod)}
  />
  <Text variant="caption" color="textSecondary">
    {pricingMethod === "fixed"
      ? "You set the price up front."
      : "You name the price when you approve each request."}
  </Text>
</Card>

{sectionTitle("Booking workflow")}
<Card padding="lg" style={{ gap: spacing.sm }}>
  <SegmentedControl
    segments={
      pricingMethod === "on_request"
        ? [{ key: "request", label: "Request first" }]
        : [
            { key: "instant", label: "Buy now" },
            { key: "request", label: "Request first" },
          ]
    }
    value={bookingType}
    onChange={(k) => setBookingType(k as BookingType)}
  />
  <Text variant="caption" color="textSecondary">
    {bookingType === "instant"
      ? "Customers book and pay immediately."
      : "Customers request a booking; you approve before they pay."}
  </Text>
</Card>
```

- [ ] **Step 4: Verify**

Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep AddService` (expect empty) and `npx eslint components/services/AddService.tsx` (no new errors).

- [ ] **Step 5: Commit**

```bash
git add components/services/AddService.tsx
git commit -m "feat(service): merchant pricing + engagement toggles"
```

---

### Task 6: Buyer on-request submit path (ServicePricingSheet)

**Files:**
- Modify: `tsa-app/components/services/ServicePricingSheet.tsx` (only if it references a removed enum value)

**Interfaces:**
- Consumes: `BookingType` from the form schema (Task 4). The `handleBookingSubmit` branch (`bookingType === 'instant'` → escrow; else → `createBookingRequest`) already works with the reduced enum.

- [ ] **Step 1: Check for removed-value references**

Run: `grep -n "request_approval\|quote_before_payment\|quote_later\|paymentTiming\|workflow" components/services/ServicePricingSheet.tsx`
Expected: no matches. If any appear, replace a `=== 'request_approval'` style check with `!== 'instant'` (any non-instant is the request path).

- [ ] **Step 2: Verify**

Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep ServicePricingSheet` (expect empty) and `npx eslint components/services/ServicePricingSheet.tsx` (no new errors).

- [ ] **Step 3: Commit (only if a change was made)**

```bash
git add components/services/ServicePricingSheet.tsx
git commit -m "refactor(service): buyer request path uses reduced booking enum"
```

---

### Task 7: Quoted price + approve-with-price (booking-requests screen)

**Files:**
- Modify: `tsa-app/app/(dashboard)/booking-requests.tsx`

**Interfaces:**
- Consumes: `approveBookingRequest(reqId, amountUsd?)`, `BookingRequest.quotedAmountUsd` (Task 4).
- Produces: buyer sees the quoted price above Pay on an approved row; seller enters an optional price when approving (ignored server-side for fixed listings).

- [ ] **Step 1: Add price-draft state + Input import; drop the dead `quoted` tone**

Import `Input` from `@/components/ui`. Add near the other hooks: `const [priceDrafts, setPriceDrafts] = useState<Record<string, string>>({});`. Remove the `quoted: { label: 'Quoted', tone: 'info' },` entry from the `STATUS_TONE` map (the `quoted` status was dropped from the `BookingRequestStatus` union in Task 4, so this key no longer type-checks).

- [ ] **Step 2: Update `handleApprove`**

```tsx
const handleApprove = (id: string) =>
  withBusy(id, async () => {
    const raw = priceDrafts[id];
    const amount = raw && raw.trim() !== '' ? Number(raw) : undefined;
    if (amount != null && (isNaN(amount) || amount <= 0)) {
      return toast.show('Enter a valid price', 'error');
    }
    const res = await approveBookingRequest(id, amount);
    if (!res.success) return toast.show(res.message || 'Failed to approve', 'error');
    toast.show('Request approved', 'success');
    load();
  });
```

- [ ] **Step 3: Show the quoted price to the buyer**

Replace the buyer Pay block with:

```tsx
{tab === 'buyer' && request.status === 'approved' ? (
  <>
    {request.quotedAmountUsd != null ? (
      <Text variant="bodyMed" weight="700">
        Quoted price: ${request.quotedAmountUsd.toFixed(2)}
      </Text>
    ) : null}
    <Button
      title="Pay & confirm"
      leftIcon="shield"
      onPress={() => handlePay(request.id)}
      loading={busyId === request.id}
      disabled={busyId === request.id}
    />
  </>
) : null}
```

- [ ] **Step 4: Add the price input to the seller Approve block**

Replace the seller `requested` block with:

```tsx
{tab === 'seller' && request.status === 'requested' ? (
  <View style={{ gap: spacing.sm }}>
    <Input
      label="Price (USD) — leave blank for a fixed-price listing"
      keyboardType="decimal-pad"
      value={priceDrafts[request.id] ?? ''}
      onChangeText={(t) => setPriceDrafts((p) => ({ ...p, [request.id]: t }))}
      placeholder="e.g. 40.00"
    />
    <View style={{ flexDirection: 'row', gap: spacing.sm }}>
      <Button
        title="Approve"
        onPress={() => handleApprove(request.id)}
        loading={busyId === request.id}
        disabled={busyId === request.id}
        style={{ flex: 1 }}
      />
      <Button
        title="Decline"
        variant="secondary"
        onPress={() => handleReject(request.id)}
        disabled={busyId === request.id}
        style={{ flex: 1 }}
      />
    </View>
  </View>
) : null}
```

- [ ] **Step 5: Verify**

Run: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep booking-requests` (expect empty) and `npx eslint "app/(dashboard)/booking-requests.tsx"` (no new errors).

- [ ] **Step 6: Commit**

```bash
git add "app/(dashboard)/booking-requests.tsx"
git commit -m "feat(service): quoted price display + approve-with-price on requests screen"
```

---

### Task 8: Full verification + open MR

**Files:** none (verification only).

- [ ] **Step 1: Backend green**

From `tsa-api-go/`: `go build ./... && go vet ./internal/... 2>&1 | grep -v '^#' | head && go test ./internal/models/ ./internal/handlers/ -run 'Booking|Service|EffectiveServiceType|Contact|ValidBooking|ValidPricing' -count=1`
Expected: build clean, vet clean, tests PASS.

- [ ] **Step 2: App green**

From `tsa-app/`: `npx tsc --noEmit -p tsconfig.json 2>&1 | grep -c 'error TS'` — must be ≤ 22 (the pre-existing baseline). Run `npx eslint` on all touched files — no new errors.

- [ ] **Step 3: Manual end-to-end drive**

Against a running backend + app:
1. **Price on request:** create a booking service with Price-on-request → merchant form hides "Buy now". Buyer submits a request (no price shown). Merchant approves *with a price*. Buyer sees the quoted price and pays. Confirm the escrowed order amount equals the quote.
2. **Fixed + Request first:** merchant approves without a price; buyer pays the listing price.
3. **Buy now:** unchanged instant escrow.

- [ ] **Step 4: Push + open MR**

```bash
git push -u origin feat/service-model-simplification
gh pr create --base main --title "Simplify service model: two dimensions + price-on-request" --body "Implements docs/superpowers/specs/2026-07-06-service-model-simplification-design.md. Collapses booking config to engagement (instant|request) x pricing (fixed|on_request), removes deposits/dynamic-pricing/quote-expiry/workflow/payment-timing, and adds provider-names-price-on-approve. See spec for rationale."
```

---

## Notes for the implementer

- **Do not** re-introduce `Workflow`, `PaymentTiming`, `DepositPercent`, `starting_from`, `dynamic`, `quote_before_payment`, `request_approval`, `quote_later`, `BookingRequestQuoted`, or `QuoteExpiresAt` anywhere — they are deliberately deleted in Task 1.
- The `booking_requests` / `service_profiles` test DDL keeps the now-orphan columns (`workflow`, `payment_timing`, `deposit_percent`, `quote_expires_at`); leave them — extra columns are harmless and removing them is unnecessary churn.
- Task 1 is intentionally the largest — it's a single atomic enum refactor that must land green. Do not split it; a partial removal leaves `internal/handlers` uncompilable. It edits **five** source files (`service_profile.go`, `booking_request.go`, `service_profile_handler.go`, `booking_form_handler.go`, `booking_request_handler.go`) plus three test files — every one of them references a removed symbol.
- **Intentional spec/plan divergence:** the spec lists `BookingConfigBuilder.tsx` among touched components, but the pricing/engagement toggles go in `AddService.tsx` (which already hosts the booking-workflow `SegmentedControl`); `BookingConfigBuilder` remains the form-field builder. This is deliberate, not an oversight.
- **Baseline before starting:** run `npx tsc --noEmit -p tsconfig.json 2>&1 | grep -c 'error TS'` from `tsa-app/` once at the start and record the number — the "≤ baseline" gate in Task 8 Step 2 anchors to that actual value (expected ~22, but confirm).
- Out of scope (unchanged): slotted bookings remain instant-only (already guarded); native date/time picker; cancellation/refund enforcement.
