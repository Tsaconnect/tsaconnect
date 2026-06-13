# P2P Multi-Currency Browse + Payment-Method Filter — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the P2P **Buy** experience work for any fiat currency (defaulting to the user's country currency), add an "All Payment Methods" filter + payment chips driven by a normalized provider model, and remove all NGN-only hardcoding.

**Architecture:** A small `provider` field on payment methods (backed by a Go catalog) makes payment methods filterable and lets international methods (PayPal/Revolut/Interac) be represented. The ad-list endpoint decorates each ad with a privacy-safe `paymentMethodSummary` (provider + label, **no** account numbers); the frontend derives the filter options from those summaries and filters **client-side**. A P2P-scoped currency hook (defaulting from the existing geolocation logic in `CurrencyContext`) drives `fiatCurrency` on the ad list. No smart-contract or trade-flow changes.

**Tech Stack:** Go (Gin + GORM + PostgreSQL) backend; React Native + Expo Router frontend; Go `testing` + `jest-expo`.

**Reference spec:** `docs/superpowers/specs/2026-05-27-p2p-multi-currency-payment-methods-design.md`

**Working directories:**
- Backend: `tsa-dev/tsa-api-go`
- Frontend: `tsa-dev/tsa-app`

**Conventions:**
- Do NOT add a `Co-Authored-By` trailer to commits.
- Commit code only (this plan/spec are not committed).
- Run Go commands from `tsa-dev/tsa-api-go`; frontend commands from `tsa-dev/tsa-app`.

---

## Key design decisions locked in

- **Filtering is client-side.** The backend adds `paymentMethodSummary` to listed ads; the frontend computes the distinct providers from loaded ads and filters in-memory. No backend `provider` query param, no SQL join over the `payment_method_ids` array. (The spec explicitly allowed "derived from the list response".)
- **`provider` is the source of truth for `type`.** On create, the server derives `type` from the provider catalog so the two never disagree.
- **`floatAnchor` means *eligibility*** (currency has a Bybit P2P feed), independent of whether the latest Bybit fetch succeeded — derived from `bybitCurrencies`, not from per-row `Stale`.

---

## File structure

**Backend (`tsa-dev/tsa-api-go`)**
- Create: `internal/services/p2p_payment_providers.go` — provider catalog + lookups (pure).
- Create: `internal/services/p2p_payment_providers_test.go` — catalog tests.
- Modify: `internal/models/p2p_payment_method.go` — add `Provider`, add `wallet` type const.
- Modify: `internal/config/database.go` — backfill `provider` after AutoMigrate.
- Modify: `internal/handlers/p2p_handler.go` — provider validation on create; `ListPaymentProviders`; `paymentMethodSummary` on `adListItem` + batch loader.
- Modify: `internal/services/p2p_trade_service.go` — `LoadProvidersByMethodIDs` batch loader.
- Modify: `internal/services/p2p_service.go` — `FloatAnchor` field + `applyFloatAnchors` helper, wired into `fetchAndCache`.
- Modify: `internal/services/p2p_service_test.go` (append) — `applyFloatAnchors` test.
- Modify: `internal/routes/routes.go` — register `GET /p2p/payment-providers`.

**Frontend (`tsa-dev/tsa-app`)**
- Modify: `services/p2p.ts` — types (`provider`, `paymentMethodSummary`), `paymentMethodsApi.providers()`, `create()` provider arg, `distinctAdProviders()` helper, `PaymentProvider` type.
- Create: `services/p2p.test.ts` — `distinctAdProviders` test.
- Modify: `services/exchangeRateApi.ts` — `floatAnchor?: boolean` on `P2PRate`.
- Create: `hooks/useP2pCurrency.ts` — P2P-scoped currency selection.
- Create: `hooks/useP2pCurrency.test.ts` — hook tests.
- Create: `components/p2p/PaymentMethodFilter.tsx` — provider filter dropdown.
- Modify: `app/p2p/index.tsx` — currency pill, payment filter, chips, de-NGN, empty state.
- Modify: `app/p2p/create-ad.tsx` — currency selector, multi-currency rate/float gating, de-NGN.
- Modify: `app/p2p/payment-methods/new.tsx` — provider picker + currency awareness.

---

# Phase A — Backend

## Task 1: Payment provider catalog (pure)

**Files:**
- Create: `internal/services/p2p_payment_providers.go`
- Test: `internal/services/p2p_payment_providers_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/services/p2p_payment_providers_test.go`:

```go
package services

import "testing"

func TestIsValidProvider(t *testing.T) {
	if !IsValidProvider("bank_transfer") {
		t.Fatal("expected bank_transfer to be valid")
	}
	if IsValidProvider("nope_not_real") {
		t.Fatal("expected unknown provider to be invalid")
	}
}

func TestProviderLabelKnown(t *testing.T) {
	if got := ProviderLabel("paypal"); got != "PayPal" {
		t.Fatalf("ProviderLabel(paypal) = %q, want PayPal", got)
	}
}

func TestProviderLabelUnknownFallsBackToTitleCase(t *testing.T) {
	if got := ProviderLabel("foo_bar"); got != "Foo Bar" {
		t.Fatalf("ProviderLabel(foo_bar) = %q, want Foo Bar", got)
	}
}

func TestProviderTypeDerivation(t *testing.T) {
	if got := ProviderType("paypal"); got != "wallet" {
		t.Fatalf("ProviderType(paypal) = %q, want wallet", got)
	}
	if got := ProviderType("bank_transfer"); got != "bank" {
		t.Fatalf("ProviderType(bank_transfer) = %q, want bank", got)
	}
}

func TestAllPaymentProvidersNonEmpty(t *testing.T) {
	if len(AllPaymentProviders()) == 0 {
		t.Fatal("expected a non-empty provider catalog")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-dev/tsa-api-go && go test ./internal/services -run 'TestIsValidProvider|TestProviderLabel|TestProviderType|TestAllPaymentProviders' -v`
Expected: FAIL — `undefined: IsValidProvider` (and the others).

- [ ] **Step 3: Write minimal implementation**

Create `internal/services/p2p_payment_providers.go`:

```go
package services

import "strings"

// PaymentProvider is one entry in the curated P2P payment-method catalog.
// The catalog is the single source of truth shared with the frontend (via
// GET /p2p/payment-providers) so labels/types never drift between client and
// server. `Type` is the coarse category derived for a provider; `Provider`
// (the slug) is the precise, filterable identifier stored on payment methods.
type PaymentProvider struct {
	Slug            string `json:"slug"`
	Label           string `json:"label"`
	Type            string `json:"type"`            // bank | mobile_money | wallet
	IdentifierLabel string `json:"identifierLabel"` // UI label for the account-id field
}

// paymentProviders is the ordered catalog. Add a provider by appending one
// line — no other code needs to change.
var paymentProviders = []PaymentProvider{
	{Slug: "bank_transfer", Label: "Bank Transfer", Type: "bank", IdentifierLabel: "Account number"},
	{Slug: "opay", Label: "OPay", Type: "mobile_money", IdentifierLabel: "Account/phone"},
	{Slug: "palmpay", Label: "PalmPay", Type: "mobile_money", IdentifierLabel: "Account/phone"},
	{Slug: "kuda", Label: "Kuda", Type: "bank", IdentifierLabel: "Account number"},
	{Slug: "interac", Label: "Interac e-Transfer", Type: "bank", IdentifierLabel: "Email"},
	{Slug: "paypal", Label: "PayPal", Type: "wallet", IdentifierLabel: "Email"},
	{Slug: "revolut", Label: "Revolut", Type: "wallet", IdentifierLabel: "Tag/phone"},
	{Slug: "wise", Label: "Wise", Type: "wallet", IdentifierLabel: "Email"},
	{Slug: "skrill", Label: "Skrill", Type: "wallet", IdentifierLabel: "Email"},
}

var providerBySlug = func() map[string]PaymentProvider {
	m := make(map[string]PaymentProvider, len(paymentProviders))
	for _, p := range paymentProviders {
		m[p.Slug] = p
	}
	return m
}()

// AllPaymentProviders returns the full catalog (ordered) for the create-ad picker.
func AllPaymentProviders() []PaymentProvider { return paymentProviders }

// PaymentProviderBySlug looks up a catalog entry. ok is false for unknown slugs.
func PaymentProviderBySlug(slug string) (PaymentProvider, bool) {
	p, ok := providerBySlug[slug]
	return p, ok
}

// IsValidProvider reports whether slug is in the catalog.
func IsValidProvider(slug string) bool {
	_, ok := providerBySlug[slug]
	return ok
}

// ProviderLabel returns the display label for a slug, falling back to a
// title-cased slug for forward-compatibility with rows written by a newer
// client than this server.
func ProviderLabel(slug string) string {
	if p, ok := providerBySlug[slug]; ok {
		return p.Label
	}
	return titleCaseSlug(slug)
}

// ProviderType returns the coarse category for a slug, or "" if unknown.
func ProviderType(slug string) string {
	if p, ok := providerBySlug[slug]; ok {
		return p.Type
	}
	return ""
}

func titleCaseSlug(slug string) string {
	parts := strings.Split(slug, "_")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-dev/tsa-api-go && go test ./internal/services -run 'TestIsValidProvider|TestProviderLabel|TestProviderType|TestAllPaymentProviders' -v`
