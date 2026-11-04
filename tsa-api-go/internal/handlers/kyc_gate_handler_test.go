package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// createHandlerUsersTable adds the users table the money-in gate reads. Raw
// DDL, not AutoMigrate: models.User's id column carries a PostgreSQL-only
// `default:gen_random_uuid()` that SQLite's driver cannot parse (repo
// convention). verification_status defaults to 'pending' exactly as it does in
// production, so a fixture that forgets to seed a user gets the unverified
// behaviour rather than an accidental pass.
func createHandlerUsersTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		name TEXT,
		email TEXT,
		wallet_address TEXT,
		verification_status TEXT DEFAULT 'pending',
		verified_legal_name TEXT
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
}

func seedHandlerUserAt(t *testing.T, db *gorm.DB, status string) *models.User {
	t.Helper()
	u := &models.User{
		ID:                 uuid.New(),
		Name:               "JOHN DOE",
		WalletAddress:      "0xWALLET",
		VerificationStatus: status,
	}
	// A verified user carries a legal name, because the gate requires both: a
	// passed verification with no stored name is one the matcher cannot
	// identify, and it is refused. Seeding only the status would model a user
	// who cannot actually transact.
	legalName := ""
	if status == models.VerificationStatusVerified {
		legalName = u.Name
	}
	if err := db.Exec(
		`INSERT INTO users (id, name, email, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?, ?)`,
		u.ID, u.Name, u.ID.String()+"@test.com", u.WalletAddress, status, legalName,
	).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// The refusal must reach the app as 403 plus the machine code it routes on,
// and must not leave a half-open order behind.
func TestITBuy_UnverifiedUserIsRefusedWithTheKYCCode(t *testing.T) {
	for _, status := range []string{
		models.VerificationStatusPending,
		models.VerificationStatusInReview,
		models.VerificationStatusRejected,
	} {
		h, db, _ := newITBuyHandler(t)
		user := seedHandlerUserAt(t, db, status)

		w := doITBuy(t, h, user, "100000")

		if w.Code != http.StatusForbidden {
			t.Fatalf("status %q: HTTP %d, want 403 (body=%s)", status, w.Code, w.Body.String())
		}
		resp := decodeITResponse(t, w)
		if resp["code"] != "kyc_verification_required" {
			t.Fatalf("status %q: code = %v, want kyc_verification_required (body=%s)", status, resp["code"], w.Body.String())
		}
		var n int64
		db.Model(&models.ITIntent{}).Count(&n)
		if n != 0 {
			t.Fatalf("status %q: %d intent(s) created despite the refusal", status, n)
		}
	}
}

// An unverified user with no virtual account must be sent to verification, not
// to activate a rail that is being retired — the VA branch sits ahead of
// OpenBuyOrder and would otherwise answer first.
func TestITBuy_UnverifiedUserIsNotSentToVAActivation(t *testing.T) {
	h, db, _ := newITBuyHandler(t)
	user := seedHandlerUserAt(t, db, models.VerificationStatusPending)

	w := doITBuy(t, h, user, "100000")

	resp := decodeITResponse(t, w)
	if data, ok := resp["data"].(map[string]any); ok && data["needsActivation"] == true {
		t.Fatalf("unverified user was told to activate a virtual account instead of to verify: %s", w.Body.String())
	}
}

func TestITBuy_VerifiedUserIsAdmitted(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := seedHandlerUserAt(t, db, models.VerificationStatusVerified)

	w := doITBuy(t, h, user, "100000")

	if w.Code != http.StatusOK {
		t.Fatalf("verified user refused: HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.ITIntent{}).Count(&n)
	if n != 1 {
		t.Fatalf("intents = %d, want the order to have opened", n)
	}
}

// The stablecoin leg surfaces the same refusal through the shared ramp error
// mappers rather than its own branch, so both legs hand the app one code.
func TestStableMappers_KYCRefusalIs403WithTheSharedCode(t *testing.T) {
	if got := stableStatusFor(services.ErrKYCVerificationRequired); got != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", got)
	}
	if got := stableCodeFor(services.ErrKYCVerificationRequired); got != "kyc_verification_required" {
		t.Fatalf("code = %q, want kyc_verification_required", got)
	}
}

// P2P maps it through mapServiceError — 403, not the 500 an unmapped service
// error would have produced.
func TestP2PMapServiceError_KYCRefusalIs403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if !mapServiceError(c, services.ErrKYCVerificationRequired) {
		t.Fatal("mapServiceError did not handle the refusal")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
}

func seedHandlerNamelessVerified(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	u := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET",
		VerificationStatus: models.VerificationStatusVerified}
	if err := db.Exec(
		`INSERT INTO users (id, name, email, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?, '')`,
		u.ID, u.Name, u.ID.String()+"@test.com", u.WalletAddress, models.VerificationStatusVerified,
	).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// A verified user with no stored legal name cannot re-verify (the KYC session
// endpoint answers "Already verified"), so the app must not route them to
// verification. They get their own code.
func TestITBuy_NamelessVerifiedUserGetsTheSupportCode(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := seedHandlerNamelessVerified(t, db)

	w := doITBuy(t, h, user, "100000")

	if w.Code != http.StatusForbidden {
		t.Fatalf("HTTP %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if code := decodeITResponse(t, w)["code"]; code != "kyc_legal_name_missing" {
		t.Fatalf("code = %v, want kyc_legal_name_missing (body=%s)", code, w.Body.String())
	}
}

func TestStableMappers_LegalNameMissingIs403WithItsOwnCode(t *testing.T) {
	if got := stableStatusFor(services.ErrKYCLegalNameMissing); got != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", got)
	}
	if got := stableCodeFor(services.ErrKYCLegalNameMissing); got != "kyc_legal_name_missing" {
		t.Fatalf("code = %q, want kyc_legal_name_missing", got)
	}
}

// Without the machine code the app has nothing to route on and the P2P screen
// can only echo the message.
func TestP2PMapServiceError_KYCRefusalCarriesTheCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for err, want := range map[error]string{
		services.ErrKYCVerificationRequired: "kyc_verification_required",
		services.ErrKYCLegalNameMissing:     "kyc_legal_name_missing",
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		mapServiceError(c, err)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%v: status = %d, want 403", err, w.Code)
		}
		if code := decodeITResponse(t, w)["code"]; code != want {
			t.Fatalf("%v: code = %v, want %s (body=%s)", err, code, want, w.Body.String())
		}
	}
}

