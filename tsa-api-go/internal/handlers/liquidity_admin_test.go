package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakePoolAdmin struct {
	err  error
	last string
	note string
}

func (f *fakePoolAdmin) PoolOrderDetail(context.Context, uuid.UUID) (*services.PoolOrderDetail, error) {
	return &services.PoolOrderDetail{Intent: &models.ITIntent{ID: uuid.New()}, Actions: []string{"retry-fill"}}, f.err
}
func (f *fakePoolAdmin) ResendPoolBuy(_ context.Context, _, _ uuid.UUID, n string) (*models.ITIntent, error) {
	f.last, f.note = "resend", n
	return &models.ITIntent{ID: uuid.New()}, f.err
}
func (f *fakePoolAdmin) VaultPoolSummary(context.Context) (*services.VaultPoolSummary, error) {
	f.last = "summary"
	return &services.VaultPoolSummary{Collateral: "100"}, f.err
}
func (f *fakePoolAdmin) SettlePoolFromCollateral(_ context.Context, _ uuid.UUID, amount, note string) (string, error) {
	f.last, f.note = "settle:"+amount, note
	return "0xsettle", f.err
}
func (f *fakePoolAdmin) RetryPoolFill(_ context.Context, _, _ uuid.UUID, n string) (*models.ITIntent, error) {
	f.last, f.note = "retry-fill", n
	return &models.ITIntent{ID: uuid.New()}, f.err
}

func poolAdminServer(f *fakePoolAdmin) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewITHandler(nil, nil, nil)
	h.SetPoolOrdersAdmin(f)
	h.SetVaultPoolAdmin(f)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", &models.User{ID: uuid.New()}) })
	r.GET("/o/:id", h.AdminPoolOrder)
	r.POST("/o/:id/resend", h.AdminResendPoolBuy)
	r.POST("/o/:id/retry-fill", h.AdminRetryPoolFill)
	r.GET("/pool", h.AdminVaultPool)
	r.POST("/pool/settle", h.AdminSettlePoolFromCollateral)
	return r
}

func TestLiquidityAdmin_OrderActionsNeedANote(t *testing.T) {
	f := &fakePoolAdmin{}
	r := poolAdminServer(f)
	id := uuid.New()
	for _, p := range []string{"/o/" + id.String() + "/resend", "/o/" + id.String() + "/retry-fill"} {
		if w := walletDo(r, http.MethodPost, p, map[string]string{}); w.Code != http.StatusBadRequest || f.last != "" {
			t.Fatalf("%s without a note = %d (called %q)", p, w.Code, f.last)
		}
	}
	if w := walletDo(r, http.MethodPost, "/o/"+id.String()+"/retry-fill", map[string]string{"note": "collateral posted"}); w.Code != http.StatusOK || f.last != "retry-fill" {
		t.Fatalf("retry-fill = %d %s", w.Code, f.last)
	}
}

func TestLiquidityAdmin_PoolSummaryAndSettle(t *testing.T) {
	f := &fakePoolAdmin{}
	r := poolAdminServer(f)
	if w := walletDo(r, http.MethodGet, "/pool", nil); w.Code != http.StatusOK || f.last != "summary" {
		t.Fatalf("summary = %d %s", w.Code, f.last)
	}
	if w := walletDo(r, http.MethodPost, "/pool/settle", map[string]string{"note": "n"}); w.Code != http.StatusBadRequest {
		t.Fatalf("settle without an amount = %d, want 400", w.Code)
	}
	if w := walletDo(r, http.MethodPost, "/pool/settle", map[string]string{"amount": "5", "note": "kept the naira"}); w.Code != http.StatusOK || f.last != "settle:5" {
		t.Fatalf("settle = %d %s", w.Code, f.last)
	}
	f.err = services.ErrLPAmount
	if w := walletDo(r, http.MethodPost, "/pool/settle", map[string]string{"amount": "0", "note": "n"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a bad amount = %d, want 400", w.Code)
	}
}

type fakeRechecker struct{ calls int }

func (f *fakeRechecker) Recheck(context.Context, uuid.UUID) error { f.calls++; return nil }

// Re-review I-D: the operator recheck needs a note and calls the forwarder once.
func TestLiquidityAdmin_ForwardRecheckNeedsANote(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewITHandler(nil, nil, nil)
	rc := &fakeRechecker{}
	h.SetForwardRechecker(rc)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", &models.User{ID: uuid.New()}) })
	r.POST("/f/:id/recheck", h.AdminRecheckForward)
	if w := walletDo(r, http.MethodPost, "/f/"+uuid.NewString()+"/recheck", map[string]string{}); w.Code != http.StatusBadRequest || rc.calls != 0 {
		t.Fatalf("no note = %d calls=%d", w.Code, rc.calls)
	}
	if w := walletDo(r, http.MethodPost, "/f/"+uuid.NewString()+"/recheck", map[string]string{"note": "provider support says pending"}); w.Code != http.StatusOK || rc.calls != 1 {
		t.Fatalf("recheck = %d calls=%d", w.Code, rc.calls)
	}
}

func TestLiquidityAdmin_RefusalIsAConflict(t *testing.T) {
	for _, e := range []error{services.ErrPoolCannotFill, services.ErrITBadState} {
		r := poolAdminServer(&fakePoolAdmin{err: e})
		if w := walletDo(r, http.MethodPost, "/o/"+uuid.NewString()+"/retry-fill", map[string]string{"note": "n"}); w.Code != http.StatusConflict {
			t.Fatalf("%v: code = %d, want 409", e, w.Code)
		}
	}
}