Expected: PASS (all 5).

- [ ] **Step 5: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/services/p2p_payment_providers.go internal/services/p2p_payment_providers_test.go
git commit -m "feat(p2p): add payment-provider catalog with lookups"
```

---

## Task 2: Add `provider` + `wallet` type to the payment-method model

**Files:**
- Modify: `internal/models/p2p_payment_method.go`

- [ ] **Step 1: Add the field + const**

In `internal/models/p2p_payment_method.go`, add a `Provider` column to the struct immediately after the `Type` field:

```go
	Type     string `gorm:"size:16;not null" json:"type"`
	Provider string `gorm:"size:32;not null;default:bank_transfer" json:"provider"`
```

And add the wallet type constant to the existing const block:

```go
const (
	P2PPaymentTypeBank        = "bank"
	P2PPaymentTypeMobileMoney = "mobile_money"
	P2PPaymentTypeWallet      = "wallet"
)
```

- [ ] **Step 2: Verify it compiles**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: builds with no errors.

- [ ] **Step 3: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/models/p2p_payment_method.go
git commit -m "feat(p2p): add provider column + wallet type to payment method model"
```

---

## Task 3: Backfill `provider` for existing rows after AutoMigrate

**Files:**
- Modify: `internal/config/database.go`

- [ ] **Step 1: Add the backfill**

GORM `AutoMigrate` creates the `provider` column with default `bank_transfer`. Existing `mobile_money` rows should map to their provider where the bank name makes it obvious. Add this block in `database.go` immediately AFTER the existing P2P `DB.Exec` constraint statements (mirror the existing post-migrate `DB.Exec` pattern):

```go
	// Backfill P2P payment-method providers for rows created before the
	// `provider` column existed. AutoMigrate defaults new/legacy rows to
	// 'bank_transfer'; refine known mobile-money providers by bank name.
	// Idempotent — only touches rows still on the default.
	DB.Exec(`UPDATE p2p_payment_methods
	         SET provider = 'opay'
	         WHERE provider = 'bank_transfer' AND type = 'mobile_money'
	           AND lower(bank_name) LIKE '%opay%'`)
	DB.Exec(`UPDATE p2p_payment_methods
	         SET provider = 'palmpay'
	         WHERE provider = 'bank_transfer' AND type = 'mobile_money'
	           AND lower(bank_name) LIKE '%palmpay%'`)
	DB.Exec(`UPDATE p2p_payment_methods
	         SET provider = 'kuda'
	         WHERE provider = 'bank_transfer'
	           AND lower(bank_name) LIKE '%kuda%'`)
```

- [ ] **Step 2: Verify it compiles**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: builds with no errors.

- [ ] **Step 3: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/config/database.go
git commit -m "feat(p2p): backfill payment-method provider on migrate"
```

---

## Task 4: Validate `provider` on create + derive `type`

**Files:**
- Modify: `internal/handlers/p2p_handler.go` (`createPaymentMethodRequest` ~149-158, `CreatePaymentMethod` ~160-191)

- [ ] **Step 1: Update the request struct**

Replace the `createPaymentMethodRequest` struct (currently at ~149-158) with one where `provider` is required and `type` is optional (derived):

```go
type createPaymentMethodRequest struct {
	Provider      string `json:"provider" binding:"required"`
	Type          string `json:"type"` // optional; derived from provider when present
	Country       string `json:"country" binding:"required"`
	Currency      string `json:"currency" binding:"required"`
	BankName      string `json:"bankName" binding:"required"`
	BankCode      string `json:"bankCode"`
	AccountNumber string `json:"accountNumber" binding:"required"`
	AccountName   string `json:"accountName" binding:"required"`
	IsDefault     bool   `json:"isDefault"`
}
```

- [ ] **Step 2: Update the handler validation + persistence**

In `CreatePaymentMethod`, replace the type-validation block (currently the `if req.Type != ... { ... }` at ~170-173) and the `pm := &models.P2PPaymentMethod{...}` construction (~174-185) with:

```go
	if !services.IsValidProvider(req.Provider) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid payment provider"})
		return
	}
	// Type is derived from the provider catalog so the two never disagree.
	pmType := services.ProviderType(req.Provider)
	pm := &models.P2PPaymentMethod{
		ID:            uuid.New(),
		UserID:        user.ID,
		Type:          pmType,
		Provider:      req.Provider,
		Country:       strings.ToUpper(req.Country),
		Currency:      strings.ToUpper(req.Currency),
		BankName:      req.BankName,
		BankCode:      req.BankCode,
		AccountNumber: req.AccountNumber,
		AccountName:   req.AccountName,
		IsDefault:     req.IsDefault,
	}
```

(The `services` package is already imported in this handler — it references `services.AdFilter` etc. If the build reports it missing, add `"<module>/internal/services"` to the import block, matching the path other handlers use.)

- [ ] **Step 3: Verify it compiles**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: builds with no errors.

- [ ] **Step 4: Run the existing handler tests to confirm no regression**

Run: `cd tsa-dev/tsa-api-go && go test ./internal/handlers -run P2P -v`
Expected: PASS (or, if an existing create-payment-method test sends `type` but not `provider`, update that test to send `provider` instead — note it in the commit).

- [ ] **Step 5: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/handlers/p2p_handler.go
git commit -m "feat(p2p): require provider on payment-method create, derive type"
```

---

## Task 5: `GET /p2p/payment-providers` endpoint

**Files:**
- Modify: `internal/handlers/p2p_handler.go` (add handler near `ListPaymentMethods`)
- Modify: `internal/routes/routes.go` (~349)

- [ ] **Step 1: Add the handler**

In `internal/handlers/p2p_handler.go`, add after `ListPaymentMethods`:

```go
// ListPaymentProviders returns the curated payment-provider catalog used by
// the create-ad / add-payment-method pickers. Static data — no auth-scoped
// rows — but kept under the authed group for consistency.
func (h *P2PHandler) ListPaymentProviders(c *gin.Context) {
	if _, ok := h.require(c); !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": services.AllPaymentProviders()})
}
```

- [ ] **Step 2: Register the route**

In `internal/routes/routes.go`, add inside the `p2pGroup` block, right after the `payment-methods` routes (~351):

