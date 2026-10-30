package handlers

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// quantityCategory is a booking category in quantity mode allowing units.
func quantityCategory(t *testing.T, db *gorm.DB, units ...string) *models.Category {
	t.Helper()
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	mode := models.BookingModeQuantity
	updates := map[string]interface{}{"booking_mode": mode}
	if len(units) > 0 {
		b, _ := json.Marshal(units)
		updates["price_units"] = string(b)
	}
	if err := db.Model(cat).Updates(updates).Error; err != nil {
		t.Fatalf("failed to configure category: %v", err)
	}
	return cat
}

func writePricedProfile(t *testing.T, db *gorm.DB, productID uuid.UUID, bookingType, priceUnit string) {
	t.Helper()
	profile := models.ServiceProfile{
		ID:            uuid.New(),
		ProductID:     productID,
		BookingType:   bookingType,
		PricingMethod: models.PricingMethodFixed,
		PriceUnit:     priceUnit,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}
}

// A quantity-mode booking charges the per-unit fare once per unit and records
// the units on the order; fixed and slot-mode listings charge once.
func TestCreateServiceOrder_QuantityPricing(t *testing.T) {
	cases := []struct {
		name      string
		quantity  bool // category in quantity mode
		unit      string
		requested int
		wantPaid  string // ProductAmount, USDC base units (fare 5)
		wantQty   int
	}{
		{"per seat x3", true, models.PriceUnitPerSeat, 3, "15000000", 3},
		{"no unit in quantity mode is per unit", true, "", 2, "10000000", 2},
		{"fixed is a total", true, models.PriceUnitFixed, 4, "5000000", 4},
		{"quantity defaults to one", true, models.PriceUnitPerSeat, 0, "5000000", 1},
		{"slot mode ignores quantity", false, models.PriceUnitPerSeat, 3, "5000000", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupCheckoutTestDB(t)
			ch := setupCheckoutHandler(t)
			buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
			seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
			cat := serviceCategory(t, db, models.ServiceTypeBooking)
			if tc.quantity {
				cat = quantityCategory(t, db)
			}
			svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5) // same-city fare 5
			writePricedProfile(t, db, svc.ID, models.BookingTypeInstant, tc.unit)

			w := postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String(), "quantity": tc.requested})
			if w.Code != http.StatusCreated {
				t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
			}
			order := firstOrder(t, w)
			if got := listingPaid(t, order["productAmount"], order["platformFee"]); got != tc.wantPaid {
				t.Errorf("productAmount = %s, want %s", got, tc.wantPaid)
			}
			if got := int(order["quantity"].(float64)); got != tc.wantQty {
				t.Errorf("quantity = %d, want %d", got, tc.wantQty)
			}
		})
	}
}

func TestCreateServiceOrder_QuantityOverMaxRefused(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, svc.ID, models.BookingTypeInstant, models.PriceUnitPerSeat)

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String(), "quantity": maxBookingUnits + 1})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

