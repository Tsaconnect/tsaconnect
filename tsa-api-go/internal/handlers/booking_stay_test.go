package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// stayNow is 2026-11-01 10:00 Lagos, the clock the stay tests book against.
var stayNow = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func dateRangeCategory(t *testing.T, db *gorm.DB) *models.Category {
	t.Helper()
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	if err := db.Model(cat).Update("booking_mode", models.BookingModeDateRange).Error; err != nil {
		t.Fatalf("failed to configure category: %v", err)
	}
	return cat
}

// stayListing is a date-range listing with a same-city fare of 5 USDC.
func stayListing(t *testing.T, db *gorm.DB, bookingType, unit string) (buyer, seller *models.User, svc *models.Product) {
	t.Helper()
	buyer = createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller = createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := dateRangeCategory(t, db)
	svc = createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writePricedProfile(t, db, svc.ID, bookingType, unit)
	return buyer, seller, svc
}

func bookStay(t *testing.T, ch *CheckoutHandler, buyer *models.User, svc *models.Product, in, out string) *httptest.ResponseRecorder {
	t.Helper()
	answers := map[string]interface{}{}
	if in != "" {
		answers["checkin"] = in
	}
	if out != "" {
		answers["checkout"] = out
	}
	return postServiceOrder(t, ch, buyer, map[string]interface{}{"productId": svc.ID.String(), "answers": answers})
}

func TestCreateServiceOrder_StayPricing(t *testing.T) {
	cases := []struct {
		name, unit, in, out string
		wantPaid            string
		wantQty             int
	}{
		{"three nights", models.PriceUnitPerNight, "2026-11-10", "2026-11-13", "15000000", 3},
		{"no unit is per night", "", "2026-11-10", "2026-11-12", "10000000", 2},
		{"days", models.PriceUnitPerDay, "2026-11-10", "2026-11-11", "5000000", 1},
		{"a started month", models.PriceUnitPerMonth, "2026-11-02", "2026-11-28", "5000000", 1},
		{"fixed is a total", models.PriceUnitFixed, "2026-11-10", "2026-11-20", "5000000", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinBookingNow(t, stayNow)
			db := setupCheckoutTestDB(t)
			ch := setupCheckoutHandler(t)
			buyer, _, svc := stayListing(t, db, models.BookingTypeInstant, tc.unit)

			w := bookStay(t, ch, buyer, svc, tc.in, tc.out)
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
			var b models.ServiceBooking
			if err := db.First(&b, "product_id = ?", svc.ID).Error; err != nil {
				t.Fatalf("stay not reserved: %v", err)
			}
			lagos, _ := time.LoadLocation("Africa/Lagos")
			if !b.SlotStart.Equal(mustDay(t, tc.in, lagos)) || !b.SlotEnd.Equal(mustDay(t, tc.out, lagos)) {
				t.Errorf("reserved %v → %v, want %s → %s Lagos", b.SlotStart, b.SlotEnd, tc.in, tc.out)
			}
		})
	}
}

func mustDay(t *testing.T, s string, loc *time.Location) time.Time {
	t.Helper()
	d, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCreateServiceOrder_StayRefusals(t *testing.T) {
	cases := []struct {
		name, in, out, want string
	}{
		{"missing dates", "", "", "Check-in is required"},
		{"not a date", "next friday", "2026-11-12", "Check-in must be a date"},
		{"past", "2026-10-31", "2026-11-02", "Check-in is in the past"},
		{"check-out first", "2026-11-12", "2026-11-12", "Check-out must be after Check-in"},
		{"too far ahead for instant", "2026-12-20", "2026-12-22", "must end within 28 days"},
		{"ends past the escrow window", "2026-11-10", "2026-12-10", "must end within 28 days"},
		{"too long", "2026-11-10", "2027-11-12", "at most 366 nights"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinBookingNow(t, stayNow)
			db := setupCheckoutTestDB(t)
			ch := setupCheckoutHandler(t)
			buyer, _, svc := stayListing(t, db, models.BookingTypeInstant, models.PriceUnitPerNight)
			w := bookStay(t, ch, buyer, svc, tc.in, tc.out)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("expected 400 %q, got %d: %s", tc.want, w.Code, w.Body.String())
			}
		})
	}
}

