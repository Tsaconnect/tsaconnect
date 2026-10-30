package handlers

import (
	"fmt"
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

// answerLabel names a field in a message.
func answerLabel(f models.BookingField) string {
	if f.Label != "" {
		return f.Label
	}
	return f.Key
}

// reserveStay holds one unit (room, property, item) for every night of stay.
// MaxCapacityPerSlot is the units available per night (0 = unlimited); a night
// already holding that many live bookings refuses the stay.
func reserveStay(tx *gorm.DB, buyerID uuid.UUID, product *models.Product, profile *models.ServiceProfile, stay *stayDates) (*models.ServiceBooking, string) {
	if profile.MaxCapacityPerSlot > 0 {
		// Serialise stays of the same listing: the per-night check reads every
		// overlapping booking and must not race another stay into the same nights.
		if tx.Dialector.Name() == "postgres" {
			tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", "svcstay:"+product.ID.String())
		}
		var held []models.ServiceBooking
		tx.Model(&models.ServiceBooking{}).
			Joins("JOIN orders ON orders.id = service_bookings.order_id").
			Where("service_bookings.product_id = ? AND service_bookings.slot_start < ? AND service_bookings.slot_end > ?",
				product.ID, stay.checkOut.UTC(), stay.checkIn.UTC()).
			Where("orders.status NOT IN ?", []string{models.OrderStatusCancelled, models.OrderStatusRefunded}).
			Select("service_bookings.slot_start, service_bookings.slot_end, service_bookings.party_size").
			Find(&held)
		for night := stay.checkIn; night.Before(stay.checkOut); night = night.AddDate(0, 0, 1) {
			taken := 0
			for _, b := range held {
				if b.SlotStart.Before(night.AddDate(0, 0, 1)) && b.SlotEnd.After(night) {
					taken += b.PartySize
				}
			}
			if taken+1 > profile.MaxCapacityPerSlot {
				return nil, fmt.Sprintf("%s is fully booked on %s", product.Name, night.Format("2 Jan 2006"))
			}
		}
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
