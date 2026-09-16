package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// fakeRampAdmin stands in for *services.ConversionOrchestrator so the two
// ramp admin routes can be exercised without a live treasury.
type fakeRampAdmin struct {
	info       *services.RampLegsInfo
	order      *models.PaycrestOrderRecord
	leg        *models.RampLeg
	err        error
	refundedAs uuid.UUID // the adminID RefundFromTreasury was actually called with
	refundedID uuid.UUID // the orderID RefundFromTreasury was actually called with
	calls      int
}

func (f *fakeRampAdmin) LegsAndOwedByOrderID(context.Context, uuid.UUID) (*models.PaycrestOrderRecord, *services.RampLegsInfo, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.order, f.info, nil
}

func (f *fakeRampAdmin) RefundFromTreasury(_ context.Context, adminID, orderID uuid.UUID) (*models.RampLeg, error) {
	f.calls++
	f.refundedAs, f.refundedID = adminID, orderID
	if f.err != nil {
		return nil, f.err
	}
	return f.leg, nil
}

func rampAdminTestRouter(ramp rampAdminService, user *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := &DisputeHandler{ramp: ramp}
	inject := func(c *gin.Context) {
		if user != nil {
			c.Set("user", user)
		}
	}
	a := r.Group("/admin/ramp/orders", inject)
	a.GET("/:id/legs", h.RampLegs)
	a.POST("/:id/refund", h.RampRefund)
	return r
}

// GET /legs must carry the UNDETERMINED "" state across the wire rather than
// dropping it — the exact same rule models.PaycrestOrderRecord.OwedToUser
// enforces, now on the admin endpoint that reads it.
func TestDisputeHandler_RampLegsSurvivesTheUndeterminedState(t *testing.T) {
	fake := &fakeRampAdmin{info: &services.RampLegsInfo{
		Legs:       []models.RampLeg{{Kind: models.RampLegKindBridge, Status: models.RampLegStatusPending}},
		OwedToUser: "",
	}}
	r := rampAdminTestRouter(fake, disputeUser())

	w, body := disputeDo(t, r, http.MethodGet, "/admin/ramp/orders/"+uuid.New().String()+"/legs", nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", w.Code, body)
	}
	data, _ := body["data"].(map[string]any)
	owed, present := data["owedToUser"]
	if !present {
		t.Fatal("owedToUser key dropped from the response — UNDETERMINED must survive")
	}
	if owed != "" {
		t.Errorf("owedToUser = %v, want the empty UNDETERMINED string", owed)
	}
}

// A dormant treasury (never wired via WithRampAdmin) must 503, not panic or
// silently succeed with nothing behind it.
func TestDisputeHandler_RampRoutesAreDormantWithoutATreasury(t *testing.T) {
	r := rampAdminTestRouter(nil, disputeUser())

	w, _ := disputeDo(t, r, http.MethodGet, "/admin/ramp/orders/"+uuid.New().String()+"/legs", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("legs status = %d, want 503", w.Code)
	}

	w, _ = disputeDo(t, r, http.MethodPost, "/admin/ramp/orders/"+uuid.New().String()+"/refund", nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("refund status = %d, want 503", w.Code)
	}
}

// The refund route must refuse an unauthenticated caller before it ever
// reaches the treasury.
func TestDisputeHandler_RampRefundRequiresAuth(t *testing.T) {
	fake := &fakeRampAdmin{leg: &models.RampLeg{ID: uuid.New()}}
	r := rampAdminTestRouter(fake, nil)

	w, _ := disputeDo(t, r, http.MethodPost, "/admin/ramp/orders/"+uuid.New().String()+"/refund", nil)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if fake.calls != 0 {
		t.Error("the treasury was called without an authenticated admin")
	}
}

// The refund request carries no address, amount or token — only the order id
// in the path — and the handler must pass exactly that through, plus the
// authenticated admin's own id for the audit log, and nothing else.
func TestDisputeHandler_RampRefundTakesOnlyTheOrderIDFromTheCaller(t *testing.T) {
	admin := disputeUser()
	fake := &fakeRampAdmin{leg: &models.RampLeg{ID: uuid.New(), ToAddr: "0xUser"}}
	r := rampAdminTestRouter(fake, admin)
	orderID := uuid.New()

	// A body carrying an address/amount/token must be ignored entirely — the
	// handler never even parses one.
	w, _ := disputeDo(t, r, http.MethodPost, "/admin/ramp/orders/"+orderID.String()+"/refund", map[string]any{
		"toAddress": "0xAttackerControlled", "amount": "999999999999", "token": "USDC",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if fake.calls != 1 {
		t.Fatalf("calls = %d, want 1", fake.calls)
	}
	if fake.refundedID != orderID {
		t.Errorf("refunded order = %s, want %s", fake.refundedID, orderID)
	}
	if fake.refundedAs != admin.ID {
		t.Errorf("refund audit id = %s, want the authenticated admin %s", fake.refundedAs, admin.ID)
	}
}

// TestDisputeHandler_RampRefundErrorMapping pins the two Decision-2 refusals
// to the SAME status but DIFFERENT, explicit messages — an operator reading
// the response must be able to tell "nothing owed" from "not yet known".
func TestDisputeHandler_RampRefundErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nothing owed", services.ErrNothingOwed, http.StatusConflict},
		{"undetermined", services.ErrOwedUndetermined, http.StatusConflict},
		{"in flight", services.ErrRefundInFlight, http.StatusConflict},
		{"not found", gorm.ErrRecordNotFound, http.StatusNotFound},
	}
	var seen = map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRampAdmin{err: tc.err}
			r := rampAdminTestRouter(fake, disputeUser())

			w, body := disputeDo(t, r, http.MethodPost, "/admin/ramp/orders/"+uuid.New().String()+"/refund", nil)

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			msg, _ := body["message"].(string)
			if msg == "" {
				t.Fatal("no message for the operator to read")
			}
			if seen[msg] {
				t.Fatalf("message %q reused across sentinels; ErrNothingOwed and ErrOwedUndetermined must read differently", msg)
			}
			seen[msg] = true
		})
	}
}
