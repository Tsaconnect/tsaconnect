package handlers

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// Buying your own listing is refused for every pay token, at every door:
// add-to-cart, cart checkout, the single-service order and pay-spendable.
func TestCheckout_RefusesBuyingYourOwnListing(t *testing.T) {
	r := newSpendableRig(t)
	seller := r.seller

	t.Run("add to cart", func(t *testing.T) {
		h := &Handlers{}
		w, _ := callOrderHandler(t, h.AddToCart, seller, map[string]interface{}{"productId": r.product.ID.String(), "quantity": 1})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "your own listing") {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("cart checkout", func(t *testing.T) {
		createTestCart(t, r.db, seller.ID, []models.CartItem{
			{ID: uuid.New(), Product: r.product.ID, Seller: seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
		})
		for _, token := range []string{"USDC", "USDT", "MCGP"} {
			r.ch.otcService = fakeMCGPPrice{usd6: 250000}
			w, _ := callOrderHandler(t, r.ch.CreateOrderFromCart, seller, withToken(token, nil))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "your own listing") {
				t.Fatalf("%s: got %d %s", token, w.Code, w.Body.String())
			}
		}
		var n int64
		config.DB.Model(&models.Order{}).Where("buyer_id = ?", seller.ID).Count(&n)
		if n != 0 {
			t.Fatalf("no order may be created with yourself, got %d", n)
		}
	})

	t.Run("service order", func(t *testing.T) {
		cat := serviceCategory(t, r.db, models.ServiceTypeBooking)
		svc := createTestService(t, r.db, seller.ID, &cat.ID, 5, 0)
		w := postJSON(t, seller, nil, map[string]interface{}{"token": "USDC", "productId": svc.ID.String()}, r.ch.CreateServiceOrder)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "your own service") {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("pay-spendable", func(t *testing.T) {
		o := createTestOrder(t, r.db, seller.ID, seller.ID, r.product.ID, models.OrderStatusPendingPayment, "MCGP")
		w, resp := call(t, r.ch.PaySpendable, seller, o.ID, nil)
		if w.Code != http.StatusBadRequest || resp["code"] != "own_listing" {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
		if len(r.payer.funded) != 0 {
			t.Fatal("nothing may be funded for an order with yourself")
		}
	})
}

func TestPaySpendable_MapsAbuseBounds(t *testing.T) {
	cases := map[error]struct {
		code   string
		status int
	}{
		services.ErrOrderMcgpTooManyOpen:    {"spendable_open_limit", http.StatusTooManyRequests},
		services.ErrOrderMcgpTooManyRefunds: {"spendable_refund_limit", http.StatusTooManyRequests},
		services.ErrOrderMcgpOwnOrder:       {"own_listing", http.StatusBadRequest},
		services.ErrSpendableOverWindow:     {"spendable_window", http.StatusServiceUnavailable},
	}
	for err, want := range cases {
		r := newSpendableRig(t)
		r.payer.fundErr = err
		o := r.order(t, models.OrderStatusPendingPayment, "")
		w, resp := call(t, r.ch.PaySpendable, r.buyer, o.ID, nil)
		if w.Code != want.status || resp["code"] != want.code {
			t.Errorf("%v: got %d %v, want %d %s", err, w.Code, resp["code"], want.status, want.code)
		}
	}
}

// The per-order cap check runs inside the order's DB transaction, so the caps
// are warmed before it opens and only the cache is read inside it.
func TestCreateOrderFromCart_MCGPWarmsCapsBeforeTheTransaction(t *testing.T) {
	r := newSpendableRig(t)
	r.ch.otcService = fakeMCGPPrice{usd6: 250000}
	createTestCart(t, r.db, r.buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: r.product.ID, Seller: r.seller.ID, Quantity: 1, Price: 10.0, AddedAt: time.Now()},
	})
	w, _ := callOrderHandler(t, r.ch.CreateOrderFromCart, r.buyer, withToken("MCGP", nil))
	if w.Code != http.StatusCreated {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	if r.payer.warmed == 0 {
		t.Fatal("the caps must be warmed off the transaction")
	}
}