```go
				p2pGroup.GET("/payment-providers", p2p.ListPaymentProviders)
```

- [ ] **Step 3: Verify it compiles**

Run: `cd tsa-dev/tsa-api-go && go build ./...`
Expected: builds with no errors.

- [ ] **Step 4: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/handlers/p2p_handler.go internal/routes/routes.go
git commit -m "feat(p2p): expose payment-provider catalog endpoint"
```

---

## Task 6: Privacy-safe `paymentMethodSummary` on the ad list

**Files:**
- Modify: `internal/services/p2p_trade_service.go` — add `LoadProvidersByMethodIDs`
- Modify: `internal/handlers/p2p_handler.go` — `adListItem` field + populate in `decorateAdsWithEffectiveRate`
- Test: `internal/services/p2p_payment_providers_test.go` (append a pure summary-mapping test)

- [ ] **Step 1: Write the failing test for the summary mapper**

The label mapping is pure. Append to `internal/services/p2p_payment_providers_test.go`:

```go
func TestPaymentSummaryFromProviders(t *testing.T) {
	// dedupes provider slugs and maps to labels, order-stable.
	got := PaymentSummaryFromProviders([]string{"bank_transfer", "opay", "bank_transfer"})
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped summaries, got %d", len(got))
	}
	if got[0].Provider != "bank_transfer" || got[0].Label != "Bank Transfer" {
		t.Fatalf("unexpected first summary: %+v", got[0])
	}
	if got[1].Provider != "opay" || got[1].Label != "OPay" {
		t.Fatalf("unexpected second summary: %+v", got[1])
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd tsa-dev/tsa-api-go && go test ./internal/services -run TestPaymentSummaryFromProviders -v`
Expected: FAIL — `undefined: PaymentSummaryFromProviders`.

- [ ] **Step 3: Implement the type + mapper in the catalog file**

Append to `internal/services/p2p_payment_providers.go`:

```go
// PaymentMethodSummary is the privacy-safe per-ad payment descriptor exposed
// in the marketplace list — provider + display label only, never account data.
type PaymentMethodSummary struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
}

// PaymentSummaryFromProviders maps a list of provider slugs to deduped,
// order-stable summaries. Unknown slugs fall back to a title-cased label.
func PaymentSummaryFromProviders(slugs []string) []PaymentMethodSummary {
	seen := make(map[string]struct{}, len(slugs))
	out := make([]PaymentMethodSummary, 0, len(slugs))
	for _, s := range slugs {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, PaymentMethodSummary{Provider: s, Label: ProviderLabel(s)})
	}
	return out
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd tsa-dev/tsa-api-go && go test ./internal/services -run TestPaymentSummaryFromProviders -v`
Expected: PASS.

- [ ] **Step 5: Add the batch provider loader to the trade service**

In `internal/services/p2p_trade_service.go`, add after `LoadAdPaymentMethods` (~1982):

```go
// LoadProvidersByMethodIDs returns a map payment-method-id → provider slug for
// the given ids in a single query. Used by the ad-list decorator to build the
// privacy-safe payment summary without an N+1 per-ad load. Deleted rows are
// excluded. Account fields are deliberately NOT selected.
func (s *P2PTradeService) LoadProvidersByMethodIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID       uuid.UUID
		Provider string
	}
	if err := s.db.WithContext(ctx).
		Model(&models.P2PPaymentMethod{}).
		Select("id", "provider").
		Where("id IN ? AND deleted_at IS NULL", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ID] = r.Provider
	}
	return out, nil
}
```

- [ ] **Step 6: Add the summary field + populate it in the decorator**

In `internal/handlers/p2p_handler.go`, extend `adListItem` (~666-670):

```go
type adListItem struct {
	models.P2PAd
	RateStale            bool                            `json:"rateStale,omitempty"`
	SellerUsername       string                          `json:"sellerUsername,omitempty"`
	PaymentMethodSummary []services.PaymentMethodSummary `json:"paymentMethodSummary,omitempty"`
}
```

Then in `decorateAdsWithEffectiveRate` (~682-715), add a batched provider lookup before the per-row loop and attach the summary inside it. Insert after the `usernames, err := h.trade.LookupUsernames(...)` block (~690-694):

```go
	// Gather every payment-method id referenced across all ads, resolve their
	// providers in one query, then attach a privacy-safe summary per ad.
	pmIDSet := map[uuid.UUID]struct{}{}
	for i := range rows {
		for _, raw := range rows[i].PaymentMethodIDs {
			if id, perr := uuid.Parse(raw); perr == nil {
				pmIDSet[id] = struct{}{}
			}
		}
	}
	pmIDs := make([]uuid.UUID, 0, len(pmIDSet))
	for id := range pmIDSet {
		pmIDs = append(pmIDs, id)
	}
	providerByID, perr := h.trade.LoadProvidersByMethodIDs(ctx, pmIDs)
	if perr != nil {
		log.Printf("[p2p] LoadProvidersByMethodIDs failed: %v — list will omit payment summaries", perr)
		providerByID = map[uuid.UUID]string{}
	}
```

Then, inside the existing `for i := range rows` loop, after `item := adListItem{P2PAd: ad}` (~697), add:

```go
		// Build the deduped provider summary for this ad's payment methods.
		slugs := make([]string, 0, len(ad.PaymentMethodIDs))
		for _, raw := range ad.PaymentMethodIDs {
			if id, perr := uuid.Parse(raw); perr == nil {
				if prov, ok := providerByID[id]; ok {
					slugs = append(slugs, prov)
				}
			}
		}
		item.PaymentMethodSummary = services.PaymentSummaryFromProviders(slugs)
```

- [ ] **Step 7: Verify it compiles + run service tests**

Run: `cd tsa-dev/tsa-api-go && go build ./... && go test ./internal/services -run 'TestPaymentSummaryFromProviders|TestProvider|TestIsValidProvider' -v`
Expected: build OK; tests PASS.

- [ ] **Step 8: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/services/p2p_payment_providers.go internal/services/p2p_payment_providers_test.go internal/services/p2p_trade_service.go internal/handlers/p2p_handler.go
git commit -m "feat(p2p): attach privacy-safe payment summary to ad list"
```

---

## Task 7: `floatAnchor` eligibility flag on rates

**Files:**
- Modify: `internal/services/p2p_service.go` — `P2PRate.FloatAnchor` field + `applyFloatAnchors` helper, wired into `fetchAndCache`
- Test: `internal/services/p2p_service_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/services/p2p_service_test.go`:

```go
func TestApplyFloatAnchors(t *testing.T) {
	rates := map[string]P2PRate{
		"NGN": {Currency: "NGN", MidRate: 1500},
		"CAD": {Currency: "CAD", MidRate: 1.4},
	}
	bybit := map[string]bool{"NGN": true}
	out := applyFloatAnchors(rates, bybit)
	if !out["NGN"].FloatAnchor {
		t.Fatal("expected NGN to be float-anchor eligible")
	}
	if out["CAD"].FloatAnchor {
		t.Fatal("expected CAD to NOT be float-anchor eligible")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd tsa-dev/tsa-api-go && go test ./internal/services -run TestApplyFloatAnchors -v`
Expected: FAIL — `out["NGN"].FloatAnchor undefined` / `undefined: applyFloatAnchors`.

- [ ] **Step 3: Add the field + helper**

In `internal/services/p2p_service.go`, add the field to `P2PRate` (after the `Stale` field, ~52):

```go
	Stale bool `json:"stale"`
	// FloatAnchor reports whether this currency is eligible for a float-rate
	// anchor (it has a Bybit P2P feed), independent of whether the latest
	// Bybit fetch succeeded. The create-ad screen uses it to gate the float
	// rate option.
	FloatAnchor bool  `json:"floatAnchor"`
	UpdatedAt   int64 `json:"updatedAt"`
```

