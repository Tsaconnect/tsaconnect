package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
	return NewITHandler(db, it, nil), db
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

func TestITHandler_Currencies(t *testing.T) {
	h, db := newITTestHandler(t)
	if err := db.AutoMigrate(&models.Currency{}); err != nil {
		t.Fatalf("migrate currencies: %v", err)
	}
	// NewCurrencyRegistry seeds the defaults and loads them.
	h.currencies = services.NewCurrencyRegistry(db)

	// Simulate a row that predates the ramp_provider column: force NGN's column
	// blank directly in the DB (NewCurrencyRegistry's seed literal sets it, so
	// without this the handler would read the same "paycrest" value whether it
	// calls RampProvider(code) or the struct field directly — the assertions
	// below wouldn't tell the two implementations apart). NGN is the row that
	// matters: it is the only live currency, so this is the failure mode the
	// endpoint exists to prevent.
	if err := db.Model(&models.Currency{}).Where("code = ?", "NGN").Update("ramp_provider", "").Error; err != nil {
		t.Fatalf("blank NGN ramp_provider: %v", err)
	}
	if err := h.currencies.Load(); err != nil {
		t.Fatalf("reload registry: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/it/currencies", nil)
	h.Currencies(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			Code         string `json:"code"`
			RampProvider string `json:"rampProvider"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byCode := map[string]string{}
	for _, c := range resp.Data {
		byCode[c.Code] = c.RampProvider
	}
	// NGN's column is blank (forced above), so this only passes if the handler
	// resolves it through RampProvider's compiled-in-default fallback rather
	// than reading the empty struct field.
	if byCode["NGN"] != "paycrest" {
		t.Errorf("NGN ramp = %q, want paycrest", byCode["NGN"])
	}
	// A payout-only currency must be listed, not omitted: the app shows it as
	// unavailable rather than pretending it does not exist.
	if _, ok := byCode["USD"]; !ok {
		t.Error("USD must be listed so the app can say it is unavailable")
	}
	if byCode["USD"] != "" {
		t.Errorf("USD ramp = %q, want empty", byCode["USD"])
	}
}

// ─── Ramp directions ───────────────────────────────────────────────────────

type stubLegs struct {
	legs map[string]services.RampLegs
	err  error
}

func (s stubLegs) RampLegs(context.Context) (map[string]services.RampLegs, error) {
	return s.legs, s.err
}

// currencyRows decodes /api/it/currencies into a code-keyed view of everything
// the direction assertions below care about.
type currencyRow struct {
	RampProvider string `json:"rampProvider"`
	RampBuy      bool   `json:"rampBuy"`
	RampSell     bool   `json:"rampSell"`
}

func getCurrencyRows(t *testing.T, h *ITHandler) map[string]currencyRow {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/it/currencies", nil)
	h.Currencies(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []struct {
			Code string `json:"code"`
			currencyRow
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := map[string]currencyRow{}
	for _, r := range resp.Data {
		out[r.Code] = r.currencyRow
	}
	return out
}

func newCurrencyHandler(t *testing.T) *ITHandler {
	t.Helper()
	h, db := newITTestHandler(t)
	if err := db.AutoMigrate(&models.Currency{}); err != nil {
		t.Fatalf("migrate currencies: %v", err)
	}
	h.currencies = services.NewCurrencyRegistry(db)
	return h
}

// The whole point of the endpoint: a currency that only sells must say so, so
// the app can render Sell without Buy instead of offering a button whose order
// no provider will fill.
func TestCurrencies_ReportsDirectionsSeparately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCurrencyHandler(t)
	h.SetRampLegs(stubLegs{legs: map[string]services.RampLegs{
		"NGN": {Buy: true, Sell: true},
		"KES": {Buy: true, Sell: true},
		"UGX": {Sell: true},
		"TZS": {Sell: true},
	}})

	rows := getCurrencyRows(t, h)

	for _, code := range []string{"NGN", "KES"} {
		if !rows[code].RampBuy || !rows[code].RampSell {
			t.Errorf("%s = %+v, want both directions", code, rows[code])
		}
	}
	for _, code := range []string{"UGX", "TZS"} {
		if rows[code].RampBuy {
			t.Errorf("%s reports a buy leg; Paycrest has offramp providers only there", code)
		}
		if !rows[code].RampSell {
			t.Errorf("%s = %+v, want a sell leg", code, rows[code])
		}
	}
}

// A currency with no provider is never tradable in either direction, whatever
// the book happens to say. GHS is the live example: Paycrest does not serve it,
// and the Flutterwave path cannot either.
func TestCurrencies_NoProviderMeansNoLegs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCurrencyHandler(t)
	h.SetRampLegs(stubLegs{legs: map[string]services.RampLegs{
		"GHS": {Buy: true, Sell: true},
		"USD": {Buy: true, Sell: true},
	}})

	rows := getCurrencyRows(t, h)

	for _, code := range []string{"GHS", "USD"} {
		if rows[code].RampProvider != "" {
			t.Fatalf("%s provider = %q, want none — fixture assumption broken",
				code, rows[code].RampProvider)
		}
		if rows[code].RampBuy || rows[code].RampSell {
			t.Errorf("%s = %+v, want no legs: a book entry cannot conjure a ramp "+
				"for a currency no provider serves", code, rows[code])
		}
	}
}

// The failure that must NOT hide the live rail: the book has never loaded, so
// directions are unknown. Reporting "no legs" would empty the IT hub for every
// Nigerian user because a public endpoint was briefly unreachable.
func TestCurrencies_UnknownDirectionsFallBackToBoth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCurrencyHandler(t)
	h.SetRampLegs(stubLegs{err: errors.New("paycrest unreachable")})

	rows := getCurrencyRows(t, h)

	if !rows["NGN"].RampBuy || !rows["NGN"].RampSell {
		t.Errorf("NGN = %+v, want both directions when the book cannot be read", rows["NGN"])
	}
	if rows["USD"].RampBuy || rows["USD"].RampSell {
		t.Errorf("USD = %+v, want no legs — the fallback applies to provider-backed "+
			"currencies only", rows["USD"])
	}
}

// Same fallback with the Paycrest stack absent entirely (no key, nil service),
// which is how every non-production environment boots.
func TestCurrencies_NoLegSourceFallsBackToBoth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCurrencyHandler(t)

	rows := getCurrencyRows(t, h)

	if !rows["NGN"].RampBuy || !rows["NGN"].RampSell {
		t.Errorf("NGN = %+v, want both directions when no leg source is wired", rows["NGN"])
	}
}

// The handler-side half of the empty-book guard. An empty map is non-nil in
// Go, so without the len check every provider-backed currency would report
// closed and the IT hub would go to "paused" for everyone — including the live
// NGN rail. RampLegs refuses to return one; this pins the caller so a future
// source cannot reintroduce it.
func TestCurrencies_EmptyLegMapFallsBackToBoth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCurrencyHandler(t)
	h.SetRampLegs(stubLegs{legs: map[string]services.RampLegs{}})

	rows := getCurrencyRows(t, h)

	if !rows["NGN"].RampBuy || !rows["NGN"].RampSell {
		t.Errorf("NGN = %+v, want both directions — an empty map is 'we do not know', "+
			"not 'nothing trades'", rows["NGN"])
	}
}

// And the distinction that keeps the guard honest: a POPULATED map that omits
// a provider-backed currency really does mean that currency is closed.
func TestCurrencies_PopulatedLegMapStillClosesAMissingCurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCurrencyHandler(t)
	h.SetRampLegs(stubLegs{legs: map[string]services.RampLegs{
		"NGN": {Buy: true, Sell: true},
	}})

	rows := getCurrencyRows(t, h)

	if !rows["NGN"].RampBuy {
		t.Error("NGN buy lost")
	}
	if rows["KES"].RampBuy || rows["KES"].RampSell {
		t.Errorf("KES = %+v, want no legs — absent from a live book means closed", rows["KES"])
	}
}

// The lookup is bounded so a slow Paycrest cannot hold up the currency list:
// the hub calls this on every focus, and the fallback is instant and correct.
func TestCurrencies_SlowLegSourceDoesNotBlockTheList(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCurrencyHandler(t)
	h.SetRampLegs(blockingLegs{})

	start := time.Now()
	rows := getCurrencyRows(t, h)
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("currency list took %s; it must give up on the leg lookup, not wait out "+
			"the Paycrest client's own 30s timeout", elapsed)
	}
	if !rows["NGN"].RampBuy || !rows["NGN"].RampSell {
		t.Errorf("NGN = %+v, want the both-directions fallback after the lookup gives up", rows["NGN"])
	}
}

// blockingLegs never answers on its own; it returns only when the caller's
// context is cancelled, which is exactly what an unresponsive upstream looks
// like from here.
type blockingLegs struct{}

func (blockingLegs) RampLegs(ctx context.Context) (map[string]services.RampLegs, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The kill switch must fire on BOTH halves. An operator disables a rail by
// setting ramp_provider to something unrecognised; requirePaycrestEnabled then
// refuses every order. If the currency list still advertised the legs Paycrest
// lists, the app would show live Buy and Sell buttons for a rail that refuses
// everything the user does with them.
func TestCurrencies_DisabledProviderReportsNoLegs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newITTestHandler(t)
	if err := db.AutoMigrate(&models.Currency{}); err != nil {
		t.Fatalf("migrate currencies: %v", err)
	}
	h.currencies = services.NewCurrencyRegistry(db)
	// The book still lists NGN — the provider is fine, we are switching it off.
	h.SetRampLegs(stubLegs{legs: map[string]services.RampLegs{"NGN": {Buy: true, Sell: true}}})

	if err := db.Model(&models.Currency{}).Where("code = ?", "NGN").
		Update("ramp_provider", "paused").Error; err != nil {
		t.Fatalf("pause NGN: %v", err)
	}
	if err := h.currencies.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	rows := getCurrencyRows(t, h)

	if rows["NGN"].RampBuy || rows["NGN"].RampSell {
		t.Errorf("NGN = %+v, want no legs — orders are refused while ramp_provider is "+
			"not paycrest, so the app must not offer the buttons", rows["NGN"])
	}
}

// A currency served by some OTHER provider must not have Paycrest's book
// consulted on its behalf. It would report closed with no error and no log —
// the silent-wrong-answer this endpoint is meant to avoid.
func TestCurrencies_NonPaycrestProviderIsNotLookedUpInPaycrestsBook(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := newITTestHandler(t)
	if err := db.AutoMigrate(&models.Currency{}); err != nil {
		t.Fatalf("migrate currencies: %v", err)
	}
	h.currencies = services.NewCurrencyRegistry(db)
	h.SetRampLegs(stubLegs{legs: map[string]services.RampLegs{"NGN": {Buy: true, Sell: true}}})

	if err := db.Model(&models.Currency{}).Where("code = ?", "GHS").
		Update("ramp_provider", services.RampProviderFlutterwave).Error; err != nil {
		t.Fatalf("set GHS provider: %v", err)
	}
	if err := h.currencies.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	rows := getCurrencyRows(t, h)

	if rows["GHS"].RampProvider != services.RampProviderFlutterwave {
		t.Fatalf("GHS provider = %q, want flutterwave — fixture assumption broken",
			rows["GHS"].RampProvider)
	}
	if rows["GHS"].RampBuy || rows["GHS"].RampSell {
		t.Errorf("GHS = %+v, want no legs until Flutterwave has a leg source of its own",
			rows["GHS"])
	}
}
