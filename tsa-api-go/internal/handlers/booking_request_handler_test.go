package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

func writeBookingProfile(t *testing.T, db *gorm.DB, productID uuid.UUID, bookingType, pricingMethod string) {
	t.Helper()
	profile := models.ServiceProfile{
		ID:            uuid.New(),
		ProductID:     productID,
		BookingType:   bookingType,
		PricingMethod: pricingMethod,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}
}

func postJSON(t *testing.T, user *models.User, params gin.Params, body map[string]interface{}, fn func(*gin.Context)) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Params = params
	b, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	fn(c)
	return w
}

// The request → approve → pay lifecycle: a non-instant booking creates an
// off-chain request; the buyer can't pay until the merchant approves; paying an
// approved request creates the escrowed order and converts the request.
func TestBookingRequestLifecycle(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5) // same-city tier fee 5 → priceable
	writeBookingProfile(t, db, svc.ID, models.BookingTypeRequest, models.PricingMethodFixed)

	idParams := gin.Params{{Key: "id", Value: svc.ID.String()}}

	// 1. Buyer submits a request.
	w := postJSON(t, buyer, idParams, map[string]interface{}{"token": "USDC"}, ch.CreateBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("create request: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var reqRow models.BookingRequest
	if err := db.Where("buyer_id = ?", buyer.ID).First(&reqRow).Error; err != nil {
		t.Fatalf("request not persisted: %v", err)
	}
	if reqRow.Status != models.BookingRequestRequested {
		t.Fatalf("expected requested, got %s", reqRow.Status)
	}

	reqParams := gin.Params{{Key: "reqId", Value: reqRow.ID.String()}}

	// 2. Buyer cannot pay before approval.
	if w := postJSON(t, buyer, reqParams, nil, ch.PayBookingRequest); w.Code != http.StatusConflict {
		t.Fatalf("pay-before-approve: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// 3. A different user cannot approve (not the seller).
	if w := postJSON(t, buyer, reqParams, nil, ch.ApproveBookingRequest); w.Code != http.StatusForbidden {
		t.Fatalf("approve-by-non-seller: expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Seller approves.
	if w := postJSON(t, seller, reqParams, nil, ch.ApproveBookingRequest); w.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Buyer pays → order created, request converted.
	w = postJSON(t, buyer, reqParams, nil, ch.PayBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("pay: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var converted models.BookingRequest
	db.First(&converted, "id = ?", reqRow.ID)
	if converted.Status != models.BookingRequestConverted || converted.OrderID == nil {
		t.Fatalf("expected converted with orderId, got status=%s orderId=%v", converted.Status, converted.OrderID)
	}
	var order models.Order
	if err := db.First(&order, "id = ?", *converted.OrderID).Error; err != nil {
		t.Fatalf("converted order not found: %v", err)
	}
	if order.Kind != models.OrderKindService || order.Status != models.OrderStatusPendingPayment {
		t.Errorf("unexpected order: kind=%s status=%s", order.Kind, order.Status)
	}

	// 6. Paying an already-converted request is rejected (idempotency guard
	// against a double-pay creating a duplicate order).
	if w := postJSON(t, buyer, reqParams, nil, ch.PayBookingRequest); w.Code != http.StatusConflict {
		t.Fatalf("second pay: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var orderCount int64
	db.Model(&models.Order{}).Where("buyer_id = ?", buyer.ID).Count(&orderCount)
	if orderCount != 1 {
		t.Fatalf("expected exactly 1 order after double-pay, got %d", orderCount)
	}
}

// A slotted booking cannot be set to request/approval mode (the pay step can't
// reserve a slot, which would strand the buyer).
func TestUpsertServiceProfile_RejectsSlottedRequestMode(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: svc.ID.String()}}
	body, _ := json.Marshal(map[string]interface{}{
		"bookingType":         models.BookingTypeRequest,
		"slotDurationMinutes": 60,
	})
	c.Request, _ = http.NewRequest("PUT", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpsertServiceProfile(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for slotted request-mode, got %d: %s", w.Code, w.Body.String())
	}
}

// A rejected request cannot be paid.
func TestBookingRequestReject(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writeBookingProfile(t, db, svc.ID, models.BookingTypeRequest, models.PricingMethodFixed)

	w := postJSON(t, buyer, gin.Params{{Key: "id", Value: svc.ID.String()}}, map[string]interface{}{"token": "USDC"}, ch.CreateBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var reqRow models.BookingRequest
	db.Where("buyer_id = ?", buyer.ID).First(&reqRow)
	reqParams := gin.Params{{Key: "reqId", Value: reqRow.ID.String()}}

	if w := postJSON(t, seller, reqParams, map[string]interface{}{"reason": "Fully booked"}, ch.RejectBookingRequest); w.Code != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := postJSON(t, buyer, reqParams, nil, ch.PayBookingRequest); w.Code != http.StatusConflict {
		t.Fatalf("pay-after-reject: expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// An instant booking service rejects the request path (buyer should check out
// directly instead).
func TestCreateBookingRequest_RejectsInstant(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writeBookingProfile(t, db, svc.ID, models.BookingTypeInstant, models.PricingMethodFixed)

	w := postJSON(t, buyer, gin.Params{{Key: "id", Value: svc.ID.String()}}, map[string]interface{}{"token": "USDC"}, ch.CreateBookingRequest)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for instant service, got %d: %s", w.Code, w.Body.String())
	}
}

// instant + on_request is an invalid combination (can't prepay an unnamed price).
func TestUpsertServiceProfile_RejectsInstantOnRequest(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: svc.ID.String()}}
	body, _ := json.Marshal(map[string]interface{}{
		"bookingType":   models.BookingTypeInstant,
		"pricingMethod": models.PricingMethodOnRequest,
	})
	c.Request, _ = http.NewRequest("PUT", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpsertServiceProfile(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for instant+on_request, got %d: %s", w.Code, w.Body.String())
	}
}

// instant + on_request must also be rejected when it only arises after
// merging a partial update onto an existing profile (preserve-existing
// semantics on BookingType/PricingMethod means a body carrying only
// bookingType can silently combine with a stored on_request pricing method).
func TestUpsertServiceProfile_RejectsInstantOnRequest_PartialUpdate(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writeBookingProfile(t, db, svc.ID, models.BookingTypeRequest, models.PricingMethodOnRequest)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: svc.ID.String()}}
	body, _ := json.Marshal(map[string]interface{}{
		"bookingType": models.BookingTypeInstant,
	})
	c.Request, _ = http.NewRequest("PUT", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpsertServiceProfile(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for partial update merging into instant+on_request, got %d: %s", w.Code, w.Body.String())
	}
}

// A partial update that adds a slot duration to an existing request-mode listing
// must be rejected on the merged values — a request booking can't reserve a slot,
// so persisting request+slot would strand the buyer at the (slot-less) pay step.
func TestUpsertServiceProfile_RejectsRequestPlusSlot_PartialUpdate(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writeBookingProfile(t, db, svc.ID, models.BookingTypeRequest, models.PricingMethodFixed)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", seller)
	c.Params = gin.Params{{Key: "id", Value: svc.ID.String()}}
	body, _ := json.Marshal(map[string]interface{}{
		"slotDurationMinutes": 60,
	})
	c.Request, _ = http.NewRequest("PUT", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpsertServiceProfile(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for partial update merging into request+slot, got %d: %s", w.Code, w.Body.String())
	}
}

// A price-on-request booking: approval must carry a price, which becomes the
// escrowed amount the buyer pays.
// TestCreateServiceOrder_RejectsRequestBooking guards the money-path bypass:
// a request-type booking must go through CreateBookingRequest → approval before
// payment, so the instant single-service checkout must refuse it. Otherwise a
// buyer escrows the base fare and skips approval (and, for on_request, skips the
// provider naming the price).
func TestCreateServiceOrder_RejectsRequestBooking(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)

	// A request + fixed booking listing → instant buy must be rejected.
	reqSvc := createTestService(t, db, seller.ID, &cat.ID, 5, 0)
	writeBookingProfile(t, db, reqSvc.ID, models.BookingTypeRequest, models.PricingMethodFixed)
	if w := postJSON(t, buyer, nil, map[string]interface{}{
		"token": "USDC", "productId": reqSvc.ID.String(),
	}, ch.CreateServiceOrder); w.Code != http.StatusBadRequest {
		t.Fatalf("instant buy of request booking: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// A plain instant booking (with a payable access fee) still buys directly —
	// the guard must not block the legitimate instant path.
	instSvc := createTestService(t, db, seller.ID, &cat.ID, 5, 0)
	if err := db.Create(&models.ServiceProfile{
		ID: uuid.New(), ProductID: instSvc.ID,
		BookingType: models.BookingTypeInstant, PricingMethod: models.PricingMethodFixed,
		AccessFeeUSD: 5, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("create instant profile: %v", err)
	}
	if w := postJSON(t, buyer, nil, map[string]interface{}{
		"token": "USDC", "productId": instSvc.ID.String(),
	}, ch.CreateServiceOrder); w.Code != http.StatusCreated {
		t.Fatalf("instant buy of instant booking: expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestBookingRequestOnRequestQuote(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	writeBookingProfile(t, db, svc.ID, models.BookingTypeRequest, models.PricingMethodOnRequest)

	w := postJSON(t, buyer, gin.Params{{Key: "id", Value: svc.ID.String()}}, map[string]interface{}{"token": "USDC"}, ch.CreateBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var reqRow models.BookingRequest
	db.Where("buyer_id = ?", buyer.ID).First(&reqRow)
	reqParams := gin.Params{{Key: "reqId", Value: reqRow.ID.String()}}

	// Approve without a price → 400.
	if w := postJSON(t, seller, reqParams, nil, ch.ApproveBookingRequest); w.Code != http.StatusBadRequest {
		t.Fatalf("approve without price: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Approve with a price → 200, stored.
	if w := postJSON(t, seller, reqParams, map[string]interface{}{"amountUsd": 42.0}, ch.ApproveBookingRequest); w.Code != http.StatusOK {
		t.Fatalf("approve with price: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	db.First(&reqRow, "id = ?", reqRow.ID)
	if reqRow.QuotedAmountUSD == nil || *reqRow.QuotedAmountUSD != 42.0 {
		t.Fatalf("expected quoted 42, got %v", reqRow.QuotedAmountUSD)
	}

	// Pay → order escrows the quoted price (42 USDC → 42000000 in 6 decimals).
	w = postJSON(t, buyer, reqParams, nil, ch.PayBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("pay: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var order models.Order
	db.Where("buyer_id = ?", buyer.ID).First(&order)
	if got := listingPaid(t, order.ProductAmount, order.PlatformFee); got != "42000000" {
		t.Errorf("expected the quoted 42000000 (productAmount + platformFee), got %s", got)
	}
}