Add the helper near `markAllStale` (~305):

```go
// applyFloatAnchors stamps FloatAnchor on each row from the set of
// Bybit-feed-eligible currencies. Eligibility is membership in bybitCurrencies,
// not per-fetch success — a transient Bybit outage marks a row Stale but the
// currency remains float-eligible. Mutates and returns the same map.
func applyFloatAnchors(rates map[string]P2PRate, bybitCurrencies map[string]bool) map[string]P2PRate {
	for code, row := range rates {
		row.FloatAnchor = bybitCurrencies[code]
		rates[code] = row
	}
	return rates
}
```

- [ ] **Step 4: Wire it into `fetchAndCache`**

In `fetchAndCache`, immediately after the Bybit-override `for cur := range ps.bybitCurrencies { ... }` loop ends and BEFORE `ttl := ps.cacheTTL` (~280), add:

```go
	// Stamp float-anchor eligibility on every row before caching.
	rates = applyFloatAnchors(rates, ps.bybitCurrencies)
```

- [ ] **Step 5: Run it to verify it passes + build**

Run: `cd tsa-dev/tsa-api-go && go build ./... && go test ./internal/services -run TestApplyFloatAnchors -v`
Expected: build OK; PASS.

- [ ] **Step 6: Commit**

```bash
cd tsa-dev/tsa-api-go
git add internal/services/p2p_service.go internal/services/p2p_service_test.go
git commit -m "feat(p2p): add floatAnchor eligibility flag to rates"
```

---

# Phase B — Frontend services & types

## Task 8: `services/p2p.ts` — types, providers API, filter helper

**Files:**
- Modify: `tsa-dev/tsa-app/services/p2p.ts`
- Test: `tsa-dev/tsa-app/services/p2p.test.ts`

- [ ] **Step 1: Write the failing test**

Create `tsa-dev/tsa-app/services/p2p.test.ts`:

```ts
import { distinctAdProviders, type P2PAd } from './p2p';

function ad(summary: { provider: string; label: string }[]): P2PAd {
  return { paymentMethodSummary: summary } as unknown as P2PAd;
}

describe('distinctAdProviders', () => {
  it('returns deduped providers across ads, order-stable', () => {
    const ads = [
      ad([{ provider: 'bank_transfer', label: 'Bank Transfer' }, { provider: 'opay', label: 'OPay' }]),
      ad([{ provider: 'opay', label: 'OPay' }, { provider: 'paypal', label: 'PayPal' }]),
    ];
    expect(distinctAdProviders(ads)).toEqual([
      { provider: 'bank_transfer', label: 'Bank Transfer' },
      { provider: 'opay', label: 'OPay' },
      { provider: 'paypal', label: 'PayPal' },
    ]);
  });

  it('ignores ads with no summary', () => {
    expect(distinctAdProviders([ad([]), {} as P2PAd])).toEqual([]);
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd tsa-dev/tsa-app && npx jest services/p2p.test.ts`
Expected: FAIL — `distinctAdProviders is not a function` / import error.

- [ ] **Step 3: Implement type + API + helper changes**

In `tsa-dev/tsa-app/services/p2p.ts`:

(a) Add to the `P2PPaymentMethod` interface (after `type:`):

```ts
  type: 'bank' | 'mobile_money' | 'wallet';
  provider: string;
```

(b) Add to the `P2PAd` interface (near `paymentMethodIds`):

```ts
  /** Privacy-safe payment descriptor for the marketplace list — provider +
   *  label only, never account numbers. Populated by the list endpoint. */
  paymentMethodSummary?: { provider: string; label: string }[];
```

(c) Add a `PaymentProvider` type near the other exported interfaces:

```ts
export interface PaymentProvider {
  slug: string;
  label: string;
  type: 'bank' | 'mobile_money' | 'wallet';
  identifierLabel: string;
}
```

(d) Extend `paymentMethodsApi` — add `providers` and a `provider` arg to `create`:

```ts
export const paymentMethodsApi = {
  list: () => request<P2PPaymentMethod[]>('GET', '/p2p/payment-methods'),

  providers: () => request<PaymentProvider[]>('GET', '/p2p/payment-providers'),

  create: (input: {
    provider: string;
    country: string;
    currency: string;
    bankName: string;
    bankCode?: string;
    accountNumber: string;
    accountName: string;
    isDefault?: boolean;
  }) => request<P2PPaymentMethod>('POST', '/p2p/payment-methods', input),

  remove: (id: string) =>
    request<void>('DELETE', `/p2p/payment-methods/${encodeURIComponent(id)}`),
};
```

(e) Add the pure filter helper near the other exported helpers (end of file, before the default export if any — this file has no default export, so append):

```ts
/**
 * Distinct, order-stable list of payment providers present across the given
 * ads' privacy-safe summaries. Used to populate the "All Payment Methods"
 * filter from already-loaded ads (no extra request).
 */
export function distinctAdProviders(
  ads: P2PAd[],
): { provider: string; label: string }[] {
  const seen = new Set<string>();
  const out: { provider: string; label: string }[] = [];
  for (const ad of ads) {
    for (const s of ad.paymentMethodSummary ?? []) {
      if (!s || seen.has(s.provider)) continue;
      seen.add(s.provider);
      out.push({ provider: s.provider, label: s.label });
    }
  }
  return out;
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd tsa-dev/tsa-app && npx jest services/p2p.test.ts`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
cd tsa-dev/tsa-app
git add services/p2p.ts services/p2p.test.ts
git commit -m "feat(p2p): add provider types, providers API, and ad-provider filter helper"
```

---

## Task 9: `floatAnchor` on the frontend rates type

**Files:**
- Modify: `tsa-dev/tsa-app/services/exchangeRateApi.ts`

- [ ] **Step 1: Add the field**

In `services/exchangeRateApi.ts`, add to the `P2PRate` interface (after `stale?:`):

```ts
  /** True when this currency is eligible for a float-rate anchor (has a Bybit feed). */
  floatAnchor?: boolean;
```

- [ ] **Step 2: Verify typecheck**

Run: `cd tsa-dev/tsa-app && npx tsc --noEmit`
Expected: no new errors referencing `exchangeRateApi.ts`.

- [ ] **Step 3: Commit**

```bash
cd tsa-dev/tsa-app
git add services/exchangeRateApi.ts
git commit -m "feat(p2p): surface floatAnchor flag on frontend rate type"
```

---

# Phase C — Frontend hook & component

## Task 10: `useP2pCurrency` hook

**Files:**
- Create: `tsa-dev/tsa-app/hooks/useP2pCurrency.ts`
- Test: `tsa-dev/tsa-app/hooks/useP2pCurrency.test.ts`

This hook owns the P2P-scoped market currency: it persists a separate selection (so it doesn't mutate the app-wide display currency) and defaults from `CurrencyContext`'s currently-selected currency until the user picks one.

- [ ] **Step 1: Write the failing test**

Create `tsa-dev/tsa-app/hooks/useP2pCurrency.test.ts`:

```ts
import { resolveInitialP2pCurrency } from './useP2pCurrency';
import type { CurrencyConfig } from '../constants/currencies';

const usd: CurrencyConfig = { code: 'USD', symbol: '$', name: 'US Dollar' };
const cad: CurrencyConfig = { code: 'CAD', symbol: 'C$', name: 'Canadian Dollar' };
const ngn: CurrencyConfig = { code: 'NGN', symbol: '₦', name: 'Nigerian Naira' };

