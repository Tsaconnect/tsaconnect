// HTTP-level tests for the brokered stablecoin sell surface (Task 12):
// POST /api/it/stable/quote, /stable/sell, /stable/sell/:id/deposit.
//
// Reuses the DB-backed ITService fixture pattern from it_handler_test.go
// (sqlite in-memory + NewITService), wired with a stub FXRateQuoter so
// QuoteStableSell can price without a live Flutterwave call — mirroring
// internal/services/it_stable_sell_test.go's newStableSellDBService.
//
// doJSON/decode already exist in this package (p2p_handler_test.go) but are
// bound to *rig and json.Marshal a Go value — the brief's tests here pass
// already-serialized JSON strings, so a same-named helper would either
// collide or double-encode. Named doStableJSON/containsJSONString instead to
// avoid both.
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubStableQuoter is a fixed FXRateQuoter: 1 USDC == 1650.25 NGN, regardless
// of the requested pair. Good enough to exercise the HTTP wiring; the pricing
// arithmetic itself is covered by internal/services/it_stable_quote_test.go.
type stubStableQuoter struct{}

func (stubStableQuoter) QuoteRate(_ context.Context, src, dst string, _ float64) (services.FWRate, error) {
	return services.FWRate{
		Source:      services.FWMoney{Currency: src, Amount: 1},
		Destination: services.FWMoney{Currency: dst, Amount: 1650.25},
	}, nil
}

// newStableTestRouter builds a gin.Engine with the three stable routes
// mounted against a real (in-memory) DB-backed ITService, and a test user
// pre-set in context (auth is faked the same way p2p_handler_test.go does
// it — c.Set("user", ...) via middleware — since these routes require an
// authenticated caller).
func newStableTestRouter(t *testing.T) (*gin.Engine, *services.ITService) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	it := services.NewITService(db, nil, stubITFx{})
	it.SetRateQuoter(stubStableQuoter{})
	it.SetStableSpreadBps(155)
	it.SetStableRateLockTTL(15 * time.Minute)
	// CreateStableSellIntent fails closed without a configured token contract
	// for the asset (see services.ErrStableNoTokenAddress) — wire one so the
	// PostStableSell tests exercise the create path, not that refusal.
	it.SetStableVerifier(nil, nil, map[string]string{"USDC": "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"})

	h := NewITHandler(db, it)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}
	// PostStableSell sits behind the same IT trade gate as Buy/Sell, so the
	// fixture user has to have accepted — the same thing newITTestRig does.
	if err := it.AcceptITAgreement(context.Background(), user.ID, "", ""); err != nil {
		t.Fatalf("accept agreement: %v", err)
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", user)
		c.Next()
	})
	g := r.Group("/api/it")
	g.POST("/stable/quote", h.PostStableQuote)
	g.POST("/stable/sell", h.PostStableSell)
	g.POST("/stable/sell/:id/deposit", h.PostStableDeposit)

	return r, it
}

// newStableTestRouterMultiUser is a variant of newStableTestRouter that picks
// the caller from an "X-Test-User" header instead of always injecting the
// same fixed user — needed to exercise the deposit endpoint's ownership
// check (the IDOR fix) and the unauthenticated path at the actual HTTP
// wiring, not just the service layer. "A" (the default) and "B" are two
// distinct users; "none" leaves no user in context at all.
func newStableTestRouterMultiUser(t *testing.T) (*gin.Engine, *services.ITService) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	it := services.NewITService(db, nil, stubITFx{})
	it.SetRateQuoter(stubStableQuoter{})
	it.SetStableSpreadBps(155)
	it.SetStableRateLockTTL(15 * time.Minute)
	// CreateStableSellIntent fails closed without a configured token contract
	// for the asset (see services.ErrStableNoTokenAddress) — wire one so the
	// PostStableSell tests exercise the create path, not that refusal.
	it.SetStableVerifier(nil, nil, map[string]string{"USDC": "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"})

	h := NewITHandler(db, it)
	userA := &models.User{ID: uuid.New(), WalletAddress: "0xWALLETA"}
	userB := &models.User{ID: uuid.New(), WalletAddress: "0xWALLETB"}
	// Both accept: this fixture exercises ownership, not the trade gate.
	// "C" (below) is deliberately left unaccepted to cover the gate itself.
	for _, u := range []*models.User{userA, userB} {
		if err := it.AcceptITAgreement(context.Background(), u.ID, "", ""); err != nil {
			t.Fatalf("accept agreement: %v", err)
		}
	}
	userC := &models.User{ID: uuid.New(), WalletAddress: "0xWALLETC"}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Test-User") {
		case "B":
			c.Set("user", userB)
		case "C":
			c.Set("user", userC) // has NOT accepted the IT agreement
		case "none":
			// deliberately leave no user in context
		default:
			c.Set("user", userA)
		}
		c.Next()
	})
	g := r.Group("/api/it")
	g.POST("/stable/quote", h.PostStableQuote)
	g.POST("/stable/sell", h.PostStableSell)
	g.POST("/stable/sell/:id/deposit", h.PostStableDeposit)

	return r, it
}

