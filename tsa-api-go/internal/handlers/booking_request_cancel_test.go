package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// `cancelled` and `expired` were declared as valid transitions but nothing ever
// set them: a buyer could not withdraw a request, an ignored request sat in
// `requested` forever, and an approved quote never went stale — so a buyer could
// pay a months-old price. These cover the two mechanisms that close that off.

// seedRequest writes a booking request at a given status and age.
func seedRequest(t *testing.T, db *gorm.DB, productID, buyerID, sellerID uuid.UUID, status string, age time.Duration) *models.BookingRequest {
	t.Helper()
	at := time.Now().Add(-age)
	r := &models.BookingRequest{
		ID:        uuid.New(),
		ProductID: productID,
		BuyerID:   buyerID,
		SellerID:  sellerID,
		Token:     "USDC",
		Status:    status,
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := db.Create(r).Error; err != nil {
		t.Fatalf("failed to seed request: %v", err)
	}
	return r
}

func reloadRequest(t *testing.T, db *gorm.DB, id uuid.UUID) models.BookingRequest {
	t.Helper()
	var r models.BookingRequest
	if err := db.First(&r, "id = ?", id).Error; err != nil {
		t.Fatalf("failed to reload request: %v", err)
	}
	return r
}

// Either party can withdraw: the buyer changing their mind, or the provider
// backing out of a booking they already approved (reject only covers the
// not-yet-approved case).
func TestCancelBookingRequest_EitherParty(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	// Buyer withdraws a request the provider has not answered.
	r1 := seedRequest(t, db, svc.ID, buyer.ID, seller.ID, models.BookingRequestRequested, 0)
	p1 := gin.Params{{Key: "reqId", Value: r1.ID.String()}}
	if w := postJSON(t, buyer, p1, nil, ch.CancelBookingRequest); w.Code != http.StatusOK {
		t.Fatalf("buyer cancel: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadRequest(t, db, r1.ID); got.Status != models.BookingRequestCancelled {
		t.Errorf("buyer cancel: want status cancelled, got %q", got.Status)
	}

	// Provider backs out of one they already approved.
	r2 := seedRequest(t, db, svc.ID, buyer.ID, seller.ID, models.BookingRequestApproved, 0)
	p2 := gin.Params{{Key: "reqId", Value: r2.ID.String()}}
	if w := postJSON(t, seller, p2, map[string]interface{}{"reason": "fully booked"}, ch.CancelBookingRequest); w.Code != http.StatusOK {
		t.Fatalf("seller cancel: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	got := reloadRequest(t, db, r2.ID)
	if got.Status != models.BookingRequestCancelled {
		t.Errorf("seller cancel: want status cancelled, got %q", got.Status)
	}
	if got.RejectReason != "fully booked" {
		t.Errorf("want the reason recorded, got %q", got.RejectReason)
	}
}

// A stranger to the request cannot touch it.
func TestCancelBookingRequest_RejectsOutsider(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	outsider := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	r := seedRequest(t, db, svc.ID, buyer.ID, seller.ID, models.BookingRequestRequested, 0)
	p := gin.Params{{Key: "reqId", Value: r.ID.String()}}
	if w := postJSON(t, outsider, p, nil, ch.CancelBookingRequest); w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an unrelated user, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadRequest(t, db, r.ID); got.Status != models.BookingRequestRequested {
		t.Errorf("status must be untouched, got %q", got.Status)
	}
}

// The reason is free text from either party and lands in an unbounded TEXT
// column, so it is capped the way booking-form answers already are.
func TestCancelBookingRequest_BoundsReason(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	r := seedRequest(t, db, svc.ID, buyer.ID, seller.ID, models.BookingRequestRequested, 0)
	p := gin.Params{{Key: "reqId", Value: r.ID.String()}}
	huge := strings.Repeat("x", maxBookingReasonLen+1)
	if w := postJSON(t, buyer, p, map[string]interface{}{"reason": huge}, ch.CancelBookingRequest); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an oversized reason, got %d: %s", w.Code, w.Body.String())
	}
	if got := reloadRequest(t, db, r.ID); got.Status != models.BookingRequestRequested {
		t.Errorf("status must be untouched after a rejected reason, got %q", got.Status)
	}
}

// A paid request is already an escrowed order; cancelling it here would
// desynchronise the two, so the transition is refused.
func TestCancelBookingRequest_RejectsConverted(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	r := seedRequest(t, db, svc.ID, buyer.ID, seller.ID, models.BookingRequestConverted, 0)
	p := gin.Params{{Key: "reqId", Value: r.ID.String()}}
	if w := postJSON(t, buyer, p, nil, ch.CancelBookingRequest); w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a converted request, got %d: %s", w.Code, w.Body.String())
	}
}