// VA mode, provisioning wired: an unverified user asking for a standing pay-in
// account is refused with the routable code, not handed a Flutterwave call.
func TestActivateVA_UnverifiedUserIsRefusedWithTheKYCCode(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetFlutterwaveClient(services.NewFlutterwaveClient("", "", "test-id", "test-secret"))
	it.SetInboundCentral(false, testCentralAccount())
	user := seedHandlerUserAt(t, db, models.VerificationStatusPending)
	_ = h.it.AcceptITAgreement(context.Background(), user.ID, "", "")

	b, _ := json.Marshal(map[string]any{"idType": "BVN", "idNumber": "12345678901"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/it/va/activate", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	h.ActivateVA(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("HTTP %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if code := decodeITResponse(t, w)["code"]; code != "kyc_verification_required" {
		t.Fatalf("code = %v, want kyc_verification_required (body=%s)", code, w.Body.String())
	}
}

// ─── Paycrest onramp ────────────────────────────────────────────────────────

var errPaycrestProviderReached = errors.New("paycrest provider reached")

// paycrestGateProvider stands in for both Paycrest client surfaces and counts
// every call, so a test can tell "refused before the provider" apart from
// "refused by the provider".
type paycrestGateProvider struct{ calls int }

func (p *paycrestGateProvider) Markets(context.Context) (*services.PaycrestMarkets, error) {
	p.calls++
	return nil, errPaycrestProviderReached
}

func (p *paycrestGateProvider) Rates(context.Context, string, string, float64, string) (*services.PaycrestRates, error) {
	p.calls++
	return nil, errPaycrestProviderReached
}

func (p *paycrestGateProvider) MarketRate(context.Context, string, string) (float64, error) {
	p.calls++
	return 0, errPaycrestProviderReached
}

func (p *paycrestGateProvider) CreateOnrampOrder(context.Context, services.PaycrestOnrampRequest) (*services.PaycrestOrder, error) {
	p.calls++
	return nil, errPaycrestProviderReached
}

func (p *paycrestGateProvider) CreateOfframpOrder(context.Context, services.PaycrestOfframpRequest) (*services.PaycrestOrder, error) {
	p.calls++
	return nil, errPaycrestProviderReached
}

func (p *paycrestGateProvider) GetOrder(context.Context, string) (*services.PaycrestOrder, error) {
	p.calls++
	return nil, errPaycrestProviderReached
}

func newPaycrestGateHandler(t *testing.T) (*PaycrestHandler, *gorm.DB, *paycrestGateProvider) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	createHandlerUsersTable(t, db)

	provider := &paycrestGateProvider{}
	quotes := services.NewPaycrestQuoteService(provider, nil, nil)
	dir := services.NewPaycrestDirectory(
		&stubPaycrestInstitutions{rows: []services.PaycrestInstitution{{Name: "Access Bank", Code: "ABNGNGLA", Type: "bank"}}},
		&stubPaycrestBankDir{rows: []services.NombaBank{{Name: "Access Bank", Code: "044"}}},
	)
	orders := services.NewPaycrestOrderService(db, provider, quotes, dir, "", "")
	if orders == nil {
		t.Fatal("NewPaycrestOrderService returned nil")
	}
	return NewPaycrestHandler(orders, quotes, dir), db, provider
}

func doPaycrest(t *testing.T, handle gin.HandlerFunc, path string, user *models.User, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	handle(c)
	return w
}

func doPaycrestBuy(t *testing.T, h *PaycrestHandler, user *models.User) *httptest.ResponseRecorder {
	t.Helper()
	return doPaycrest(t, h.Buy, "/api/paycrest/buy", user, map[string]any{
		"amount": "800", "currency": "NGN", "chain": "base", "token": "USDC",
		"refundBankCode": "044", "refundAccount": "0123456789",
		"buyerId": map[string]any{"type": "bvn", "number": "22212345678", "birthDate": "1990-04-12"},
	})
}

// An unverified buyer gets the routable 403 and nothing reaches Paycrest —
// no provider order means no bank account the user could pay into.
func TestPaycrestBuy_UnverifiedUserIsRefusedWithTheKYCCode(t *testing.T) {
	for _, status := range []string{
		models.VerificationStatusPending,
		models.VerificationStatusInReview,
		models.VerificationStatusRejected,
	} {
		h, db, provider := newPaycrestGateHandler(t)
		user := seedHandlerUserAt(t, db, status)

		w := doPaycrestBuy(t, h, user)

		if w.Code != http.StatusForbidden {
			t.Fatalf("status %q: HTTP %d, want 403 (body=%s)", status, w.Code, w.Body.String())
		}
		resp := decodeITResponse(t, w)
		if resp["success"] != false || resp["code"] != "kyc_verification_required" {
			t.Fatalf("status %q: body = %s, want success=false code=kyc_verification_required", status, w.Body.String())
		}
		if provider.calls != 0 {
			t.Fatalf("status %q: %d provider call(s) made before the refusal", status, provider.calls)
		}
		var n int64
		db.Model(&models.PaycrestOrderRecord{}).Count(&n)
		if n != 0 {
			t.Fatalf("status %q: %d order(s) written despite the refusal", status, n)
		}
	}
}

func TestPaycrestBuy_NamelessVerifiedUserGetsTheSupportCode(t *testing.T) {
	h, db, provider := newPaycrestGateHandler(t)
	user := seedHandlerNamelessVerified(t, db)

	w := doPaycrestBuy(t, h, user)

	if w.Code != http.StatusForbidden {
		t.Fatalf("HTTP %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	if code := decodeITResponse(t, w)["code"]; code != "kyc_legal_name_missing" {
		t.Fatalf("code = %v, want kyc_legal_name_missing (body=%s)", code, w.Body.String())
	}
	if provider.calls != 0 {
		t.Fatalf("%d provider call(s) made before the refusal", provider.calls)
	}
}

func TestPaycrestBuy_VerifiedUserReachesTheProvider(t *testing.T) {
	h, db, provider := newPaycrestGateHandler(t)
	user := seedHandlerUserAt(t, db, models.VerificationStatusVerified)

	w := doPaycrestBuy(t, h, user)

	if w.Code == http.StatusForbidden {
		t.Fatalf("verified buyer refused: HTTP 403 (body=%s)", w.Body.String())
	}
	if provider.calls == 0 {
		t.Fatalf("verified buyer never reached the provider (HTTP %d, body=%s)", w.Code, w.Body.String())
	}
}

// The sell is money out. An unverified holder must reach the provider, not a
// KYC refusal that would trap their stablecoin.
func TestPaycrestSell_UnverifiedUserIsNotGated(t *testing.T) {
	h, db, provider := newPaycrestGateHandler(t)
	user := seedHandlerUserAt(t, db, models.VerificationStatusPending)

	w := doPaycrest(t, h.Sell, "/api/paycrest/sell", user, map[string]any{
		"amount": "800", "currency": "NGN", "chain": "base", "token": "USDC",
		"bankCode": "044", "accountNumber": "0123456789",
	})

	if w.Code == http.StatusForbidden {
		t.Fatalf("unverified seller refused: HTTP 403 (body=%s) — money-out must not be gated", w.Body.String())
	}
	if provider.calls == 0 {
		t.Fatalf("sell never reached the provider (HTTP %d, body=%s)", w.Code, w.Body.String())
	}
}

func TestPaycrestError_KYCRefusalIs403WithTheCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for err, want := range map[error]string{
		services.ErrKYCVerificationRequired: "kyc_verification_required",
		services.ErrKYCLegalNameMissing:     "kyc_legal_name_missing",
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		paycrestError(c, err)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%v: status = %d, want 403", err, w.Code)
		}
		if code := decodeITResponse(t, w)["code"]; code != want {
			t.Fatalf("%v: code = %v, want %s (body=%s)", err, code, want, w.Body.String())
		}
	}
}

