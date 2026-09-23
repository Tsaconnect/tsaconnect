package handlers

import (
	"bytes"
	"context"
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
	h, _, db := newPayHandlerAdminRig(t)
	return h, db
}

// newPayHandlerAdminRig is newPayHandlerRig plus the concrete
// *services.PayPaymentService and a real (network-inert in these tests)
// webhook sender, for tests that need to seed payments/deliveries directly
// or drive AdminResendWebhook end to end.
func newPayHandlerAdminRig(t *testing.T) (*PayHandler, *services.PayPaymentService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{}, &models.PayAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return payHandlerNow }
	merchants := services.NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	payments := services.NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now)
	sender := services.NewPayWebhookSender(db, merchants, http.DefaultClient, now)
	return NewPayHandler(merchants, payments, sender, false), payments, db
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

func TestPayAdmin_UpdateWithAStaleExpectedVersionAnswers409MerchantStale(t *testing.T) {
	h, db := newPayHandlerRig(t)
	m := seedHandlerMerchant(t, db)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := &models.User{ID: uuid.New(), Role: models.RoleSuperAdmin}
	r.PUT("/admin/pay/merchants/:id", func(c *gin.Context) { c.Set("user", admin) }, h.AdminUpdateMerchant)
	url := "/admin/pay/merchants/" + m.ID.String()

	w := payDo(r, "PUT", url, map[string]any{"customerFeeSharePercent": 50, "expectedVersion": 1}, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version":2`) {
		t.Fatalf("current version: %d %s", w.Code, w.Body.String())
	}
	w = payDo(r, "PUT", url, map[string]any{"customerFeeSharePercent": 20, "expectedVersion": 1}, nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"merchant_stale"`) {
		t.Fatalf("stale version: %d %s", w.Code, w.Body.String())
	}
	var stored models.PayMerchant
	db.First(&stored, "id = ?", m.ID)
	if stored.CustomerFeeSharePercent != 50 {
		t.Fatalf("a stale update landed: share %d", stored.CustomerFeeSharePercent)
	}
}

func TestPayAdmin_MerchantReadsIncludeTheVersion(t *testing.T) {
	h, db := newPayHandlerRig(t)
	m := seedHandlerMerchant(t, db)
	r := payAdminRouter(h)
	r.GET("/admin/pay/merchants/:id", h.AdminGetMerchant)
	w := payDo(r, "GET", "/admin/pay/merchants/"+m.ID.String(), nil, nil)
	if !strings.Contains(w.Body.String(), `"version":1`) {
		t.Fatalf("get: %s", w.Body.String())
	}
}

func payAdminRouter(h *PayHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	admin := &models.User{ID: uuid.New(), Role: models.RoleSuperAdmin}
	g := r.Group("/admin/pay", func(c *gin.Context) { c.Set("user", admin) })
	g.GET("/payments", h.AdminListPayments)
	g.GET("/payments/:id/deliveries", h.AdminPaymentDeliveries)
	g.POST("/deliveries/:id/resend", h.AdminResendWebhook)
	return r
}

func TestPayAdmin_ListPaymentsFiltersByMerchantAndAddsMerchantName(t *testing.T) {
	h, payments, db := newPayHandlerAdminRig(t)
	m1 := seedHandlerMerchant(t, db)
	m2 := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: uuid.New(), BusinessName: "Blue Air", Status: models.PayMerchantActive,
		SettlementMode: models.PaySettlementFiat, BankCode: "058", AccountNumber: "0000000001", CustomerFeeSharePercent: 100,
		MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000, WebhookSecretVersion: 1, ReturnDomains: "blueair.ng"}
	if err := db.Create(m2).Error; err != nil {
		t.Fatal(err)
	}
	in1 := services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "R1"}
	in2 := services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "R2"}
	p1, _, err := payments.Create(context.Background(), m1, models.PayModeTest, in1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := payments.Create(context.Background(), m2, models.PayModeTest, in2); err != nil {
		t.Fatal(err)
	}

	r := payAdminRouter(h)
	w := payDo(r, "GET", "/admin/pay/payments?merchantId="+m1.ID.String(), nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data struct {
			Payments []struct {
				ID           string `json:"id"`
				MerchantName string `json:"merchantName"`
			} `json:"payments"`
			Total int64 `json:"total"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Data.Total != 1 || len(out.Data.Payments) != 1 {
		t.Fatalf("merchant-scoped list = %+v", out.Data)
	}
	if out.Data.Payments[0].ID != p1.ID.String() || out.Data.Payments[0].MerchantName != "Sky Air" {
		t.Fatalf("row = %+v, want p1/Sky Air", out.Data.Payments[0])
	}
}

func TestPayAdmin_PaymentDeliveriesListsAndResendRequeuesAFailedOne(t *testing.T) {
	h, payments, db := newPayHandlerAdminRig(t)
	m := seedHandlerMerchant(t, db)
	db.Model(m).Update("test_webhook_url", "https://skyair.ng/hooks")
	m.TestWebhookURL = "https://skyair.ng/hooks"

	in := services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "R1"}
	p, _, err := payments.Create(context.Background(), m, models.PayModeTest, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := payments.SimulateTestOutcome(context.Background(), &models.User{ID: uuid.New()}, p.ID, true); err != nil {
		t.Fatal(err)
	}
	var delivery models.PayWebhookDelivery
	if err := db.First(&delivery).Error; err != nil {
		t.Fatal(err)
	}
	failedAt := payHandlerNow
	if err := db.Model(&delivery).Updates(map[string]any{"failed_at": &failedAt, "next_attempt_at": nil, "attempts": 7}).Error; err != nil {
		t.Fatal(err)
	}

	r := payAdminRouter(h)
	w := payDo(r, "GET", "/admin/pay/payments/"+p.ID.String()+"/deliveries", nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), delivery.ID.String()) {
		t.Fatalf("deliveries: %d %s", w.Code, w.Body.String())
	}

	if w := payDo(r, "POST", "/admin/pay/deliveries/"+uuid.New().String()+"/resend", nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("resend unknown delivery: %d %s", w.Code, w.Body.String())
	}
	if w := payDo(r, "POST", "/admin/pay/deliveries/"+delivery.ID.String()+"/resend", nil, nil); w.Code != http.StatusOK {
		t.Fatalf("resend: %d %s", w.Code, w.Body.String())
	}
	var got models.PayWebhookDelivery
	if err := db.First(&got, "id = ?", delivery.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.FailedAt != nil || got.Attempts != 0 || got.NextAttemptAt == nil {
		t.Fatalf("after admin resend: %+v", got)
	}
}
