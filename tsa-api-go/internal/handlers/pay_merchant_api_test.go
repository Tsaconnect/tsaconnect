package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestPayMerchantAPI_CreateThenReadAndRetry(t *testing.T) {
	h, db := newPayHandlerRig(t)
	m := seedHandlerMerchant(t, db)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) { c.Set("payMerchant", m); c.Set("payMode", models.PayModeTest); c.Next() }
	r.POST("/pay/v1/payments", inject, h.CreatePayment)
	r.GET("/pay/v1/payments/:id", inject, h.GetPayment)

	body := map[string]any{"amount": 5_000_000, "currency": "NGN", "reference": "PNR-1", "returnUrl": "https://skyair.ng/done"}
	w := payDo(r, "POST", "/pay/v1/payments", body, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var resp struct{ Data services.PayPaymentView }
	json.Unmarshal(w.Body.Bytes(), &resp)
	if w = payDo(r, "POST", "/pay/v1/payments", body, nil); w.Code != http.StatusOK {
		t.Fatalf("retry must answer 200 with the same payment, got %d", w.Code)
	}
	body["amount"] = 1
	if w = payDo(r, "POST", "/pay/v1/payments", body, nil); w.Code != http.StatusConflict {
		t.Fatalf("changed body: %d", w.Code)
	}
	if w = payDo(r, "GET", "/pay/v1/payments/"+resp.Data.ID.String(), nil, nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"pending"`) {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	if w = payDo(r, "GET", "/pay/v1/payments/not-a-uuid", nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad id: %d", w.Code)
	}
}
