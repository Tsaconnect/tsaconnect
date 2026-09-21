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

// ---------- helpers for the three service types ----------

func createTypedServiceCategory(t *testing.T, db *gorm.DB, serviceType string) *models.Category {
	t.Helper()
	cat := &models.Category{
		ID:          uuid.New(),
		Title:       "Cat " + uuid.New().String()[:8],
		Type:        models.CategoryTypeService,
		ServiceType: serviceType,
		IsActive:    true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(cat).Error; err != nil {
		t.Fatalf("failed to create category: %v", err)
	}
	return cat
}

func addMenuItem(t *testing.T, db *gorm.DB, productID uuid.UUID, name string, price float64, available bool) *models.ServiceMenuItem {
	t.Helper()
	it := &models.ServiceMenuItem{
		ID:          uuid.New(),
		ProductID:   productID,
		Name:        name,
		PriceUSD:    price,
		IsAvailable: available,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(it).Error; err != nil {
		t.Fatalf("failed to create menu item: %v", err)
	}
	// Force the explicit availability (the default:true tag flips a false on Create).
	if err := db.Model(it).Update("is_available", available).Error; err != nil {
		t.Fatalf("failed to set availability: %v", err)
	}
	return it
}

func saveProfile(t *testing.T, db *gorm.DB, p *models.ServiceProfile) {
	t.Helper()
	p.ID = uuid.New()
	p.CreatedAt = time.Now()
	p.UpdatedAt = time.Now()
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}
}

func addAvailability(t *testing.T, db *gorm.DB, productID uuid.UUID, dow int, start, end string) {
	t.Helper()
	row := &models.ServiceAvailability{
		ID: uuid.New(), ProductID: productID, DayOfWeek: dow,
		StartTime: start, EndTime: end, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("failed to create availability: %v", err)
	}
}

// postCheckout posts a checkout with arbitrary extra body fields (menuSelections, bookingSlots, serviceTiers).
func postCheckout(t *testing.T, ch *CheckoutHandler, buyer *models.User, extra map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	payload := map[string]interface{}{
		"token": "USDC", "buyerCity": "Lagos", "buyerState": "Lagos", "buyerCountry": "Nigeria",
	}
	for k, v := range extra {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ch.CreateOrderFromCart(c)
	return w
}

// ---------- Order type ----------

func TestOrderService_MenuTotalRecomputedFromDB(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	svc.ShippingSameCity = 4 // delivery fee same_city
	db.Save(svc)
	item1 := addMenuItem(t, db, svc.ID, "Jollof", 10, true)
	item2 := addMenuItem(t, db, svc.ID, "Drink", 2.5, true)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, Price: 0, AddedAt: time.Now()}})

	// Buyer sends a bogus client price which must be ignored — server recomputes.
	w := postCheckout(t, ch, buyer, map[string]interface{}{
		"menuSelections": map[string]interface{}{
			svc.ID.String(): []map[string]interface{}{
				{"itemId": item1.ID.String(), "quantity": 2}, // 20
				{"itemId": item2.ID.String(), "quantity": 1}, // 2.5
			},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	// 22.5 USDC in ProductAmount, 4 USDC delivery in ShippingAmount (6 decimals).
	if got := listingPaid(t, order["productAmount"], order["platformFee"]); got != "22500000" {
		t.Errorf("expected productAmount 22500000, got %s (productAmount + platformFee)", got)
	}
	if order["shippingAmount"] != "4000000" {
		t.Errorf("expected shippingAmount 4000000, got %v", order["shippingAmount"])
	}
}

func TestOrderService_NotContactGated(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	item := addMenuItem(t, db, svc.ID, "Cake", 8, true)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})

	// No contact payment — order services must NOT be contact-gated (F1).
	w := postCheckout(t, ch, buyer, map[string]interface{}{
		"menuSelections": map[string]interface{}{
			svc.ID.String(): []map[string]interface{}{{"itemId": item.ID.String(), "quantity": 1}},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 (no gate for order), got %d: %s", w.Code, w.Body.String())
	}
}

func TestOrderService_RejectsUnavailableItem(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	item := addMenuItem(t, db, svc.ID, "Sold out", 5, false) // not available

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})

	w := postCheckout(t, ch, buyer, map[string]interface{}{
		"menuSelections": map[string]interface{}{
			svc.ID.String(): []map[string]interface{}{{"itemId": item.ID.String(), "quantity": 1}},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unavailable item, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOrderService_RejectsCrossProductItem(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	// An item belonging to a DIFFERENT service (another merchant's product).
	otherSvc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	foreignItem := addMenuItem(t, db, otherSvc.ID, "Foreign", 100, true)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})

	w := postCheckout(t, ch, buyer, map[string]interface{}{
		"menuSelections": map[string]interface{}{
			svc.ID.String(): []map[string]interface{}{{"itemId": foreignItem.ID.String(), "quantity": 1}},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for cross-product item, got %d: %s", w.Code, w.Body.String())
	}
}

func TestOrderService_ResidentialDeliveryPriceable(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	svc.ShippingResidential = 2 // residential is PRICEABLE for order (F6/DV3)
	db.Save(svc)
	item := addMenuItem(t, db, svc.ID, "Meal", 10, true)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})

	w := postCheckout(t, ch, buyer, map[string]interface{}{
		"serviceTiers": map[string]string{svc.ID.String(): models.BookingTierResidential},
		"menuSelections": map[string]interface{}{
			svc.ID.String(): []map[string]interface{}{{"itemId": item.ID.String(), "quantity": 1}},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if order["shippingZone"] != models.BookingTierResidential {
		t.Errorf("expected residential zone, got %v", order["shippingZone"])
	}
	if order["shippingAmount"] != "2000000" {
		t.Errorf("expected residential delivery 2000000, got %v", order["shippingAmount"])
	}
}

// ---------- Booking type (slot-based) ----------

// nextWeekdaySlot returns a slotStart for the given weekday at hour:00 in the future.
func nextWeekdaySlot(hour int) time.Time {
	base := time.Date(2030, 1, 7, hour, 0, 0, 0, time.UTC) // 2030-01-07 is a Monday
	return base
}

func setupBookingService(t *testing.T, db *gorm.DB, sellerID uuid.UUID, capacity int) *models.Product {
	t.Helper()
	cat := createTypedServiceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, sellerID, &cat.ID, 0, 0)
	svc.ShippingSameCity = 15 // booking fee for same_city
	db.Save(svc)
	saveProfile(t, db, &models.ServiceProfile{
		ProductID: svc.ID, SlotDurationMinutes: 60, MaxCapacityPerSlot: capacity,
	})
	slot := nextWeekdaySlot(10)
	addAvailability(t, db, svc.ID, int(slot.Weekday()), "09:00", "17:00")
	return svc
}

func TestBookingService_ReservesSlotAndCharges(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	svc := setupBookingService(t, db, seller.ID, 2)
	slot := nextWeekdaySlot(10)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})

	w := postCheckout(t, ch, buyer, map[string]interface{}{
		"bookingSlots": map[string]interface{}{
			svc.ID.String(): map[string]interface{}{"slotStart": slot.Format(time.RFC3339), "partySize": 1},
		},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if got := listingPaid(t, order["productAmount"], order["platformFee"]); got != "15000000" {
		t.Errorf("expected booking fee 15000000, got %s (productAmount + platformFee)", got)
	}
	// Exactly one booking row created (F9).
	var count int64
	db.Model(&models.ServiceBooking{}).Where("product_id = ?", svc.ID).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 service booking, got %d", count)
	}
}

func TestBookingService_CapacityEnforced(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	svc := setupBookingService(t, db, seller.ID, 1) // capacity 1
	slot := nextWeekdaySlot(10)

	// First buyer takes the only seat.
	b1 := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	createTestCart(t, db, b1.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})
	w1 := postCheckout(t, ch, b1, map[string]interface{}{
		"bookingSlots": map[string]interface{}{svc.ID.String(): map[string]interface{}{"slotStart": slot.Format(time.RFC3339), "partySize": 1}},
	})
	if w1.Code != http.StatusCreated {
		t.Fatalf("first booking should succeed, got %d: %s", w1.Code, w1.Body.String())
	}

	// Second buyer, same slot → full.
	b2 := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	createTestCart(t, db, b2.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})
	w2 := postCheckout(t, ch, b2, map[string]interface{}{
		"bookingSlots": map[string]interface{}{svc.ID.String(): map[string]interface{}{"slotStart": slot.Format(time.RFC3339), "partySize": 1}},
	})
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("second booking should be rejected (full), got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestBookingService_CancelledOrderFreesCapacity(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	svc := setupBookingService(t, db, seller.ID, 1)
	slot := nextWeekdaySlot(10)

	b1 := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	createTestCart(t, db, b1.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})
	w1 := postCheckout(t, ch, b1, map[string]interface{}{
		"bookingSlots": map[string]interface{}{svc.ID.String(): map[string]interface{}{"slotStart": slot.Format(time.RFC3339), "partySize": 1}},
	})
	if w1.Code != http.StatusCreated {
		t.Fatalf("first booking should succeed, got %d", w1.Code)
	}
	o1 := firstOrder(t, w1)
	// Cancel that order → capacity should free (JOIN excludes cancelled, F4).
	db.Model(&models.Order{}).Where("id = ?", o1["id"]).Update("status", models.OrderStatusCancelled)

	b2 := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	createTestCart(t, db, b2.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})
	w2 := postCheckout(t, ch, b2, map[string]interface{}{
		"bookingSlots": map[string]interface{}{svc.ID.String(): map[string]interface{}{"slotStart": slot.Format(time.RFC3339), "partySize": 1}},
	})
	if w2.Code != http.StatusCreated {
		t.Fatalf("booking after cancel should succeed, got %d: %s", w2.Code, w2.Body.String())
	}
}

