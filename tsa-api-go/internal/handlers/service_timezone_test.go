package handlers

import (
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

// Booking times are wall-clock in the service's own timezone. A Lagos merchant
// who opens at 09:00 means 09:00 WAT (08:00 UTC), not 09:00 UTC — before the
// timezone field existed every slot in the system surfaced an hour late.

// writeSlotProfile creates a slot-based booking profile in the given zone.
func writeSlotProfile(t *testing.T, db *gorm.DB, productID uuid.UUID, tz string, slotMin, capacity int) {
	t.Helper()
	profile := models.ServiceProfile{
		ID:                  uuid.New(),
		ProductID:           productID,
		Timezone:            tz,
		SlotDurationMinutes: slotMin,
		MaxCapacityPerSlot:  capacity,
		BookingType:         models.BookingTypeInstant,
		PricingMethod:       models.PricingMethodFixed,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("failed to create profile: %v", err)
	}
}

// writeAvailability opens a weekly wall-clock window on the given weekday.
func writeAvailability(t *testing.T, db *gorm.DB, productID uuid.UUID, dow int, start, end string) {
	t.Helper()
	w := models.ServiceAvailability{
		ID:        uuid.New(),
		ProductID: productID,
		DayOfWeek: dow,
		StartTime: start,
		EndTime:   end,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := db.Create(&w).Error; err != nil {
		t.Fatalf("failed to create availability: %v", err)
	}
}

func getSlots(t *testing.T, h *Handlers, serviceID uuid.UUID, from, to string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: serviceID.String()}}
	c.Request, _ = http.NewRequest("GET", "/?from="+from+"&to="+to, nil)
	h.GetServiceSlots(c)
	return w
}

