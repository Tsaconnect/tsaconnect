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

// Services hold no inventory: checkout ignores their stock, so the cart must
// too — on the first add as well as a repeat one.
func TestAddToCart_IgnoresStockForServices(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	service := createTestProduct(t, db, seller.ID, 10)
	if err := db.Model(service).Updates(map[string]interface{}{"type": models.ProductTypeService, "stock": 0}).Error; err != nil {
		t.Fatalf("make service: %v", err)
	}

	add := map[string]interface{}{"productId": service.ID.String(), "quantity": 1}
	for i := 0; i < 2; i++ {
		if w, _ := callOrderHandler(t, h.AddToCart, buyer, add); w.Code != http.StatusOK {
			t.Fatalf("add %d: expected 200, got %d: %s", i+1, w.Code, w.Body.String())
		}
	}
}

// "Buy now" re-sends the buyer's pick every time it is tapped. With replace set,
// the cart line becomes exactly that pick — quantity, specs and note — instead
// of growing by one per tap, and a repeat on a 1-in-stock item is not refused.
func TestAddToCart_ReplaceSetsTheLineInsteadOfAddingToIt(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 0.37)
	if err := db.Model(product).Update("stock", 1).Error; err != nil {
		t.Fatalf("set stock: %v", err)
	}

	first := map[string]interface{}{
		"productId": product.ID.String(), "quantity": 1, "replace": true,
		"selectedAttributes": []map[string]string{{"name": "Colour", "value": "White"}},
		"notes":              "first",
	}
	again := map[string]interface{}{
		"productId": product.ID.String(), "quantity": 1, "replace": true,
		"selectedAttributes": []map[string]string{{"name": "Colour", "value": "Blue"}},
		"notes":              "second",
	}
	for i, body := range []map[string]interface{}{first, again, again} {
		if w, _ := callOrderHandler(t, h.AddToCart, buyer, body); w.Code != http.StatusOK {
			t.Fatalf("add %d: expected 200, got %d: %s", i+1, w.Code, w.Body.String())
		}
	}

	var cart models.Cart
	if err := config.DB.Where("user_id = ? AND status = ?", buyer.ID, "active").First(&cart).Error; err != nil {
		t.Fatalf("load cart: %v", err)
	}
	items := cart.GetItems()
	if len(items) != 1 || items[0].Quantity != 1 {
		t.Fatalf("cart should hold exactly 1 of the item, got %+v", items)
	}
	if items[0].Notes != "second" {
		t.Errorf("notes = %q, want the latest pick", items[0].Notes)
	}
	if len(items[0].SelectedAttributes) != 1 || items[0].SelectedAttributes[0].Value != "Blue" {
		t.Errorf("attributes = %+v, want the latest pick", items[0].SelectedAttributes)
	}
}

// Replace is still bounded by stock — it only stops counting what the line held.
func TestAddToCart_ReplaceRefusesMoreThanStock(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 0.37)
	if err := db.Model(product).Update("stock", 2).Error; err != nil {
		t.Fatalf("set stock: %v", err)
	}

	body := map[string]interface{}{"productId": product.ID.String(), "quantity": 3, "replace": true}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, body); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
