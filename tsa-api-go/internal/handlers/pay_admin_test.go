package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var payHandlerNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func newPayHandlerRig(t *testing.T) (*PayHandler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return payHandlerNow }
	merchants := services.NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	payments := services.NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now)
	return NewPayHandler(merchants, payments, nil, false), db
}

func payDo(r *gin.Engine, method, path string, body any, header map[string]string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func seedHandlerMerchant(t *testing.T, db *gorm.DB) *models.PayMerchant {
	t.Helper()
	m := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: uuid.New(), BusinessName: "Sky Air", Status: models.PayMerchantActive,
		SettlementMode: models.PaySettlementFiat, BankCode: "058", AccountNumber: "0123456789", CustomerFeeSharePercent: 100,
		MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000, WebhookSecretVersion: 1, ReturnDomains: "skyair.ng"}
	if err := db.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPayAdmin_IssueKeysShowsThemOnceAndNeverInReads(t *testing.T) {
	h, db := newPayHandlerRig(t)
	m := seedHandlerMerchant(t, db)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := &models.User{ID: uuid.New(), Role: models.RoleSuperAdmin}
	g := r.Group("/admin/pay", func(c *gin.Context) { c.Set("user", admin) })
	g.GET("/merchants/:id", h.AdminGetMerchant)
	g.POST("/merchants/:id/keys", h.AdminIssueKeys)

	w := payDo(r, "POST", "/admin/pay/merchants/"+m.ID.String()+"/keys", map[string]string{"scope": "all"}, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "tsapk_live_") || !strings.Contains(w.Body.String(), "whsec_") {
		t.Fatalf("issue: %d %s", w.Code, w.Body.String())
	}
	w = payDo(r, "GET", "/admin/pay/merchants/"+m.ID.String(), nil, nil)
	for _, leak := range []string{"tsapk_live_", "tsapk_test_", "whsec_", "KeyHash", "key_hash"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Fatalf("a read leaked %q: %s", leak, w.Body.String())
		}
	}
	if w := payDo(r, "POST", "/admin/pay/merchants/"+m.ID.String()+"/keys", map[string]string{"scope": "everything"}, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown scope: %d", w.Code)
	}
}

func TestPayAdmin_SensitiveUpdateNeedsASuperAdmin(t *testing.T) {
	h, db := newPayHandlerRig(t)
	m := seedHandlerMerchant(t, db)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := &models.User{ID: uuid.New(), Role: models.RoleAdmin}
	r.PUT("/admin/pay/merchants/:id", func(c *gin.Context) { c.Set("user", admin) }, h.AdminUpdateMerchant)
	if w := payDo(r, "PUT", "/admin/pay/merchants/"+m.ID.String(), map[string]any{"webhookUrl": "https://skyair.ng/h"}, nil); w.Code != http.StatusForbidden {
		t.Fatalf("admin changing the webhook: %d", w.Code)
	}
	if w := payDo(r, "PUT", "/admin/pay/merchants/"+m.ID.String(), map[string]any{"customerFeeSharePercent": 0}, nil); w.Code != http.StatusOK {
		t.Fatalf("admin changing the share: %d %s", w.Code, w.Body.String())
	}
}