describe('resolveInitialP2pCurrency', () => {
  it('prefers a persisted code when it resolves in supported currencies', () => {
    expect(resolveInitialP2pCurrency('CAD', ngn, [usd, cad, ngn])).toEqual(cad);
  });

  it('falls back to the app currency when no persisted code', () => {
    expect(resolveInitialP2pCurrency(null, ngn, [usd, cad, ngn])).toEqual(ngn);
  });

  it('falls back to the app currency when persisted code is unknown', () => {
    expect(resolveInitialP2pCurrency('XXX', ngn, [usd, cad, ngn])).toEqual(ngn);
  });
});
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd tsa-dev/tsa-app && npx jest hooks/useP2pCurrency.test.ts`
Expected: FAIL — `resolveInitialP2pCurrency is not a function`.

- [ ] **Step 3: Implement the hook**

Create `tsa-dev/tsa-app/hooks/useP2pCurrency.ts`:

```ts
// useP2pCurrency — P2P-scoped fiat market selection.
//
// Distinct from the app-wide display currency in CurrencyContext: switching
// the P2P trading market to CAD must NOT change how the user's portfolio is
// displayed. We persist a separate key and default from the app currency
// (which itself defaults from geolocation) until the user picks one here.

import { useCallback, useEffect, useState } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useCurrency } from '../contexts/CurrencyContext';
import { CurrencyConfig } from '../constants/currencies';

const P2P_CURRENCY_KEY = 'p2p_market_currency';

/** Pure resolver (exported for tests): persisted code wins if it resolves in
 *  the supported list; otherwise fall back to the app currency. */
export function resolveInitialP2pCurrency(
  persistedCode: string | null,
  appCurrency: CurrencyConfig,
  supported: CurrencyConfig[],
): CurrencyConfig {
  if (persistedCode) {
    const match = supported.find((c) => c.code === persistedCode);
    if (match) return match;
  }
  return appCurrency;
}

