package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// zeroFeeQuoter prices every leg at no fee. The fee split is covered where it
// belongs (services); these tests are about WHERE the money is sent, and a
// no-fee quote keeps the naira figure under test equal to the one asked for.
type zeroFeeQuoter struct{}

func (zeroFeeQuoter) Quote(_ context.Context, in services.FeeQuoteInput) (*services.FeeQuote, error) {
	// quoteITFees always hands over a 2dp naira string, so dropping the point
	// recovers the exact kobo it started from.
	minor, ok := new(big.Int).SetString(strings.ReplaceAll(in.Amount, ".", ""), 10)
	if !ok {
		minor = big.NewInt(0)
	}
	return &services.FeeQuote{
		Viable:    true,
		TotalFees: services.FeeLine{Minor: big.NewInt(0)},
		NetAmount: services.FeeLine{Minor: minor},
	}, nil
}

func testCentralAccount() services.CentralAccount {
	return services.CentralAccount{
		AccountNumber: "0123456789",
		BankName:      "Providus Bank",
		AccountName:   "TSA CONNECT LTD",
	}
}

// newITBuyHandler builds an ITHandler whose MCGP buy path actually reaches
// OpenBuyOrder: a live rate, a fee quoter, and the virtual-account table the VA
// branch reads.
func newITBuyHandler(t *testing.T) (*ITHandler, *gorm.DB, *services.ITService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITRate{}, &models.PaymentMerchant{},
		&models.FiatVirtualAccount{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	createHandlerUsersTable(t, db)
	swap := services.NewMCGPSwapService(nil, &config.Config{
		Env:              "development",
		MCGPSwapBAddress: "0x00000000000000000000000000000000000bEEF0",
	})
	swap.SetOperatorSigner(fakeITReleaseSigner{})
	it := services.NewITService(db, swap, stubITFx{})
	it.SetFeeQuoter(zeroFeeQuoter{})
	if _, err := it.SetRate(context.Background(), uuid.New(), 20000, 0); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	return NewITHandler(db, it, nil), db, it
}

func doITBuy(t *testing.T, h *ITHandler, user *models.User, ngnKobo string) *httptest.ResponseRecorder {
	t.Helper()
	_ = h.it.AcceptITAgreement(context.Background(), user.ID, "", "")
	// Buying is gated on verification, so these fixtures need a real users row.
	// OR IGNORE: a test that seeded its own status (kyc_gate_handler_test.go)
	// keeps it.
	if err := h.db.Exec(
		`INSERT OR IGNORE INTO users (id, name, wallet_address, verification_status, verified_legal_name) VALUES (?, ?, ?, ?, ?)`,
		user.ID, user.Name, user.WalletAddress, models.VerificationStatusVerified, user.Name,
	).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	b, _ := json.Marshal(map[string]any{"ngnKobo": ngnKobo})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/it/buy", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	h.Buy(c)
	return w
}

func itBuyData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rec.Body.String())
	}
	data, _ := body["data"].(map[string]any)
	if data == nil {
		t.Fatalf("no data in body=%s", rec.Body.String())
	}
	return data
}

func seedNGNVA(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	if err := db.Create(&models.FiatVirtualAccount{
		UserID: userID, Currency: "NGN", Provider: "flutterwave",
		FwCustomerID: "cust_1", Reference: "ITVA-REF", AccountNumber: "9988776655",
		BankName: "Wema Bank", AccountName: "JOHN DOE", Status: models.FiatVAStatusActive,
	}).Error; err != nil {
		t.Fatalf("seed va: %v", err)
	}
}

// The cutover's whole point: a user with no legacy virtual account can still
// buy, and is sent to the company account with the figure the matcher will
// compare their transfer against.
func TestBuy_CentralMode_SendsPayerToTheCompanyAccount(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}

	// Deliberately fractional: the order is minted at whole naira, so the payer
	// must be shown ₦5,000 — a figure their bank app can actually send.
	data := itBuyData(t, doITBuy(t, h, user, "500037"))

	if data["needsActivation"] != false {
		t.Fatalf("needsActivation = %v, want false; body=%v", data["needsActivation"], data)
	}
	payTo, _ := data["payTo"].(map[string]any)
	if payTo == nil {
		t.Fatalf("payTo missing; body=%v", data)
	}
	want := testCentralAccount()
	if payTo["accountNumber"] != want.AccountNumber || payTo["bank"] != want.BankName ||
		payTo["accountName"] != want.AccountName {
		t.Fatalf("payTo = %v, want the central account %+v", payTo, want)
	}
	if payTo["amountNgn"] != "5000" {
		t.Fatalf("payTo.amountNgn = %v, want 5000", payTo["amountNgn"])
	}
	var intent models.ITIntent
	if err := db.First(&intent, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("no intent minted: %v", err)
	}
	if intent.NgnKobo != "500000" {
		t.Fatalf("intent.NgnKobo = %s, want 500000 (the figure the payer was shown)", intent.NgnKobo)
	}
}

// A configured account with the mode off must change nothing — staging the
// INBOUND_CENTRAL_* values is how a cutover is prepared.
func TestBuy_VAMode_StillShowsTheUsersVirtualAccount(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(false, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	seedNGNVA(t, db, user.ID)

	data := itBuyData(t, doITBuy(t, h, user, "500000"))

	if data["needsActivation"] != false {
		t.Fatalf("needsActivation = %v, want false", data["needsActivation"])
	}
	payTo, _ := data["payTo"].(map[string]any)
	if payTo == nil || payTo["accountNumber"] != "9988776655" || payTo["bank"] != "Wema Bank" {
		t.Fatalf("payTo = %v, want the user's virtual account", payTo)
	}
	// No amount in VA mode: attribution there is the VA's own reference, and an
	// amount the client starts enforcing would be a rule that does not exist.
	if _, present := payTo["amountNgn"]; present {
		t.Fatalf("payTo carries amountNgn in VA mode: %v", payTo)
	}
}

func doITGetVA(t *testing.T, h *ITHandler, user *models.User) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/it/va", nil)
	c.Set("user", user)
	h.GetVA(c)
	return w
}

