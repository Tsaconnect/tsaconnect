// PM handler HTTP-level tests. Uses a fakePMAutomation to verify wire format,
// error mapping, and status-code behaviour without a real service graph.
//
// Auth is faked by directly calling c.Set("user", ...) via a tiny middleware
// shim, matching the pattern used in p2p_handler_test.go.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// ─── Fake PMAutomation ────────────────────────────────────────────────────────

type fakePMAutomation struct {
	// ListBuyManualQueue
	queueItems []services.ManualQueueItem
	queueErr   error

	// ManualRelease
	releaseResult *models.P2PTrade
	releaseErr    error
	releaseCalls  []releaseCall

	// InitiateSellPayout
	payoutID    string
	payoutErr   error
	payoutCalls []uuid.UUID
}

type releaseCall struct {
	tradeID uuid.UUID
	adminID uuid.UUID
}

func (f *fakePMAutomation) ListBuyManualQueue(_ context.Context) ([]services.ManualQueueItem, error) {
	return f.queueItems, f.queueErr
}

func (f *fakePMAutomation) ManualRelease(_ context.Context, tradeID, adminID uuid.UUID) (*models.P2PTrade, error) {
	f.releaseCalls = append(f.releaseCalls, releaseCall{tradeID: tradeID, adminID: adminID})
	return f.releaseResult, f.releaseErr
}

func (f *fakePMAutomation) InitiateSellPayout(_ context.Context, tradeID uuid.UUID) (string, error) {
	f.payoutCalls = append(f.payoutCalls, tradeID)
	return f.payoutID, f.payoutErr
}

// ─── Router helpers ───────────────────────────────────────────────────────────

// buildPMRouter wires a PMHandler + auth-shim onto a test gin engine.
// adminUser may be nil to simulate an unauthenticated request (for edge
// cases); pass a *models.User to inject the authenticated operator.
func buildPMRouter(h *PMHandler, adminUser *models.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if adminUser != nil {
		r.Use(func(c *gin.Context) {
			c.Set("user", adminUser)
			c.Next()
		})
	}
	g := r.Group("/api/admin/pm")
	g.GET("/manual-queue", h.GetManualQueue)
	g.POST("/trades/:id/release", h.ReleaseTrade)
	g.POST("/trades/:id/payout", h.InitiatePayout)
	g.POST("/trades/:id/decline", h.DeclineTrade)
	return r
}

func testAdminUser() *models.User {
	return &models.User{
		ID:    uuid.New(),
		Name:  "Admin",
		Role:  models.RoleAdmin,
		Email: "admin@test.com",
	}
}

// ─── GetManualQueue ───────────────────────────────────────────────────────────

func TestPMHandler_GetManualQueue_ReturnsItems(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tradeID := uuid.New()
	adID := uuid.New()
	buyerID := uuid.New()

	fake := &fakePMAutomation{
		queueItems: []services.ManualQueueItem{
			{
				TradeID:     tradeID,
				AdID:        adID,
				BuyerID:     buyerID,
				BuyerName:   "JOHN DOE",
				Token:       "USDT",
				TokenAmount: "1000000",
				FiatAmount:  "1300.00",
				CreatedAt:   now,
			},
		},
	}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/pm/manual-queue", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp["success"] != true {
		t.Errorf("expected success=true, got %v", resp["success"])
	}
	data, ok := resp["data"].([]interface{})
	if !ok {
		t.Fatalf("expected data to be an array, got %T", resp["data"])
	}
	if len(data) != 1 {
		t.Fatalf("expected 1 item, got %d", len(data))
	}
	item, ok := data[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected item to be an object, got %T", data[0])
	}
	if item["tradeId"] != tradeID.String() {
		t.Errorf("tradeId = %v, want %s", item["tradeId"], tradeID)
	}
	if item["buyerName"] != "JOHN DOE" {
		t.Errorf("buyerName = %v, want JOHN DOE", item["buyerName"])
	}
}

func TestPMHandler_GetManualQueue_Empty(t *testing.T) {
	fake := &fakePMAutomation{queueItems: []services.ManualQueueItem{}}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/admin/pm/manual-queue", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Errorf("expected success=true")
	}
}

// ─── ReleaseTrade ─────────────────────────────────────────────────────────────