export function useP2pCurrency() {
  const { currency: appCurrency, supportedCurrencies } = useCurrency();
  const [currency, setCurrencyState] = useState<CurrencyConfig>(appCurrency);
  const [userChosen, setUserChosen] = useState(false);

  // Load persisted choice once.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const saved = await AsyncStorage.getItem(P2P_CURRENCY_KEY);
        if (cancelled) return;
        if (saved) {
          setUserChosen(true);
          setCurrencyState(
            resolveInitialP2pCurrency(saved, appCurrency, supportedCurrencies),
          );
        }
      } catch (e) {
        console.warn('[useP2pCurrency] AsyncStorage read failed', e);
      }
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Until the user picks, track the app currency (and upgrade stub→full
  // metadata once rates load and supportedCurrencies fills in).
  useEffect(() => {
    if (userChosen) {
      const fresh = supportedCurrencies.find((c) => c.code === currency.code);
      if (fresh && fresh !== currency) setCurrencyState(fresh);
      return;
    }
    setCurrencyState(appCurrency);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [appCurrency, supportedCurrencies, userChosen]);

  const setCurrency = useCallback(
    async (code: string) => {
      const upper = code.toUpperCase();
      const match = supportedCurrencies.find((c) => c.code === upper);
      if (!match) return;
      setUserChosen(true);
      setCurrencyState(match);
      try {
        await AsyncStorage.setItem(P2P_CURRENCY_KEY, upper);
      } catch (e) {
        console.warn('[useP2pCurrency] AsyncStorage write failed', e);
      }
    },
    [supportedCurrencies],
  );

  return { currency, setCurrency };
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd tsa-dev/tsa-app && npx jest hooks/useP2pCurrency.test.ts`
Expected: PASS (all 3).

- [ ] **Step 5: Commit**

```bash
cd tsa-dev/tsa-app
git add hooks/useP2pCurrency.ts hooks/useP2pCurrency.test.ts
git commit -m "feat(p2p): add P2P-scoped market currency hook"
```

---

## Task 11: `PaymentMethodFilter` component

**Files:**
- Create: `tsa-dev/tsa-app/components/p2p/PaymentMethodFilter.tsx`

A controlled dropdown showing "All Payment Methods" plus the providers passed in. No data fetching — the parent supplies options derived from loaded ads.

- [ ] **Step 1: Implement the component**

Create `tsa-dev/tsa-app/components/p2p/PaymentMethodFilter.tsx`:

```tsx
// PaymentMethodFilter — controlled "All Payment Methods" dropdown for the P2P
// browse screen. Options are derived by the parent from loaded ads; selecting
// one filters the list client-side. `selected === null` means "All".

import React, { useState } from 'react';
import {
  Modal,
  Pressable,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { P2P_GOLD } from '../../constants/p2p';

const GOLD = P2P_GOLD;

export interface ProviderOption {
  provider: string;
  label: string;
}

interface Props {
  options: ProviderOption[];
  selected: string | null;
  onChange: (provider: string | null) => void;
}

const ALL_LABEL = 'All Payment Methods';

const PaymentMethodFilter: React.FC<Props> = ({ options, selected, onChange }) => {
  const [open, setOpen] = useState(false);
  const selectedLabel =
    selected === null
      ? ALL_LABEL
      : options.find((o) => o.provider === selected)?.label ?? ALL_LABEL;

  const pick = (provider: string | null) => {
    onChange(provider);
    setOpen(false);
  };

  return (
    <>
      <TouchableOpacity
        style={styles.trigger}
        onPress={() => setOpen(true)}
        activeOpacity={0.7}
      >
        <Text style={styles.triggerText} numberOfLines={1}>
          {selectedLabel}
        </Text>
        <Icon name="arrow-drop-down" size={20} color={GOLD.dark} />
      </TouchableOpacity>

      <Modal visible={open} transparent animationType="fade" onRequestClose={() => setOpen(false)}>
        <Pressable style={styles.backdrop} onPress={() => setOpen(false)}>
          <Pressable style={styles.sheet} onPress={() => {}}>
            <Text style={styles.sheetTitle}>Payment method</Text>
            <TouchableOpacity style={styles.row} onPress={() => pick(null)} activeOpacity={0.7}>
              <Text style={styles.rowText}>{ALL_LABEL}</Text>
              {selected === null && <Icon name="check" size={18} color={GOLD.dark} />}
            </TouchableOpacity>
            {options.map((o) => (
              <TouchableOpacity
                key={o.provider}
                style={styles.row}
                onPress={() => pick(o.provider)}
                activeOpacity={0.7}
              >
                <Text style={styles.rowText}>{o.label}</Text>
                {selected === o.provider && <Icon name="check" size={18} color={GOLD.dark} />}
              </TouchableOpacity>
            ))}
          </Pressable>
        </Pressable>
      </Modal>
    </>
  );
};

const styles = StyleSheet.create({
  trigger: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: GOLD.muted,
    backgroundColor: '#FFF',
    maxWidth: 200,
  },
  triggerText: { flex: 1, fontSize: 13, fontWeight: '600', color: GOLD.dark },
  backdrop: { flex: 1, backgroundColor: 'rgba(0,0,0,0.4)', justifyContent: 'center', padding: 24 },
  sheet: { backgroundColor: '#FFF', borderRadius: 14, paddingVertical: 8 },
  sheetTitle: { fontSize: 13, fontWeight: '800', color: '#000', paddingHorizontal: 16, paddingVertical: 10 },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 16,
    paddingVertical: 13,
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: '#F0F0F0',
  },
  rowText: { fontSize: 15, color: '#1A1A1A' },
});

export default PaymentMethodFilter;
```

- [ ] **Step 2: Verify typecheck**

Run: `cd tsa-dev/tsa-app && npx tsc --noEmit`
Expected: no errors in `PaymentMethodFilter.tsx`. (If `P2P_GOLD` lacks `muted`/`dark`, check `constants/p2p.ts` for the exact token names and use those — `index.tsx` already uses `GOLD.dark`, `GOLD.muted`.)

- [ ] **Step 3: Commit**

```bash
cd tsa-dev/tsa-app
git add components/p2p/PaymentMethodFilter.tsx
git commit -m "feat(p2p): add payment-method filter dropdown component"
```

---

# Phase D — Frontend screens

## Task 12: P2P browse screen — currency pill, filter, chips, de-NGN, empty state

**Files:**
- Modify: `tsa-dev/tsa-app/app/p2p/index.tsx`

This is a UI task (no unit test). Verify manually in Step 6.

- [ ] **Step 1: Update imports + add hooks/state**

At the top of `app/p2p/index.tsx`, extend imports:

```tsx
import {
  adsApi,
  distinctAdProviders,
  formatFiat,
  formatTokenAmount,
  type P2PAd,
} from '../../services/p2p';
import { P2P_GOLD } from '../../constants/p2p';
import { useUserRole } from '../../hooks/useUserRole';
import { useP2pCurrency } from '../../hooks/useP2pCurrency';
import CurrencySelector from '../../components/currency/CurrencySelector';
import PaymentMethodFilter from '../../components/p2p/PaymentMethodFilter';
```

Inside `P2PMarketScreen`, add after the existing `token` state (~33):

```tsx
  const { currency, setCurrency } = useP2pCurrency();
  const [providerFilter, setProviderFilter] = useState<string | null>(null);
```

- [ ] **Step 2: Make the list query currency-aware**

Replace the `load` callback (~42-49) with:

```tsx
  const load = useCallback(async () => {
    const res = await adsApi.list({ side: 'sell', token, fiatCurrency: currency.code });
    if (res.success && res.data) {
      setAds(res.data);
    }
    setLoading(false);
    setRefreshing(false);
  }, [token, currency.code]);
```

Reset the provider filter when currency/token changes — add below `load`:

```tsx
  useEffect(() => {
    setProviderFilter(null);
  }, [currency.code, token]);
```

- [ ] **Step 3: Derive filter options + filtered ads**

Add after the `load`/effects (before `return`):

```tsx
  const providerOptions = distinctAdProviders(ads);
  const visibleAds =
    providerFilter === null
      ? ads
      : ads.filter((a) =>
          (a.paymentMethodSummary ?? []).some((s) => s.provider === providerFilter),
        );
```

- [ ] **Step 4: Add the currency pill + filter row, fix copy**

(a) In the header row, add the currency pill next to the heading. Replace the `<Text style={styles.heading}>Buy {token}</Text>` line (~70) with:

```tsx
          <View style={styles.headingBlock}>
            <Text style={styles.heading}>Buy {token}</Text>
            <CurrencySelector variant="header" onChange={(c) => setCurrency(c.code)} />
          </View>
```

> Note: `CurrencySelector` updates the app-wide currency via its own `useCurrency().setCurrency` AND calls our `onChange`. To keep the P2P market independent we drive `useP2pCurrency` from `onChange`; the app-wide change is acceptable here since the pill is explicitly a market selector. If product later wants full independence, swap `CurrencySelector` for a P2P-local picker that reuses `supportedCurrencies` without calling the context setter.

(b) Replace the subheading (~117-120) with currency-aware copy:

```tsx
        <Text style={styles.subheading}>
          Buy {token} for {currency.code} from P2P sellers. Funds are escrowed
          on chain until the seller releases.
        </Text>
```

(c) Add the payment-method filter row just below the `tokenRow` block (after ~157, before the `loading ?` block):

```tsx
        {providerOptions.length > 0 && (
          <View style={styles.filterRow}>
            <PaymentMethodFilter
              options={providerOptions}
              selected={providerFilter}
              onChange={setProviderFilter}
            />
          </View>
        )}
```

- [ ] **Step 5: Render chips, fix price/limit currency, use `visibleAds`, fix empty state**

(a) Replace the list render block (`loading ? ... : ads.length === 0 ? ... : ads.map(...)`, ~159-170) with:

```tsx
        {loading ? (
          <ActivityIndicator size="large" color={GOLD.dark} style={{ marginTop: 60 }} />
        ) : visibleAds.length === 0 ? (
          <View style={styles.empty}>
            <Icon name="storefront" size={48} color={GOLD.muted} />
            <Text style={styles.emptyText}>
              No {token} sellers for {currency.code} yet.
            </Text>
            {isMerchant && (
              <TouchableOpacity
                style={styles.postAdBtn}
                onPress={() => router.push('/p2p/create-ad')}
                activeOpacity={0.85}
              >
                <Icon name="add" size={18} color="#000" />
                <Text style={styles.postAdText}>Post an ad</Text>
              </TouchableOpacity>
            )}
          </View>
        ) : (
          visibleAds.map((ad) => <AdCard key={ad.id} ad={ad} currencyCode={currency.code} />)
        )}
```

(b) Update `AdCard` to take a `currencyCode` and render it + chips. Change the component signature + the `priceCurrency`/limit blocks. Replace `const AdCard: React.FC<{ ad: P2PAd }> = ({ ad }) => {` with:

```tsx
const AdCard: React.FC<{ ad: P2PAd; currencyCode: string }> = ({ ad, currencyCode }) => {
```

Replace the `priceBlock` `<Text style={styles.priceCurrency}>NGN</Text>` (~220) with:

```tsx
        <Text style={styles.priceCurrency}>{currencyCode}</Text>
```

Replace the limit value line (`₦{minLimit} – ₦{maxLimit}`, ~245) with:

```tsx
            <Text style={styles.metaValue}>
              {currencyCode} {minLimit} – {maxLimit}
            </Text>
```

Add a chips row inside the card, just before the closing `</TouchableOpacity>` of `AdCard` (after the `metaGrid` view, ~249):

```tsx
      {(ad.paymentMethodSummary ?? []).length > 0 && (
        <View style={styles.chipsRow}>
          {(ad.paymentMethodSummary ?? []).slice(0, 3).map((s) => (
            <View key={s.provider} style={styles.payChip}>
              <Text style={styles.payChipText}>{s.label}</Text>
            </View>
          ))}
        </View>
      )}
```

- [ ] **Step 6: Add the new styles**

Add these keys to the `StyleSheet.create({...})` object:

```tsx
  headingBlock: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    flex: 1,
    gap: 10,
  },
  filterRow: { flexDirection: 'row', marginBottom: 14 },
  postAdBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    marginTop: 16,
    backgroundColor: GOLD.primary,
    paddingHorizontal: 18,
    paddingVertical: 10,
    borderRadius: 10,
  },
  postAdText: { fontWeight: '800', color: '#000', fontSize: 14 },
  chipsRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 6, marginTop: 12 },
  payChip: {
    backgroundColor: GOLD.light,
    borderRadius: 6,
    paddingHorizontal: 8,
    paddingVertical: 3,
  },
  payChipText: { fontSize: 11, fontWeight: '600', color: GOLD.dark },
```

> The existing `headerRow` lays out heading + actions horizontally. Wrapping the heading + pill in `headingBlock` keeps the action icons to the right. If the layout looks cramped, move the `CurrencySelector` to its own row under `headerRow` — verify visually in Step 7.

- [ ] **Step 7: Verify typecheck + manual run**

Run: `cd tsa-dev/tsa-app && npx tsc --noEmit`
Expected: no errors in `app/p2p/index.tsx`.

Then run the app and verify on the P2P screen:
```bash
cd tsa-dev/tsa-app && npx expo start
```
- The currency pill shows and opens the picker; selecting CAD re-lists ads for CAD.
- Ads show their price/limit in the selected currency and payment chips.
- The "All Payment Methods" filter appears when ads have payment methods; selecting a provider narrows the list; "All" restores it.
- A currency with no ads shows "No {token} sellers for {currency.code} yet" + a Post-an-ad button (merchants only).

- [ ] **Step 8: Commit**

```bash
cd tsa-dev/tsa-app
git add app/p2p/index.tsx
git commit -m "feat(p2p): multi-currency browse with payment filter, chips, and clean empty state"
```

---

## Task 13: Create-ad — currency selector + multi-currency rate/float gating + de-NGN

**Files:**
- Modify: `tsa-dev/tsa-app/app/p2p/create-ad.tsx`

- [ ] **Step 1: Add imports + currency state**

Add imports:

```tsx
import { useCurrency } from '../../contexts/CurrencyContext';
import CurrencySelector from '../../components/currency/CurrencySelector';
```

Inside `CreateAdScreen`, add after the `token` state (~52):

```tsx
  const { currency, setCurrency } = useCurrency();