// A pre-cutover user still HAS a virtual-account row, and handing it back is
// handing back a pay-into card for an account the ingestor does not read. The
// money would leave the user's bank and match nothing.
func TestGetVA_CentralMode_WillNotHandBackTheStaleAccount(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	seedNGNVA(t, db, user.ID)
	it.SetInboundCentral(true, testCentralAccount())

	w := doITGetVA(t, h, user)

	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "9988776655") {
		t.Fatalf("stale virtual account leaked into the response: %s", w.Body.String())
	}
}

func TestGetVA_VAMode_Unchanged(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	seedNGNVA(t, db, user.ID)
	it.SetInboundCentral(false, testCentralAccount())

	w := doITGetVA(t, h, user)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "9988776655") {
		t.Fatalf("status = %d, want 200 with the user's account; body=%s", w.Code, w.Body.String())
	}
}

// With a Flutterwave client wired — production's shape — an activation request
// after the cutover must be told the feature is gone, not handed the generic
// 500 that reads as "try again" and has the user hammering a dead path.
func TestActivateVA_CentralMode_SaysRetiredRatherThanFailed(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetFlutterwaveClient(services.NewFlutterwaveClient("", "", "test-id", "test-secret"))
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}
	_ = h.it.AcceptITAgreement(context.Background(), user.ID, "", "")

	b, _ := json.Marshal(map[string]any{"idType": "BVN", "idNumber": "12345678901"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/it/va/activate", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	h.ActivateVA(c)

	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "retired") {
		t.Fatalf("body does not say the feature is retired: %s", w.Body.String())
	}
}

// The refusal that strands users after the cutover — kept in VA mode, where a
// user genuinely can activate, and gone in central mode, where there is nothing
// to activate.
func TestBuy_ActivationRefusal_OnlyInVAMode(t *testing.T) {
	h, _, it := newITBuyHandler(t)
	it.SetInboundCentral(false, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}

	data := itBuyData(t, doITBuy(t, h, user, "500000"))
	if data["needsActivation"] != true {
		t.Fatalf("needsActivation = %v, want true for a VA-mode user with no account", data["needsActivation"])
	}
	if data["payTo"] != nil {
		t.Fatalf("payTo = %v, want nil when activation is required", data["payTo"])
	}
}

// The screen is the only instruction the payer gets, and the claim is the only
// thing their transfer can be matched against. A pre-cutover order still
// carries fractional kobo, so a screen that truncates where the matcher rounds
// tells the buyer to send 1000 naira against a claim asking 1001 — a hold every
// single time, with the money already gone.
func TestCentralPayTo_HalfNaira_ShowsTheFigureTheMatcherWillAskFor(t *testing.T) {
	_, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := &models.User{ID: uuid.New(), Name: "JOHN DOE", WalletAddress: "0xWALLET"}

	// Minted before OpenBuyOrder started snapping the ask to whole naira, which
	// is the only way a fractional order still reaches this screen.
	intent := &models.ITIntent{
		ID:            uuid.New(),
		UserID:        user.ID,
		Direction:     models.ITDirectionBuy,
		Status:        models.ITStatusPendingPayment,
		Asset:         models.ITAssetMCGP,
		Currency:      "NGN",
		WalletAddress: user.WalletAddress,
		NgnKobo:       "100050",
		McgpAmount:    "1",
		RateKobo:      "100050",
		PaymentRef:    "IT-PRECUTOVER01",
	}
	if err := db.Create(intent).Error; err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	shown, _ := centralPayTo(testCentralAccount(), intent.NgnKobo)["amountNgn"].(string)

	claims, err := services.NewITMCGPClaimSource(db, it).OpenClaims(context.Background(), "NGN")
	if err != nil {
		t.Fatalf("OpenClaims: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claims, want 1", len(claims))
	}
	asked := strconv.FormatInt(claims[0].AmountKobo/100, 10)
	if shown != asked {
		t.Fatalf("screen shows %s naira but the claim asks %s naira - the payer cannot satisfy both", shown, asked)
	}
	if shown != "1001" {
		t.Fatalf("amountNgn = %q, want 1001 (half a naira rounds away from zero)", shown)
	}
}

// The pay-into screen and the matcher round in different numeric domains — the
// screen works on the arbitrary-precision decimal string stored in ngn_kobo,
// the matcher on int64 — so they cannot share an implementation. They can only
// be held in step by a test that walks both.
//
// This is not a hypothetical drift: the screen truncated where the matcher
// rounds until a review caught it, which made every half-naira order ask for a
// figure the matcher would refuse, holding the credit every time.
func TestWholeNairaFromKoboAgreesWithTheMatcher(t *testing.T) {
	for kobo := int64(-100_050); kobo <= 100_050; kobo += 7 {
		want := services.RoundKoboToNaira(kobo) / 100
		got := wholeNairaFromKobo(strconv.FormatInt(kobo, 10))
		if got != strconv.FormatInt(want, 10) {
			t.Fatalf("wholeNairaFromKobo(%d) = %q, matcher would ask for %d — "+
				"the screen and the claim disagree, so the payer cannot satisfy both",
				kobo, got, want)
		}
	}
}
