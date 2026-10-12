package routes

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type routesPoolAdmin struct{}

func (routesPoolAdmin) PoolOrderDetail(context.Context, uuid.UUID) (*services.PoolOrderDetail, error) {
	return &services.PoolOrderDetail{}, nil
}
func (routesPoolAdmin) RedeliverPoolBuy(context.Context, uuid.UUID, uuid.UUID, string) (*models.ITIntent, error) {
	return &models.ITIntent{}, nil
}
func (routesPoolAdmin) ResendPoolBuy(context.Context, uuid.UUID, uuid.UUID, string) (*models.ITIntent, error) {
	return &models.ITIntent{}, nil
}
func (routesPoolAdmin) RefundPoolBuyOverride(context.Context, uuid.UUID, uuid.UUID, string) (*models.ITIntent, error) {
	return &models.ITIntent{}, nil
}
func (routesPoolAdmin) PayPoolSliceLP(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) (*models.P2PSettlementForward, error) {
	return &models.P2PSettlementForward{}, nil
}
func (routesPoolAdmin) NotePoolSliceReturned(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, string) error {
	return nil
}
func (routesPoolAdmin) ClearPoolSliceRelease(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) error {
	return nil
}

type routesRechecker struct{}

func (routesRechecker) Recheck(context.Context, uuid.UUID) error { return nil }

func liquidityRoutesRig(t *testing.T, wire func(*handlers.ITHandler)) (*gin.Engine, *gorm.DB, *config.Config) {
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
	config.DB = db
	cfg := &config.Config{JWTSecret: "liquidity-admin-routes-secret"}
	ith := handlers.NewITHandler(db, nil, nil)
	wire(ith)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r, cfg, &handlers.Handlers{Config: cfg}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		ith, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return r, db, cfg
}

// §15 / P20: P2P admins read a pool order; only super admins move money on it
// (override, re-deliver, re-send, F22 pay/returned, forward recheck).
func TestLiquidityAdminRoutes_ActionsNeedSuperAdmin(t *testing.T) {
	r, db, cfg := liquidityRoutesRig(t, func(h *handlers.ITHandler) {
		h.SetPoolOrdersAdmin(routesPoolAdmin{})
		h.SetForwardRechecker(routesRechecker{})
	})
	order := "/api/admin/liquidity/orders/" + uuid.NewString()
	slice := order + "/slices/" + uuid.NewString()
	actions := []string{
		order + "/redeliver",
		order + "/resend",
		order + "/refund-override",
		slice + "/pay-lp",
		slice + "/returned",
		slice + "/clear-release",
		"/api/admin/p2p/forwards/" + uuid.NewString() + "/recheck",
	}
	for _, tc := range []struct {
		role         string
		get, actions int
	}{
		{models.RoleSuperAdmin, http.StatusOK, http.StatusOK},
		{models.RoleAdmin, http.StatusOK, http.StatusForbidden},
		{models.RoleSupport, http.StatusForbidden, http.StatusForbidden},
		{models.RoleUser, http.StatusForbidden, http.StatusForbidden},
	} {
		tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, tc.role).ID)
		if w := payRoutesDo(r, "GET", order, tok, nil); w.Code != tc.get {
			t.Fatalf("%s GET: %d %s", tc.role, w.Code, w.Body.String())
		}
		for _, p := range actions {
			if w := payRoutesDo(r, "POST", p, tok, map[string]string{"note": "n", "txHash": "0x1"}); w.Code != tc.actions {
				t.Fatalf("%s POST %s: %d %s", tc.role, p, w.Code, w.Body.String())
			}
		}
	}
	if w := payRoutesDo(r, "GET", order, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}

	// No pool desk: the order routes are not registered; the forward recheck
	// is independent of the pool (merchant forwards use it too).
	r2, db2, cfg2 := liquidityRoutesRig(t, func(h *handlers.ITHandler) { h.SetForwardRechecker(routesRechecker{}) })
	tok := payRoutesToken(t, cfg2.JWTSecret, payRoutesUser(t, db2, models.RoleSuperAdmin).ID)
	if w := payRoutesDo(r2, "GET", order, tok, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unwired pool GET = %d, want 404", w.Code)
	}
	if w := payRoutesDo(r2, "POST", "/api/admin/p2p/forwards/"+uuid.NewString()+"/recheck", tok, map[string]string{"note": "n"}); w.Code != http.StatusOK {
		t.Fatalf("recheck without the pool = %d, want 200", w.Code)
	}
}
