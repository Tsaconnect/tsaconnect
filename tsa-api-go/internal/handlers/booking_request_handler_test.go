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
