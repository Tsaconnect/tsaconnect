package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Buying needs a verified buyer: both order-creating routes refuse an
// unverified one with the code the app routes to verification, and no order
// is created.
func TestCheckout_RefusesAnUnverifiedBuyer(t *testing.T) {
	r := newSpendableRig(t)
	buyer := createTestUserWithLocation(t, r.db, "Lagos", "Lagos", "NG")
	if err := r.db.Model(&models.User{}).Where("id = ?", buyer.ID).
		Update("verification_status", models.VerificationStatusPending).Error; err != nil {
		t.Fatal(err)
	}
	refused := func(t *testing.T, w interface{ String() string }, code int) {
		t.Helper()
		if code != http.StatusForbidden || !strings.Contains(w.String(), "kyc_verification_required") {
			t.Fatalf("got %d %s", code, w.String())
		}
	}

	t.Run("cart checkout", func(t *testing.T) {
		createTestCart(t, r.db, buyer.ID, []models.CartItem{
			{ID: uuid.New(), Product: r.product.ID, Seller: r.seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
		})
		w, _ := callOrderHandler(t, r.ch.CreateOrderFromCart, buyer, withToken("USDC", nil))
		refused(t, w.Body, w.Code)
	})

	t.Run("service order", func(t *testing.T) {
		cat := serviceCategory(t, r.db, models.ServiceTypeBooking)
		svc := createTestService(t, r.db, r.seller.ID, &cat.ID, 5, 0)
		w := postJSON(t, buyer, nil, map[string]interface{}{"token": "USDC", "productId": svc.ID.String()}, r.ch.CreateServiceOrder)
		refused(t, w.Body, w.Code)
	})

	var n int64
	config.DB.Model(&models.Order{}).Where("buyer_id = ?", buyer.ID).Count(&n)
	if n != 0 {
		t.Fatalf("no order may be created for an unverified buyer, got %d", n)
	}
}
