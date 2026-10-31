package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/datatypes"
)

func getOrderDetail(t *testing.T, ch *CheckoutHandler, user *models.User, orderID string) map[string]interface{} {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Params = gin.Params{{Key: "id", Value: orderID}}
	c.Request, _ = http.NewRequest("GET", "/", nil)
	ch.GetOrderDetail(c)
	if w.Code != http.StatusOK {
		t.Fatalf("order detail: %d %s", w.Code, w.Body)
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Data
}

// bookingLines reads the order's booking lines as "Label: Value".
func bookingLines(t *testing.T, order map[string]interface{}) []string {
	t.Helper()
	booking, ok := order["booking"].(map[string]interface{})
	if !ok {
		t.Fatalf("no booking on the order: %v", order)
	}
	var out []string
	for _, l := range booking["lines"].([]interface{}) {
		line := l.(map[string]interface{})
		out = append(out, line["label"].(string)+": "+line["value"].(string))
	}
	return out
}

// Both parties read what a stay booked: the nights, then the dates as asked.
func TestGetOrderDetail_StayBooking(t *testing.T) {
	pinBookingNow(t, stayNow)
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer, seller, svc := stayListing(t, db, models.BookingTypeInstant, models.PriceUnitPerNight)

	w := bookStay(t, ch, buyer, svc, "2026-11-10", "2026-11-13")
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	id := firstOrder(t, w)["id"].(string)

	want := []string{"Nights: 3", "Check-in: Tue 10 Nov 2026", "Check-out: Fri 13 Nov 2026"}
	for _, who := range []*models.User{seller, buyer} {
		if got := bookingLines(t, getOrderDetail(t, ch, who, id)); !reflect.DeepEqual(got, want) {
			t.Errorf("lines = %q, want %q", got, want)
		}
	}
}

// A seat booking shows its seats and the category form's answers under their
// labels; an answer the form no longer asks keeps its catalogue label.
func TestGetOrderDetail_SeatBookingAnswers(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := quantityCategory(t, db)
	setCategoryColumn(t, db, cat.ID, "booking_fields", datatypes.JSON(
		`[{"key":"departure","required":true},{"key":"destination","required":true},{"key":"vehicle_type","options":["Bus","Car"]}]`))
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, svc.ID, models.BookingTypeInstant, models.PriceUnitPerSeat)
	unlimitedPlaces(t, db, svc.ID)

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId": svc.ID.String(), "quantity": 2,
		"answers": map[string]interface{}{"departure": "Lagos", "destination": "Abuja", "vehicle_type": "Bus"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	id := firstOrder(t, w)["id"].(string)
	// The merchant later drops vehicle_type from the form.
	setCategoryColumn(t, db, cat.ID, "booking_fields", datatypes.JSON(
		`[{"key":"departure","required":true},{"key":"destination","required":true}]`))

	got := bookingLines(t, getOrderDetail(t, ch, seller, id))
	want := []string{"Seats: 2", "From: Lagos", "To: Abuja", "Vehicle type: Bus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// A slot booking shows its time in the listing's zone and the places taken.
func TestGetOrderDetail_SlotBooking(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	svc := setupBookingService(t, db, seller.ID, 3)
	slot := nextWeekdaySlot(10)

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId":   svc.ID.String(),
		"bookingSlot": map[string]interface{}{"slotStart": slot.Format(time.RFC3339), "partySize": 2},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	id := firstOrder(t, w)["id"].(string)

	lagos, _ := time.LoadLocation("Africa/Lagos")
	local := slot.In(lagos)
	want := []string{"When: " + local.Format("Mon 2 Jan 2006, 15:04") + "–" + local.Add(time.Hour).Format("15:04"), "Places: 2"}
	if got := bookingLines(t, getOrderDetail(t, ch, seller, id)); !reflect.DeepEqual(got, want) {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// A product order carries no booking.
func TestGetOrderDetail_ProductHasNoBooking(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	order := models.Order{ID: uuid.New(), BuyerID: buyer.ID, SellerID: seller.ID, ProductID: uuid.New(),
		Kind: models.OrderKindProduct, Quantity: 1, Token: "USDC", Status: models.OrderStatusPendingPayment}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	if b, ok := getOrderDetail(t, ch, buyer, order.ID.String())["booking"]; ok {
		t.Fatalf("product order has a booking: %v", b)
	}
}

func TestFormatBookedSlot(t *testing.T) {
	lagos, _ := time.LoadLocation("Africa/Lagos")
	start := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC) // 14:00 Lagos
	cases := []struct {
		name       string
		start, end time.Time
		want       string
	}{
		{"same day", start, start.Add(time.Hour), "Tue 3 Nov 2026, 14:00–15:00"},
		{"no end", start, start, "Tue 3 Nov 2026, 14:00"},
		{"overnight", start, start.Add(12 * time.Hour), "Tue 3 Nov 2026, 14:00 – Wed 4 Nov 2026, 02:00"},
	}
	for _, tc := range cases {
		if got := formatBookedSlot(tc.start, tc.end, lagos); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFormatBookingAnswer(t *testing.T) {
	cases := []struct {
		typ  string
		v    interface{}
		want string
	}{
		{models.BookingFieldText, " Window seat ", "Window seat"},
		{models.BookingFieldDate, "2026-11-10", "Tue 10 Nov 2026"},
		{models.BookingFieldDate, "soon", "soon"},
		{models.BookingFieldText, float64(2), "2"},
		{models.BookingFieldText, true, "Yes"},
		{models.BookingFieldText, false, "No"},
		{models.BookingFieldText, []interface{}{"Wi-Fi", "Pool"}, "Wi-Fi, Pool"},
		{models.BookingFieldText, nil, ""},
	}
	for _, tc := range cases {
		if got := formatBookingAnswer(tc.typ, tc.v); got != tc.want {
			t.Errorf("formatBookingAnswer(%q, %v) = %q, want %q", tc.typ, tc.v, got, tc.want)
		}
	}
}
