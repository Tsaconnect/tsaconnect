package routes

import (
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

// Review I1: a parked unknown-provider buy's only exit, the refund, is
// mounted on the always-present super-admin IT group — reachable with no
// company wallet wired — and refunds exactly once.
func TestITAdminRefundRoute_SuperAdminOnlyWithoutCompanyWallet(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec(payRoutesUsersDDL).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ITIntent{}, &models.ITIntentAuditLog{}, &models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	config.DB = db
	cfg := &config.Config{JWTSecret: "it-admin-refund-routes-secret"}
	ith := handlers.NewITHandler(db, services.NewITService(db, nil, nil), nil)
	if ith.CompanyWalletEnabled() {
		t.Fatal("rig must run without the company wallet")
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r, cfg, &handlers.Handlers{Config: cfg}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		ith, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	buyer := payRoutesUser(t, db, models.RoleUser)
	intent := &models.ITIntent{
		ID: uuid.New(), UserID: buyer.ID, Direction: models.ITDirectionBuy, Status: models.ITStatusBuyAwaitingRate,
		Provider: "bogus", Asset: models.ITAssetUSDC, AssetAmount: "7278150", Currency: "NGN",
		NgnKobo: "1000000", AmountMinor: "1000000", McgpAmount: "0", FeeNgnKobo: "0", NetNgnKobo: "1000000",
		PlatformKeptKobo: "0", ReceivedAmount: "1000000", PaymentRef: "chg_parked",
		FailureReason: "could not price at settlement: the intent's provider has no terms",
	}
	if err := db.Create(intent).Error; err != nil {
		t.Fatal(err)
	}
	path := "/api/it/admin/stable/buy/" + intent.ID.String() + "/refund"
	note := map[string]string{"note": "returned by bank transfer"}
	status := func() string {
		var got models.ITIntent
		db.First(&got, "id = ?", intent.ID)
		return got.Status
	}

	if w := payRoutesDo(r, http.MethodPost, path, "", note); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", w.Code)
	}
	admin := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleAdmin).ID)
	if w := payRoutesDo(r, http.MethodPost, path, admin, note); w.Code != http.StatusForbidden || status() != models.ITStatusBuyAwaitingRate {
		t.Fatalf("plain admin: %d %s, status %s", w.Code, w.Body, status())
	}
	super := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleSuperAdmin).ID)
	if w := payRoutesDo(r, http.MethodPost, path, super, note); w.Code != http.StatusOK || status() != models.ITStatusRefunded {
		t.Fatalf("super admin: %d %s, status %s", w.Code, w.Body, status())
	}
	if w := payRoutesDo(r, http.MethodPost, path, super, note); w.Code != http.StatusConflict {
		t.Fatalf("second refund: %d %s, want 409", w.Code, w.Body)
	}
	var audits int64
	db.Model(&models.ITIntentAuditLog{}).Where("intent_id = ?", intent.ID).Count(&audits)
	if audits != 1 {
		t.Fatalf("%d audit rows, want exactly one refund", audits)
	}
	// The console's own route stays gated on the wallet being wired.
	if w := payRoutesDo(r, http.MethodPost, "/api/admin/ramp/company-wallet/orders/"+intent.ID.String()+"/refund", super, note); w.Code != http.StatusNotFound {
		t.Fatalf("company-wallet route without a wallet: %d, want 404", w.Code)
	}
}
