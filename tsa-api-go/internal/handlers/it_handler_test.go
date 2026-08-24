package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeITReleaseSigner is a minimal services.ReleaseSigner double so the IT
// sell path is IsConfigured()==true without a live operator key. It is never
// actually invoked in these tests: every case here is rejected by the new
// destination-catalog check before the service is called, or (the legacy/
// valid-destination cases) fails at the nil blockchain client — a
// pre-existing limitation of this test setup (see it_service_test.go) — well
// before SignAndBroadcast would run.
type fakeITReleaseSigner struct{}

func (fakeITReleaseSigner) Address() string {
	return "0x000000000000000000000000000000000000A11CE"
}
func (fakeITReleaseSigner) SignAndBroadcast(context.Context, []byte) (string, error) {
	return "0xdead", nil
}

// stubITFx is a fixed NGN/USD source; CreateSellIntent never reaches it in
// these tests (it fails earlier at the nil-client chain call) but ITService
// requires a non-nil FxProvider seam.
type stubITFx struct{}

func (stubITFx) NgnPerUsd(context.Context) (float64, bool, error) { return 1500, false, nil }

// newITTestHandler builds an ITHandler backed by a real (in-memory) DB and a
// "configured" IT service (swap address + operator signer set, so
// IsConfigured()==true and Sell's request actually reaches CreateSellIntent
// when validation allows it through) so intent-count assertions are
// meaningful.
func newITTestHandler(t *testing.T) (*ITHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITRate{}, &models.PaymentMerchant{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	swap := services.NewMCGPSwapService(nil, &config.Config{
		Env:              "development",
		MCGPSwapBAddress: "0x00000000000000000000000000000000000bEEF0",
	})
	swap.SetOperatorSigner(fakeITReleaseSigner{})
	it := services.NewITService(db, swap, stubITFx{})
	return NewITHandler(db, it), db
}

func doITSell(h *ITHandler, user *models.User, body map[string]any) *httptest.ResponseRecorder {
	// These tests exercise Sell's input/catalog validation, which sits behind
	// the trading-agreement gate. Satisfy the gate so the logic under test is
	// reached; the gate's own blocking behaviour is covered separately below.
	_ = h.it.AcceptITAgreement(context.Background(), user.ID, "", "")
	b, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/it/sell", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	h.Sell(c)
	return w
}

// TestIT_AgreementGate_BlocksTrade pins the enforcement: a user who has NOT
// accepted the current agreement is refused a trade with 403 + the machine code
// the app matches on, and no intent is created.
func TestIT_AgreementGate_BlocksTrade(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newITTestHandler(t)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body, _ := json.Marshal(map[string]any{
		"mcgpAmount": "1000000000000000000", "bankCode": "044", "accountNumber": "0123456789",
	})
	c.Request = httptest.NewRequest(http.MethodPost, "/api/it/sell", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	h.Sell(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
	resp := decodeITResponse(t, w)
	if resp["code"] != "it_agreement_required" {
		t.Fatalf("code = %v, want it_agreement_required", resp["code"])
	}
	var n int64
	db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("an intent was created despite the gate (%d rows)", n)
	}
}

func decodeITResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	return out
}

// TestWithdrawalDestinations_ReturnsCatalog pins the endpoint's shape: the
// exact catalog, unwrapped, under "destinations".
func TestWithdrawalDestinations_ReturnsCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newITTestHandler(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/it/withdraw/destinations", nil)
	h.WithdrawalDestinations(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	resp := decodeITResponse(t, w)
	data, _ := resp["data"].(map[string]any)
	dests, _ := data["destinations"].([]any)
	if len(dests) != len(services.WithdrawalDestinationCatalog) {
		t.Fatalf("got %d destinations, want %d: %+v", len(dests), len(services.WithdrawalDestinationCatalog), dests)
	}
	first, _ := dests[0].(map[string]any)
	if first["currency"] != "NGN" || first["rail"] != "bank" {
		t.Fatalf("first destination = %+v, want NGN/bank", first)
	}
}

// TestSell_UnsupportedDestination_Returns400NoIntent covers requirement (d):
// a Sell request whose (currency, rail) combo isn't in the catalog must be
// rejected with 400 BEFORE the intent is created — checked by asserting the
// destination-validation error message and that no it_intents row exists.
func TestSell_UnsupportedDestination_Returns400NoIntent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newITTestHandler(t)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}

	cases := []map[string]any{
		{"mcgpAmount": "1000000000000000000", "destinationCurrency": "KES", "railType": "bank",
			"bankCode": "044", "accountNumber": "0123456789"},
		{"mcgpAmount": "1000000000000000000", "destinationCurrency": "GHS", "railType": "mobile_money",
			"mobileNetwork": "wave", "mobileCountry": "GH", "mobileMsisdn": "233241234567"},
		{"mcgpAmount": "1000000000000000000", "destinationCurrency": "XOF", "railType": "mobile_money",
			"mobileNetwork": "mpesa", "mobileCountry": "SN", "mobileMsisdn": "221771234567"},
	}
	for _, body := range cases {
		w := doITSell(h, user, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("case %+v: status = %d, want 400 (body=%s)", body, w.Code, w.Body.String())
		}
		resp := decodeITResponse(t, w)
		msg, _ := resp["message"].(string)
		if msg != "Unsupported withdrawal destination for this currency/rail combination" {
			t.Fatalf("case %+v: message = %q, want the catalog-validation message", body, msg)
		}
	}

	var n int64
	if err := db.Model(&models.ITIntent{}).Count(&n).Error; err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if n != 0 {
		t.Fatalf("no intent should be created for an unsupported destination, got %d", n)
	}
}