```

- [ ] **Step 2: Filter payment methods by the chosen currency**

Replace the PM-loading effect body (~80-83) so it filters by the selected currency, and re-runs when currency changes. Replace:

```tsx
        if (res.success && res.data) {
          setPaymentMethods(res.data.filter((m) => m.currency === 'NGN'));
        }
```
with:
```tsx
        if (res.success && res.data) {
          setPaymentMethods(res.data.filter((m) => m.currency === currency.code));
        }
```

And change that effect's dependency array from `[]` to `[currency.code]` (the `useEffect(() => { ... }, [])` wrapping the two IIFEs at ~104). Also clear stale selections when the currency changes — add a new effect:

```tsx
  useEffect(() => {
    setSelectedPmIds([]);
  }, [currency.code]);
```

- [ ] **Step 3: Read the selected currency's rate + float eligibility**

Replace the suggested-rate effect body (~92-97) with currency-aware logic:

```tsx
      try {
        const res = await exchangeRateService.getRates();
        const row = res.rates?.[currency.code];
        if (row) {
          setSuggestedRate(row.binanceSell);
          // Float ads need a Bybit anchor; gate on eligibility + freshness.
          setRateStale(!!row.stale || row.floatAnchor === false);
        } else {
          setSuggestedRate(null);
          setRateStale(true);
        }
      } catch (err) {
        console.warn('[create-ad] exchangeRateService.getRates failed', err);
      }
```

Also add `currency.code` to that effect's dependency array.

- [ ] **Step 4: Force fixed rate when float isn't available; add the currency selector UI**

(a) When the selected currency is not float-eligible, snap the UI to fixed. Add an effect:

```tsx
  useEffect(() => {
    if (rateStale && rateType === 'float') setRateType('fixed');
  }, [rateStale, rateType]);
```

(b) Add the currency selector below the Token row. After the token `</View>` (~362), insert:

```tsx
          <Text style={styles.label}>Currency</Text>
          <View style={styles.currencyRow}>
            <CurrencySelector variant="header" onChange={(c) => setCurrency(c.code)} />
            <Text style={styles.currencyHint}>Buyers pay in {currency.code}</Text>
          </View>
```

(c) Add the style keys:

```tsx
  currencyRow: { flexDirection: 'row', alignItems: 'center', gap: 12 },
  currencyHint: { fontSize: 12, color: '#666' },
```

- [ ] **Step 5: De-NGN the copy and payload**

Replace each NGN/₦ literal with `currency.code` / `currency.symbol`:

- Subheading (~338): `You're offering {token.symbol} for {currency.code}. Publishing is off-chain — ...`
- Fixed-rate label (~426): `Rate ({currency.code} per 1 {token.symbol})`
- Fixed-rate input prefix `₦` (~428): `{currency.symbol}`
- Bybit hint (~444): `Current Bybit P2P sell rate: {currency.symbol}{suggestedRate.toFixed(2)}`
- Float anchor hint (~480): `Anchor: Bybit P2P sell · Market: {currency.symbol}{suggestedRate.toFixed(2)}` and `Your rate: {currency.symbol}{effectiveRate.toFixed(2)}`
- Min/Max labels (~500, ~514): `Min order ({currency.code})` / `Max order ({currency.code})`
- Preview "At" value (~583): `{currency.symbol}{effectiveRate != null ? effectiveRate.toFixed(2) : '0'} / {token.symbol}`
- Preview "Total value" (~590): `{currency.symbol}{previewFiat.toLocaleString(undefined, { maximumFractionDigits: 2 })}`

Replace the `basePayload.fiatCurrency` (~241) from `'NGN'` to `currency.code`:

```tsx
        fiatCurrency: currency.code,
```

When the currency isn't float-eligible, also reflect it in the float-unavailable hint (~473) so the seller understands why:

```tsx
                    {rateStale
                      ? `Live rate / market feed unavailable for ${currency.code} — float ads can't be published. Use a fixed rate.`
                      : ''}
```
(Leave the existing fixed-text if you prefer; the key behavior is the `valid` gate already blocks float when `rateStale`.)

- [ ] **Step 6: Verify typecheck + manual run**

Run: `cd tsa-dev/tsa-app && npx tsc --noEmit`
Expected: no errors in `create-ad.tsx`.

Manual: open Create ad, switch currency to a non-NGN one (e.g. CAD):
- Payment-method list filters to that currency (empty → "Add a payment method" CTA).
- For a currency with a Bybit feed, float is offered; for one without (`floatAnchor === false`), float is blocked and the UI stays on Fixed.
- All copy shows the selected currency's code/symbol, not NGN/₦.

- [ ] **Step 7: Commit**

```bash
cd tsa-dev/tsa-app
git add app/p2p/create-ad.tsx
git commit -m "feat(p2p): currency-aware create-ad with float gating and de-NGN copy"
```

---

## Task 14: Add-payment-method — provider picker + currency awareness

**Files:**
- Modify: `tsa-dev/tsa-app/app/p2p/payment-methods/new.tsx`

This screen is currently NG/NUBAN-specific. Add a provider picker (from the catalog) and make country/currency follow the provider so non-NG methods (PayPal, Interac, etc.) can be added. Keep the Nomba name-enquiry path for NG bank methods only.

- [ ] **Step 1: Add imports + provider state**

Add imports:

```tsx
import { paymentMethodsApi, type PaymentProvider } from '../../../services/p2p';
import { useCurrency } from '../../../contexts/CurrencyContext';
```
(Replace the existing `import { paymentMethodsApi } from '../../../services/p2p';` line.)

Add state inside `NewPaymentMethodScreen` (after `type` state ~44):

```tsx
  const { currency } = useCurrency();
  const [providers, setProviders] = useState<PaymentProvider[]>([]);
  const [provider, setProvider] = useState<PaymentProvider | null>(null);
  const [showProviderPicker, setShowProviderPicker] = useState(false);
```

- [ ] **Step 2: Load the provider catalog on mount**

Add an effect:

```tsx
  useEffect(() => {
    (async () => {
      const res = await paymentMethodsApi.providers();
      if (res.success && res.data) {
        setProviders(res.data);
        setProvider((prev) => prev ?? res.data!.find((p) => p.slug === 'bank_transfer') ?? res.data![0]);
      }
    })();
  }, []);
