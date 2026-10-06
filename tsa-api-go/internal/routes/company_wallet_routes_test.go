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

type routesWalletAdmin struct{}

func (routesWalletAdmin) CompanyWalletStatus(context.Context, float64) (*services.CompanyWalletStatus, error) {
	return &services.CompanyWalletStatus{}, nil
}
func (routesWalletAdmin) ListCompanyWalletOrders(context.Context) ([]services.CompanyWalletOrder, error) {
	return nil, nil
}
func (routesWalletAdmin) DeliverParkedTreasuryBuy(context.Context, uuid.UUID, uuid.UUID, string) (*models.ITIntent, error) {
	return &models.ITIntent{}, nil
}
func (routesWalletAdmin) MarkStableBuyRefunded(context.Context, uuid.UUID, uuid.UUID, string) (*models.ITIntent, error) {
	return &models.ITIntent{}, nil
}
func (routesWalletAdmin) ResendTreasuryBuy(context.Context, uuid.UUID, uuid.UUID, string) (*models.ITIntent, error) {
	return &models.ITIntent{}, nil
}

// D57: the company-wallet endpoints are super-admin only, through the real router.
func TestCompanyWalletRoutes_SuperAdminOnly(t *testing.T) {
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
	cfg := &config.Config{JWTSecret: "company-wallet-routes-secret"}
	ith := handlers.NewITHandler(db, nil, nil)
	ith.SetCompanyWalletAdmin(routesWalletAdmin{}, 50)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r, cfg, &handlers.Handlers{Config: cfg}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		ith, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	for _, tc := range []struct {
		role string
		want int
	}{
		{models.RoleSuperAdmin, http.StatusOK},
		{models.RoleAdmin, http.StatusForbidden},
	} {
		tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, tc.role).ID)
		if w := payRoutesDo(r, "GET", "/api/admin/ramp/company-wallet", tok, nil); w.Code != tc.want {
			t.Fatalf("%s GET: %d %s", tc.role, w.Code, w.Body.String())
		}
		if w := payRoutesDo(r, "POST", "/api/admin/ramp/company-wallet/orders/"+uuid.NewString()+"/refund", tok,
			map[string]string{"note": "sent back"}); w.Code != tc.want {
			t.Fatalf("%s POST: %d %s", tc.role, w.Code, w.Body.String())
		}
	}
	if w := payRoutesDo(r, "GET", "/api/admin/ramp/company-wallet", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
}