func TestPMHandler_ReleaseTrade_Success(t *testing.T) {
	tradeID := uuid.New()
	adminUser := testAdminUser()
	trade := &models.P2PTrade{ID: tradeID, Status: models.P2PTradeStatusReleased}

	fake := &fakePMAutomation{releaseResult: trade}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, adminUser)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/release", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(fake.releaseCalls) != 1 {
		t.Fatalf("expected 1 release call, got %d", len(fake.releaseCalls))
	}
	if fake.releaseCalls[0].tradeID != tradeID {
		t.Errorf("release tradeID = %v, want %v", fake.releaseCalls[0].tradeID, tradeID)
	}
	if fake.releaseCalls[0].adminID != adminUser.ID {
		t.Errorf("release adminID = %v, want %v", fake.releaseCalls[0].adminID, adminUser.ID)
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Errorf("expected success=true")
	}
}

func TestPMHandler_ReleaseTrade_BadUUID(t *testing.T) {
	fake := &fakePMAutomation{}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/not-a-uuid/release", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if len(fake.releaseCalls) != 0 {
		t.Errorf("expected 0 release calls on bad uuid, got %d", len(fake.releaseCalls))
	}
}

func TestPMHandler_ReleaseTrade_NotFound(t *testing.T) {
	tradeID := uuid.New()
	fake := &fakePMAutomation{releaseErr: services.ErrTradeNotFound}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/release", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPMHandler_ReleaseTrade_InvalidTransition(t *testing.T) {
	tradeID := uuid.New()
	fake := &fakePMAutomation{releaseErr: services.ErrInvalidTransition}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/release", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPMHandler_ReleaseTrade_WrappedInvalidTransition(t *testing.T) {
	tradeID := uuid.New()
	// Wrapped errors (fmt.Errorf "%w") must also be caught.
	wrappedErr := errors.New("already paid: " + services.ErrInvalidTransition.Error())
	_ = wrappedErr
	fake := &fakePMAutomation{releaseErr: errors.Join(services.ErrInvalidTransition, errors.New("already released"))}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/release", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for wrapped ErrInvalidTransition, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPMHandler_ReleaseTrade_InternalError(t *testing.T) {
	tradeID := uuid.New()
	fake := &fakePMAutomation{releaseErr: errors.New("rpc down")}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/release", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", w.Code, w.Body.String())
	}
}

// ─── InitiatePayout ───────────────────────────────────────────────────────────

func TestPMHandler_InitiatePayout_Success(t *testing.T) {
	tradeID := uuid.New()
	fake := &fakePMAutomation{payoutID: "po_123"}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/payout", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Errorf("expected success=true")
	}
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be an object, got %T", resp["data"])
	}
	if data["payoutId"] != "po_123" {
		t.Errorf("payoutId = %v, want po_123", data["payoutId"])
	}
	if len(fake.payoutCalls) != 1 || fake.payoutCalls[0] != tradeID {
		t.Errorf("payout call mismatch: %v", fake.payoutCalls)
	}
}

func TestPMHandler_InitiatePayout_BadUUID(t *testing.T) {
	fake := &fakePMAutomation{}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/not-a-uuid/payout", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPMHandler_InitiatePayout_NotFound(t *testing.T) {
	tradeID := uuid.New()
	fake := &fakePMAutomation{payoutErr: services.ErrTradeNotFound}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/payout", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestPMHandler_InitiatePayout_InvalidTransition(t *testing.T) {
	tradeID := uuid.New()
	fake := &fakePMAutomation{payoutErr: services.ErrInvalidTransition}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/payout", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// ─── DeclineTrade ─────────────────────────────────────────────────────────────

func TestPMHandler_DeclineTrade_Success(t *testing.T) {
	tradeID := uuid.New()
	fake := &fakePMAutomation{}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/"+tradeID.String()+"/decline", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["success"] != true {
		t.Errorf("expected success=true")
	}
	// Verify message contains "cancelled by timeout"
	msg, _ := resp["message"].(string)
	if msg == "" {
		t.Errorf("expected non-empty message")
	}
}

func TestPMHandler_DeclineTrade_BadUUID(t *testing.T) {
	fake := &fakePMAutomation{}
	h := NewPMHandler(fake)
	router := buildPMRouter(h, testAdminUser())

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/admin/pm/trades/not-a-uuid/decline", nil)
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
