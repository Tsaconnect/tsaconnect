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
	err    error
	last   string
	note   string
	intent uuid.UUID // the order id a slice action was given
}

func (f *fakePoolAdmin) PoolOrderDetail(context.Context, uuid.UUID) (*services.PoolOrderDetail, error) {
	return &services.PoolOrderDetail{Intent: &models.ITIntent{ID: uuid.New()}, Actions: []string{"redeliver"}}, f.err
}
func (f *fakePoolAdmin) RedeliverPoolBuy(_ context.Context, _, _ uuid.UUID, n string) (*models.ITIntent, error) {
	f.last, f.note = "redeliver", n
	return &models.ITIntent{ID: uuid.New()}, f.err
}
func (f *fakePoolAdmin) ResendPoolBuy(_ context.Context, _, _ uuid.UUID, n string) (*models.ITIntent, error) {
	f.last, f.note = "resend", n
	return &models.ITIntent{ID: uuid.New()}, f.err
}
func (f *fakePoolAdmin) RefundPoolBuyOverride(_ context.Context, _, _ uuid.UUID, n string) (*models.ITIntent, error) {
	f.last, f.note = "refund-override", n
	return &models.ITIntent{ID: uuid.New()}, f.err
}
func (f *fakePoolAdmin) PayPoolSliceLP(_ context.Context, _, intent, _ uuid.UUID, n string) (*models.P2PSettlementForward, error) {
	f.last, f.note, f.intent = "pay-lp", n, intent
	return &models.P2PSettlementForward{ID: uuid.New()}, f.err
}
func (f *fakePoolAdmin) NotePoolSliceReturned(_ context.Context, _, intent, _ uuid.UUID, _, n string) error {
	f.last, f.note, f.intent = "returned", n, intent
	return f.err
}

func (f *fakePoolAdmin) ClearPoolSliceRelease(_ context.Context, _, intent, _ uuid.UUID, n string) error {
	f.last, f.note, f.intent = "clear-release", n, intent
	return f.err
}

func poolAdminServer(f *fakePoolAdmin) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewITHandler(nil, nil, nil)
	h.SetPoolOrdersAdmin(f)
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", &models.User{ID: uuid.New()}) })
	r.GET("/o/:id", h.AdminPoolOrder)
	r.POST("/o/:id/redeliver", h.AdminRedeliverPoolBuy)
	r.POST("/o/:id/resend", h.AdminResendPoolBuy)
	r.POST("/o/:id/refund-override", h.AdminRefundPoolBuyOverride)
	r.POST("/o/:id/slices/:sliceId/pay-lp", h.AdminPayPoolSliceLP)
	r.POST("/o/:id/slices/:sliceId/returned", h.AdminNotePoolSliceReturned)
	r.POST("/o/:id/slices/:sliceId/clear-release", h.AdminClearPoolSliceRelease)
	return r
}

func TestLiquidityAdmin_ActionsNeedANote(t *testing.T) {
	f := &fakePoolAdmin{}
	r := poolAdminServer(f)
	id := uuid.New()
	for _, p := range []string{
		"/o/" + id.String() + "/redeliver",
		"/o/" + id.String() + "/resend",
		"/o/" + id.String() + "/refund-override",
		"/o/" + id.String() + "/slices/" + uuid.NewString() + "/pay-lp",
		"/o/" + id.String() + "/slices/" + uuid.NewString() + "/returned",
		"/o/" + id.String() + "/slices/" + uuid.NewString() + "/clear-release",
	} {
		if w := walletDo(r, http.MethodPost, p, map[string]string{}); w.Code != http.StatusBadRequest || f.last != "" {
			t.Fatalf("%s without a note = %d (called %q)", p, w.Code, f.last)
		}
	}
	if w := walletDo(r, http.MethodPost, "/o/"+id.String()+"/redeliver", map[string]string{"note": "lost slice"}); w.Code != http.StatusOK || f.last != "redeliver" {
		t.Fatalf("redeliver = %d %s", w.Code, f.last)
	}
	if w := walletDo(r, http.MethodPost, "/o/"+id.String()+"/slices/"+uuid.NewString()+"/returned",
		map[string]string{"note": "handed back", "txHash": "0xabc"}); w.Code != http.StatusOK || f.last != "returned" {
		t.Fatalf("returned = %d %s", w.Code, f.last)
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
		f := &fakePoolAdmin{err: e}
		r := poolAdminServer(f)
		if w := walletDo(r, http.MethodPost, "/o/"+uuid.NewString()+"/redeliver", map[string]string{"note": "n"}); w.Code != http.StatusConflict {
			t.Fatalf("%v: code = %d, want 409", e, w.Code)
		}
		if w := walletDo(r, http.MethodPost, "/o/"+uuid.NewString()+"/slices/"+uuid.NewString()+"/pay-lp", map[string]string{"note": "n"}); w.Code != http.StatusConflict {
			t.Fatalf("%v: pay-lp code = %d, want 409", e, w.Code)
		}
	}
}

// Review m-8: a slice action hands the service the order id in its route, so
// the service can refuse a slice of another order.
func TestLiquidityAdmin_SliceActionsPassTheRouteOrder(t *testing.T) {
	f := &fakePoolAdmin{}
	r := poolAdminServer(f)
	id := uuid.New()
	for _, act := range []string{"pay-lp", "returned", "clear-release"} {
		f.intent = uuid.Nil
		if w := walletDo(r, http.MethodPost, "/o/"+id.String()+"/slices/"+uuid.NewString()+"/"+act, map[string]string{"note": "n"}); w.Code != http.StatusOK || f.intent != id {
			t.Fatalf("%s = %d intent=%s, want the route's order %s", act, w.Code, f.intent, id)
		}
	}
	if w := walletDo(r, http.MethodPost, "/o/not-a-uuid/slices/"+uuid.NewString()+"/pay-lp", map[string]string{"note": "n"}); w.Code != http.StatusBadRequest {
		t.Fatalf("bad order id = %d, want 400", w.Code)
	}
}
