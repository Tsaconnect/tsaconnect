package routes

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newNameChangeRoutesRig runs the real SetupRoutes with name changes wired and
// the feature flag as given, over SQLite.
func newNameChangeRoutesRig(t *testing.T, enabled bool) (*gin.Engine, *config.Config, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec(payRoutesUsersDDL).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.UserNameChange{}, &models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{},
		&models.PayAuditEvent{}, &models.PayOpenLink{}, &models.PayRetiredSlug{}, &models.PayMerchantNameChange{}); err != nil {
		t.Fatal(err)
	}
	config.DB = db

	cfg := &config.Config{JWTSecret: "name-change-routes-secret", NameChangeEnabled: enabled}
	h := &handlers.Handlers{Config: cfg, NameChanges: services.NewNameChangeService(db,
		services.NameChangeConfig{Enabled: enabled, FeeUSD: "1.00"}, nil, time.Now)}
	now := func() time.Time { return time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC) }
	merchants := services.NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	payments := services.NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now)
	payh := handlers.NewPayHandler(merchants, payments, nil, false).WithBusinessNameChange(enabled)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r, cfg, h, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, payh)
	return r, cfg, db
}

// Held name changes and business renames are reviewed by any true admin; a
// merchant, support or plain user is refused.
func TestNameChangeRoutes_AdminReviewNeedsAnAdmin(t *testing.T) {
	r, cfg, db := newNameChangeRoutesRig(t, true)
	id := uuid.New().String()
	paths := []struct{ method, path string }{
		{"GET", "/api/admin/name-changes?status=held_for_review"},
		{"POST", "/api/admin/name-changes/" + id + "/approve"},
		{"POST", "/api/admin/name-changes/" + id + "/reject"},
		{"POST", "/api/admin/name-changes/" + id + "/refund-outcome"},
		{"POST", "/api/admin/name-changes/" + id + "/clear-payment"},
		{"GET", "/api/admin/pay/merchant-name-changes?status=pending"},
		{"POST", "/api/admin/pay/merchant-name-changes/" + id + "/approve"},
		{"POST", "/api/admin/pay/merchant-name-changes/" + id + "/reject"},
	}
	for _, role := range []string{models.RoleUser, models.RoleSupport, models.RoleMerchant} {
		tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, role).ID)
		for _, p := range paths {
			if w := payRoutesDo(r, p.method, p.path, tok, map[string]string{"note": ""}); w.Code != http.StatusForbidden {
				t.Fatalf("%s on %s %s: %d %s", role, p.method, p.path, w.Code, w.Body.String())
			}
		}
	}
	for _, role := range []string{models.RoleAdmin, models.RoleSuperAdmin} {
		tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, role).ID)
		if w := payRoutesDo(r, "GET", "/api/admin/name-changes", tok, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changes":[]`) {
			t.Fatalf("%s listing name changes: %d %s", role, w.Code, w.Body.String())
		}
		if w := payRoutesDo(r, "GET", "/api/admin/pay/merchant-name-changes", tok, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"changes":[]`) {
			t.Fatalf("%s listing business renames: %d %s", role, w.Code, w.Body.String())
		}
		// Recording a refund's on-chain outcome is super_admin only.
		w := payRoutesDo(r, "POST", "/api/admin/name-changes/"+id+"/refund-outcome", tok, map[string]any{"sent": true, "txHash": "0x" + strings.Repeat("ab", 32)})
		if role == models.RoleAdmin && w.Code != http.StatusForbidden {
			t.Fatalf("a plain admin recording a refund outcome: %d %s", w.Code, w.Body.String())
		}
		if role == models.RoleSuperAdmin && w.Code != http.StatusNotFound {
			t.Fatalf("a super admin recording an unknown change's refund: %d %s", w.Code, w.Body.String())
		}
		// So is clearing a named payment that will never pay.
		w = payRoutesDo(r, "POST", "/api/admin/name-changes/"+id+"/clear-payment", tok, map[string]any{"reason": "not on basescan"})
		if role == models.RoleAdmin && w.Code != http.StatusForbidden {
			t.Fatalf("a plain admin clearing a payment: %d %s", w.Code, w.Body.String())
		}
		if role == models.RoleSuperAdmin && w.Code != http.StatusNotFound {
			t.Fatalf("a super admin clearing an unknown change's payment: %d %s", w.Code, w.Body.String())
		}
		// Nothing held under this id: the route is reached and says so.
		if w := payRoutesDo(r, "POST", "/api/admin/name-changes/"+id+"/approve", tok, map[string]string{"note": ""}); w.Code != http.StatusNotFound {
			t.Fatalf("%s approving an unknown change: %d %s", role, w.Code, w.Body.String())
		}
	}
}

// With the flag off the app can still ask (and is told it is off), but nothing
// can be started and the owner rename route does not exist.
func TestNameChangeRoutes_FlagOff(t *testing.T) {
	r, cfg, db := newNameChangeRoutesRig(t, false)
	tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)

	w := payRoutesDo(r, "GET", "/api/users/me/name-change", tok, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("overview: %d %s", w.Code, w.Body.String())
	}
	// Starting a change is off...
	if w := payRoutesDo(r, "POST", "/api/users/me/name-change", tok, map[string]string{"payMethod": "mcgp_spendable"}); w.Code != http.StatusServiceUnavailable ||
		!strings.Contains(w.Body.String(), `"code":"name_change_disabled"`) {
		t.Fatalf("POST /api/users/me/name-change: %d %s", w.Code, w.Body.String())
	}
	// ...and the name change has no wallet proof of its own: the account's
	// wallet is proven through /api/wallet/challenge like every other debit.
	if w := payRoutesDo(r, "GET", "/api/users/me/name-change/wallet-proof?walletAddress=0x87c2422be9A11b71A5c530ecd156134D3b04E3cE&chain=sonic", tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("the name change's own wallet-proof route is gone: %d %s", w.Code, w.Body.String())
	}
	// ...but a change already running can still be paid, verified or
	// cancelled (this user has none, so each says so).
	for _, path := range []string{"/api/users/me/name-change/confirm-payment", "/api/users/me/name-change/cancel"} {
		w := payRoutesDo(r, "POST", path, tok, map[string]string{"txHash": "0x" + strings.Repeat("ab", 32)})
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"no_open_change"`) {
			t.Fatalf("POST %s with the flag off: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := payRoutesDo(r, "PUT", "/api/pay/merchant/business-name", tok, map[string]string{"name": "Kite"}); w.Code != http.StatusNotFound {
		t.Fatalf("business-name with the flag off: %d %s", w.Code, w.Body.String())
	}
	// /users/:id still answers alongside /users/me/name-change.
	if w := payRoutesDo(r, "GET", "/api/users/"+uuid.New().String(), tok, nil); w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "404 page not found") {
		t.Fatalf("GET /users/:id lost its route: %d %s", w.Code, w.Body.String())
	}
}

func TestNameChangeRoutes_FlagOnRegistersTheOwnerRename(t *testing.T) {
	r, cfg, db := newNameChangeRoutesRig(t, true)
	tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	// A user without a merchant reaches the route and is told so.
	w := payRoutesDo(r, "PUT", "/api/pay/merchant/business-name", tok, map[string]string{"name": "Kite", "password": "x"})
	if !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatalf("business-name route missing: %d %s", w.Code, w.Body.String())
	}
}