```

- [ ] **Step 3: Gate Nomba name-enquiry to NG bank_transfer/kuda only**

The auto-resolve effect currently fires for any `type === 'bank'`. Restrict it to NG bank providers. Change the guard at the top of the resolve effect (~83) from:

```tsx
    if (type !== 'bank' || !bank || accountNumber.length !== 10) {
```
to:
```tsx
    const isNgBank = provider?.type === 'bank' && currency.code === 'NGN' && provider?.slug !== 'interac';
    if (!isNgBank || !bank || accountNumber.length !== 10) {
```

- [ ] **Step 4: Add the provider picker UI + adjust the bank field**

Replace the `typeRow` block (the bank/mobile_money chips, ~144-164) with a provider trigger:

```tsx
          <Text style={styles.label}>Provider</Text>
          <TouchableOpacity
            style={styles.input}
            onPress={() => setShowProviderPicker(true)}
            activeOpacity={0.7}
          >
            <Text style={[styles.inputText, !provider && styles.placeholder]}>
              {provider?.label || 'Choose a provider'}
            </Text>
            <Icon name="arrow-drop-down" size={24} color={GOLD.dark} />
          </TouchableOpacity>
```

Only show the NG bank dropdown when the provider is an NG bank; otherwise let the user free-type a bank/wallet name. Replace the `Bank` label + bank `TouchableOpacity` (~166-180) with:

```tsx
          {provider?.type === 'bank' && currency.code === 'NGN' && provider?.slug !== 'interac' ? (
            <>
              <Text style={styles.label}>Bank</Text>
              <TouchableOpacity
                style={styles.input}
                onPress={() => setShowBankPicker(true)}
                activeOpacity={0.7}
              >
                <Text style={[styles.inputText, !bank && styles.placeholder]}>
                  {bank?.name || 'Choose a bank'}
                </Text>
                {banksLoading ? (
                  <ActivityIndicator size="small" color={GOLD.dark} />
                ) : (
                  <Icon name="arrow-drop-down" size={24} color={GOLD.dark} />
                )}
              </TouchableOpacity>
            </>
          ) : (
            <>
              <Text style={styles.label}>{provider?.label || 'Provider'} name / handle</Text>
              <View style={styles.input}>
                <TextInput
                  style={styles.inputField}
                  placeholder={provider?.label ? `e.g. ${provider.label}` : 'Provider name'}
                  placeholderTextColor="#999"
                  value={bank?.name ?? ''}
                  onChangeText={(v) => setBank({ code: '', name: v })}
                />
              </View>
            </>
          )}
```

The account-number label should follow the provider's identifier label. Replace the `Account number` label (~182):

```tsx
          <Text style={styles.label}>{provider?.identifierLabel || 'Account number'}</Text>
```

- [ ] **Step 5: Update validation + submit payload**

Replace `valid` (~111-114):

```tsx
  const valid =
    !!provider &&
    !!bank?.name &&
    accountNumber.trim().length >= 4 &&
    accountName.trim().length >= 2;
```

Replace the `paymentMethodsApi.create({...})` call (~119-128) to send `provider`, the selected currency, and a country derived from currency (NG for NGN, else empty string is rejected by backend `binding:"required"` — send the currency code's region or fall back to the currency code itself; backend only stores it):

```tsx
    const res = await paymentMethodsApi.create({
      provider: provider!.slug,
      country: currency.code === 'NGN' ? 'NG' : currency.code.slice(0, 2),
      currency: currency.code,
      bankName: bank!.name,
      bankCode: bank!.code || undefined,
      accountNumber: accountNumber.trim(),
      accountName: accountName.trim(),
      isDefault,
    });
```

- [ ] **Step 6: Add the provider picker modal**

Reuse the existing `BankPickerModal` pattern. Add a simple provider modal before the closing `</SafeAreaView>` (next to `<BankPickerModal .../>`, ~281):

```tsx
      <Modal
        visible={showProviderPicker}
        animationType="slide"
        transparent
        onRequestClose={() => setShowProviderPicker(false)}
      >
        <Pressable style={styles.modalBackdrop} onPress={() => setShowProviderPicker(false)}>
          <Pressable style={styles.modalSheet} onPress={() => {}}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>Choose a provider</Text>
              <TouchableOpacity onPress={() => setShowProviderPicker(false)} hitSlop={10}>
                <Icon name="close" size={24} color="#333" />
              </TouchableOpacity>
            </View>
            <FlatList
              data={providers}
              keyExtractor={(p) => p.slug}
              keyboardShouldPersistTaps="handled"
              renderItem={({ item }) => {
                const selected = item.slug === provider?.slug;
                return (
                  <TouchableOpacity
                    style={[styles.pickerRow, selected && styles.pickerRowSelected]}
                    onPress={() => {
                      setProvider(item);
                      setBank(null);
                      setShowProviderPicker(false);
                    }}
                    activeOpacity={0.7}
                  >
                    <Text style={styles.pickerText}>{item.label}</Text>
                    {selected && <Icon name="check" size={20} color={GOLD.dark} />}
                  </TouchableOpacity>
                );
              }}
            />
          </Pressable>
        </Pressable>
      </Modal>
```

- [ ] **Step 7: Verify typecheck + manual run**

Run: `cd tsa-dev/tsa-app && npx tsc --noEmit`
Expected: no errors in `new.tsx`.

Manual: from the P2P screen set currency to NGN, add a bank method (NUBAN auto-resolve still works); set currency to CAD, add an Interac/Bank method by free-typing the name; add a PayPal (wallet) method and confirm it saves and appears as a chip on a created ad.

- [ ] **Step 8: Commit**

```bash
cd tsa-dev/tsa-app
git add app/p2p/payment-methods/new.tsx
git commit -m "feat(p2p): provider picker + multi-currency add-payment-method"
```

---

# Phase E — Full verification

## Task 15: End-to-end build, lint, and manual pass

**Files:** none (verification only)

- [ ] **Step 1: Backend build + tests**

Run: `cd tsa-dev/tsa-api-go && go build ./... && go test ./internal/services ./internal/handlers -run 'P2P|Provider|FloatAnchor|PaymentSummary' -v`
Expected: build OK; tests PASS.

- [ ] **Step 2: Frontend tests + typecheck + lint**

Run: `cd tsa-dev/tsa-app && npx jest services/p2p.test.ts hooks/useP2pCurrency.test.ts && npx tsc --noEmit && npm run lint`
Expected: tests PASS; no type errors; lint clean (or pre-existing warnings only).

- [ ] **Step 3: Manual E2E (Expo)**

Run: `cd tsa-dev/tsa-app && npx expo start`
Walk the golden path:
1. P2P screen defaults to your country currency (geolocation). Pill switches markets (NGN ↔ CAD ↔ USD); ad list refreshes per currency.
2. Ads render price/limits in the selected currency with payment chips; "All Payment Methods" filter narrows by provider and resets to All.
3. An empty currency shows "No {token} sellers for {code} yet" + Post-an-ad (merchant).
4. Create-ad: currency selector filters payment methods; float disabled for non-Bybit currencies; copy shows the right symbol.
5. Add-payment-method: NG bank auto-resolves; CAD/PayPal methods save via free-type + provider picker.
6. Open an existing NGN ad detail + open a trade to confirm no regression in the buy flow.

- [ ] **Step 4: Final summary**

Confirm all tasks committed (`git log --oneline` in each repo) and report the manual-verification results (UI correctness can only be confirmed by running the app — state explicitly if any step couldn't be exercised).

---

## Self-review notes (author)

- **Spec coverage:** currency selector (T10, T12), payment provider model + filter + chips (T1–T6, T8, T11, T12), de-NGN (T12, T13), empty state (T12), float gating (T7, T9, T13), create-ad currency (T13), provider picker on add-method (T14). All spec sections map to a task.
- **Client-side filtering** deviates from the spec's optional backend `provider` param — this is the spec's explicitly-allowed "derived from the list response" path; documented at top.
- **Type consistency:** `paymentMethodSummary: {provider,label}[]` is the same shape in Go (`PaymentMethodSummary`), the frontend `P2PAd`, `distinctAdProviders`, and `PaymentMethodFilter` (`ProviderOption`). `provider` slug strings flow unchanged end to end. `floatAnchor` is `bool`/`boolean?` in both layers.
- **No-account-number guarantee:** the list path only ever selects `id, provider` (T6 `LoadProvidersByMethodIDs`) and emits `{provider,label}` — account fields are never loaded for the list.
