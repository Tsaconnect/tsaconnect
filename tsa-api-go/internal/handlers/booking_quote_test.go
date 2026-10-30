package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func postQuote(t *testing.T, ch *CheckoutHandler, buyer *models.User, body map[string]interface{}) (*httptest.ResponseRecorder, serviceQuote) {
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
	ch.QuoteServiceOrder(c)
	var resp struct {
		Data serviceQuote `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	return w, resp.Data
}

// A quote prices a stay exactly as checkout would and leaves nothing behind.
func TestQuoteServiceOrder_Stay(t *testing.T) {
	pinBookingNow(t, stayNow)
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer, _, svc := stayListing(t, db, models.BookingTypeInstant, models.PriceUnitPerNight)

	answers := map[string]interface{}{"checkin": "2026-11-10", "checkout": "2026-11-13"}
	w, q := postQuote(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String(), "answers": answers})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if q.Units != 3 || !q.ChargesPerUnit || q.UnitPriceUSD != 5 || q.PriceUSD != 15 || q.TotalUSD != 15 {
		t.Errorf("unexpected quote: %+v", q)
	}
	var orders, bookings int64
	db.Model(&models.Order{}).Count(&orders)
	db.Model(&models.ServiceBooking{}).Count(&bookings)
	if orders != 0 || bookings != 0 {
		t.Errorf("a quote wrote %d orders and %d bookings", orders, bookings)
	}

	// Not priceable yet: the reason comes back as a 400.
	w, _ = postQuote(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String(), "answers": map[string]interface{}{"checkin": "2026-11-10"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Check-out") {
		t.Errorf("missing check-out: expected 400 naming it, got %d: %s", w.Code, w.Body.String())
	}
}

func TestQuoteServiceOrder_QuantityAndQuote(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)

	tickets := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, tickets.ID, models.BookingTypeInstant, models.PriceUnitPerTicket)
	unlimitedPlaces(t, db, tickets.ID)
	_, q := postQuote(t, ch, buyer, map[string]interface{}{"productId": tickets.ID.String(), "quantity": 4})
	if q.Units != 4 || q.UnitPriceUSD != 5 || q.TotalUSD != 20 {
		t.Errorf("tickets: unexpected quote %+v", q)
	}

	quoted := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, quoted.ID, models.BookingTypeRequest, "")
	db.Model(&models.ServiceProfile{}).Where("product_id = ?", quoted.ID).Update("pricing_method", models.PricingMethodOnRequest)
	if _, q := postQuote(t, ch, buyer, map[string]interface{}{"productId": quoted.ID.String(), "quantity": 2}); !q.OnRequest || q.TotalUSD != 0 {
		t.Errorf("quoted: expected onRequest with no price, got %+v", q)
	}
}

// Listings carry what they charge: a booking's cheapest area fare and what it
// buys, an order's cheapest menu item, a contact service's price.
func TestPopulateServicePrices(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")

	stayCat := dateRangeCategory(t, db)
	stay := createTestService(t, db, seller.ID, &stayCat.ID, 0, 50)
	db.Model(stay).Update("shipping_same_state", 40) // the cheapest area
	writePricedProfile(t, db, stay.ID, models.BookingTypeInstant, models.PriceUnitPerNight)
	// Charged once per booking, so not part of the "/ night" price.
	db.Model(&models.ServiceProfile{}).Where("product_id = ?", stay.ID).Update("access_fee_usd", 10)

	quoteCat := quantityCategory(t, db)
	// A whole-booking price does include it.
	fixed := createTestService(t, db, seller.ID, &quoteCat.ID, 0, 25)
	writePricedProfile(t, db, fixed.ID, models.BookingTypeInstant, models.PriceUnitFixed)
	db.Model(&models.ServiceProfile{}).Where("product_id = ?", fixed.ID).Update("access_fee_usd", 10)
	quoted := createTestService(t, db, seller.ID, &quoteCat.ID, 0, 5)
	writePricedProfile(t, db, quoted.ID, models.BookingTypeRequest, "")
	db.Model(&models.ServiceProfile{}).Where("product_id = ?", quoted.ID).Update("pricing_method", models.PricingMethodOnRequest)

	orderCat := serviceCategory(t, db, models.ServiceTypeOrder)
	menu := createTestService(t, db, seller.ID, &orderCat.ID, 0, 0)
	for _, price := range []float64{7, 3} {
		item := models.ServiceMenuItem{ID: uuid.New(), ProductID: menu.ID, Name: "Item", PriceUSD: price, IsAvailable: true,
			CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := db.Create(&item).Error; err != nil {
			t.Fatalf("menu item: %v", err)
		}
	}

	contactCat := serviceCategory(t, db, models.ServiceTypeContact)
	contact := createTestService(t, db, seller.ID, &contactCat.ID, 12, 0)

	var products []models.Product
	db.Where("id IN ?", []uuid.UUID{stay.ID, fixed.ID, quoted.ID, menu.ID, contact.ID}).Find(&products)
	populateServicePrices(db, products)
	got := map[uuid.UUID]*models.ServicePriceSummary{}
	for i := range products {
		got[products[i].ID] = products[i].ServicePrice
	}

	if s := got[stay.ID]; s == nil || s.FromUSD != 40 || s.BookingMode != models.BookingModeDateRange || !s.ChargesPerUnit || s.PriceUnit != models.PriceUnitPerNight {
		t.Errorf("stay: %+v", s)
	}
	if s := got[fixed.ID]; s == nil || s.FromUSD != 35 || s.ChargesPerUnit {
		t.Errorf("fixed: %+v", s)
	}
	if s := got[quoted.ID]; s == nil || !s.OnRequest || s.FromUSD != 0 || s.BookingType != models.BookingTypeRequest {
		t.Errorf("quoted: %+v", s)
	}
	if s := got[menu.ID]; s == nil || s.ServiceType != models.ServiceTypeOrder || s.FromUSD != 3 {
		t.Errorf("menu: %+v", s)
	}
	if s := got[contact.ID]; s == nil || s.ServiceType != models.ServiceTypeContact || s.FromUSD != 12 {
		t.Errorf("contact: %+v", s)
	}
}
