package handlers

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// maxBookingUnits caps the seats/tickets one booking can buy.
const maxBookingUnits = 50

// maxStayNights caps one date-range booking.
const maxStayNights = 366

// stayFieldPairs are the answer keys a date-range booking reads its stay from,
// in preference order: the first pair whose start key is on the form wins.
var stayFieldPairs = [][2]models.BookingField{
	{
		{Key: "checkin", Type: models.BookingFieldDate, Label: "Check-in", Group: models.BookingGroupDateTime},
		{Key: "checkout", Type: models.BookingFieldDate, Label: "Check-out", Group: models.BookingGroupDateTime},
	},
	{
		{Key: "start_date", Type: models.BookingFieldDate, Label: "Start date", Group: models.BookingGroupDateTime},
		{Key: "end_date", Type: models.BookingFieldDate, Label: "End date", Group: models.BookingGroupDateTime},
	},
}

// stayPair returns the start/end keys cfg reads the stay from.
func stayPair(cfg *models.BookingFieldConfig) (start, end string) {
	if cfg != nil {
		for _, pair := range stayFieldPairs {
			for _, f := range cfg.Fields {
				if f.Key == pair[0].Key {
					return pair[0].Key, pair[1].Key
				}
			}
		}
	}
	return stayFieldPairs[0][0].Key, stayFieldPairs[0][1].Key
}

// withStayFields returns a copy of cfg in which the stay's start and end are
// required date fields. A missing one is added next to its partner, or both at
// the top (check-in/check-out unless the form uses start/end dates). A
// date-range price cannot be worked out without both dates.
func withStayFields(cfg *models.BookingFieldConfig) *models.BookingFieldConfig {
	out := &models.BookingFieldConfig{}
	if cfg != nil {
		out.Fields = append(out.Fields, cfg.Fields...)
	}
	startKey, _ := stayPair(out)
	var pair [2]models.BookingField
	for _, p := range stayFieldPairs {
		if p[0].Key == startKey {
			pair = p
		}
	}
	index := func(key string) int {
		for i, f := range out.Fields {
			if f.Key == key {
				return i
			}
		}
		return -1
	}
	insert := func(at int, f models.BookingField) {
		f.Required = true
		out.Fields = append(out.Fields[:at], append([]models.BookingField{f}, out.Fields[at:]...)...)
	}
	if index(pair[0].Key) < 0 {
		insert(0, pair[0])
	}
	if index(pair[1].Key) < 0 {
		insert(index(pair[0].Key)+1, pair[1])
	}
	for _, key := range []string{pair[0].Key, pair[1].Key} {
		f := &out.Fields[index(key)]
		f.Type = models.BookingFieldDate
		f.Required = true
	}
	return out
}

// stayDates is a parsed date-range booking: local midnights in the service's
// zone, checkOut exclusive.
type stayDates struct {
	checkIn, checkOut time.Time
}

// bookingInput is what a booking buys beyond the listing itself, resolved once
// from the request by resolveBooking and handed to the pricing.
type bookingInput struct {
	mode string
	// units multiplies a per-unit price: seats in quantity mode, nights/days/
	// months in date_range mode; 0 means one booking at the listing price.
	units int
	stay  *stayDates
	// seatDay is the local day a capacity-limited seat/ticket booking without
	// slots holds its places on: the form's date answer. nil books against the
	// listing's whole run (a one-off event whose form asks no date).
	seatDay *time.Time
}

