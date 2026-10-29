package routes

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// The core fee routes mount beside /api/admin/fees without a route clash, sit behind the admin
// boundary, and answer 503 while no core set is selected.
func TestCoreFeeRoutes_AdminOnlyAndDormant(t *testing.T) {
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
	cfg := &config.Config{JWTSecret: "core-fee-routes-secret"}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r, cfg, &handlers.Handlers{Config: cfg}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		handlers.NewFeeAdminHandler(cfg, nil, nil, nil), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	RegisterCoreFees(r, cfg, handlers.NewCoreFeeHandler(db, nil))

	admin := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleAdmin).ID)
	merchant := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleMerchant).ID)
	for _, path := range []string{"/api/admin/fees/core", "/api/admin/fees/core/146"} {
		if w := payRoutesDo(r, "GET", path, admin, nil); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("admin GET %s: %d %s", path, w.Code, w.Body.String())
		}
		if w := payRoutesDo(r, "GET", path, merchant, nil); w.Code != http.StatusForbidden {
			t.Fatalf("merchant GET %s: %d", path, w.Code)
		}
	}
	if w := payRoutesDo(r, "POST", "/api/admin/fees/core/146/plan", "", map[string]any{}); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous plan: %d", w.Code)
	}
	// The FeeManager intent routes are still there.
	if w := payRoutesDo(r, "GET", "/api/admin/fees/intents", admin, nil); w.Code == http.StatusNotFound {
		t.Fatalf("intents route lost: %d", w.Code)
	}
}
