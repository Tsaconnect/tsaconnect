package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestStableCodeFor_CoversBuyErrors(t *testing.T) {
	// An unmapped error reaches the app as an empty code, which the client
	// cannot branch on — it becomes a generic failure on a money path.
	for _, tc := range []struct {
		err  error
		want string
	}{
		{services.ErrStableBuyBelowMinimum, "stable_buy_below_minimum"},
		{services.ErrStableBuyAlreadyOpen, "stable_buy_already_open"},
		{services.ErrStableBuyNoVA, "stable_buy_no_va"},
		{services.ErrUnsupportedChainAsset, "stable_unsupported_chain_asset"},
		{services.ErrStableDeliveryExhausted, "stable_delivery_exhausted"},
		// The buy path raises this whenever HasStableBuy is unsatisfied.
		{services.ErrStableQuoteUnavailable, "stable_not_configured"},
		// Not buy-specific: it_stable_sell.go raises ErrITBadState at seven
		// sites, so a buy-flavoured code here would mislabel a sell failure.
		{services.ErrITBadState, "stable_bad_state"},
	} {
		if got := stableCodeFor(tc.err); got != tc.want {
			t.Fatalf("code for %v = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestStableStatusFor_BuyInputErrorsAre400(t *testing.T) {
	for _, err := range []error{
		services.ErrStableBuyBelowMinimum,
		services.ErrStableBuyAlreadyOpen,
		services.ErrStableBuyNoVA,
		// 400, not 503. The rail is healthy; an out-of-date client asked for
		// the wrong one, and a 5xx would have it retry forever.
		services.ErrStableBuyWrongRamp,
		services.ErrUnsupportedChainAsset,
	} {
		if got := stableStatusFor(err); got != 400 {
			t.Fatalf("status for %v = %d, want 400", err, got)
		}
	}
}

// stubBuyQuoter echoes a fixed NGN->asset probe: "to receive 1 unit, pay
// 1650.25 NGN". stubStableQuoter (it_stable_handler_test.go) is direction-fixed
// for the SELL leg (asset->currency); QuoteStableBuy probes the reverse
// direction (currency->asset), so reusing that stub here would echo back
// whatever currency/asset labels it is given with the sell-shaped amounts —
// which is how an earlier version of this fixture failed the independent-rate
// plausibility check with a rate off by six orders of magnitude. Mirrors
// buyRateQuoter in internal/services/it_stable_buy_quote_test.go.
// The buy leg refuses everything unless HasStableBuy is satisfied — converter,
// merchant id, rate source and deliverer all wired. These two stubs supply the
// converter and deliverer halves; nothing in a handler test exercises them, but
// without them every buy request is refused as "not enabled".
type stubBuyConverter struct{}

func (stubBuyConverter) FetchBalances(context.Context) ([]services.FWBalance, error) {
	return nil, nil
}

func (stubBuyConverter) ConvertBalance(_ context.Context, _, _, _ string, _ services.FWAmount, _ string) (services.FWTransfer, error) {
	return services.FWTransfer{}, nil
}

type stubBuyDeliverer struct{}

func (stubBuyDeliverer) DirectTransfer(context.Context, services.FWDirectTransferRequest) (services.FWTransfer, error) {
	return services.FWTransfer{}, nil
}

func wireStableBuyPrereqs(it *services.ITService) {
	it.SetStableConverter(stubBuyConverter{}, "merchant_fixture")
	it.SetStableDeliverer(stubBuyDeliverer{})
}

type stubBuyQuoter struct{}

func (stubBuyQuoter) QuoteRate(_ context.Context, src, dst string, destAmount float64) (services.FWRate, error) {
	return services.FWRate{
		Source:      services.FWMoney{Currency: src, Amount: services.FWQuoteAmount(1650.25 * destAmount)},
		Destination: services.FWMoney{Currency: dst, Amount: services.FWQuoteAmount(destAmount)},
	}, nil
}

// newStableBuyTestRouterWithIdempotency wires PostStableBuy behind the real
// middleware.Idempotency, matching routes.go's production wiring
// (it.POST("/stable/buy", itIdem, ith.PostStableBuy)).
//
// PostStableBuy's UserDeliveryAddress call reads the user back from the DB
// (it is never trusted from the request body), so — unlike the plain
// it_handler_test.go fixtures that only Set the user into gin context — this
// needs a REAL users row. models.User's id column carries a PostgreSQL-only
// `default:gen_random_uuid()` that SQLite's driver cannot parse, so the table
// is raw DDL, the same workaround it_stable_idempotency_test.go documents and
// internal/services/it_stable_buy_test.go's newStableBuyDeliveryTestDB uses.
func newStableBuyTestRouterWithIdempotency(t *testing.T) (*gin.Engine, *gorm.DB, uuid.UUID) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITAgreementAcceptance{}, &models.FiatVirtualAccount{}, &models.WalletAddress{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		username TEXT NOT NULL UNIQUE,
		email TEXT NOT NULL UNIQUE,
		password TEXT NOT NULL,
		wallet_address TEXT,
		verification_status TEXT DEFAULT 'pending'
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	// RequestIdempotency has jsonb-typed columns AutoMigrate cannot express
	// under sqlite — same raw-DDL workaround it_stable_idempotency_test.go uses.
	if err := db.Exec(`CREATE TABLE request_idempotencies (
		key TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		response_status INTEGER NOT NULL,
		response_body TEXT,
		response_headers TEXT,
		created_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create request_idempotencies: %v", err)
	}

	userID := uuid.New()
	const evmAddr = "0x2222222222222222222222222222222222222222"
	// Verified: the buy leg refuses an unverified user outright, so an
	// unverified fixture would make every test here fail at the KYC gate
	// rather than at the behaviour it pins.
	if err := db.Exec(`INSERT INTO users (id, name, username, email, password, wallet_address, verification_status)
		VALUES (?, 'Ada Okafor', 'ada', 'ada@test.com', 'x', ?, ?)`,
		userID.String(), evmAddr, models.VerificationStatusVerified).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&models.FiatVirtualAccount{
		ID: uuid.New(), UserID: userID, Currency: "NGN",
		AccountNumber: "1234567890", BankName: "Wema Bank", AccountName: "TSA / Ada Okafor",
		Status:    models.FiatVAStatusActive,
		Reference: "ITVA-" + userID.String(),
	}).Error; err != nil {
		t.Fatalf("seed va: %v", err)
	}

	it := services.NewITService(db, nil, stubITFx{})
	it.SetRateQuoter(stubBuyQuoter{})
	it.SetStableSpreadBps(155)
	it.SetStableRateLockTTL(15 * time.Minute)
	wireStableBuyPrereqs(it)
	if err := it.AcceptITAgreement(context.Background(), userID, "", ""); err != nil {
		t.Fatalf("accept agreement: %v", err)
	}

	h := NewITHandler(db, it, nil)
	idem := middleware.Idempotency(db)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", &models.User{ID: userID})
		c.Next()
	})
	g := r.Group("/api/it")
	g.POST("/stable/buy", idem, h.PostStableBuy)

	return r, db, userID
}

func TestPostStableBuy_RetriedIdempotencyKey_DoesNotMintASecondPurchase(t *testing.T) {
	r, db, _ := newStableBuyTestRouterWithIdempotency(t)

	const body = `{"asset":"USDC","amount":"16502500","currency":"NGN","destChain":"POLYGON"}`
	makeReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/it/stable/buy", bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "buy-key-1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w1 := makeReq()
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: status = %d body=%s", w1.Code, w1.Body.String())
	}

	w2 := makeReq()
	if w2.Code != http.StatusOK {
		t.Fatalf("replay: status = %d body=%s", w2.Code, w2.Body.String())
	}
	if w2.Body.String() != w1.Body.String() {
		t.Fatalf("replay body mismatch — a retry minted a fresh purchase instead of replaying:\nfirst=%s\nsecond=%s",
			w1.Body.String(), w2.Body.String())
	}
	if w2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("expected Idempotent-Replayed: true on the second call")
	}

	var count int64
	if err := db.Model(&models.ITIntent{}).Count(&count).Error; err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if count != 1 {
		t.Fatalf("intent count = %d, want 1 — a replayed create must not mint a second purchase", count)
	}
}

// TestPostStableBuyQuote_ResponseIsMarkedIndicative pins the contract the
// spec calls out explicitly: the app must never be able to present a buy
// quote as a firm price, because the binding rate is struck only when the
// naira actually lands (see ConvertStableBuy). "indicative" must always be
// present and true. PostStableBuyQuote needs no authenticated user, so this
// fixture is deliberately smaller than the create-route one above.
func TestPostStableBuyQuote_ResponseIsMarkedIndicative(t *testing.T) {
	gin.SetMode(gin.TestMode)
	it := services.NewITService(nil, nil, stubITFx{})
	it.SetRateQuoter(stubBuyQuoter{})
	it.SetStableSpreadBps(155)
	it.SetStableRateLockTTL(15 * time.Minute)
	wireStableBuyPrereqs(it)
	h := NewITHandler(nil, it, nil)

	r := gin.New()
	r.POST("/api/it/stable/buy/quote", h.PostStableBuyQuote)

	const body = `{"asset":"USDC","amount":"16502500","currency":"NGN"}`
	req := httptest.NewRequest(http.MethodPost, "/api/it/stable/buy/quote", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	if !containsJSONString(w.Body.Bytes(), `"indicative":true`) {
		t.Fatalf("response does not mark itself indicative: %s", w.Body.String())
	}
}

// Phase B1 is NGN-only: the money arrives through the user's standing NGN
// virtual account. An unsupported currency used to be priced and quoted first
// and only failed at the VA lookup, reported as a missing virtual account.
func TestPostStableBuy_RejectsANonNGNCurrencyUpFront(t *testing.T) {
	r, db, userID := newStableBuyTestRouterWithIdempotency(t)

	const body = `{"asset":"USDC","amount":"16502500","currency":"EUR","destChain":"POLYGON"}`
	req := httptest.NewRequest(http.MethodPost, "/api/it/stable/buy", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "buy-key-eur")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", w.Code, w.Body.String())
	}
	if !containsJSONString(w.Body.Bytes(), `"stable_buy_unsupported_currency"`) {
		t.Fatalf("response carries no machine code the app can branch on: %s", w.Body.String())
	}
	var count int64
	db.Model(&models.ITIntent{}).Where("user_id = ?", userID).Count(&count)
	if count != 0 {
		t.Fatalf("intent count = %d, want 0 — nothing may be written for an unsupported currency", count)
	}
}