// resolveBooking reads what a booking request buys from its category's mode:
// a quantity of seats/tickets, or a stay from the form's dates. Non-booking
// services and slot mode resolve to a plain one-off booking. answers is nil on
// paths that carry no booking form (the cart), which cannot book a stay. A
// non-empty errMsg rejects the booking.
func resolveBooking(db *gorm.DB, product *models.Product, profile *models.ServiceProfile, serviceType string, quantity int, answers map[string]interface{}) (bookingInput, string) {
	if serviceType != models.ServiceTypeBooking {
		return bookingInput{}, ""
	}
	in := bookingInput{mode: serviceBookingMode(db, product)}
	switch in.mode {
	case models.BookingModeQuantity:
		switch {
		case quantity <= 0:
			in.units = 1
		case quantity > maxBookingUnits:
			return in, fmt.Sprintf("You can book at most %d at a time", maxBookingUnits)
		default:
			in.units = quantity
		}
		if profile.SlotDurationMinutes <= 0 && profile.MaxCapacityPerSlot > 0 {
			form, _ := serviceBookingForm(db, product, profile)
			if key := seatDateKey(form); key != "" {
				if answers == nil {
					return in, fmt.Sprintf("Book %s from its page so you can pick a date", product.Name)
				}
				day, msg := parseSeatDay(form, key, answers, profile)
				if msg != "" {
					return in, msg
				}
				in.seatDay = &day
			}
		}
	case models.BookingModeDateRange:
		if answers == nil {
			return in, fmt.Sprintf("Book %s from its page so you can pick your dates", product.Name)
		}
		form, _ := serviceBookingForm(db, product, profile)
		stay, msg := parseStay(form, answers, profile)
		if msg != "" {
			return in, msg
		}
		in.stay = stay
		in.units = models.StayUnits(profile.PriceUnit, stay.checkIn, stay.checkOut)
	}
	return in, ""
}

// parseStay reads and checks the stay dates from answers: both YYYY-MM-DD, the
// check-in today or later in the service's zone, the check-out after it, at most
// maxStayNights long. An instant stay must also END inside the booking lead,
// like a slot: the escrow gives a booking until its end plus the delivery grace,
// capped at the escrow window, so a later check-out would let the buyer reclaim
// mid-stay. A request is delivered when the merchant confirms it, so it is not
// capped (same rule as slots); long stays go through request mode.
func parseStay(form *models.BookingFieldConfig, answers map[string]interface{}, profile *models.ServiceProfile) (*stayDates, string) {
	startKey, endKey := stayPair(form)
	loc := profile.Location()
	day := func(key, label string) (time.Time, string) {
		s, _ := answers[key].(string)
		t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), loc)
		if err != nil {
			return time.Time{}, fmt.Sprintf("%s must be a date (YYYY-MM-DD)", label)
		}
		return t, ""
	}
	labels := map[string]string{}
	for _, f := range form.Fields {
		labels[f.Key] = answerLabel(f)
	}
	in, msg := day(startKey, labels[startKey])
	if msg != "" {
		return nil, msg
	}
	out, msg := day(endKey, labels[endKey])
	if msg != "" {
		return nil, msg
	}
	now := bookingNow().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	switch nights := models.StayNights(in, out); {
	case in.Before(today):
		return nil, fmt.Sprintf("%s is in the past", labels[startKey])
	case nights <= 0:
		return nil, fmt.Sprintf("%s must be after %s", labels[endKey], labels[startKey])
	case nights > maxStayNights:
		return nil, fmt.Sprintf("A booking can be at most %d nights", maxStayNights)
	}
	if profile.BookingType != models.BookingTypeRequest && out.After(bookingHorizon(bookingNow())) {
		return nil, fmt.Sprintf("Stays booked instantly must end within %d days", MaxBookingLeadDays)
	}
	return &stayDates{checkIn: in, checkOut: out}, ""
}

// seatDateKey is the answer a seat/ticket booking is dated by: the form's first
// date field (a coach's travel date, an event's date), or "" when it asks none.
func seatDateKey(form *models.BookingFieldConfig) string {
	if form == nil {
		return ""
	}
	for _, f := range form.Fields {
		if f.Type == models.BookingFieldDate {
			return f.Key
		}
	}
	return ""
}

// parseSeatDay reads the day a seat/ticket booking is for, a local midnight in
// the service's zone: today or later and, booked instantly, inside the booking
// lead — the order's escrow runs to the end of that day plus the delivery grace,
// as a slot's does, so a later day would let the buyer reclaim before it.
func parseSeatDay(form *models.BookingFieldConfig, key string, answers map[string]interface{}, profile *models.ServiceProfile) (time.Time, string) {
	label := key
	for _, f := range form.Fields {
		if f.Key == key {
			label = answerLabel(f)
		}
	}
	loc := profile.Location()
	s, _ := answers[key].(string)
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), loc)
	if err != nil {
		return time.Time{}, fmt.Sprintf("%s must be a date (YYYY-MM-DD)", label)
	}
	now := bookingNow().In(loc)
	if day.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)) {
		return time.Time{}, fmt.Sprintf("%s is in the past", label)
	}
	if profile.BookingType != models.BookingTypeRequest && day.AddDate(0, 0, 1).After(bookingHorizon(bookingNow())) {
		return time.Time{}, fmt.Sprintf("Bookings made instantly must be for a date within %d days", MaxBookingLeadDays)
	}
	return day, ""
}

