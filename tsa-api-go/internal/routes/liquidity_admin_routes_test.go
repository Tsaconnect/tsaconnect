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
func (routesPoolAdmin) ResendPoolBuy(context.Context, uuid.UUID, uuid.UUID, string) (*models.ITIntent, error) {
	return &models.ITIntent{}, nil
}
func (routesPoolAdmin) VaultPoolSummary(context.Context) (*services.VaultPoolSummary, error) {
	return &services.VaultPoolSummary{}, nil
}
func (routesPoolAdmin) SettlePoolFromCollateral(context.Context, uuid.UUID, string, string) (string, error) {
	return "0x1", nil
}
func (routesPoolAdmin) RetryPoolFill(context.Context, uuid.UUID, uuid.UUID, string) (*models.ITIntent, error) {
	return &models.ITIntent{}, nil
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

// P2P admins read a pool order and the pool's books; only super admins move
// money (re-send, retry a fill, settle from collateral, forward recheck).
func TestLiquidityAdminRoutes_ActionsNeedSuperAdmin(t *testing.T) {
	r, db, cfg := liquidityRoutesRig(t, func(h *handlers.ITHandler) {
		h.SetPoolOrdersAdmin(routesPoolAdmin{})
		h.SetVaultPoolAdmin(routesPoolAdmin{})
		h.SetForwardRechecker(routesRechecker{})
	})
	order := "/api/admin/liquidity/orders/" + uuid.NewString()
	actions := []string{
		order + "/resend",
		order + "/retry-fill",
		"/api/admin/liquidity/pool/settle-from-collateral",
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
		for _, g := range []string{order, "/api/admin/liquidity/pool"} {
			if w := payRoutesDo(r, "GET", g, tok, nil); w.Code != tc.get {
				t.Fatalf("%s GET %s: %d %s", tc.role, g, w.Code, w.Body.String())
			}
		}
		for _, p := range actions {
			if w := payRoutesDo(r, "POST", p, tok, map[string]string{"note": "n", "amount": "1"}); w.Code != tc.actions {
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
