// Coverage for stableCodeFor (it_handler.go) and the machine `code` field it
// feeds into utils.ErrorResponseWithCode on the three stable handlers. The
// case that matters most: ErrStableDepositAlreadyClaimed must surface as
// "stable_deposit_already_claimed" so the app can show it prominently rather
// than as a generic error.
package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestStableCodeFor_MapsEachSentinelToItsSnakeCaseCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"already claimed", services.ErrStableDepositAlreadyClaimed, "stable_deposit_already_claimed"},
		{"intent not found", services.ErrStableIntentNotFound, "stable_intent_not_found"},
		{"quote expired", services.ErrStableQuoteExpired, "stable_quote_expired"},
		{"rate implausible", services.ErrStableRateImplausible, "stable_rate_implausible"},
		{"below minimum", services.ErrStableBelowMinimum, "stable_below_minimum"},
		{"not an asset", services.ErrStableNotAnAsset, "stable_not_an_asset"},
		{"no deposit address", services.ErrStableNoDepositAddress, "stable_not_configured"},
		{"no token address", services.ErrStableNoTokenAddress, "stable_not_configured"},
		{"fee exceeds proceeds", services.ErrStableFeeExceedsProceeds, "stable_fee_exceeds_proceeds"},
		// Wrapped errors must still match via errors.Is, same as stableStatusFor.
		{"wrapped already claimed", fmtErrorf(services.ErrStableDepositAlreadyClaimed), "stable_deposit_already_claimed"},
		{"unmapped", errors.New("some other failure"), ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stableCodeFor(tc.err); got != tc.want {
				t.Errorf("stableCodeFor(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// fmtErrorf wraps an error the same way the service layer does (fmt.Errorf
// with %w), so the mapping test also proves stableCodeFor matches through a
// wrapper and not just the bare sentinel.
func fmtErrorf(err error) error {
	return fmt.Errorf("wrapped: %w", err)
}

// newStableCodeTestFixture wires two intents whose deposit hash collides —
// the actual condition ReportStableDeposit maps to
// ErrStableDepositAlreadyClaimed — through the real HTTP route, so the
// assertion below exercises the full handler → utils.ErrorResponseWithCode
// path, not just the mapping function in isolation.
func newStableCodeTestFixture(t *testing.T) *gin.Engine {
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
	// Partial unique index on deposit_tx_hash — the real DB constraint
	// (config/database.go) that makes a second report against a different
	// intent surface as ErrStableDepositAlreadyClaimed rather than silently
	// succeeding. AutoMigrate on ITIntent alone doesn't create it (it's not a
	// struct tag), same workaround internal/services/it_stable_confirm_test.go
	// uses for the equivalent service-level test.
	if err := db.Exec(`CREATE UNIQUE INDEX uq_it_intents_deposit_tx_hash
	    ON it_intents (deposit_tx_hash) WHERE deposit_tx_hash <> ''`).Error; err != nil {
		t.Fatalf("create unique index: %v", err)
	}

	it := services.NewITService(db, nil, stubITFx{})
	it.SetRateQuoter(stubStableQuoter{})
	it.SetStableSpreadBps(155)
	it.SetStableRateLockTTL(15 * time.Minute)
	it.SetStableDepositAddress("POLYGON", "0x742d35Cc6634C0532925a3b844Bc454e4438f44e")
	it.SetStableVerifier(nil, nil, map[string]string{"USDC": "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359"})

	h := NewITHandler(db, it)
	userA := &models.User{ID: uuid.New(), WalletAddress: "0xWALLETA"}
	userB := &models.User{ID: uuid.New(), WalletAddress: "0xWALLETB"}
	for _, u := range []*models.User{userA, userB} {
		if err := it.AcceptITAgreement(context.Background(), u.ID, "", ""); err != nil {
			t.Fatalf("accept agreement: %v", err)
		}
	}

	r := gin.New()
	r.Use(func(c *gin.Context) {
		if c.GetHeader("X-Test-User") == "B" {
			c.Set("user", userB)
		} else {
			c.Set("user", userA)
		}
		c.Next()
	})
	g := r.Group("/api/it")
	g.POST("/stable/sell", h.PostStableSell)
	g.POST("/stable/sell/:id/deposit", h.PostStableDeposit)
	return r
}

func TestPostStableDeposit_AlreadyClaimed_ResponseCarriesTheMachineCode(t *testing.T) {
	r := newStableCodeTestFixture(t)
	const sellBody = `{"asset":"USDC","amount":"100","currency":"NGN","sourceChain":"BSC",
	  "bank":{"code":"044","number":"0690000031","name":"Ada Okafor"}}`

	createIntent := func(asUser string) string {
		req := httptest.NewRequest(http.MethodPost, "/api/it/stable/sell", bytes.NewReader([]byte(sellBody)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", asUser)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("create intent (%s): status = %d body=%s", asUser, w.Code, w.Body.String())
		}
		data := decodeStableResponse(t, w.Body.Bytes())
		intent, ok := data["intent"].(map[string]any)
		if !ok {
			t.Fatalf("create intent (%s): no intent object: %v", asUser, data)
		}
		id, _ := intent["id"].(string)
		if id == "" {
			t.Fatalf("create intent (%s): no id: %v", asUser, intent)
		}
		return id
	}

	intentA := createIntent("A")
	intentB := createIntent("B")

	const sharedHash = "0xshared-deposit"
	depositReq := func(intentID, asUser string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/it/stable/sell/"+intentID+"/deposit",
			bytes.NewReader([]byte(`{"txHash":"`+sharedHash+`"}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Test-User", asUser)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	first := depositReq(intentA, "A")
	if first.Code != http.StatusOK {
		t.Fatalf("first deposit report: status = %d body=%s", first.Code, first.Body.String())
	}

	second := depositReq(intentB, "B")
	if second.Code != http.StatusBadRequest {
		t.Fatalf("second deposit report (same hash, different intent): status = %d, want 400, body=%s",
			second.Code, second.Body.String())
	}
	if !containsJSONString(second.Body.Bytes(), `"code":"stable_deposit_already_claimed"`) {
		t.Fatalf("response carries no stable_deposit_already_claimed code: %s", second.Body.String())
	}
}

// TestStableStatusFor_NoTokenAddressIs503 pins ErrStableNoTokenAddress to the
// same class as ErrStableNoDepositAddress: the request was fine, the platform
// is not configured for it, so this is a 503 rather than a 400.
func TestStableStatusFor_NoTokenAddressIs503(t *testing.T) {
	if got := stableStatusFor(services.ErrStableNoTokenAddress); got != http.StatusServiceUnavailable {
		t.Fatalf("stableStatusFor(ErrStableNoTokenAddress) = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

// TestPostStableSell_NoTokenAddressConfigured_Returns503WithStableNotConfigured
// exercises the full handler path (not just the mapping functions in
// isolation): with a Flutterwave deposit address wired but no token contract
// for the asset, PostStableSell must refuse — never returning an intent with
// an unresolvable deposit instruction — and surface the same machine code the
// app already handles for a missing deposit address.
func TestPostStableSell_NoTokenAddressConfigured_Returns503WithStableNotConfigured(t *testing.T) {
	r := newStableCodeTestFixture(t)

	// The fixture wires only "USDC" — a USDT sell has no configured token.
	const sellBody = `{"asset":"USDT","amount":"100","currency":"NGN","sourceChain":"BSC",
	  "bank":{"code":"044","number":"0690000031","name":"Ada Okafor"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/it/stable/sell", bytes.NewReader([]byte(sellBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-User", "A")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusServiceUnavailable, w.Body.String())
	}
	if !containsJSONString(w.Body.Bytes(), `"code":"stable_not_configured"`) {
		t.Fatalf("response carries no stable_not_configured code: %s", w.Body.String())
	}
}