func TestBookingService_RejectsSlotOutsideAvailability(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	svc := setupBookingService(t, db, seller.ID, 5) // available 09:00-17:00
	badSlot := nextWeekdaySlot(20)                  // 20:00 — outside window

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})
	w := postCheckout(t, ch, buyer, map[string]interface{}{
		"bookingSlots": map[string]interface{}{svc.ID.String(): map[string]interface{}{"slotStart": badSlot.Format(time.RFC3339), "partySize": 1}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for slot outside availability, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------- Contact type ----------

func TestContactService_AccessFeeCharged(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeContact)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	saveProfile(t, db, &models.ServiceProfile{ProductID: svc.ID, AccessFeeUSD: 12, CalloutFeeUSD: 3})
	payContactFee(t, db, buyer.ID, svc.ID) // contact still gated

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})

	w := postCheckout(t, ch, buyer, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if got := listingPaid(t, order["productAmount"], order["platformFee"]); got != "12000000" {
		t.Errorf("expected access fee 12000000 in productAmount, got %s (productAmount + platformFee)", got)
	}
	if order["shippingAmount"] != "3000000" {
		t.Errorf("expected call-out 3000000 in shippingAmount, got %v", order["shippingAmount"])
	}
}

func TestContactService_StillGated(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeContact)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	saveProfile(t, db, &models.ServiceProfile{ProductID: svc.ID, AccessFeeUSD: 12})

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})

	// No contact payment → gated.
	w := postCheckout(t, ch, buyer, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (contact gate), got %d: %s", w.Code, w.Body.String())
	}
}

