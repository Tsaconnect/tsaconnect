// Regression coverage for the IT write-route idempotency wiring
// (internal/routes/routes.go: the `itIdem := middleware.Idempotency(config.DB)`
// instance attached to the IT group's write routes, POST /stable/sell among
// them). A retried "create sell intent" call must not mint a second intent —
// every stablecoin sell intent is a claim on a deposit.
//
// A full exercise of routes.go's SetupRoutes is impractical here: it takes
// ~29 handler dependencies, a package-level config.DB (used by both
// middleware.Auth and the idem instantiation — not the *config.Config param),
// and a real JWT-authenticated models.User row. models.User's `id` column
// defaults via `gen_random_uuid()` (Postgres-only — see user.go and the
// CLAUDE.md note on wallet tests using raw DDL instead of AutoMigrate for
// exactly this reason), so standing one up under the sqlite fixtures the rest
// of this package uses would mean hand-writing the whole users table DDL for
// a single test. Instead this test mounts the exact same middleware
// constructor (middleware.Idempotency) in front of the exact same handler
// (ITHandler.PostStableSell) in the same order routes.go wires them, and
// proves the behavior that matters: a replayed key replays the cached
// response and does not create a second row.
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

// newStableTestRouterWithIdempotency is newStableTestRouter (it_stable_handler_test.go)
// plus the real middleware.Idempotency wired onto the write routes, matching
// routes.go's production order (auth's user-in-context stand-in, then idem,
// then the handler).
func newStableTestRouterWithIdempotency(t *testing.T) (*gin.Engine, *services.ITService, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// RequestIdempotency has jsonb-typed columns (gorm.io/datatypes) that
	// AutoMigrate cannot express under sqlite — same raw-DDL workaround
	// internal/middleware/idempotency_test.go uses.
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

	it := services.NewITService(db, nil, stubITFx{})
	it.SetRateQuoter(stubStableQuoter{})
	it.SetStableSpreadBps(155)
	it.SetStableRateLockTTL(15 * time.Minute)
	it.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")
	it.SetStableVerifier(nil, nil, map[string]string{"USDC": "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"})

	h := NewITHandler(db, it, nil)
	user := &models.User{ID: uuid.New(), WalletAddress: "0xWALLET"}
	if err := it.AcceptITAgreement(context.Background(), user.ID, "", ""); err != nil {
		t.Fatalf("accept agreement: %v", err)
	}

	idem := middleware.Idempotency(db)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("user", user)
		c.Next()
	})
	g := r.Group("/api/it")
	g.POST("/stable/sell", idem, h.PostStableSell)

	return r, it, db
}

// doStableJSONWithHeader is doStableJSON (it_stable_handler_test.go) plus one
// extra request header — needed here to set Idempotency-Key.
func doStableJSONWithHeader(t *testing.T, r *gin.Engine, method, path, body, headerKey, headerVal string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerKey, headerVal)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPostStableSell_RetriedIdempotencyKey_DoesNotMintASecondIntent(t *testing.T) {
	r, _, db := newStableTestRouterWithIdempotency(t)

	const body = `{"asset":"USDC","amount":"100","currency":"NGN","sourceChain":"BSC",
	  "bank":{"code":"044","number":"0690000031","name":"Ada Okafor"}}`

	makeReq := func() *httptest.ResponseRecorder {
		return doStableJSONWithHeader(t, r, http.MethodPost, "/api/it/stable/sell", body, "Idempotency-Key", "sell-key-1")
	}

	w1 := makeReq()
	if w1.Code != 200 {
		t.Fatalf("first call: status = %d body=%s", w1.Code, w1.Body.String())
	}

	w2 := makeReq()
	if w2.Code != 200 {
		t.Fatalf("replay: status = %d body=%s", w2.Code, w2.Body.String())
	}
	if w2.Body.String() != w1.Body.String() {
		t.Fatalf("replay body mismatch — a retry minted a fresh intent instead of replaying:\nfirst=%s\nsecond=%s",
			w1.Body.String(), w2.Body.String())
	}
	if w2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("expected Idempotent-Replayed: true on the second call")
	}

	// The real assertion: exactly one ITIntent row exists, not two.
	var count int64
	if err := db.Model(&models.ITIntent{}).Count(&count).Error; err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if count != 1 {
		t.Fatalf("intent count = %d, want 1 — a replayed key must not create a second claim on a deposit", count)
	}
}

func TestPostStableSell_DifferentIdempotencyKeys_EachCreatesItsOwnIntent(t *testing.T) {
	// Sanity check on the flip side: two genuinely distinct requests (distinct
	// keys) must each go through — the middleware must not over-suppress.
	r, _, db := newStableTestRouterWithIdempotency(t)

	const body = `{"asset":"USDC","amount":"100","currency":"NGN","sourceChain":"BSC",
	  "bank":{"code":"044","number":"0690000031","name":"Ada Okafor"}}`

	w1 := doStableJSONWithHeader(t, r, http.MethodPost, "/api/it/stable/sell", body, "Idempotency-Key", "sell-key-a")
	if w1.Code != 200 {
		t.Fatalf("first call: status = %d body=%s", w1.Code, w1.Body.String())
	}
	w2 := doStableJSONWithHeader(t, r, http.MethodPost, "/api/it/stable/sell", body, "Idempotency-Key", "sell-key-b")
	if w2.Code != 200 {
		t.Fatalf("second call: status = %d body=%s", w2.Code, w2.Body.String())
	}

	var count int64
	if err := db.Model(&models.ITIntent{}).Count(&count).Error; err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if count != 2 {
		t.Fatalf("intent count = %d, want 2 — distinct keys must not be collapsed together", count)
	}
}