// answerLabel names a field in a message.
func answerLabel(f models.BookingField) string {
	if f.Label != "" {
		return f.Label
	}
	return f.Key
}

// stayUnavailable says why stay cannot be held — a night already holding the
// listing's MaxCapacityPerSlot live bookings (units available per night,
// 0 = unlimited) — or "" when every night has room. It fails closed: a failed
// read refuses the stay rather than reading as empty nights.
func stayUnavailable(db *gorm.DB, product *models.Product, profile *models.ServiceProfile, stay *stayDates) string {
	if profile.MaxCapacityPerSlot <= 0 {
		return ""
	}
	var held []models.ServiceBooking
	if err := liveServiceBookings(db, product.ID).
		Where("service_bookings.slot_start < ? AND service_bookings.slot_end > ?", stay.checkOut.UTC(), stay.checkIn.UTC()).
		Select("service_bookings.slot_start, service_bookings.slot_end, service_bookings.party_size").
		Find(&held).Error; err != nil {
		log.Printf("[booking] stay availability unreadable for service %s: %v", product.ID, err)
		return "Could not check availability. Please try again."
	}
	for night := stay.checkIn; night.Before(stay.checkOut); night = night.AddDate(0, 0, 1) {
		taken := 0
		for _, b := range held {
			if b.SlotStart.Before(night.AddDate(0, 0, 1)) && b.SlotEnd.After(night) {
				taken += b.PartySize
			}
		}
		if taken+1 > profile.MaxCapacityPerSlot {
			return fmt.Sprintf("%s is fully booked on %s", product.Name, night.Format("2 Jan 2006"))
		}
	}
	return ""
}

// reserveStay holds one unit (room, property, item) for every night of stay,
// refusing it when any night is full (stayUnavailable).
func reserveStay(tx *gorm.DB, buyerID uuid.UUID, product *models.Product, profile *models.ServiceProfile, stay *stayDates) (*models.ServiceBooking, string) {
	if profile.MaxCapacityPerSlot > 0 && tx.Dialector.Name() == "postgres" {
		// Serialise stays of the same listing: the per-night check reads every
		// overlapping booking and must not race another stay into the same nights.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "svcstay:"+product.ID.String()).Error; err != nil {
			log.Printf("[booking] stay lock failed for service %s: %v", product.ID, err)
			return nil, "Could not check availability. Please try again."
		}
	}
	if msg := stayUnavailable(tx, product, profile, stay); msg != "" {
		return nil, msg
	}
	return &models.ServiceBooking{
		ID:        uuid.New(),
		ProductID: product.ID,
		BuyerID:   buyerID,
		SlotStart: stay.checkIn.UTC(),
		SlotEnd:   stay.checkOut.UTC(),
		PartySize: 1,
		Status:    models.ServiceBookingStatusReserved,
	}, ""
}

// reserveUnslotted holds what a booking without a time slot takes: a stay's
// nights, or seats/tickets against the listing's capacity. nil when the booking
// holds nothing (no limit, or not a stay or quantity booking).
func reserveUnslotted(tx *gorm.DB, buyerID uuid.UUID, product *models.Product, profile *models.ServiceProfile, bk bookingInput) (*models.ServiceBooking, string) {
	switch {
	case bk.stay != nil:
		return reserveStay(tx, buyerID, product, profile, bk.stay)
	case bk.mode == models.BookingModeQuantity && profile.SlotDurationMinutes <= 0:
		return reserveSeats(tx, buyerID, product, profile, bk)
	}
	return nil, ""
}

