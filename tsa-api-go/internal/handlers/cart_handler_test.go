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

// The note and specs travel from the cart onto the order the seller reads, so
// the API bounds them itself rather than trusting the app's 500-char limit.
func TestAddToCart_BoundsNotesSpecsAndQuantity(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10)
	service := createTestProduct(t, db, seller.ID, 10)
	if err := db.Model(service).Updates(map[string]interface{}{"type": models.ProductTypeService, "stock": 0}).Error; err != nil {
		t.Fatalf("make service: %v", err)
	}

	manyAttrs := make([]map[string]string, maxCartItemAttributes+1)
	for i := range manyAttrs {
		manyAttrs[i] = map[string]string{"name": "k", "value": "v"}
	}
	cases := map[string]map[string]interface{}{
		"long note":        {"productId": product.ID.String(), "quantity": 1, "notes": strings.Repeat("x", maxCartItemNoteRunes+1)},
		"too many specs":   {"productId": product.ID.String(), "quantity": 1, "selectedAttributes": manyAttrs},
		"long spec value":  {"productId": product.ID.String(), "quantity": 1, "selectedAttributes": []map[string]string{{"name": "Colour", "value": strings.Repeat("x", maxCartAttributeValueLen+1)}}},
		"huge service qty": {"productId": service.ID.String(), "quantity": maxCartItemQuantity + 1},
	}
	for name, body := range cases {
		if w, _ := callOrderHandler(t, h.AddToCart, buyer, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d: %s", name, w.Code, w.Body.String())
		}
	}

	// A merge that would push a service line past the cap is refused too,
	// rather than overflowing.
	add := map[string]interface{}{"productId": service.ID.String(), "quantity": maxCartItemQuantity}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, add); w.Code != http.StatusOK {
		t.Fatalf("service add at the cap: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	one := map[string]interface{}{"productId": service.ID.String(), "quantity": 1}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, one); w.Code != http.StatusBadRequest {
		t.Fatalf("merge past the cap: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// Replace makes the line the buyer's current pick, price included.
func TestAddToCart_ReplaceRefreshesThePrice(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10)

	body := map[string]interface{}{"productId": product.ID.String(), "quantity": 1, "replace": true}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, body); w.Code != http.StatusOK {
		t.Fatalf("first add: %d %s", w.Code, w.Body.String())
	}
	if err := db.Model(product).Update("price", 12.5).Error; err != nil {
		t.Fatalf("reprice: %v", err)
	}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, body); w.Code != http.StatusOK {
		t.Fatalf("replace: %d %s", w.Code, w.Body.String())
	}

	var cart models.Cart
	if err := config.DB.Where("user_id = ? AND status = ?", buyer.ID, "active").First(&cart).Error; err != nil {
		t.Fatalf("load cart: %v", err)
	}
	if got := cart.GetItems()[0].Price; got != 12.5 {
		t.Fatalf("line price = %v, want the current 12.5", got)
	}
}

// Replace over stock on an existing line is refused and leaves the line as it
// was; a replace clears specs/note it doesn't send, collapses duplicate lines
// for the product, and never touches other products' lines.
func TestAddToCart_ReplaceOnAnExistingLine(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10)
	other := createTestProduct(t, db, seller.ID, 4)
	if err := db.Model(product).Update("stock", 2).Error; err != nil {
		t.Fatalf("set stock: %v", err)
	}
	// A legacy cart can hold two lines for one product.
	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 10, Notes: "keep me",
			SelectedAttributes: []models.CartItemAttribute{{Name: "Colour", Value: "Blue"}}, AddedAt: time.Now()},
		{ID: uuid.New(), Product: other.ID, Seller: seller.ID, Quantity: 3, Price: 4, AddedAt: time.Now()},
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 10, AddedAt: time.Now()},
	})
	load := func() []models.CartItem {
		var cart models.Cart
		if err := config.DB.Where("user_id = ? AND status = ?", buyer.ID, "active").First(&cart).Error; err != nil {
			t.Fatalf("load cart: %v", err)
		}
		return cart.GetItems()
	}

	over := map[string]interface{}{"productId": product.ID.String(), "quantity": 3, "replace": true}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, over); w.Code != http.StatusBadRequest {
		t.Fatalf("replace over stock: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if items := load(); len(items) != 3 || items[0].Quantity != 1 || items[0].Notes != "keep me" {
		t.Fatalf("refused replace changed the cart: %+v", items)
	}

	bare := map[string]interface{}{"productId": product.ID.String(), "quantity": 2, "replace": true}
	if w, _ := callOrderHandler(t, h.AddToCart, buyer, bare); w.Code != http.StatusOK {
		t.Fatalf("replace: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	items := load()
	if len(items) != 2 {
		t.Fatalf("expected the duplicate line collapsed (2 lines), got %+v", items)
	}
	for _, it := range items {
		switch it.Product {
		case product.ID:
			if it.Quantity != 2 || it.Notes != "" || len(it.SelectedAttributes) != 0 {
				t.Errorf("replaced line = %+v, want qty 2 with no note or specs", it)
			}
		case other.ID:
			if it.Quantity != 3 {
				t.Errorf("other product's line changed: %+v", it)
			}
		}
	}
}
