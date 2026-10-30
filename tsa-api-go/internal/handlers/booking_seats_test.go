package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// seatListing is an instant per-seat listing in a quantity category with no
// time slots, capacity places, and a same-city fare of 5 USDC. dated gives it a
// booking form that asks a travel date.
func seatListing(t *testing.T, capacity int, dated bool) (db *gorm.DB, ch *CheckoutHandler, svc *models.Product, buyers []*models.User) {
	t.Helper()
	var fields []models.BookingField
	if dated {
		fields = []models.BookingField{{Key: "travel_date", Type: models.BookingFieldDate, Label: "Travel date", Required: true}}
	}
	return seatListingWithForm(t, capacity, fields)
}

// seatListingWithForm is seatListing with the given booking form (none when empty).
func seatListingWithForm(t *testing.T, capacity int, fields []models.BookingField) (db *gorm.DB, ch *CheckoutHandler, svc *models.Product, buyers []*models.User) {
	t.Helper()
	pinBookingNow(t, stayNow)
	db = setupCheckoutTestDB(t)
	ch = setupCheckoutHandler(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)
	svc = createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, svc.ID, models.BookingTypeInstant, models.PriceUnitPerSeat)
	updates := map[string]interface{}{"max_capacity_per_slot": capacity, "slot_duration_minutes": 0}
	if len(fields) > 0 {
		var p models.ServiceProfile
		p.SetFieldConfig(&models.BookingFieldConfig{Fields: fields})
		updates["field_config"] = p.FieldConfig
	}
	if err := db.Model(&models.ServiceProfile{}).Where("product_id = ?", svc.ID).Updates(updates).Error; err != nil {
		t.Fatalf("configure profile: %v", err)
	}
	for i := 0; i < 4; i++ {
		buyers = append(buyers, createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria"))
	}
	return db, ch, svc, buyers
}

func bookSeats(t *testing.T, ch *CheckoutHandler, buyer *models.User, svc *models.Product, n int, date string) *httptest.ResponseRecorder {
	t.Helper()
	body := map[string]interface{}{"productId": svc.ID.String(), "quantity": n}
	if date != "" {
		body["answers"] = map[string]interface{}{"travel_date": date}
	}
	return postServiceOrder(t, ch, buyer, body)
}

func expectRefused(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), want) {
		t.Fatalf("expected 400 %q, got %d: %s", want, w.Code, w.Body.String())
	}
}

