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
	if err := db.AutoMigrate(&models.ITIntent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	it := services.NewITService(db, nil, stubITFx{})
	it.SetRateQuoter(stubStableQuoter{})
	it.SetStableSpreadBps(155)
	it.SetStableRateLockTTL(15 * time.Minute)

	h := NewITHandler(db, it)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}

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