// ---------- menu handler CRUD + ownership ----------

func callMenuCreate(t *testing.T, h *Handlers, user *models.User, productID uuid.UUID, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Params = gin.Params{{Key: "id", Value: productID.String()}}
	b, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	h.CreateServiceMenuItem(c)
	return w
}

func TestMenuHandler_CreatePersistsUnavailable(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	owner := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, owner.ID, &cat.ID, 0, 0)

	f := false
	w := callMenuCreate(t, h, owner, svc.ID, map[string]interface{}{"name": "Off menu", "priceUsd": 5, "isAvailable": f})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var item models.ServiceMenuItem
	db.Where("product_id = ?", svc.ID).First(&item)
	if item.IsAvailable {
		t.Errorf("expected IsAvailable=false persisted, got true (default:true override bug)")
	}
}

func TestMenuHandler_RejectsNonOwner(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}
	owner := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	attacker := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, owner.ID, &cat.ID, 0, 0)

	w := callMenuCreate(t, h, attacker, svc.ID, map[string]interface{}{"name": "Sneaky", "priceUsd": 1})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-owner, got %d: %s", w.Code, w.Body.String())
	}
}

// ---------- cart-less single-service checkout (CreateServiceOrder) ----------

func postServiceOrder(t *testing.T, ch *CheckoutHandler, buyer *models.User, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	base := map[string]interface{}{"token": "USDC", "buyerCity": "Lagos", "buyerState": "Lagos", "buyerCountry": "Nigeria"}
	for k, v := range body {
		base[k] = v
	}
	b, _ := json.Marshal(base)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	ch.CreateServiceOrder(c)
	return w
}

