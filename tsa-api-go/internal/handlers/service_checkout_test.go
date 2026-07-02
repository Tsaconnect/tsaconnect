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

// ---------- helpers ----------

func createTestCategory(t *testing.T, db *gorm.DB, catType, feeMode string) *models.Category {
	t.Helper()
	// Mirror the production backfill: a booking fee-mode category maps to the
	// booking service type; everything else defaults to contact.
	serviceType := models.ServiceTypeContact
	if feeMode == models.ServiceFeeModeBooking {
		serviceType = models.ServiceTypeBooking
	}
	cat := &models.Category{
		ID:             uuid.New(),
		Title:          "Cat " + uuid.New().String()[:8],
		Type:           catType,
		ServiceFeeMode: feeMode,
		ServiceType:    serviceType,
		IsActive:       true,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	if err := db.Create(cat).Error; err != nil {
		t.Fatalf("failed to create category: %v", err)
	}
	return cat
}

func createTestService(t *testing.T, db *gorm.DB, sellerID uuid.UUID, categoryID *uuid.UUID, price, calloutSameCity float64) *models.Product {
	t.Helper()
	svc := &models.Product{
		ID:               uuid.New(),
		UserID:           sellerID,
		Name:             "Test Service " + uuid.New().String()[:8],
		Price:            price,
		Stock:            0, // services hold no inventory
		Status:           "active",
		Type:             models.ProductTypeService,
		CategoryID:       categoryID,
		ShippingSameCity: calloutSameCity,
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := db.Create(svc).Error; err != nil {
		t.Fatalf("failed to create service: %v", err)
	}
	return svc
}

func payContactFee(t *testing.T, db *gorm.DB, callerID, serviceID uuid.UUID) {
	t.Helper()
	row := &models.ServiceContactPayment{
		ID:        uuid.New(),
		CallerID:  callerID,
		ServiceID: serviceID,
		Token:     "USDC",
		FeeAmount: "100000",
		CreatedAt: time.Now(),
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("failed to create contact payment: %v", err)
	}
}

func setBookingFee(t *testing.T, db *gorm.DB, categoryID uuid.UUID, tier string, amount float64) {
	t.Helper()
	row := &models.CategoryBookingFee{
		ID:         uuid.New(),
		CategoryID: categoryID,
		Tier:       tier,
		AmountUSD:  amount,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("failed to create booking fee: %v", err)
	}
}

func postServiceCheckout(t *testing.T, ch *CheckoutHandler, buyer *models.User, serviceTiers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	payload := map[string]interface{}{
		"token":        "USDC",
		"buyerCity":    "Lagos",
		"buyerState":   "Lagos",
		"buyerCountry": "Nigeria",
	}
	if serviceTiers != nil {
		payload["serviceTiers"] = serviceTiers
	}
	body, _ := json.Marshal(payload)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ch.CreateOrderFromCart(c)
	return w
}

func firstOrder(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	data, ok := resp["data"].(map[string]interface{})
	if !ok {
		t.Fatalf("no data in response: %s", w.Body.String())
	}
	orders := data["orders"].([]interface{})
	if len(orders) != 1 {
		t.Fatalf("expected 1 order, got %d: %s", len(orders), w.Body.String())
	}
	return orders[0].(map[string]interface{})
}

// ---------- pure helpers ----------

func TestGetCalloutRate(t *testing.T) {
	svc := &models.Product{
		ShippingSameCity:      3,
		ShippingSameState:     6,
		ShippingSameCountry:   12,
		ShippingInternational: 30,
	}
	cases := []struct {
		tier string
		want float64
	}{
		{models.BookingTierResidential, 0}, // always free
		{ShippingZoneSameCity, 3},
		{ShippingZoneSameState, 6},
		{ShippingZoneSameCountry, 12},
		{ShippingZoneInternational, 30},
	}
	for _, tc := range cases {
		if got := GetCalloutRate(svc, tc.tier); got != tc.want {
			t.Errorf("GetCalloutRate(%s) = %v, want %v", tc.tier, got, tc.want)
		}
	}
}

func TestResolveServiceTier(t *testing.T) {
	cases := []struct {
		name     string
		detected string
		declared string
		booking  bool
		want     string
	}{
		{"residential honored from same_city", ShippingZoneSameCity, models.BookingTierResidential, false, models.BookingTierResidential},
		{"residential ignored from same_state", ShippingZoneSameState, models.BookingTierResidential, false, ShippingZoneSameState},
		{"no declaration keeps detected", ShippingZoneSameState, "", false, ShippingZoneSameState},
		{"lga honored (booking) from same_state", ShippingZoneSameState, models.BookingTierSameLGA, true, models.BookingTierSameLGA},
		{"lga ignored for standard", ShippingZoneSameCity, models.BookingTierSameLGA, false, ShippingZoneSameCity},
		{"international maps to outside_country for booking", ShippingZoneInternational, "", true, models.BookingTierOutsideCountry},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveServiceTier(tc.detected, tc.declared, tc.booking); got != tc.want {
				t.Errorf("resolveServiceTier(%s,%s,%v) = %s, want %s", tc.detected, tc.declared, tc.booking, got, tc.want)
			}
		})
	}
}

