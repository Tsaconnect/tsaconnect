package routes

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// newPayRefundRoutesRig is the real router with refunds wired as cmd/server
// wires them — no chain, so only test-mode refunds can be made, which is all
// these route tests need.
func newPayRefundRoutesRig(t *testing.T) (*gin.Engine, *config.Config, *gorm.DB) {
	t.Helper()
	r, cfg, db, _ := newPayRoutesRigWith(t, func(db *gorm.DB, merchants *services.PayMerchantService, payments *services.PayPaymentService, payh *handlers.PayHandler) {
		if err := db.AutoMigrate(&models.PayRefund{}, &models.PaycrestOrderRecord{}, &models.RampSellIntent{}, &models.WalletLinkChallenge{}); err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`CREATE TABLE request_idempotencies (key TEXT PRIMARY KEY, user_id TEXT NOT NULL,
			request_hash TEXT NOT NULL, response_status INTEGER NOT NULL, response_body TEXT, response_headers TEXT,
			created_at DATETIME)`).Error; err != nil {
			t.Fatal(err)
		}
		now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
		refunds := services.NewPayRefundService(db, payments, merchants, nil, nil, 5, now)
		payments.SetRefundAggregator(refunds)
		payh.WithRefunds(refunds)
	})
	return r, cfg, db
}

// payRefundRoutesPaid is a succeeded test payment of m's, paid by a TSA user.
func payRefundRoutesPaid(t *testing.T, db *gorm.DB, m *models.PayMerchant, ref string) *models.PayPayment {
	t.Helper()
	payer := payRoutesUser(t, db, models.RoleUser)
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	p := &models.PayPayment{MerchantID: m.ID, Mode: models.PayModeTest, Reference: ref, AmountKobo: 1_500_000, Currency: "NGN",
		Source: models.PaySourceLink, FeeBps: 100, CustomerFeeSharePercent: 100, CustomerTotalKobo: 1_500_000, MerchantNetKobo: 1_485_000,
		SettlementMode: models.PaySettlementFiat, ExpiresAt: at.Add(time.Hour), Status: models.PayStatusSucceeded, SucceededAt: &at, PayerUserID: &payer.ID}
	if err := db.Create(p).Error; err != nil {
		t.Fatal(err)
	}
	return p
}

func payRefundRoutesCreate(r *gin.Engine, tok, paymentID, key string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	json.NewEncoder(&buf).Encode(map[string]any{"amount": "1", "reason": "Cancelled"})
	req := httptest.NewRequest(http.MethodPost, "/api/pay/merchant/payments/"+paymentID+"/refunds?mode=test", &buf)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// With refunds off (no chain registry, so cmd/server never wires them)
// every owner and admin refund route is still mounted and answers 503
// refunds_unavailable — never a 404 a client would read as a missing refund.
func TestPayRefundRoutes_OffAnswersRefundsUnavailable(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	u, _ := payRoutesOwner(t, db)
	owner := payRoutesToken(t, cfg.JWTSecret, u.ID)
	super := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleSuperAdmin).ID)
	id := uuid.NewString()
	for _, rt := range []struct{ method, path, tok string }{
		{http.MethodPost, "/api/pay/merchant/payments/" + id + "/refunds?mode=test", owner},
		{http.MethodGet, "/api/pay/merchant/payments/" + id + "/refunds?mode=test", owner},
		{http.MethodGet, "/api/pay/merchant/refunds?mode=test", owner},
		{http.MethodGet, "/api/pay/merchant/refunds/" + id, owner},
		{http.MethodPost, "/api/pay/merchant/refunds/" + id + "/cancel", owner},
		{http.MethodPost, "/api/pay/merchant/refunds/" + id + "/submit", owner},
		{http.MethodGet, "/api/admin/pay/refunds", super},
		{http.MethodPost, "/api/admin/pay/refunds/" + id + "/reevaluate", super},
	} {
		w := payRoutesDo(r, rt.method, rt.path, rt.tok, map[string]any{})
		var body struct{ Message, Code string }
		json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusServiceUnavailable || body.Code != "refunds_unavailable" || body.Message != "Refunds aren't available yet." {
			t.Errorf("%s %s: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
}

// Every refund route needs a session and is never cached.
func TestPayRefundRoutes_EveryRouteIsAuthedAndUncached(t *testing.T) {
	r, _, _ := newPayRefundRoutesRig(t)
	id := uuid.NewString()
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/pay/merchant/payments/" + id + "/refunds?mode=test"},
		{http.MethodGet, "/api/pay/merchant/payments/" + id + "/refunds?mode=test"},
		{http.MethodGet, "/api/pay/merchant/refunds?mode=test"},
		{http.MethodGet, "/api/pay/merchant/refunds/" + id},
		{http.MethodPost, "/api/pay/merchant/refunds/" + id + "/cancel"},
		{http.MethodPost, "/api/pay/merchant/refunds/" + id + "/submit"},
	} {
		w := payRoutesDo(r, rt.method, rt.path, "", map[string]any{})
		if w.Code != http.StatusUnauthorized || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: %d %v", rt.method, rt.path, w.Code, w.Header())
		}
	}
}