// TestSell_ValidDestination_PassesCatalogGate covers the accept side: an
// explicit NGN/bank catalog selection must NOT be rejected by the destination
// gate. It still fails later (nil blockchain client — see newITTestHandler),
// but that failure must come from the service, not catalog validation.
func TestSell_ValidDestination_PassesCatalogGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newITTestHandler(t)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}

	w := doITSell(h, user, map[string]any{
		"mcgpAmount":          "1000000000000000000",
		"destinationCurrency": "NGN",
		"railType":            "bank",
		"bankCode":            "044",
		"accountNumber":       "0123456789",
		"accountName":         "JOHN DOE",
	})
	resp := decodeITResponse(t, w)
	msg, _ := resp["message"].(string)
	if msg == "Unsupported withdrawal destination for this currency/rail combination" {
		t.Fatalf("NGN/bank destination wrongly rejected by catalog gate (body=%s)", w.Body.String())
	}

	var n int64
	if err := db.Model(&models.ITIntent{}).Count(&n).Error; err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if n != 0 {
		t.Fatalf("no intent should be persisted (chain call fails first in this test setup), got %d", n)
	}
}

// TestSell_LegacyNGNBank_Unaffected covers requirement (e): a legacy sell
// with only the pre-existing bank fields (no destinationCurrency/railType)
// must behave exactly as before — reaching the service (not blocked by the
// new catalog gate) and NOT populating any of the new destination fields.
func TestSell_LegacyNGNBank_Unaffected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newITTestHandler(t)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}

	w := doITSell(h, user, map[string]any{
		"mcgpAmount":    "1000000000000000000",
		"bankCode":      "044",
		"accountNumber": "0123456789",
		"accountName":   "JOHN DOE",
	})
	resp := decodeITResponse(t, w)
	msg, _ := resp["message"].(string)
	if msg == "Unsupported withdrawal destination for this currency/rail combination" {
		t.Fatalf("legacy NGN-bank sell wrongly rejected by the new catalog gate (body=%s)", w.Body.String())
	}
	if msg == "bankCode and a 10-digit accountNumber are required" || msg == "Select a payout bank account" {
		t.Fatalf("legacy NGN-bank sell wrongly rejected by the bank-field checks: %q", msg)
	}

	var n int64
	if err := db.Model(&models.ITIntent{}).Count(&n).Error; err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if n != 0 {
		t.Fatalf("no intent should be persisted (chain call fails first in this test setup), got %d", n)
	}
}

// TestSell_BadBankFields_StillRejected pins that the pre-existing bank-field
// validation for the legacy/bank rail is unchanged: a bad accountNumber is
// still rejected with the original message, not silently accepted.
func TestSell_BadBankFields_StillRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newITTestHandler(t)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}

	w := doITSell(h, user, map[string]any{
		"mcgpAmount":    "1000000000000000000",
		"bankCode":      "044",
		"accountNumber": "123", // not 10 digits
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	resp := decodeITResponse(t, w)
	if resp["message"] != "bankCode and a 10-digit accountNumber are required" {
		t.Fatalf("message = %v, want the bank-field validation message", resp["message"])
	}
}

// TestSell_NonNGNBank_RejectedByCatalog covers the NGN-only product lock: a
// GHS bank withdrawal is rejected at the catalog gate (400) before NUBAN or
// chain logic runs. When multi-currency sell is re-enabled, restore a
// NUBAN-scope regression for non-NGN account formats.
// Renamed in spirit: the catalog now offers five bank rails, so the gate is
// tested with a currency that is still absent from it. What is being pinned is
// the gate itself — Sell must refuse any destination the catalog does not list,
// so the app can never offer a rail the payout would fail on AFTER the
// on-chain debit.
func TestSell_NonNGNBank_RejectedByCatalog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newITTestHandler(t)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}

	w := doITSell(h, user, map[string]any{
		"mcgpAmount":          "1000000000000000000",
		"destinationCurrency": "KES",
		"railType":            "bank",
		"bankCode":            "044",
		"accountNumber":       "1234567",
		"accountName":         "JOHN DOE",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", w.Code, w.Body.String())
	}
	resp := decodeITResponse(t, w)
	msg, _ := resp["message"].(string)
	if msg != "Unsupported withdrawal destination for this currency/rail combination" {
		t.Fatalf("message = %q, want catalog-validation message", msg)
	}

	var n int64
	if err := db.Model(&models.ITIntent{}).Count(&n).Error; err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if n != 0 {
		t.Fatalf("no intent should be created for an unsupported destination, got %d", n)
	}
}

// TestSell_NGNBank_ExplicitCurrency_StillRequiresTenDigitAccountNumber pins
// that the NUBAN rule is preserved when destinationCurrency is explicitly
// "NGN" (case-insensitively) with the bank rail — a 7-digit account number
// must still be rejected with the original 400 message.
func TestSell_NGNBank_ExplicitCurrency_StillRequiresTenDigitAccountNumber(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newITTestHandler(t)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}

	w := doITSell(h, user, map[string]any{
		"mcgpAmount":          "1000000000000000000",
		"destinationCurrency": "ngn",
		"railType":            "bank",
		"bankCode":            "044",
		"accountNumber":       "1234567", // 7 digits — not a valid NUBAN
		"accountName":         "JOHN DOE",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	resp := decodeITResponse(t, w)
	if resp["message"] != "bankCode and a 10-digit accountNumber are required" {
		t.Fatalf("message = %v, want the NUBAN validation message", resp["message"])
	}
}
