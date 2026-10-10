package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// partlyRefunded answers every payment as $12.50 refunded of $33, counting
// how often it is asked: a page is one question, not one per row.
type partlyRefunded struct{ calls int }

func (a *partlyRefunded) AggregateByPayment(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]services.PayRefundAgg, error) {
	a.calls++
	out := make(map[uuid.UUID]services.PayRefundAgg, len(ids))
	for _, id := range ids {
		out[id] = services.PayRefundAgg{RefundedMicroUSD: 12_500_000, CapMicroUSD: 33_000_000, Status: services.PayRefundStatusPartial}
	}
	return out, nil
}

func (a *partlyRefunded) TotalsConfirmed(context.Context, uuid.UUID, string, time.Time, time.Time) (services.PaySummaryRefunds, error) {
	return services.PaySummaryRefunds{}, nil
}

const partlyRefundedJSON = `"refundedAmount":"12.50","refundStatus":"partial"`

// Every answer about an existing payment carries its refund totals: the
// merchant API's read, replay and reference lookup, and the admin list.
func TestPayRefundTotals_OnEveryExistingPaymentAnswer(t *testing.T) {
	h, payments, db := newPayHandlerAdminRig(t)
	agg := &partlyRefunded{}
	payments.SetRefundAggregator(agg)
	m := seedHandlerMerchant(t, db)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	inject := func(c *gin.Context) { c.Set("payMerchant", m); c.Set("payMode", models.PayModeTest); c.Next() }
	r.POST("/pay/v1/payments", inject, h.CreatePayment)
	r.GET("/pay/v1/payments", inject, h.FindPaymentByReference)
	r.GET("/pay/v1/payments/:id", inject, h.GetPayment)

	body := map[string]any{"amount": 5_000_000, "currency": "NGN", "reference": "PNR-9"}
	w := payDo(r, "POST", "/pay/v1/payments", body, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct{ Data services.PayPaymentView }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("create body: %v %s", err, w.Body.String())
	}

	for name, w := range map[string]*httptest.ResponseRecorder{
		"replay":    payDo(r, "POST", "/pay/v1/payments", body, nil),
		"get":       payDo(r, "GET", "/pay/v1/payments/"+created.Data.ID.String(), nil, nil),
		"reference": payDo(r, "GET", "/pay/v1/payments?reference="+url.QueryEscape("PNR-9"), nil, nil),
	} {
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), partlyRefundedJSON) {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), `"payment":{`) {
			t.Errorf("%s: the payment is data itself, never wrapped: %s", name, w.Body.String())
		}
	}

	if _, _, err := payments.Create(context.Background(), m, models.PayModeTest, services.CreatePayPaymentInput{AmountKobo: 5_000_000, Currency: "NGN", Reference: "PNR-10"}); err != nil {
		t.Fatal(err)
	}
	agg.calls = 0
	w = payDo(payAdminRouter(h), "GET", "/admin/pay/payments", nil, nil)
	if w.Code != http.StatusOK || strings.Count(w.Body.String(), partlyRefundedJSON) != 2 || agg.calls != 1 {
		t.Fatalf("admin list (%d aggregate calls): %d %s", agg.calls, w.Code, w.Body.String())
	}
}

// An open link's detail lists its payments with their refund totals.
func TestPayRefundTotals_OnAnOpenLinksPayments(t *testing.T) {
	o := newOwnerRig(t)
	o.payments.SetRefundAggregator(&partlyRefunded{})
	l := o.newOpenLink(t, map[string]any{"label": "Shop 12"})
	w := payDo(o.r, http.MethodPost, "/api/pay/public/open/"+l.Slug+"/payments", map[string]any{"amount": 450_000}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("pay: %d %s", w.Code, w.Body.String())
	}
	var started struct{ Data struct{ ID uuid.UUID } }
	json.Unmarshal(w.Body.Bytes(), &started)
	if _, err := o.payments.SimulateTestOutcome(t.Context(), &models.User{ID: o.owner.ID}, started.Data.ID, true); err != nil {
		t.Fatal(err)
	}
	detail := o.openLink(http.MethodGet, "/api/pay/merchant/open-links/"+l.ID.String()+"?mode=test", nil)
	if detail.status != http.StatusOK || len(detail.Detail.Payments) != 1 || detail.Detail.Payments[0].RefundedAmount != "12.50" ||
		detail.Detail.Payments[0].RefundStatus != services.PayRefundStatusPartial || detail.Detail.Payments[0].LinkLabel != "Shop 12" {
		t.Fatalf("detail: %d %s", detail.status, detail.raw)
	}
}