func TestValidNextStatusesForKind_Service(t *testing.T) {
	// Service may confirm directly from escrowed; product may not.
	if !contains(models.ValidNextStatusesForKind(models.OrderStatusEscrowed, models.OrderKindService), models.OrderStatusCompleted) {
		t.Error("service escrowed -> completed should be allowed")
	}
	if contains(models.ValidNextStatusesForKind(models.OrderStatusEscrowed, models.OrderKindProduct), models.OrderStatusCompleted) {
		t.Error("product escrowed -> completed should NOT be allowed")
	}
	// Service never enters shipped.
	if contains(models.ValidNextStatusesForKind(models.OrderStatusEscrowed, models.OrderKindService), models.OrderStatusShipped) {
		t.Error("service escrowed -> shipped should NOT be allowed")
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ---------- checkout integration ----------

func TestCreateOrderFromCart_Service_GateRejectsWithoutContactPayment(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTestCategory(t, db, models.CategoryTypeService, models.ServiceFeeModeStandard)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, Price: 20.0, AddedAt: time.Now()}})

	// No contact payment → gate rejects.
	w := postServiceCheckout(t, ch, buyer, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 (gate), got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateOrderFromCart_Service_StandardPriced(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTestCategory(t, db, models.CategoryTypeService, models.ServiceFeeModeStandard)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0) // stock 0 — must NOT block
	payContactFee(t, db, buyer.ID, svc.ID)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, Price: 20.0, AddedAt: time.Now()}})

	w := postServiceCheckout(t, ch, buyer, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if order["kind"] != models.OrderKindService {
		t.Errorf("expected kind service, got %v", order["kind"])
	}
	if order["shippingZone"] != ShippingZoneSameCity {
		t.Errorf("expected zone same_city, got %v", order["shippingZone"])
	}
	// price 20 USDC in ProductAmount, call-out 3 USDC in ShippingAmount (6 decimals).
	if order["productAmount"] != "20000000" {
		t.Errorf("expected productAmount 20000000, got %v", order["productAmount"])
	}
	if order["shippingAmount"] != "3000000" {
		t.Errorf("expected shippingAmount 3000000, got %v", order["shippingAmount"])
	}
}

func TestCreateOrderFromCart_Service_ResidentialFreeCallout(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTestCategory(t, db, models.CategoryTypeService, models.ServiceFeeModeStandard)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	payContactFee(t, db, buyer.ID, svc.ID)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, Price: 20.0, AddedAt: time.Now()}})

	// Buyer declares the provider is in their residential area → call-out free.
	w := postServiceCheckout(t, ch, buyer, map[string]string{svc.ID.String(): models.BookingTierResidential})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if order["shippingZone"] != models.BookingTierResidential {
		t.Errorf("expected zone residential, got %v", order["shippingZone"])
	}
	if order["shippingAmount"] != "0" {
		t.Errorf("expected free call-out (0), got %v", order["shippingAmount"])
	}
}

func TestCreateOrderFromCart_Service_Booking(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTestCategory(t, db, models.CategoryTypeService, models.ServiceFeeModeBooking)
	setBookingFee(t, db, cat.ID, ShippingZoneSameCity, 5.0)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0) // no merchant price for booking
	payContactFee(t, db, buyer.ID, svc.ID)

	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, Price: 0, AddedAt: time.Now()}})

	w := postServiceCheckout(t, ch, buyer, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	// Admin fare (5 USDC) lands in ProductAmount; no separate call-out.
	if order["productAmount"] != "5000000" {
		t.Errorf("expected productAmount 5000000 (booking fare), got %v", order["productAmount"])
	}
	if order["shippingAmount"] != "0" {
		t.Errorf("expected shippingAmount 0 for booking, got %v", order["shippingAmount"])
	}
}