// Without a date, a listing's capacity is its total: bookings fill it, a
// cancelled one frees its places.
func TestCreateServiceOrder_SeatsWithoutSlotsAreCapped(t *testing.T) {
	db, ch, svc, b := seatListing(t, 3, false)

	if w := bookSeats(t, ch, b[0], svc, 2, ""); w.Code != http.StatusCreated {
		t.Fatalf("first booking: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	expectRefused(t, bookSeats(t, ch, b[1], svc, 2, ""), "Only 1 left")
	if w := bookSeats(t, ch, b[1], svc, 1, ""); w.Code != http.StatusCreated {
		t.Fatalf("last place: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	expectRefused(t, bookSeats(t, ch, b[2], svc, 1, ""), "sold out")

	db.Model(&models.Order{}).Where("buyer_id = ?", b[0].ID).Update("status", models.OrderStatusCancelled)
	if w := bookSeats(t, ch, b[2], svc, 2, ""); w.Code != http.StatusCreated {
		t.Fatalf("after cancel: expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// A dated listing's capacity is per day, and an instant booking's day must be
// inside the booking lead.
func TestCreateServiceOrder_SeatsPerDate(t *testing.T) {
	_, ch, svc, b := seatListing(t, 2, true)

	if w := bookSeats(t, ch, b[0], svc, 2, "2026-11-10"); w.Code != http.StatusCreated {
		t.Fatalf("first booking: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	expectRefused(t, bookSeats(t, ch, b[1], svc, 1, "2026-11-10"), "sold out on 10 Nov 2026")
	if w := bookSeats(t, ch, b[1], svc, 2, "2026-11-11"); w.Code != http.StatusCreated {
		t.Fatalf("next day: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	expectRefused(t, bookSeats(t, ch, b[2], svc, 1, "2026-10-31"), "in the past")
	expectRefused(t, bookSeats(t, ch, b[2], svc, 1, "2027-01-20"), "within")
}

// A request for places that are already taken is refused when it is made.
func TestBookingRequest_SoldOutSeatsRefusedAtRequest(t *testing.T) {
	db, ch, svc, b := seatListing(t, 2, false)
	if w := bookSeats(t, ch, b[0], svc, 2, ""); w.Code != http.StatusCreated {
		t.Fatalf("first booking: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	db.Model(&models.ServiceProfile{}).Where("product_id = ?", svc.ID).Update("booking_type", models.BookingTypeRequest)

	w := postJSON(t, b[1], gin.Params{{Key: "id", Value: svc.ID.String()}}, map[string]interface{}{"token": "USDC", "quantity": 1}, ch.CreateBookingRequest)
	expectRefused(t, w, "sold out")
}

// Creating a profile with capacity 0 keeps it unlimited rather than taking the
// column's default of one.
func TestUpsertServiceProfile_CreateKeepsUnlimitedCapacity(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	if w := putProfile(t, &Handlers{}, seller, svc.ID, map[string]interface{}{"maxCapacityPerSlot": 0}); w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if p := loadProfile(t, db, svc.ID); p.MaxCapacityPerSlot != 0 {
		t.Errorf("stored capacity = %d, want 0 (unlimited)", p.MaxCapacityPerSlot)
	}
}

// A booking is dated only by a field that names its day: a return date or a
// date of birth earlier in the form is not it, and a day-counted listing needs
// its date even when the form marks it optional.
func TestCreateServiceOrder_SeatDateField(t *testing.T) {
	_, ch, svc, b := seatListingWithForm(t, 1, []models.BookingField{
		{Key: "date_of_birth", Type: models.BookingFieldDate, Label: "Date of birth"},
		{Key: "return_date", Type: models.BookingFieldDate, Label: "Return date"},
		{Key: "travel_date", Type: models.BookingFieldDate, Label: "Travel date"},
	})
	book := func(buyer *models.User, answers map[string]interface{}) *httptest.ResponseRecorder {
		return postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String(), "quantity": 1, "answers": answers})
	}
	expectRefused(t, book(b[0], map[string]interface{}{"date_of_birth": "1990-05-01"}), "Pick a travel date")
	if w := book(b[0], map[string]interface{}{"date_of_birth": "1990-05-01", "travel_date": "2026-11-10"}); w.Code != http.StatusCreated {
		t.Fatalf("dated by travel date: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	expectRefused(t, book(b[1], map[string]interface{}{"travel_date": "2026-11-10"}), "sold out on 10 Nov 2026")

	// A form whose only date is not a booking day counts the listing's whole run.
	_, ch2, svc2, b2 := seatListingWithForm(t, 1, []models.BookingField{
		{Key: "date_of_birth", Type: models.BookingFieldDate, Label: "Date of birth"},
	})
	body := map[string]interface{}{"productId": svc2.ID.String(), "quantity": 1, "answers": map[string]interface{}{"date_of_birth": "1990-05-01"}}
	if w := postServiceOrder(t, ch2, b2[0], body); w.Code != http.StatusCreated {
		t.Fatalf("undated: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	expectRefused(t, postServiceOrder(t, ch2, b2[1], body), "sold out")
}

// A quote refused because the places are taken says so with a code the sheet
// shows; one refused because the form is still being filled in carries none.
func TestQuoteServiceOrder_SoldOutIsCoded(t *testing.T) {
	_, ch, svc, b := seatListing(t, 2, true)
	if w := bookSeats(t, ch, b[0], svc, 2, "2026-11-10"); w.Code != http.StatusCreated {
		t.Fatalf("first booking: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	codeOf := func(w *httptest.ResponseRecorder) string {
		var resp struct {
			Code string `json:"code"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Code
	}

	w, _ := postQuote(t, ch, b[1], map[string]interface{}{"productId": svc.ID.String(), "quantity": 1,
		"answers": map[string]interface{}{"travel_date": "2026-11-10"}})
	if w.Code != http.StatusBadRequest || codeOf(w) != bookingUnavailableCode || !strings.Contains(w.Body.String(), "sold out on 10 Nov 2026") {
		t.Fatalf("sold out: expected 400 %s, got %d: %s", bookingUnavailableCode, w.Code, w.Body.String())
	}
	w, _ = postQuote(t, ch, b[1], map[string]interface{}{"productId": svc.ID.String(), "quantity": 1})
	if w.Code != http.StatusBadRequest || codeOf(w) != "" {
		t.Fatalf("no date yet: expected an uncoded 400, got %d: %s", w.Code, w.Body.String())
	}
}