// seatWindow is the slot range a seat/ticket hold is stored under: its day, or
// zero times for an undated booking (a one-off event's whole run), which keeps
// its escrow on the listing's handling days.
func seatWindow(bk bookingInput) (start, end time.Time) {
	if bk.seatDay == nil {
		return time.Time{}, time.Time{}
	}
	return bk.seatDay.UTC(), bk.seatDay.AddDate(0, 0, 1).UTC()
}

// seatsUnavailable says why a seat/ticket booking without slots cannot have
// its units — the listing's MaxCapacityPerSlot (0 = unlimited) already taken on
// its day, or across every live undated booking — or "" when they fit. It fails
// closed on an unreadable count.
func seatsUnavailable(db *gorm.DB, product *models.Product, profile *models.ServiceProfile, bk bookingInput) string {
	if profile.MaxCapacityPerSlot <= 0 {
		return ""
	}
	start, end := seatWindow(bk)
	held := liveServiceBookings(db, product.ID)
	if bk.seatDay != nil {
		held = held.Where("service_bookings.slot_start < ? AND service_bookings.slot_end > ?", end, start)
	} else {
		held = held.Where("service_bookings.slot_start = ?", start)
	}
	var taken int64
	if err := held.Select("COALESCE(SUM(service_bookings.party_size),0)").Scan(&taken).Error; err != nil {
		log.Printf("[booking] seat availability unreadable for service %s: %v", product.ID, err)
		return "Could not check availability. Please try again."
	}
	if left := profile.MaxCapacityPerSlot - int(taken); max(bk.units, 1) > left {
		on := ""
		if bk.seatDay != nil {
			on = " on " + bk.seatDay.Format("2 Jan 2006")
		}
		if left <= 0 {
			return fmt.Sprintf("%s is sold out%s", product.Name, on)
		}
		return fmt.Sprintf("Only %d left for %s%s", left, product.Name, on)
	}
	return ""
}

// unslottedUnavailable is the availability check reserveUnslotted makes,
// without holding anything: a request is checked when it is submitted so the
// merchant is not asked to approve what is already full.
func unslottedUnavailable(db *gorm.DB, product *models.Product, profile *models.ServiceProfile, bk bookingInput) string {
	switch {
	case bk.stay != nil:
		return stayUnavailable(db, product, profile, bk.stay)
	case bk.mode == models.BookingModeQuantity && profile.SlotDurationMinutes <= 0:
		return seatsUnavailable(db, product, profile, bk)
	}
	return ""
}

// reserveSeats holds a seat/ticket booking's units, refusing them when they no
// longer fit (seatsUnavailable).
func reserveSeats(tx *gorm.DB, buyerID uuid.UUID, product *models.Product, profile *models.ServiceProfile, bk bookingInput) (*models.ServiceBooking, string) {
	if profile.MaxCapacityPerSlot <= 0 {
		return nil, ""
	}
	if tx.Dialector.Name() == "postgres" {
		// The count must not race another booking into the same places.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "svcseats:"+product.ID.String()).Error; err != nil {
			log.Printf("[booking] seat lock failed for service %s: %v", product.ID, err)
			return nil, "Could not check availability. Please try again."
		}
	}
	if msg := seatsUnavailable(tx, product, profile, bk); msg != "" {
		return nil, msg
	}
	start, end := seatWindow(bk)
	units := max(bk.units, 1)
	return &models.ServiceBooking{
		ID:        uuid.New(),
		ProductID: product.ID,
		BuyerID:   buyerID,
		SlotStart: start,
		SlotEnd:   end,
		PartySize: units,
		Status:    models.ServiceBookingStatusReserved,
	}, ""
}

// answersOrEmpty makes a missing answers payload an empty one: a path that
// carries a booking form always has answers, even when none were given.
func answersOrEmpty(a map[string]interface{}) map[string]interface{} {
	if a == nil {
		return map[string]interface{}{}
	}
	return a
}

// bookingRequestQuantity is the seat count a request keeps; a stay's length is
// read again from its answers at pay.
func bookingRequestQuantity(bk bookingInput) int {
	if bk.mode == models.BookingModeQuantity {
		return bk.units
	}
	return 0
}
