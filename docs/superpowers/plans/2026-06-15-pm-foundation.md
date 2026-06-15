# PM Foundation Implementation Plan (Phase 1, Plan 1 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the PM (Payment Merchant) domain foundation — the data model, the payer-name match logic, the Nomba gateway boundary, and the marketplace PM filter — with zero dependency on Nomba's (still-unverified) wire API or on-chain signing.

**Architecture:** PM is automated P2P. This plan adds only additive data (a `PaymentMerchant` entity + `pm_id`/Nomba fields on ads/trades), one pure matching function, and an interface that the next plan implements against the real Nomba API. No existing P2P behavior changes.

**Tech Stack:** Go 1.24, GORM/PostgreSQL (SQLite in-memory for tests), `github.com/google/uuid`.

**Spec:** `docs/superpowers/specs/2026-06-15-pm-payment-merchant-design.md` (§4 data model, §5 matching, §7 gateway).

---

## File Structure

- Create `internal/models/payment_merchant.go` — the `PaymentMerchant` entity + status constants.
- Modify `internal/models/p2p_ad.go` — add `PMID *uuid.UUID`.
- Modify `internal/models/p2p_trade.go` — add Nomba/payout fields.
- Modify `internal/config/database.go` — register `PaymentMerchant` in AutoMigrate.
- Create `internal/services/pm_match.go` — `MatchesPayerName` (pure name-match).
- Create `internal/services/pm_match_test.go` — its tests.
- Create `internal/services/nomba_gateway.go` — the `NombaGateway` interface + event DTOs (boundary only, no impl).

Each task is self-contained and committable.

---

## Task 1: `PaymentMerchant` model + migration

**Files:**
- Create: `internal/models/payment_merchant.go`
- Modify: `internal/config/database.go` (AutoMigrate list)
- Test: `internal/models/payment_merchant_test.go`

- [ ] **Step 1: Write the failing test**

```go
package models

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPaymentMerchant_PersistsAndDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&PaymentMerchant{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pm := PaymentMerchant{
		ID:                   uuid.New(),
		UserID:               uuid.New(),
		Name:                 "TSA Desk",
		NombaAccountID:       "acct_123",
		InboundAccountNumber: "0123456789",
		InboundBank:          "Nomba",
		OperatorWalletAddress: "0x0000000000000000000000000000000000000001",
	}
	if err := db.Create(&pm).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got PaymentMerchant
	if err := db.First(&got, "id = ?", pm.ID).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Status != PaymentMerchantStatusPending {
		t.Fatalf("default status = %q, want %q", got.Status, PaymentMerchantStatusPending)
	}
	if !got.IsActive() {
		// pending is not active
	} else {
		t.Fatal("pending merchant must not be active")
	}
	got.Status = PaymentMerchantStatusActive
	if !got.IsActive() {
		t.Fatal("active merchant must report IsActive()")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/models/ -run TestPaymentMerchant_PersistsAndDefaults -v`
Expected: FAIL — `undefined: PaymentMerchant`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/models/payment_merchant.go`:

```go
package models

import (
	"time"

	"github.com/google/uuid"
)

// Payment-merchant status values.
const (
	PaymentMerchantStatusPending = "pending" // approved but not yet configured/activated
	PaymentMerchantStatusActive  = "active"  // live: automation runs
	PaymentMerchantStatusPaused  = "paused"  // kill switch: no new trades, in-flight drain
)