// Through the real router: a test refund is made once per Idempotency-Key and
// comes back confirmed, its routes read it with no mode, and another
// merchant's owner can neither read nor cancel it.
func TestPayRefundRoutes_CreateReadCancelScopedToTheOwner(t *testing.T) {
	r, cfg, db := newPayRefundRoutesRig(t)
	u, m := payRoutesOwner(t, db)
	tok := payRoutesToken(t, cfg.JWTSecret, u.ID)
	p := payRefundRoutesPaid(t, db, m, "RR-1")

	first, again := payRefundRoutesCreate(r, tok, p.ID.String(), "tap-1"), payRefundRoutesCreate(r, tok, p.ID.String(), "tap-1")
	if first.Code != http.StatusCreated || again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" ||
		first.Body.String() != again.Body.String() {
		t.Fatalf("first %d %s, again %d %q", first.Code, first.Body.String(), again.Code, again.Header().Get("Idempotent-Replayed"))
	}
	var created struct {
		Data struct {
			Refund struct{ ID, Status, Mode string }
		}
	}
	json.Unmarshal(first.Body.Bytes(), &created)
	if created.Data.Refund.Status != models.PayRefundConfirmed || created.Data.Refund.Mode != models.PayModeTest {
		t.Fatalf("%s", first.Body.String())
	}
	var n int64
	if db.Model(&models.PayRefund{}).Count(&n); n != 1 {
		t.Fatalf("a replayed key made %d refunds", n)
	}
	id := created.Data.Refund.ID
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant/refunds/"+id, tok, nil); w.Code != http.StatusOK {
		t.Fatalf("detail %d %s", w.Code, w.Body.String())
	}
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant/payments/"+p.ID.String()+"/refunds?mode=test", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("payment refunds %d %s", w.Code, w.Body.String())
	}
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant/refunds?mode=test", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("list %d %s", w.Code, w.Body.String())
	}

	other, _ := payRoutesOwner(t, db)
	otherTok := payRoutesToken(t, cfg.JWTSecret, other.ID)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/pay/merchant/refunds/" + id},
		{http.MethodPost, "/api/pay/merchant/refunds/" + id + "/cancel"},
		{http.MethodGet, "/api/pay/merchant/payments/" + p.ID.String() + "/refunds?mode=test"},
	} {
		if w := payRoutesDo(r, rt.method, rt.path, otherTok, nil); w.Code != http.StatusNotFound {
			t.Errorf("another owner %s %s: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
}

// Creating and cancelling share ten writes a minute (a replayed create is not
// one); handing over a signed transfer has its own ten.
func TestPayRefundRoutes_WritesAreLimitedTenAMinute(t *testing.T) {
	r, cfg, db := newPayRefundRoutesRig(t)
	u, m := payRoutesOwner(t, db)
	tok := payRoutesToken(t, cfg.JWTSecret, u.ID)
	p := payRefundRoutesPaid(t, db, m, "RR-2")
	if w := payRefundRoutesCreate(r, tok, p.ID.String(), "tap-1"); w.Code != http.StatusCreated {
		t.Fatalf("first %d %s", w.Code, w.Body.String())
	}
	if w := payRefundRoutesCreate(r, tok, p.ID.String(), "tap-1"); w.Code != http.StatusCreated {
		t.Fatalf("replay %d", w.Code)
	}
	for i := 2; i <= 9; i++ {
		if w := payRefundRoutesCreate(r, tok, p.ID.String(), ""); w.Code == http.StatusTooManyRequests {
			t.Fatalf("create %d in a minute refused", i)
		}
	}
	cancel := "/api/pay/merchant/refunds/" + uuid.NewString() + "/cancel"
	if w := payRoutesDo(r, http.MethodPost, cancel, tok, nil); w.Code == http.StatusTooManyRequests {
		t.Fatal("the 10th write in a minute refused")
	}
	if w := payRoutesDo(r, http.MethodPost, cancel, tok, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 11th write (a cancel) in a minute: %d", w.Code)
	}
	if w := payRefundRoutesCreate(r, tok, p.ID.String(), ""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 12th write (a create) in a minute: %d", w.Code)
	}
	submit := "/api/pay/merchant/refunds/" + uuid.NewString() + "/submit"
	for i := 1; i <= 10; i++ {
		if w := payRoutesDo(r, http.MethodPost, submit, tok, map[string]any{}); w.Code == http.StatusTooManyRequests {
			t.Fatalf("submit %d in a minute refused", i)
		}
	}
	if w := payRoutesDo(r, http.MethodPost, submit, tok, map[string]any{}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 11th submit in a minute: %d", w.Code)
	}
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant/refunds?mode=test", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("a read after the write limit: %d", w.Code)
	}
}