// The buyer-ID refusals carry the codes the app routes on: ask for the ID,
// or reopen it for editing with the reason shown.
func TestPaycrestError_BuyerIDRefusalsCarryTheirCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, invalid := services.BuyerID{Type: "bvn", Number: "1", BirthDate: "1990-01-01"}.Normalized(time.Now())
	for _, tc := range []struct {
		err  error
		code string
		msg  string
	}{
		{services.ErrBuyerIDRequired, services.BuyerIDRequiredCode, "Add your BVN or NIN"},
		{invalid, services.BuyerIDInvalidCode, "Your BVN is 11 digits."},
		{fmt.Errorf("%w: %w", services.ErrBuyerIDRejected, errors.New("paycrest 400")), services.BuyerIDRejectedCode, "weren't accepted"},
		{services.ErrRefundAccountNotBuyers, services.RefundNotBuyersCode, "in your own name"},
		// Wrapped by the ramp router's outcome class, as it reaches rampError.
		{ramp.WrapClass(ramp.ErrRefusedForUser, services.ErrBuyerIDRequired), services.BuyerIDRequiredCode, "Add your BVN or NIN"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		rampError(c, tc.err)
		body := decodeITResponse(t, w)
		if w.Code != http.StatusBadRequest || body["code"] != tc.code {
			t.Fatalf("%v: HTTP %d code %v, want 400 %s", tc.err, w.Code, body["code"], tc.code)
		}
		if msg, _ := body["message"].(string); !strings.Contains(msg, tc.msg) {
			t.Fatalf("%v: message %q, want it to contain %q", tc.err, msg, tc.msg)
		}
	}
}