// PaymentMerchant is a corporate body that provides AUTOMATED P2P liquidity,
// settled via its own Nomba account (design §4). Phase 1 holds exactly one row
// (TSA), seeded manually. Secrets (Nomba client id/secret, the operator private
// key) live in a secret manager — only references/addresses are stored here.
type PaymentMerchant struct {
	ID     uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	UserID uuid.UUID `gorm:"type:uuid;not null;index" json:"userId"`
	Name   string    `gorm:"size:128;not null" json:"name"`

	Status string `gorm:"size:16;not null;default:'pending'" json:"status"`

	// Nomba (per-PM). NombaCredsRef points at the secret-store entry holding the
	// client id/secret; the raw creds are NEVER stored in this table.
	NombaAccountID       string `gorm:"size:64" json:"nombaAccountId"`
	NombaCredsRef        string `gorm:"size:128" json:"-"`
	InboundAccountNumber string `gorm:"size:32" json:"inboundAccountNumber"`
	InboundBank          string `gorm:"size:64" json:"inboundBank"`

	// OperatorWalletAddress is the EVM address whose key (held in the secret
	// manager) the backend uses to sign release/deposit. The key is NEVER stored.
	OperatorWalletAddress string `gorm:"size:42" json:"operatorWalletAddress"`

	// Caps (USD). 0 = no cap.
	PerTradeCapUSD float64 `gorm:"not null;default:0" json:"perTradeCapUsd"`
	DailyCapUSD    float64 `gorm:"not null;default:0" json:"dailyCapUsd"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// IsActive reports whether the merchant's automation should run.
func (m *PaymentMerchant) IsActive() bool {
	return m != nil && m.Status == PaymentMerchantStatusActive
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/models/ -run TestPaymentMerchant_PersistsAndDefaults -v`
Expected: PASS.

- [ ] **Step 5: Register in AutoMigrate**

In `internal/config/database.go`, in the `DB.AutoMigrate(...)` list (next to the reward models added earlier), add:

```go
		&models.PaymentMerchant{},
```

- [ ] **Step 6: Build + commit**

Run: `cd tsa-api-go && go build ./... && go test ./internal/models/ -run TestPaymentMerchant -v`
Expected: build OK, PASS.

```bash
git add tsa-api-go/internal/models/payment_merchant.go tsa-api-go/internal/models/payment_merchant_test.go tsa-api-go/internal/config/database.go
git commit -m "feat(pm): PaymentMerchant model + migration"
```

---

## Task 2: P2P ad/trade PM fields

**Files:**
- Modify: `internal/models/p2p_ad.go` (add `PMID`)
- Modify: `internal/models/p2p_trade.go` (add Nomba/payout fields)
- Test: `internal/models/p2p_pm_fields_test.go`

- [ ] **Step 1: Write the failing test**

```go
package models

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestP2PPMFields_Persist(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err := db.AutoMigrate(&P2PAd{}, &P2PTrade{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pmID := uuid.New()
	ad := P2PAd{ID: uuid.New(), SellerID: uuid.New(), Side: P2PAdSideSell, Token: "USDT", PMID: &pmID}
	if err := db.Create(&ad).Error; err != nil {
		t.Fatalf("create ad: %v", err)
	}
	var gotAd P2PAd
	db.First(&gotAd, "id = ?", ad.ID)
	if gotAd.PMID == nil || *gotAd.PMID != pmID {
		t.Fatalf("ad.PMID = %v, want %v", gotAd.PMID, pmID)
	}

	tr := P2PTrade{
		ID: uuid.New(), AdID: ad.ID, BuyerID: uuid.New(), SellerID: uuid.New(),
		Token: "USDT", TokenAmount: "1", FiatAmount: "1500", FiatCurrency: "NGN",
		Rate: "1500", PaymentMethodID: uuid.New(),
		PaymentRef: "TSA-7F3A", NombaPayoutID: "po_1",
		PayoutAccountNumber: "0123456789", PayoutBank: "044", PayoutAccountName: "JOHN DOE",
	}
	if err := db.Create(&tr).Error; err != nil {
		t.Fatalf("create trade: %v", err)
	}
	var gotTr P2PTrade
	db.First(&gotTr, "id = ?", tr.ID)
	if gotTr.PaymentRef != "TSA-7F3A" || gotTr.PayoutAccountName != "JOHN DOE" {
		t.Fatalf("trade fields not persisted: %+v", gotTr)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/models/ -run TestP2PPMFields_Persist -v`
Expected: FAIL — `unknown field 'PMID'` / `'PaymentRef'`.

- [ ] **Step 3: Add the fields**

In `internal/models/p2p_ad.go`, inside the `P2PAd` struct (near `Status`), add:

```go
	// PMID links the ad to a PaymentMerchant when set; nil = a normal (manual)
	// P2P ad. This is the marketplace "PM"/automated filter key (design §4).
	PMID *uuid.UUID `gorm:"type:uuid;index" json:"pmId,omitempty"`
```

In `internal/models/p2p_trade.go`, inside the `P2PTrade` struct (near `ProofImageURL`), add:

```go
	// PM automation links (design §4). Empty on manual P2P trades.
	PaymentRef          string `gorm:"size:32" json:"paymentRef,omitempty"`           // optional buyer reference aid
	NombaInboundEventID string `gorm:"size:64" json:"nombaInboundEventId,omitempty"` // matched inbound webhook (buy)
	NombaPayoutID       string `gorm:"size:64" json:"nombaPayoutId,omitempty"`       // Nomba payout reference (sell)
	PayoutAccountNumber string `gorm:"size:32" json:"payoutAccountNumber,omitempty"` // user's KYC-name-verified bank (sell)
	PayoutBank          string `gorm:"size:64" json:"payoutBank,omitempty"`
	PayoutAccountName   string `gorm:"size:128" json:"payoutAccountName,omitempty"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/models/ -run TestP2PPMFields_Persist -v`
Expected: PASS.

- [ ] **Step 5: Build + commit**

Run: `cd tsa-api-go && go build ./...`
Expected: build OK.

```bash
git add tsa-api-go/internal/models/p2p_ad.go tsa-api-go/internal/models/p2p_trade.go tsa-api-go/internal/models/p2p_pm_fields_test.go
git commit -m "feat(pm): pm_id on ads + Nomba/payout fields on trades"
```

---

## Task 3: Payer-name match function (the buy-flow auto-release gate)

The auto-release decision (design §5) hinges on `sender bank-account name ≈ buyer KYC name`. This is a pure, well-bounded function — implement and test it in isolation. Rule: normalize to lowercase letter-only tokens, drop single-letter initials; **all but at most one** KYC token must appear in the sender tokens, and at least **two** tokens must match (so a single common first name can't auto-pass). A 1-token KYC name requires an exact token match.

**Files:**
- Create: `internal/services/pm_match.go`
- Test: `internal/services/pm_match_test.go`

- [ ] **Step 1: Write the failing test**

```go
package services

import "testing"

func TestMatchesPayerName(t *testing.T) {
	cases := []struct {
		sender, kyc string
		want        bool
		note        string
	}{
		{"ADEYEMI JOHN OLU", "John Adeyemi", true, "reordered, extra middle name"},
		{"John A. Doe", "JOHN DOE", true, "middle initial dropped"},
		{"Doe John", "John Doe", true, "reordered two tokens"},
		{"JANE SMITH", "John Doe", false, "different person"},
		{"John", "John Doe", false, "only first name matches (need 2)"},
		{"John Doe Holdings Ltd", "John Doe", true, "extra business tokens, both names present"},
		{"Mary", "Mary", true, "single-token exact"},
		{"Mary", "Maria", false, "single-token mismatch"},
		{"", "John Doe", false, "empty sender"},
		{"John Doe", "", false, "empty kyc"},
	}
	for _, c := range cases {
		if got := MatchesPayerName(c.sender, c.kyc); got != c.want {
			t.Errorf("MatchesPayerName(%q, %q) = %v, want %v (%s)", c.sender, c.kyc, got, c.want, c.note)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/services/ -run TestMatchesPayerName -v`
Expected: FAIL — `undefined: MatchesPayerName`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/services/pm_match.go`:

```go
package services

import "strings"

// nameTokens lowercases s, keeps only letter runs as tokens, and drops
// single-letter tokens (initials like "A.").
func nameTokens(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z')
	}) {
		if len(f) > 1 {
			out = append(out, f)
		}
	}
	return out
}

// MatchesPayerName reports whether a bank sender name matches a KYC name closely
// enough to AUTO-release a PM buy trade (design §5). Conservative by intent: a
// non-match routes to the PM manual-release queue, not a failure.
//
// Rule: normalize both to letter-only tokens (single letters dropped). The KYC
// name must have >= 1 token. If it has a single token, require an exact token
// match. Otherwise require at least 2 KYC tokens present in the sender set AND
// at most one KYC token missing (tolerates one extra/typo'd middle name).
func MatchesPayerName(sender, kyc string) bool {
	st := nameTokens(sender)
	kt := nameTokens(kyc)
	if len(st) == 0 || len(kt) == 0 {
		return false
	}
	senderSet := make(map[string]bool, len(st))
	for _, t := range st {
		senderSet[t] = true
	}
	matched := 0
	for _, t := range kt {
		if senderSet[t] {
			matched++
		}
	}
	if len(kt) == 1 {
		return matched == 1
	}
	return matched >= 2 && matched >= len(kt)-1
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/services/ -run TestMatchesPayerName -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/services/pm_match.go tsa-api-go/internal/services/pm_match_test.go
git commit -m "feat(pm): payer-name match (buy-flow auto-release gate)"
```

---

## Task 4: `NombaGateway` interface + event DTOs (the boundary)

The automation layer (Plan 2) codes against this interface; Plan 2 implements it against the real Nomba API once §12 is confirmed. Defining it now lets the orchestration be built and tested against a mock without the wire format.

**Files:**
- Create: `internal/services/nomba_gateway.go`
- Test: `internal/services/nomba_gateway_test.go`

- [ ] **Step 1: Write the failing test (a compile-time conformance check + mock)**

```go
package services

import (
	"context"
	"testing"
)

// mockGateway is the test double Plan 2's orchestration will reuse.
type mockGateway struct {
	payoutID  string
	payoutErr error
}

func (m *mockGateway) InitiatePayout(_ context.Context, _ PayoutRequest) (string, error) {
	return m.payoutID, m.payoutErr
}

func TestNombaGateway_MockConforms(t *testing.T) {
	var g NombaGateway = &mockGateway{payoutID: "po_1"}
	id, err := g.InitiatePayout(context.Background(), PayoutRequest{
		BankCode: "044", AccountNumber: "0123456789", AmountNGN: "1500.00", Reference: "t-1",
	})
	if err != nil || id != "po_1" {
		t.Fatalf("InitiatePayout = (%q, %v), want (po_1, nil)", id, err)
	}
	// DTOs the webhook processor will consume must exist with these fields.
	in := InboundPayment{SenderAccountName: "JOHN DOE", SenderAccountNumber: "0123", AmountNGN: "1500.00", DestinationAccount: "acct_123", EventID: "ev_1", Status: "settled"}
	ps := PayoutStatus{PayoutID: "po_1", Status: "success", EventID: "ev_2"}
	if in.Status != "settled" || ps.Status != "success" {
		t.Fatal("DTO fields unexpectedly empty")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/services/ -run TestNombaGateway_MockConforms -v`
Expected: FAIL — `undefined: NombaGateway` / `PayoutRequest` / `InboundPayment` / `PayoutStatus`.

- [ ] **Step 3: Write the interface + DTOs**

Create `internal/services/nomba_gateway.go`:

```go
package services

import "context"

// NombaGateway is the per-PM boundary to Nomba. Plan 2 implements it against the
// live API (payout endpoint + webhook payloads + signature) once design §12 is
// confirmed; the automation orchestration codes against this interface only.
type NombaGateway interface {
	// InitiatePayout sends NGN to a bank account from the PM's Nomba account and
	// returns Nomba's payout id. Status arrives later via a PayoutStatus webhook.
	InitiatePayout(ctx context.Context, req PayoutRequest) (payoutID string, err error)
}

// PayoutRequest is a sell-flow naira payout to the user's bank.
type PayoutRequest struct {
	BankCode      string
	AccountNumber string
	AmountNGN     string // decimal string, e.g. "1500.00"
	Reference     string // our trade reference (idempotency)
}

// InboundPayment is a normalized inbound naira credit (buy flow). The webhook
// receiver (Plan 2) parses Nomba's raw payload into this. Status is "settled"
// when the credit is final (we only auto-release on settled — design §9).
type InboundPayment struct {
	EventID             string
	DestinationAccount  string // identifies which PM was paid
	SenderAccountName   string
	SenderAccountNumber string
	AmountNGN           string
	Status              string // "settled" | "pending" | ...
}

// PayoutStatus is a normalized payout-result webhook (sell flow).
type PayoutStatus struct {
	EventID  string
	PayoutID string
	Status   string // "success" | "failed" | "pending"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/services/ -run TestNombaGateway_MockConforms -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tsa-api-go/internal/services/nomba_gateway.go tsa-api-go/internal/services/nomba_gateway_test.go
git commit -m "feat(pm): NombaGateway interface + event DTOs (boundary)"
```

---

## Task 5: Marketplace PM filter

Let users filter the P2P ad list to PM (automated) ads via a `?pm=true|false` query param. Exact integration points: `AdFilter` + `ListAds` at `internal/services/p2p_trade_service.go:2862-2914`; handler `ListAds` at `internal/handlers/p2p_handler.go:870-898`.

**Files:**
- Modify: `internal/services/p2p_trade_service.go` (`AdFilter` struct + `ListAds` query)
- Modify: `internal/handlers/p2p_handler.go` (`ListAds` param parsing)
- Test: `internal/services/p2p_pm_filter_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/services/p2p_pm_filter_test.go` (package `services` — can construct `&P2PTradeService{db: db}` since `ListAds` only reads `s.db`):

```go
package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestListAds_PMFilter(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&models.P2PAd{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pmID := uuid.New()
	// PM ad + a normal ad, both active/same token/side.
	pmAd := models.P2PAd{ID: uuid.New(), SellerID: uuid.New(), Side: models.P2PAdSideSell, Token: "USDT", Status: models.P2PAdStatusActive, PMID: &pmID}
	plainAd := models.P2PAd{ID: uuid.New(), SellerID: uuid.New(), Side: models.P2PAdSideSell, Token: "USDT", Status: models.P2PAdStatusActive}
	if err := db.Create(&pmAd).Error; err != nil {
		t.Fatalf("create pm ad: %v", err)
	}
	if err := db.Create(&plainAd).Error; err != nil {
		t.Fatalf("create plain ad: %v", err)
	}

	svc := &P2PTradeService{db: db}
	yes := true
	pmOnly, err := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive, PM: &yes})
	if err != nil {
		t.Fatalf("list pm: %v", err)
	}
	if len(pmOnly) != 1 || pmOnly[0].PMID == nil {
		t.Fatalf("pm=true returned %d ads, want 1 PM ad", len(pmOnly))
	}
	no := false
	plainOnly, _ := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive, PM: &no})
	if len(plainOnly) != 1 || plainOnly[0].PMID != nil {
		t.Fatalf("pm=false returned %d ads, want 1 non-PM ad", len(plainOnly))
	}
	all, _ := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive})
	if len(all) != 2 {
		t.Fatalf("no filter returned %d ads, want 2", len(all))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd tsa-api-go && go test ./internal/services/ -run TestListAds_PMFilter -v`
Expected: FAIL — `unknown field 'PM' in struct literal of type services.AdFilter`.

- [ ] **Step 3: Add `PM` to `AdFilter`**

In `internal/services/p2p_trade_service.go`, in the `AdFilter` struct (line ~2862), add after `ChainID int64`:

```go
	// PM filters by automated PaymentMerchant ads: true = only PM ads (pm_id set),
	// false = only normal ads (pm_id null), nil = both.
	PM *bool
```

- [ ] **Step 4: Add the query clause in `ListAds`**

In `ListAds`, after the `if f.ChainID != 0 { ... }` block (line ~2903) and before the `limit :=` line, add:

```go
	if f.PM != nil {
		if *f.PM {
			q = q.Where("pm_id IS NOT NULL")
		} else {
			q = q.Where("pm_id IS NULL")
		}
	}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd tsa-api-go && go test ./internal/services/ -run TestListAds_PMFilter -v`
Expected: PASS.

- [ ] **Step 6: Thread the handler query param**

In `internal/handlers/p2p_handler.go`, in `ListAds` (line ~870), after the `if cid := c.Query("chainId"); cid != "" { ... }` block (line ~893) and before the `if f.Status == ""` block, add:

```go
	if pm := c.Query("pm"); pm != "" {
		b := pm == "true" || pm == "1"
		f.PM = &b
	}
```

- [ ] **Step 7: Build + commit**

Run: `cd tsa-api-go && go build ./... && go test ./internal/services/ -run TestListAds_PMFilter -v`
Expected: build OK, PASS.

```bash
git add tsa-api-go/internal/services/p2p_trade_service.go tsa-api-go/internal/handlers/p2p_handler.go tsa-api-go/internal/services/p2p_pm_filter_test.go
git commit -m "feat(pm): marketplace PM filter (?pm=) on the P2P ad list"
```

---

## Done / Next

After Task 5: `go build ./...`, `go vet ./...`, and `go test ./internal/models/ ./internal/services/` are green. The PM foundation is in place with no behavior change to existing P2P.

**Plan 2 (next, gated on confirming Nomba's API — design §12):** implement `NombaGateway` against the live Nomba API (payout, inbound/payout webhook parsing, HMAC verify), the `POST /api/webhooks/nomba` route + `WebhookEvent` dedup, the per-PM operator-key signer, and the buy/sell **automation orchestration** (match → markPaid → operator-signed release; sell deposit → payout → release-on-success; failure/retry/manual-queue). Requires reading the exact `p2p_trade_service.go` `MarkPaid` / `PrepareRelease` / `ConfirmRelease` signatures before writing code.

**Plan 3:** app — PM order screens, the pay-to-account display, and the PM manual-release queue UI.