type slotsPayload struct {
	Data struct {
		Slots []struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"slots"`
	} `json:"data"`
}

// A 09:00 Lagos window must produce a slot at 08:00 UTC, not 09:00 UTC.
func TestGetServiceSlotsUsesServiceTimezone(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)

	// 2026-09-14 is a Monday.
	writeSlotProfile(t, db, svc.ID, "Africa/Lagos", 60, 1)
	writeAvailability(t, db, svc.ID, int(time.Monday), "09:00", "11:00")

	w := getSlots(t, h, svc.ID, "2026-09-14", "2026-09-14")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var got slotsPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(got.Data.Slots) != 2 {
		t.Fatalf("expected 2 slots from a 2h window at 60m, got %d", len(got.Data.Slots))
	}

	// 09:00 WAT == 08:00 UTC.
	wantFirst := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	if !got.Data.Slots[0].Start.UTC().Equal(wantFirst) {
		t.Errorf("first slot: want %s, got %s",
			wantFirst.Format(time.RFC3339), got.Data.Slots[0].Start.UTC().Format(time.RFC3339))
	}
	wantSecond := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	if !got.Data.Slots[1].Start.UTC().Equal(wantSecond) {
		t.Errorf("second slot: want %s, got %s",
			wantSecond.Format(time.RFC3339), got.Data.Slots[1].Start.UTC().Format(time.RFC3339))
	}
}

// A service with no timezone set behaves as Africa/Lagos, the platform default,
// so existing listings keep working without a data migration.
func TestGetServiceSlotsDefaultsToLagos(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)

	writeSlotProfile(t, db, svc.ID, "", 60, 1) // no timezone recorded
	writeAvailability(t, db, svc.ID, int(time.Monday), "09:00", "10:00")

	w := getSlots(t, h, svc.ID, "2026-09-14", "2026-09-14")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got slotsPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(got.Data.Slots) != 1 {
		t.Fatalf("expected 1 slot, got %d", len(got.Data.Slots))
	}
	want := time.Date(2026, 9, 14, 8, 0, 0, 0, time.UTC)
	if !got.Data.Slots[0].Start.UTC().Equal(want) {
		t.Errorf("want %s, got %s", want.Format(time.RFC3339), got.Data.Slots[0].Start.UTC().Format(time.RFC3339))
	}
}

// A wall-clock time that a DST spring-forward skips does not exist, and Go
// normalises it backwards onto the hour before — so 01:00 and 02:00 would be
// advertised as two slots resolving to ONE instant, silently sharing capacity
// and booking a buyer an hour earlier than they picked. The skipped hour must
// not be offered at all.
func TestGetServiceSlotsSkipsDSTGap(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)

	// 2026-03-08 is a Sunday and the US spring-forward date: 02:00–03:00 local
	// never happens.
	writeSlotProfile(t, db, svc.ID, "America/New_York", 60, 1)
	writeAvailability(t, db, svc.ID, int(time.Sunday), "01:00", "04:00")

	w := getSlots(t, h, svc.ID, "2026-03-08", "2026-03-08")
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got slotsPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	// 01:00 and 03:00 are real; 02:00 is not.
	if len(got.Data.Slots) != 2 {
		t.Fatalf("want 2 real slots across the gap, got %d", len(got.Data.Slots))
	}
	seen := map[int64]bool{}
	for _, s := range got.Data.Slots {
		u := s.Start.UTC().Unix()
		if seen[u] {
			t.Fatalf("two slots resolved to the same instant %s", s.Start.UTC().Format(time.RFC3339))
		}
		seen[u] = true
	}
}

// Checkout must accept the same instant the slots endpoint advertised, and must
// still reject a slot that is off the grid once read in the service's zone.
func TestReserveBookingSlotHonoursServiceTimezone(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := &CheckoutHandler{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	writeAvailability(t, db, svc.ID, int(time.Monday), "09:00", "11:00")

	profile := models.ServiceProfile{
		Timezone:            "Africa/Lagos",
		SlotDurationMinutes: 60,
		MaxCapacityPerSlot:  1,
	}

	// The next Monday at least two days out: in the future, and well inside the
	// booking lead time (MaxBookingLeadDays).
	monday := nextWeekdayUTC(time.Now(), time.Monday, 2)

	// 08:00 UTC == 09:00 WAT, the window's opening slot.
	ok := monday.Add(8 * time.Hour)
	booking, errMsg := ch.reserveBookingSlot(db, buyer.ID, svc, &profile, bookingSlotRequest{SlotStart: ok, PartySize: 1})
	if errMsg != "" {
		t.Fatalf("expected the 09:00 WAT slot to be accepted, got %q", errMsg)
	}
	if booking == nil || !booking.SlotStart.UTC().Equal(ok) {
		t.Fatalf("booking did not record the requested instant")
	}

	// 09:00 UTC == 10:00 WAT is still on the grid and inside the window.
	onGrid := monday.Add(9 * time.Hour)
	if _, errMsg := ch.reserveBookingSlot(db, buyer.ID, svc, &profile, bookingSlotRequest{SlotStart: onGrid, PartySize: 1}); errMsg != "" {
		t.Errorf("expected the 10:00 WAT slot to be accepted, got %q", errMsg)
	}

	// 07:00 UTC == 08:00 WAT is before the window opens.
	toEarly := monday.Add(7 * time.Hour)
	if _, errMsg := ch.reserveBookingSlot(db, buyer.ID, svc, &profile, bookingSlotRequest{SlotStart: toEarly, PartySize: 1}); errMsg == "" {
		t.Error("expected a slot before the window opens to be rejected")
	}

	// 08:30 UTC == 09:30 WAT sits off the 60-minute grid.
	offGrid := monday.Add(8*time.Hour + 30*time.Minute)
	if _, errMsg := ch.reserveBookingSlot(db, buyer.ID, svc, &profile, bookingSlotRequest{SlotStart: offGrid, PartySize: 1}); errMsg == "" {
		t.Error("expected an off-grid slot to be rejected")
	}
}

// nextWeekdayUTC is midnight UTC of the first given weekday at least minDays after now.
func nextWeekdayUTC(now time.Time, day time.Weekday, minDays int) time.Time {
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, minDays)
	for d.Weekday() != day {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// Slot bookings are limited to MaxBookingLeadDays ahead (owner 2026-10-27): the order escrow gives
// a seller at most 30 days to deliver, and a slot is delivered on the day.
func TestReserveBookingSlotRefusesBeyondLeadTime(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := &CheckoutHandler{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	for dow := 0; dow < 7; dow++ {
		writeAvailability(t, db, svc.ID, dow, "00:00", "23:00")
	}
	profile := models.ServiceProfile{Timezone: "UTC", SlotDurationMinutes: 60, MaxCapacityPerSlot: 5}

	today := time.Now().UTC().Truncate(time.Hour)
	inside := today.Add(time.Duration(MaxBookingLeadDays)*24*time.Hour - 2*time.Hour)
	if _, errMsg := ch.reserveBookingSlot(db, buyer.ID, svc, &profile, bookingSlotRequest{SlotStart: inside, PartySize: 1}); errMsg != "" {
		t.Fatalf("a slot inside the lead time must be accepted, got %q", errMsg)
	}
	beyond := today.Add(time.Duration(MaxBookingLeadDays)*24*time.Hour + 2*time.Hour)
	_, errMsg := ch.reserveBookingSlot(db, buyer.ID, svc, &profile, bookingSlotRequest{SlotStart: beyond, PartySize: 1})
	if errMsg != "Bookings can be made up to 28 days ahead" {
		t.Fatalf("a slot beyond the lead time must be refused, got %q", errMsg)
	}
}

// The slot list never offers a slot checkout would refuse.
func TestGetServiceSlotsStopsAtLeadTime(t *testing.T) {
	db := setupCheckoutTestDB(t)
	h := &Handlers{}

	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := serviceCategory(t, db, models.ServiceTypeBooking)
	svc := createTestService(t, db, seller.ID, &cat.ID, 20.0, 3.0)
	writeSlotProfile(t, db, svc.ID, "UTC", 60, 1)
	for dow := 0; dow < 7; dow++ {
		writeAvailability(t, db, svc.ID, dow, "09:00", "10:00")
	}

	from := time.Now().UTC().AddDate(0, 0, MaxBookingLeadDays-3)
	to := time.Now().UTC().AddDate(0, 0, MaxBookingLeadDays+5)
	w := getSlots(t, h, svc.ID, from.Format("2006-01-02"), to.Format("2006-01-02"))
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var got slotsPayload
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(got.Data.Slots) == 0 {
		t.Fatal("expected some slots inside the lead time")
	}
	horizon := maxBookingSlotStart(time.Now())
	for _, slot := range got.Data.Slots {
		if slot.Start.After(horizon) {
			t.Fatalf("slot %s is beyond the %d-day lead time", slot.Start.Format(time.RFC3339), MaxBookingLeadDays)
		}
	}
}