func doStableJSON(t *testing.T, r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func containsJSONString(body []byte, want string) bool {
	return bytes.Contains(body, []byte(want))
}

func TestPostStableQuote_RejectsAnUnsupportedAsset(t *testing.T) {
	r, _ := newStableTestRouter(t)
	w := doStableJSON(t, r, http.MethodPost, "/api/it/stable/quote",
		`{"asset":"DOGE","amount":"100","currency":"NGN"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPostStableQuote_RejectsAMissingAmount(t *testing.T) {
	r, _ := newStableTestRouter(t)
	w := doStableJSON(t, r, http.MethodPost, "/api/it/stable/quote", `{"asset":"USDC","currency":"NGN"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPostStableSell_ReturnsFlutterwavesDepositAddress(t *testing.T) {
	// The response must point the user at Flutterwave. A response carrying an
	// address of ours would mean the platform had become a custodian.
	r, svc := newStableTestRouter(t)
	svc.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")

	w := doStableJSON(t, r, http.MethodPost, "/api/it/stable/sell",
		`{"asset":"USDC","amount":"100","currency":"NGN","sourceChain":"BSC",
		  "bank":{"code":"044","number":"0690000031","name":"Ada Okafor"}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if !containsJSONString(w.Body.Bytes(), "0x742d35Cc6634C0532925a3b844Bc454e4438f44e") {
		t.Fatalf("response carries no Flutterwave deposit address: %s", w.Body.String())
	}
}

func TestPostStableDeposit_RejectsAnEmptyTxHash(t *testing.T) {
	r, _ := newStableTestRouter(t)
	w := doStableJSON(t, r, http.MethodPost,
		"/api/it/stable/sell/00000000-0000-0000-0000-000000000000/deposit", `{"txHash":""}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPostStableDeposit_RejectsAMalformedIntentID(t *testing.T) {
	r, _ := newStableTestRouter(t)
	w := doStableJSON(t, r, http.MethodPost, "/api/it/stable/sell/not-a-uuid/deposit", `{"txHash":"0xabc"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// decodeStableResponse unwraps the standard {"success","message","data"}
// envelope (utils.SuccessResponse) into a raw data map, for tests that need
// to inspect individual response fields and their concrete JSON types.
func decodeStableResponse(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var out struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, body)
	}
	return out.Data
}

func TestPostStableQuote_ResponseCarriesEveryItemisedMoneyFieldAsAString(t *testing.T) {
	// The two properties that matter most for this endpoint: nothing about the
	// charge is hidden (spread and fee are itemised separately — collapsing
	// them would bury a charge the user is entitled to see), and every money
	// value crosses the wire as a JSON string so large minor-unit amounts
	// survive JS clients intact. Neither was covered by any existing test —
	// none of them decoded the response body at all.
	r, _ := newStableTestRouter(t)
	w := doStableJSON(t, r, http.MethodPost, "/api/it/stable/quote",
		`{"asset":"USDC","amount":"100","currency":"NGN"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	data := decodeStableResponse(t, w.Body.Bytes())

	for _, field := range []string{"gross", "afterSpread", "fee", "net", "rate", "expiresAt"} {
		if _, ok := data[field]; !ok {
			t.Fatalf("response is missing %q: %v", field, data)
		}
	}

	// This must fail if any of these fields is emitted as a raw JSON number
	// (e.g. via an unconverted float64 in the handler) rather than a string —
	// exactly the regression this test exists to catch.
	for _, field := range []string{"amount", "gross", "afterSpread", "fee", "net", "rate"} {
		v, ok := data[field].(string)
		if !ok {
			t.Fatalf("field %q = %T(%v), want string", field, data[field], data[field])
		}
		if v == "" {
			t.Fatalf("field %q is an empty string", field)
		}
	}
}

func TestPostStableDeposit_RequiresAuthentication(t *testing.T) {
	r, _ := newStableTestRouterMultiUser(t)
	req := httptest.NewRequest(http.MethodPost,
		"/api/it/stable/sell/00000000-0000-0000-0000-000000000000/deposit",
		bytes.NewReader([]byte(`{"txHash":"0xabc"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "none")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%s", w.Code, w.Body.String())
	}
}

func TestPostStableDeposit_RejectsAnotherUsersIntent(t *testing.T) {
	// The IDOR, end to end through the actual route wiring: user B must not
	// be able to attach a deposit report to user A's sell intent, and A's
	// intent must be left completely untouched by the attempt.
	r, svc := newStableTestRouterMultiUser(t)
	svc.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")

	sellReq := httptest.NewRequest(http.MethodPost, "/api/it/stable/sell",
		bytes.NewReader([]byte(`{"asset":"USDC","amount":"100","currency":"NGN","sourceChain":"BSC",
		  "bank":{"code":"044","number":"0690000031","name":"Ada Okafor"}}`)))
	sellReq.Header.Set("Content-Type", "application/json")
	sellReq.Header.Set("X-Test-User", "A")
	sellW := httptest.NewRecorder()
	r.ServeHTTP(sellW, sellReq)
	if sellW.Code != http.StatusOK {
		t.Fatalf("sell status = %d body=%s", sellW.Code, sellW.Body.String())
	}
	sellData := decodeStableResponse(t, sellW.Body.Bytes())
	intentField, ok := sellData["intent"].(map[string]any)
	if !ok {
		t.Fatalf("sell response carries no intent object: %v", sellData)
	}
	victimID, _ := intentField["id"].(string)
	if victimID == "" {
		t.Fatalf("sell response intent has no id: %v", intentField)
	}

	depositReq := httptest.NewRequest(http.MethodPost,
		"/api/it/stable/sell/"+victimID+"/deposit",
		bytes.NewReader([]byte(`{"txHash":"0xattacker"}`)))
	depositReq.Header.Set("Content-Type", "application/json")
	depositReq.Header.Set("X-Test-User", "B")
	depositW := httptest.NewRecorder()
	r.ServeHTTP(depositW, depositReq)

	if depositW.Code == http.StatusOK {
		t.Fatalf("cross-user deposit report succeeded: %s", depositW.Body.String())
	}

	victimUUID, err := uuid.Parse(victimID)
	if err != nil {
		t.Fatalf("parse victim id: %v", err)
	}
	got, err := svc.GetIntent(context.Background(), victimUUID)
	if err != nil {
		t.Fatalf("reload victim intent: %v", err)
	}
	if got.DepositTxHash != "" {
		t.Fatalf("victim depositTxHash = %q, want empty", got.DepositTxHash)
	}
	if got.Status != models.ITStatusPendingDeposit {
		t.Fatalf("victim status = %q, want %q", got.Status, models.ITStatusPendingDeposit)
	}
}

func TestPostStableSell_RequiresTheITAgreement(t *testing.T) {
	// The brokered ramp routes real user funds to a third party under the CBN
	// VASP pilot. It must sit behind the same trade gate ActivateVA/Buy/Sell
	// use — user C has never accepted, so the sell must be refused before any
	// intent is created.
	r, svc := newStableTestRouterMultiUser(t)
	svc.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")

	req := httptest.NewRequest(http.MethodPost, "/api/it/stable/sell",
		bytes.NewReader([]byte(`{"asset":"USDC","amount":"100","currency":"NGN","sourceChain":"BSC",
		  "bank":{"code":"044","number":"0690000031","name":"Ada Okafor"}}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "C")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", w.Code, w.Body.String())
	}
	// The app keys off this machine code to show the agreement — the same
	// contract the other three gated endpoints already have.
	if !containsJSONString(w.Body.Bytes(), "it_agreement_required") {
		t.Fatalf("response carries no it_agreement_required code: %s", w.Body.String())
	}
}
