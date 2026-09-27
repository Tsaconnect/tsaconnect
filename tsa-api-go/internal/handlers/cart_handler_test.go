package handlers

import (
	"net/http"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Adding a listing that is already in the cart must be checked against the
// quantity the cart ends up holding, not just the quantity in this request.
// Otherwise tapping "Add to cart" twice on a 1-in-stock item leaves 2 in the
// cart, and every quote and order for that cart is refused for insufficient
// stock.
func TestAddToCart_RefusesToExceedStockAcrossAdds(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 0.37)
	if err := db.Model(product).Update("stock", 1).Error; err != nil {
		t.Fatalf("set stock: %v", err)
	}

	add := map[string]interface{}{"productId": product.ID.String(), "quantity": 1}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, add); w.Code != http.StatusOK {
		t.Fatalf("first add: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, add); w.Code != http.StatusBadRequest {
		t.Fatalf("second add: expected 400, got %d: %s", w.Code, w.Body.String())
	}

	var cart models.Cart
	if err := config.DB.Where("user_id = ? AND status = ?", buyer.ID, "active").First(&cart).Error; err != nil {
		t.Fatalf("load cart: %v", err)
	}
	items := cart.GetItems()
	if len(items) != 1 || items[0].Quantity != 1 {
		t.Fatalf("cart should still hold 1 of the item, got %+v", items)
	}
}
