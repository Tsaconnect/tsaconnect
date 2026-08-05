package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fakeBillSvc implements billService in-memory.
type fakeBillSvc struct {
	quoteIn  *services.BillQuoteInput
	payWallet string
	order    models.BillOrder
	err      error
}

func (f *fakeBillSvc) Quote(_ context.Context, in services.BillQuoteInput) (*services.BillQuoteResult, error) {
	f.quoteIn = &in
	if f.err != nil {
		return nil, f.err
	}
	return &services.BillQuoteResult{Order: &f.order, McgpAmount: "37", RateKobo: "2700", NgnAmount: 1000}, nil
}
func (f *fakeBillSvc) Pay(_ context.Context, _ uuid.UUID, wallet string, _ uuid.UUID) (*models.BillOrder, error) {
	f.payWallet = wallet
	if f.err != nil {
		return nil, f.err
	}
	return &f.order, nil
}
func (f *fakeBillSvc) Validate(context.Context, string, string, string) (string, error) {
	return "ADA OBI", f.err
}
func (f *fakeBillSvc) Providers(context.Context, string) ([]services.BillProvider, error) {
	return []services.BillProvider{{Code: "01", Name: "MTN"}}, f.err
}
func (f *fakeBillSvc) Plans(context.Context, string, string) ([]services.BillPlan, error) {
	return []services.BillPlan{{Code: "2", Name: "500 MB", AmountNGN: 317}}, f.err
}
func (f *fakeBillSvc) GetOrder(context.Context, uuid.UUID, uuid.UUID) (*models.BillOrder, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &f.order, nil
}
func (f *fakeBillSvc) ListOrders(context.Context, uuid.UUID, int) ([]models.BillOrder, error) {
	return []models.BillOrder{f.order}, f.err
}

func billTestRouter(svc billService, user *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewBillHandler(svc)
	inject := func(c *gin.Context) {
		if user != nil {
			c.Set("user", user)
		}
	}
	g := r.Group("/bills", inject)
	g.GET("/catalog/:category", h.GetProviders)
	g.GET("/catalog/:category/:provider/plans", h.GetPlans)
	g.POST("/validate", h.Validate)
	g.POST("/quote", h.Quote)
	g.GET("/orders", h.ListOrders)
	g.GET("/orders/:id", h.GetOrder)
	g.POST("/orders/:id/pay", h.Pay)
	return r
}

func billDo(t *testing.T, r *gin.Engine, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func billTestUser() *models.User {
	return &models.User{ID: uuid.New(), WalletAddress: "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"}
}

func TestBillHandlerQuotePassesUser(t *testing.T) {
	svc := &fakeBillSvc{order: models.BillOrder{ID: uuid.New(), Status: models.BillStatusQuoted}}
	user := billTestUser()
	r := billTestRouter(svc, user)
	w, out := billDo(t, r, http.MethodPost, "/bills/quote", gin.H{
		"category": "airtime", "provider": "01", "phoneNumber": "08030001111", "ngnAmount": 1000,
	})
	if w.Code != http.StatusOK || out["success"] != true {
		t.Fatalf("quote: %d %s", w.Code, w.Body.String())
	}
	if svc.quoteIn.UserID != user.ID || svc.quoteIn.NgnAmount != 1000 || svc.quoteIn.Category != "airtime" {
		t.Fatalf("input not threaded: %+v", svc.quoteIn)
	}
}

func TestBillHandlerAuthRequired(t *testing.T) {
	svc := &fakeBillSvc{}
	r := billTestRouter(svc, nil) // no user in context
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/bills/quote"},
		{http.MethodPost, "/bills/orders/" + uuid.NewString() + "/pay"},
		{http.MethodGet, "/bills/orders"},
		{http.MethodGet, "/bills/orders/" + uuid.NewString()},
	} {
		w, _ := billDo(t, r, tc.method, tc.path, gin.H{})
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: code %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

func TestBillHandlerPayRequiresWallet(t *testing.T) {
	svc := &fakeBillSvc{order: models.BillOrder{ID: uuid.New()}}
	user := billTestUser()
	user.WalletAddress = ""
	r := billTestRouter(svc, user)
	w, _ := billDo(t, r, http.MethodPost, "/bills/orders/"+uuid.NewString()+"/pay", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400 for missing wallet", w.Code)
	}
}

func TestBillHandlerPayThreadsWallet(t *testing.T) {
	svc := &fakeBillSvc{order: models.BillOrder{ID: uuid.New(), Status: models.BillStatusDelivered}}
	user := billTestUser()
	r := billTestRouter(svc, user)
	w, _ := billDo(t, r, http.MethodPost, "/bills/orders/"+uuid.NewString()+"/pay", nil)
	if w.Code != http.StatusOK || svc.payWallet != user.WalletAddress {
		t.Fatalf("code=%d wallet=%q", w.Code, svc.payWallet)
	}
}

func TestBillHandlerBadOrderID(t *testing.T) {
	svc := &fakeBillSvc{}
	r := billTestRouter(svc, billTestUser())
	w, _ := billDo(t, r, http.MethodPost, "/bills/orders/not-a-uuid/pay", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("code %d, want 400", w.Code)
	}
}

func TestBillHandlerStatusMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{services.ErrBillsDisabled, http.StatusServiceUnavailable},
		{services.ErrBillRateStale, http.StatusServiceUnavailable},
		{services.ErrBillNotFound, http.StatusNotFound},
		{services.ErrBillPayInsufficient, http.StatusPaymentRequired},
		{services.ErrBillQuoteExpired, http.StatusBadRequest},
		{fmt.Errorf("wrapped: %w", services.ErrBillBadRequest), http.StatusBadRequest},
	}
	for _, tc := range cases {
		svc := &fakeBillSvc{err: tc.err}
		r := billTestRouter(svc, billTestUser())
		w, _ := billDo(t, r, http.MethodPost, "/bills/quote", gin.H{"category": "airtime"})
		if w.Code != tc.want {
			t.Errorf("%v → %d, want %d", tc.err, w.Code, tc.want)
		}
	}
}

func TestBillHandlerCatalog(t *testing.T) {
	svc := &fakeBillSvc{}
	r := billTestRouter(svc, billTestUser())
	w, out := billDo(t, r, http.MethodGet, "/bills/catalog/airtime", nil)
	if w.Code != http.StatusOK || out["success"] != true {
		t.Fatalf("catalog: %d %s", w.Code, w.Body.String())
	}
	w, _ = billDo(t, r, http.MethodGet, "/bills/catalog/data/01/plans", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("plans: %d", w.Code)
	}
}