func TestCreateServiceOrder_Order_NoCartNeeded(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	svc.ShippingSameCity = 4
	db.Save(svc)
	item := addMenuItem(t, db, svc.ID, "Combo", 15, true)

	// No cart created — the endpoint must work without one.
	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId":      svc.ID.String(),
		"menuSelections": []map[string]interface{}{{"itemId": item.ID.String(), "quantity": 2}},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if got := listingPaid(t, order["productAmount"], order["platformFee"]); got != "30000000" { // 15*2
		t.Errorf("expected productAmount 30000000, got %s (productAmount + platformFee)", got)
	}
	if order["shippingAmount"] != "4000000" {
		t.Errorf("expected delivery 4000000, got %v", order["shippingAmount"])
	}
}

func TestCreateServiceOrder_Booking_ReservesSlot(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	svc := setupBookingService(t, db, seller.ID, 2)
	slot := nextWeekdaySlot(10)

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId":   svc.ID.String(),
		"bookingSlot": map[string]interface{}{"slotStart": slot.Format(time.RFC3339), "partySize": 1},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.ServiceBooking{}).Where("product_id = ?", svc.ID).Count(&count)
	if count != 1 {
		t.Errorf("expected 1 booking, got %d", count)
	}
}

func TestCreateServiceOrder_Booking_RejectsPastSlot(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	svc := setupBookingService(t, db, seller.ID, 5)
	// A slot on the right weekday but in the past (2020).
	past := time.Date(2020, 1, 6, 10, 0, 0, 0, time.UTC) // 2020-01-06 is a Monday
	addAvailability(t, db, svc.ID, int(past.Weekday()), "09:00", "17:00")

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId":   svc.ID.String(),
		"bookingSlot": map[string]interface{}{"slotStart": past.Format(time.RFC3339), "partySize": 1},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for past slot, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateServiceOrder_Booking_RejectsUnalignedSlot(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	svc := setupBookingService(t, db, seller.ID, 1) // 60-min slots, window 09:00-17:00
	// 10:30 is inside the window but off the 60-min grid from 09:00 → must reject
	// (else capacity could be bypassed with offset starts).
	unaligned := nextWeekdaySlot(10).Add(30 * time.Minute)

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId":   svc.ID.String(),
		"bookingSlot": map[string]interface{}{"slotStart": unaligned.Format(time.RFC3339), "partySize": 1},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unaligned slot, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateServiceOrder_ContactStillGated(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeContact)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	saveProfile(t, db, &models.ServiceProfile{ProductID: svc.ID, AccessFeeUSD: 10})

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String()})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (contact gate), got %d: %s", w.Code, w.Body.String())
	}
}

// ---------- product order carries selected specs + purchase note ----------

func TestCreateOrderFromCart_Product_CarriesSpecsAndNote(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")

	product := &models.Product{
		ID: uuid.New(), UserID: seller.ID, Name: "Sneakers", Price: 20, Stock: 10,
		Status: "active", Type: models.ProductTypeProduct, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := db.Create(product).Error; err != nil {
		t.Fatalf("create product: %v", err)
	}

	createTestCart(t, db, buyer.ID, []models.CartItem{{
		ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 20,
		SelectedAttributes: []models.CartItemAttribute{
			{Name: "Color", Value: "White, Blue"},
			{Name: "Size", Value: "42"},
		},
		Notes:   "Please pack the white pair on top",
		AddedAt: time.Now(),
	}})

	w := postCheckout(t, ch, buyer, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var order models.Order
	if err := db.Where("buyer_id = ?", buyer.ID).First(&order).Error; err != nil {
		t.Fatalf("load order: %v", err)
	}
	if order.PurchaseNote != "Please pack the white pair on top" {
		t.Errorf("expected purchase note carried onto order, got %q", order.PurchaseNote)
	}
	attrs := order.GetSelectedAttributes()
	if len(attrs) != 2 {
		t.Fatalf("expected 2 selected attributes, got %d", len(attrs))
	}
	if attrs[0].Name != "Color" || attrs[0].Value != "White, Blue" {
		t.Errorf("unexpected first attribute: %+v", attrs[0])
	}
}
