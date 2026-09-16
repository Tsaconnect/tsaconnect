package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fakeBillSvc implements billService in-memory.
type fakeBillSvc struct {
	quoteIn   *services.BillQuoteInput
	payWallet string
	order     models.BillOrder
	report    models.BillReport
	reportIn  *fakeBillReportIn
	resolveIn *fakeBillResolveIn
	err       error

	paymentAction string // "reverse" | "settle" — which admin payment action ran
	paymentOrder  uuid.UUID
	stuck         []services.BillStuckPayment
}

func (f *fakeBillSvc) AdminReversePayment(_ context.Context, orderID uuid.UUID) (*models.BillOrder, error) {
	f.paymentAction, f.paymentOrder = "reverse", orderID
	if f.err != nil {
		return nil, f.err
	}
	return &f.order, nil
}
func (f *fakeBillSvc) AdminSettlePayment(_ context.Context, orderID uuid.UUID) (*models.BillOrder, error) {
	f.paymentAction, f.paymentOrder = "settle", orderID
	if f.err != nil {
		return nil, f.err
	}
	return &f.order, nil
}
func (f *fakeBillSvc) ListStuckPayments(context.Context, time.Duration, int) ([]services.BillStuckPayment, error) {
	return f.stuck, f.err
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
func (f *fakeBillSvc) ReportOrder(_ context.Context, userID, orderID uuid.UUID, reason string) (*models.BillReport, error) {
	f.reportIn = &fakeBillReportIn{userID: userID, orderID: orderID, reason: reason}
	if f.err != nil {
		return nil, f.err
	}
	return &f.report, nil
}
func (f *fakeBillSvc) GetOrderReport(context.Context, uuid.UUID, uuid.UUID) (*models.BillReport, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &f.report, nil
}
func (f *fakeBillSvc) ListReportsAdmin(context.Context, string, int, int) ([]services.BillReportAdminRow, int64, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	return []services.BillReportAdminRow{{Report: f.report, Order: &f.order}}, 1, nil
}
func (f *fakeBillSvc) ResolveReport(_ context.Context, adminID, reportID uuid.UUID, status, resolution string) (*models.BillReport, error) {
	f.resolveIn = &fakeBillResolveIn{adminID: adminID, reportID: reportID, status: status, resolution: resolution}
	if f.err != nil {
		return nil, f.err
	}
	return &f.report, nil
}

type fakeBillReportIn struct {
	userID, orderID uuid.UUID
	reason          string
}

type fakeBillResolveIn struct {
	adminID, reportID  uuid.UUID
	status, resolution string
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
	g.POST("/orders/:id/report", h.ReportOrder)
	g.GET("/orders/:id/report", h.GetOrderReport)
	a := r.Group("/admin/bills", inject)
	a.GET("/reports", h.ListReportsAdmin)
	a.POST("/reports/:id/resolve", h.ResolveReportAdmin)
	a.GET("/payments/stuck", h.ListStuckPaymentsAdmin)
	a.POST("/orders/:id/reverse-payment", h.ReversePaymentAdmin)
	a.POST("/orders/:id/settle-payment", h.SettlePaymentAdmin)
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
		{http.MethodPost, "/bills/orders/" + uuid.NewString() + "/report"},
		{http.MethodGet, "/bills/orders/" + uuid.NewString() + "/report"},
		{http.MethodPost, "/admin/bills/reports/" + uuid.NewString() + "/resolve"},
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

func TestBillHandlerReportOrder(t *testing.T) {
	reportID := uuid.New()
	svc := &fakeBillSvc{report: models.BillReport{ID: reportID, Status: models.BillReportStatusOpen}}
	user := billTestUser()
	r := billTestRouter(svc, user)
	orderID := uuid.New()

	w, out := billDo(t, r, http.MethodPost, "/bills/orders/"+orderID.String()+"/report",
		gin.H{"reason": "no airtime received"})
	if w.Code != http.StatusCreated || out["success"] != true {
		t.Fatalf("report: %d %s", w.Code, w.Body.String())
	}
	if svc.reportIn == nil || svc.reportIn.userID != user.ID ||
		svc.reportIn.orderID != orderID || svc.reportIn.reason != "no airtime received" {
		t.Fatalf("input not threaded: %+v", svc.reportIn)
	}
}

func TestBillHandlerReportConflictMapped(t *testing.T) {
	svc := &fakeBillSvc{err: services.ErrBillAlreadyReported}
	r := billTestRouter(svc, billTestUser())
	w, out := billDo(t, r, http.MethodPost, "/bills/orders/"+uuid.NewString()+"/report",
		gin.H{"reason": "x"})
	if w.Code != http.StatusConflict {
		t.Fatalf("code %d, want 409", w.Code)
	}
	if out["message"] != services.ErrBillAlreadyReported.Error() {
		t.Fatalf("message %q not passed through", out["message"])
	}
}

func TestBillHandlerResolveReport(t *testing.T) {
	svc := &fakeBillSvc{report: models.BillReport{ID: uuid.New(), Status: models.BillReportStatusResolved}}
	admin := billTestUser()
	r := billTestRouter(svc, admin)
	reportID := uuid.New()

	w, out := billDo(t, r, http.MethodPost, "/admin/bills/reports/"+reportID.String()+"/resolve",
		gin.H{"status": "resolved", "resolution": "refunded"})
	if w.Code != http.StatusOK || out["success"] != true {
		t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
	}
	if svc.resolveIn == nil || svc.resolveIn.adminID != admin.ID ||
		svc.resolveIn.reportID != reportID || svc.resolveIn.status != "resolved" ||
		svc.resolveIn.resolution != "refunded" {
		t.Fatalf("input not threaded: %+v", svc.resolveIn)
	}
}

func TestBillHandlerListReportsAdmin(t *testing.T) {
	svc := &fakeBillSvc{report: models.BillReport{ID: uuid.New()}}
	r := billTestRouter(svc, billTestUser())
	w, out := billDo(t, r, http.MethodGet, "/admin/bills/reports?status=open", nil)
	if w.Code != http.StatusOK || out["success"] != true {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	data, _ := out["data"].(map[string]any)
	if data == nil || data["total"] != float64(1) {
		t.Fatalf("payload: %v", out["data"])
	}
}

func TestBillHandlerAdminPaymentActions(t *testing.T) {
	for _, action := range []string{"reverse", "settle"} {
		svc := &fakeBillSvc{order: models.BillOrder{ID: uuid.New(), Status: models.BillStatusRefunded}}
		r := billTestRouter(svc, billTestUser())
		orderID := uuid.New()
		w, out := billDo(t, r, http.MethodPost, "/admin/bills/orders/"+orderID.String()+"/"+action+"-payment", nil)
		if w.Code != http.StatusOK || out["success"] != true || svc.paymentAction != action || svc.paymentOrder != orderID {
			t.Fatalf("%s: %d %s action=%q order=%s", action, w.Code, w.Body.String(), svc.paymentAction, svc.paymentOrder)
		}
	}
}

// An operator must be able to tell "refused: the bill was delivered" from
// "could not reach the vendor", and see why.
func TestBillHandlerAdminPaymentRefusals(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: the vendor reports this bill delivered", services.ErrBillPaymentConflict), http.StatusConflict},
		{fmt.Errorf("%w: timeout", services.ErrBillVendUnverified), http.StatusServiceUnavailable},
		{fmt.Errorf("%w: rpc down", services.ErrBillDebitUnverified), http.StatusServiceUnavailable},
		{fmt.Errorf("%w: the debit is not confirmed on-chain yet", services.ErrBillPayBadState), http.StatusConflict},
	}
	for _, tc := range cases {
		svc := &fakeBillSvc{err: tc.err}
		r := billTestRouter(svc, billTestUser())
		w, out := billDo(t, r, http.MethodPost, "/admin/bills/orders/"+uuid.New().String()+"/reverse-payment", nil)
		if w.Code != tc.want {
			t.Errorf("%v → %d, want %d", tc.err, w.Code, tc.want)
		}
		if msg, _ := out["message"].(string); msg != tc.err.Error() {
			t.Errorf("message = %q, want the reason %q", msg, tc.err.Error())
		}
	}
}

func TestBillHandlerListStuckPayments(t *testing.T) {
	svc := &fakeBillSvc{stuck: []services.BillStuckPayment{{OrderStatus: models.BillStatusVending}}}
	r := billTestRouter(svc, billTestUser())
	w, out := billDo(t, r, http.MethodGet, "/admin/bills/payments/stuck?minutes=60", nil)
	if w.Code != http.StatusOK || out["success"] != true {
		t.Fatalf("stuck: %d %s", w.Code, w.Body.String())
	}
	data, _ := out["data"].(map[string]any)
	if rows, _ := data["stuck"].([]any); len(rows) != 1 || data["thresholdMinutes"] != float64(60) {
		t.Fatalf("payload: %v", out["data"])
	}
}