// The quantity asked for in a request is kept and charged when it is paid.
func TestBookingRequest_QuantityChargedAtPay(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, svc.ID, models.BookingTypeRequest, models.PriceUnitPerSeat)

	idParams := gin.Params{{Key: "id", Value: svc.ID.String()}}
	if w := postJSON(t, buyer, idParams, map[string]interface{}{"token": "USDC", "quantity": 3}, ch.CreateBookingRequest); w.Code != http.StatusCreated {
		t.Fatalf("create request: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var r models.BookingRequest
	db.Where("buyer_id = ?", buyer.ID).First(&r)
	if r.Quantity != 3 {
		t.Fatalf("request quantity = %d, want 3", r.Quantity)
	}
	reqParams := gin.Params{{Key: "reqId", Value: r.ID.String()}}
	if w := postJSON(t, seller, reqParams, nil, ch.ApproveBookingRequest); w.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w := postJSON(t, buyer, reqParams, nil, ch.PayBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("pay: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if got := listingPaid(t, order["productAmount"], order["platformFee"]); got != "15000000" {
		t.Errorf("productAmount = %s, want 15000000", got)
	}
}

func TestUpsertServiceProfile_PriceUnit(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db, models.PriceUnitPerSeat, models.PriceUnitOnRequest)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)

	for _, tc := range []struct {
		unit string
		want int
	}{
		{models.PriceUnitPerNight, http.StatusBadRequest},  // not offered by the category
		{models.PriceUnitOnRequest, http.StatusBadRequest}, // a quote is the pricing method
		{"per_fortnight", http.StatusBadRequest},
		{"", http.StatusBadRequest}, // the category restricts units: pick one
		{models.PriceUnitPerSeat, http.StatusOK},
	} {
		if w := putProfile(t, &Handlers{}, seller, svc.ID, map[string]interface{}{"priceUnit": tc.unit}); w.Code != tc.want {
			t.Errorf("priceUnit %q: expected %d, got %d: %s", tc.unit, tc.want, w.Code, w.Body.String())
		}
	}

	// A category with no price units configured accepts any unit.
	open := serviceCategory(t, db, models.ServiceTypeBooking)
	svc2 := createTestService(t, db, seller.ID, &open.ID, 0, 5)
	if w := putProfile(t, &Handlers{}, seller, svc2.ID, map[string]interface{}{"priceUnit": models.PriceUnitPerHour}); w.Code != http.StatusOK {
		t.Fatalf("unconfigured category: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if p := loadProfile(t, db, svc2.ID); p.PriceUnit != models.PriceUnitPerHour {
		t.Errorf("stored priceUnit = %q, want per_hour", p.PriceUnit)
	}
}

func TestGetBookingFormSchema_QuantityMode(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, svc.ID, models.BookingTypeInstant, models.PriceUnitPerSeat)

	w := getFormSchema(t, &Handlers{}, svc.ID)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			BookingMode    string `json:"bookingMode"`
			PriceUnit      string `json:"priceUnit"`
			ChargesPerUnit bool   `json:"chargesPerUnit"`
			MaxQuantity    int    `json:"maxQuantity"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	d := resp.Data
	if d.BookingMode != models.BookingModeQuantity || d.PriceUnit != models.PriceUnitPerSeat || !d.ChargesPerUnit || d.MaxQuantity != maxBookingUnits {
		t.Errorf("unexpected schema: %+v", d)
	}
}

// The cart path takes a quantity-mode booking's units from the cart line.
func TestCreateOrderFromCart_QuantityBooking(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, svc.ID, models.BookingTypeInstant, models.PriceUnitPerSeat)
	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 2, AddedAt: time.Now()}})

	w := postServiceCheckout(t, ch, buyer, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if got := listingPaid(t, order["productAmount"], order["platformFee"]); got != "10000000" {
		t.Errorf("productAmount = %s, want 10000000", got)
	}
	if got := int(order["quantity"].(float64)); got != 2 {
		t.Errorf("quantity = %d, want 2", got)
	}
}

// A quote is a total and a slotted listing offers no more places than a slot holds.
func TestGetBookingFormSchema_QuantityLimits(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)
	schemaOf := func(svc uuid.UUID) (bool, int) {
		t.Helper()
		w := getFormSchema(t, &Handlers{}, svc)
		var resp struct {
			Data struct {
				ChargesPerUnit bool `json:"chargesPerUnit"`
				MaxQuantity    int  `json:"maxQuantity"`
			} `json:"data"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &resp) != nil {
			t.Fatalf("form schema: %d %s", w.Code, w.Body.String())
		}
		return resp.Data.ChargesPerUnit, resp.Data.MaxQuantity
	}

	quoted := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, quoted.ID, models.BookingTypeRequest, "")
	db.Model(&models.ServiceProfile{}).Where("product_id = ?", quoted.ID).Update("pricing_method", models.PricingMethodOnRequest)
	if perUnit, _ := schemaOf(quoted.ID); perUnit {
		t.Error("a quoted listing must not say it charges per unit")
	}

	slotted := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, slotted.ID, models.BookingTypeInstant, models.PriceUnitPerSeat)
	db.Model(&models.ServiceProfile{}).Where("product_id = ?", slotted.ID).
		Updates(map[string]interface{}{"slot_duration_minutes": 60, "max_capacity_per_slot": 8})
	if _, limit := schemaOf(slotted.ID); limit != 8 {
		t.Errorf("maxQuantity = %d, want the slot's capacity 8", limit)
	}
}

// A quoted listing needs no unit even where the category restricts them.
func TestUpsertServiceProfile_QuoteNeedsNoUnit(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db, models.PriceUnitFixed)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	body := map[string]interface{}{"bookingType": models.BookingTypeRequest, "pricingMethod": models.PricingMethodOnRequest, "priceUnit": ""}
	if w := putProfile(t, &Handlers{}, seller, svc.ID, body); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// Switching to a fixed price re-checks the stored unit, and a quote keeps no
// unit the category refuses.
func TestUpsertServiceProfile_PriceUnitFollowsPricingMethod(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db, models.PriceUnitPerSeat)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	quote := map[string]interface{}{"bookingType": models.BookingTypeRequest, "pricingMethod": models.PricingMethodOnRequest}

	if w := putProfile(t, &Handlers{}, seller, svc.ID, withField(quote, "priceUnit", models.PriceUnitPerNight)); w.Code != http.StatusBadRequest {
		t.Fatalf("quote with a refused unit: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if w := putProfile(t, &Handlers{}, seller, svc.ID, withField(quote, "priceUnit", "")); w.Code != http.StatusOK {
		t.Fatalf("quote with no unit: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if w := putProfile(t, &Handlers{}, seller, svc.ID, map[string]interface{}{"pricingMethod": models.PricingMethodFixed}); w.Code != http.StatusBadRequest {
		t.Fatalf("fixed price with no unit: expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func withField(m map[string]interface{}, k string, v interface{}) map[string]interface{} {
	out := map[string]interface{}{k: v}
	for kk, vv := range m {
		out[kk] = vv
	}
	return out
}
