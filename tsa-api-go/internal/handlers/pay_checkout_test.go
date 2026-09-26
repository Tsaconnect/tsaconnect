package handlers

import (
	"context"
	"encoding/json"
	"net/http"
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

// newPayCheckoutRig is newPayHandlerRig plus the concrete
// *services.PayPaymentService, so these tests can drive Lock/Succeed
// directly to set up a payment's state before hitting the handler under
// test (there is no HTTP route for Lock — the app calls it through the
// checkout flow Task 13 builds).
func newPayCheckoutRig(t *testing.T) (*PayHandler, *services.PayPaymentService, *gorm.DB) {
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
	return NewPayHandler(merchants, payments, nil, false), payments, db
}

func payCheckoutIn() services.CreatePayPaymentInput {
	return services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "PNR-1",
		Description: "LOS-ABV 12 Oct", ReturnURL: "https://skyair.ng/done"}
}

func TestPayCheckout_PublicStatusKeySetAndReturnURL(t *testing.T) {
	h, payments, db := newPayCheckoutRig(t)
	m := seedHandlerMerchant(t, db)
	ctx := context.Background()
	p, _, err := payments.Create(ctx, m, models.PayModeTest, payCheckoutIn())
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/pay/public/:id", h.Public)

	w := payDo(r, "GET", "/pay/public/"+p.ID.String(), nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("public: %d %s", w.Code, w.Body.String())
	}
	var resp struct{ Data map[string]any }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// An API payment's description is the merchant's own (a PNR here) and never
	// reaches the public page.
	pendingKeys := []string{"id", "merchantName", "mode", "source", "status", "amount", "customerTotal", "currency", "expiresAt", "appUrl"}
	if len(resp.Data) != len(pendingKeys) {
		t.Fatalf("pending key set = %v", resp.Data)
	}
	for _, k := range pendingKeys {
		if _, ok := resp.Data[k]; !ok {
			t.Fatalf("missing key %q: %v", k, resp.Data)
		}
	}

	if _, _, err := payments.Lock(ctx, &models.User{ID: uuid.New()}, p.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := payments.Succeed(ctx, p.ID, "ref"); err != nil {
		t.Fatal(err)
	}
	w = payDo(r, "GET", "/pay/public/"+p.ID.String(), nil, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp.Data["returnUrl"]; !ok {
		t.Fatalf("succeeded payment missing returnUrl: %v", resp.Data)
	}
	// When it was paid, for the receipt.
	if resp.Data["succeededAt"] != payHandlerNow.Format(time.RFC3339) {
		t.Fatalf("succeeded payment succeededAt = %v", resp.Data["succeededAt"])
	}
	if len(resp.Data) != len(pendingKeys)+2 {
		t.Fatalf("succeeded key set = %v", resp.Data)
	}

	if w := payDo(r, "GET", "/pay/public/not-a-uuid", nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad uuid: %d", w.Code)
	}
	if w := payDo(r, "GET", "/pay/public/"+uuid.New().String(), nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
}

func TestPayCheckout_CheckoutBadUUIDAndUnknownID(t *testing.T) {
	h, _, _ := newPayCheckoutRig(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	u := &models.User{ID: uuid.New()}
	inject := func(c *gin.Context) { c.Set("user", u); c.Next() }
	r.GET("/pay/checkout/:id", inject, h.Checkout)

	if w := payDo(r, "GET", "/pay/checkout/not-a-uuid", nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad uuid: %d", w.Code)
	}
	if w := payDo(r, "GET", "/pay/checkout/"+uuid.New().String(), nil, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
}

func TestPayCheckout_TestOutcomeApprovesAndValidatesBody(t *testing.T) {
	h, payments, db := newPayCheckoutRig(t)
	m := seedHandlerMerchant(t, db)
	ctx := context.Background()
	p, _, _ := payments.Create(ctx, m, models.PayModeTest, payCheckoutIn())
	u := &models.User{ID: m.UserID}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) { c.Set("user", u); c.Next() }
	r.POST("/pay/checkout/:id/test-outcome", inject, h.TestOutcome)

	// Someone who merely holds the payment id may not resolve it.
	stranger := gin.New()
	stranger.POST("/pay/checkout/:id/test-outcome", func(c *gin.Context) { c.Set("user", &models.User{ID: uuid.New()}); c.Next() }, h.TestOutcome)
	if w := payDo(stranger, "POST", "/pay/checkout/"+p.ID.String()+"/test-outcome", map[string]string{"outcome": "approve"}, nil); w.Code != http.StatusForbidden {
		t.Fatalf("stranger: %d %s", w.Code, w.Body.String())
	}

	if w := payDo(r, "POST", "/pay/checkout/"+p.ID.String()+"/test-outcome", map[string]string{"outcome": "bogus"}, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bogus outcome: %d %s", w.Code, w.Body.String())
	}
	w := payDo(r, "POST", "/pay/checkout/"+p.ID.String()+"/test-outcome", map[string]string{"outcome": "approve"}, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
}

// TestOutcome must hand back the same payer-facing PayCheckoutView shape
// Checkout itself returns — the app types the response as PayCheckout and
// substitutes it directly into checkout state (app/pay.tsx). Before this
// fix TestOutcome returned h.payments.View(p), the merchant-facing
// PayPaymentView: no merchantName/payable/youArePaying/settlementMode, and
// it leaked merchant-only reference/settlementRef to the payer.
func TestPayCheckout_TestOutcomeKeySetMatchesCheckoutView(t *testing.T) {
	h, payments, db := newPayCheckoutRig(t)
	m := seedHandlerMerchant(t, db)
	ctx := context.Background()
	p, _, err := payments.Create(ctx, m, models.PayModeTest, payCheckoutIn())
	if err != nil {
		t.Fatal(err)
	}
	u := &models.User{ID: m.UserID}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) { c.Set("user", u); c.Next() }
	r.POST("/pay/checkout/:id/test-outcome", inject, h.TestOutcome)

	w := payDo(r, "POST", "/pay/checkout/"+p.ID.String()+"/test-outcome", map[string]string{"outcome": "approve"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	var resp struct{ Data map[string]any }
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}

	wantKeys := []string{
		"id", "merchantName", "mode", "status", "amount", "customerTotal", "fee",
		"merchantNet", "currency", "description", "expiresAt", "settlementMode",
		"returnUrl", "payable", "notPayableWhy", "youArePaying",
	}
	if len(resp.Data) != len(wantKeys) {
		t.Fatalf("key set = %v", resp.Data)
	}
	for _, k := range wantKeys {
		if _, ok := resp.Data[k]; !ok {
			t.Fatalf("missing key %q: %v", k, resp.Data)
		}
	}
	for _, merchantOnly := range []string{"reference", "settlementRef"} {
		if _, ok := resp.Data[merchantOnly]; ok {
			t.Fatalf("merchant-only field %q leaked to the payer: %v", merchantOnly, resp.Data)
		}
	}
}

func TestPayCheckout_CancelPendingThenConflictOnceProcessing(t *testing.T) {
	h, payments, db := newPayCheckoutRig(t)
	m := seedHandlerMerchant(t, db)
	ctx := context.Background()
	p, _, _ := payments.Create(ctx, m, models.PayModeTest, payCheckoutIn())

	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) { c.Set("payMerchant", m); c.Set("payMode", models.PayModeTest); c.Next() }
	r.POST("/pay/v1/payments/:id/cancel", inject, h.CancelPayment)

	w := payDo(r, "POST", "/pay/v1/payments/"+p.ID.String()+"/cancel", nil, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"expired"`) {
		t.Fatalf("cancel pending: %d %s", w.Code, w.Body.String())
	}

	in := payCheckoutIn()
	in.Reference = "PNR-2"
	p2, _, _ := payments.Create(ctx, m, models.PayModeTest, in)
	if _, _, err := payments.Lock(ctx, &models.User{ID: uuid.New()}, p2.ID, false); err != nil {
		t.Fatal(err)
	}
	if w := payDo(r, "POST", "/pay/v1/payments/"+p2.ID.String()+"/cancel", nil, nil); w.Code != http.StatusConflict {
		t.Fatalf("cancel processing: %d %s", w.Code, w.Body.String())
	}

	if w := payDo(r, "POST", "/pay/v1/payments/not-a-uuid/cancel", nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad uuid: %d", w.Code)
	}
}