// Same-day check-in is allowed; nights already holding the listing's capacity
// refuse a stay that overlaps them, and a stay after them goes through.
func TestCreateServiceOrder_StayCapacityPerNight(t *testing.T) {
	pinBookingNow(t, stayNow)
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer, _, svc := stayListing(t, db, models.BookingTypeInstant, models.PriceUnitPerNight) // capacity 1
	other := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")

	if w := bookStay(t, ch, buyer, svc, "2026-11-01", "2026-11-03"); w.Code != http.StatusCreated {
		t.Fatalf("first stay: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	w := bookStay(t, ch, other, svc, "2026-11-02", "2026-11-04")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "fully booked on 2 Nov 2026") {
		t.Fatalf("overlap: expected 400 fully booked on 2 Nov, got %d: %s", w.Code, w.Body.String())
	}
	if w := bookStay(t, ch, other, svc, "2026-11-03", "2026-11-05"); w.Code != http.StatusCreated {
		t.Fatalf("back-to-back: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// A cancelled order frees its nights.
	db.Model(&models.Order{}).Where("buyer_id = ?", buyer.ID).Update("status", models.OrderStatusCancelled)
	if w := bookStay(t, ch, other, svc, "2026-11-01", "2026-11-03"); w.Code != http.StatusCreated {
		t.Fatalf("after cancel: expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// A request is checked when submitted and charged for its stay when paid; it
// may be further ahead than an instant booking.
func TestBookingRequest_StayChargedAtPay(t *testing.T) {
	pinBookingNow(t, stayNow)
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer, seller, svc := stayListing(t, db, models.BookingTypeRequest, models.PriceUnitPerNight)

	idParams := gin.Params{{Key: "id", Value: svc.ID.String()}}
	bad := map[string]interface{}{"token": "USDC", "answers": map[string]interface{}{"checkin": "2026-12-20", "checkout": "2026-12-20"}}
	if w := postJSON(t, buyer, idParams, bad, ch.CreateBookingRequest); w.Code != http.StatusBadRequest {
		t.Fatalf("bad dates: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	good := map[string]interface{}{"token": "USDC", "answers": map[string]interface{}{"checkin": "2026-12-20", "checkout": "2026-12-24"}}
	if w := postJSON(t, buyer, idParams, good, ch.CreateBookingRequest); w.Code != http.StatusCreated {
		t.Fatalf("create request: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var r models.BookingRequest
	db.Where("buyer_id = ?", buyer.ID).First(&r)
	reqParams := gin.Params{{Key: "reqId", Value: r.ID.String()}}
	if w := postJSON(t, seller, reqParams, nil, ch.ApproveBookingRequest); w.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	w := postJSON(t, buyer, reqParams, nil, ch.PayBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("pay: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := firstOrder(t, w)
	if got := listingPaid(t, order["productAmount"], order["platformFee"]); got != "20000000" {
		t.Errorf("productAmount = %s, want 20000000 (4 nights)", got)
	}
	var n int64
	db.Model(&models.ServiceBooking{}).Where("product_id = ?", svc.ID).Count(&n)
	if n != 1 {
		t.Errorf("expected the stay reserved at pay, got %d bookings", n)
	}
}

func TestCreateOrderFromCart_StayRefused(t *testing.T) {
	pinBookingNow(t, stayNow)
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer, seller, svc := stayListing(t, db, models.BookingTypeInstant, models.PriceUnitPerNight)
	createTestCart(t, db, buyer.ID, []models.CartItem{{ID: uuid.New(), Product: svc.ID, Seller: seller.ID, Quantity: 1, AddedAt: time.Now()}})

	w := postServiceCheckout(t, ch, buyer, nil)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "pick your dates") {
		t.Fatalf("expected 400 pick your dates, got %d: %s", w.Code, w.Body.String())
	}
}

// The form a date-range listing is served always asks for its stay: check-in
// and check-out are added when missing, or the start/end pair the form already
// uses is made required.
func TestGetBookingFormSchema_StayFields(t *testing.T) {
	db := setupCheckoutTestDB(t)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := dateRangeCategory(t, db)

	type field struct {
		Key      string `json:"key"`
		Type     string `json:"type"`
		Required bool   `json:"required"`
	}
	schemaOf := func(svc uuid.UUID) (string, []field) {
		w := getFormSchema(t, &Handlers{}, svc)
		var resp struct {
			Data struct {
				BookingMode string  `json:"bookingMode"`
				Fields      []field `json:"fields"`
			} `json:"data"`
		}
		json.Unmarshal(w.Body.Bytes(), &resp)
		return resp.Data.BookingMode, resp.Data.Fields
	}

	bare := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	mode, fields := schemaOf(bare.ID)
	if mode != models.BookingModeDateRange || len(fields) != 2 || fields[0].Key != "checkin" || fields[1].Key != "checkout" ||
		!fields[0].Required || fields[0].Type != models.BookingFieldDate {
		t.Errorf("bare listing: got mode %s fields %+v", mode, fields)
	}

	rental := createTestService(t, db, seller.ID, &cat.ID, 0, 5)
	writeFieldConfig(t, db, rental.ID, &models.BookingFieldConfig{Fields: []models.BookingField{
		{Key: "start_date", Type: models.BookingFieldDate, Label: "Start date"},
		{Key: "special_requests", Type: models.BookingFieldTextarea, Label: "Special requests"},
	}})
	_, fields = schemaOf(rental.ID)
	keys := []string{}
	for _, f := range fields {
		keys = append(keys, f.Key)
	}
	if strings.Join(keys, ",") != "start_date,end_date,special_requests" {
		t.Errorf("rental keys = %v, want end_date added after start_date", keys)
	}
	for _, f := range fields {
		if (f.Key == "start_date" || f.Key == "end_date") && !f.Required {
			t.Errorf("%s should be required", f.Key)
		}
	}
}

// A quoted stay is still held night by night when it is paid: the quote sets
// the price, not the room.
func TestBookingRequest_QuotedStayReservesNights(t *testing.T) {
	pinBookingNow(t, stayNow)
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer, seller, svc := stayListing(t, db, models.BookingTypeRequest, "")
	db.Model(&models.ServiceProfile{}).Where("product_id = ?", svc.ID).Update("pricing_method", models.PricingMethodOnRequest)
	other := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")

	request := func(u *models.User) gin.Params {
		t.Helper()
		body := map[string]interface{}{"token": "USDC", "answers": map[string]interface{}{"checkin": "2026-11-10", "checkout": "2026-11-12"}}
		if w := postJSON(t, u, gin.Params{{Key: "id", Value: svc.ID.String()}}, body, ch.CreateBookingRequest); w.Code != http.StatusCreated {
			t.Fatalf("create request: expected 201, got %d: %s", w.Code, w.Body.String())
		}
		var r models.BookingRequest
		db.Where("buyer_id = ?", u.ID).First(&r)
		p := gin.Params{{Key: "reqId", Value: r.ID.String()}}
		if w := postJSON(t, seller, p, map[string]interface{}{"amountUsd": 40}, ch.ApproveBookingRequest); w.Code != http.StatusOK {
			t.Fatalf("approve: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		return p
	}
	first, second := request(buyer), request(other)

	w := postJSON(t, buyer, first, nil, ch.PayBookingRequest)
	if w.Code != http.StatusCreated {
		t.Fatalf("first pay: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	if got := listingPaid(t, firstOrder(t, w)["productAmount"], firstOrder(t, w)["platformFee"]); got != "40000000" {
		t.Errorf("productAmount = %s, want the 40 USDC quote", got)
	}
	if w := postJSON(t, other, second, nil, ch.PayBookingRequest); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "fully booked") {
		t.Fatalf("second pay: expected 400 fully booked, got %d: %s", w.Code, w.Body.String())
	}
}
