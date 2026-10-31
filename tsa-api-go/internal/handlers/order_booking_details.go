package handlers

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// orderBookingDetails is what a booking order booked, as the lines both parties
// read on the order: the slot, how many places or nights, then the buyer's
// booking-form answers. Built here once so every client shows the same thing
// and a merchant's own form fields keep their labels.
type orderBookingDetails struct {
	Mode  string             `json:"mode"`
	Lines []orderBookingLine `json:"lines"`
}

type orderBookingLine struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// unitNouns is what one unit of a per-unit price is called (app: utils/priceUnit.ts).
var unitNouns = map[string]string{
	models.PriceUnitPerHour:    "hour",
	models.PriceUnitPerDay:     "day",
	models.PriceUnitPerNight:   "night",
	models.PriceUnitPerMonth:   "month",
	models.PriceUnitPerSession: "session",
	models.PriceUnitPerSeat:    "seat",
	models.PriceUnitPerTicket:  "ticket",
}

// bookingDetailsFor reads a service order's booking, or nil when it booked
// nothing (a contact or menu order, or a legacy one with no answers or slot).
func bookingDetailsFor(db *gorm.DB, order *models.Order) *orderBookingDetails {
	if order.Kind != models.OrderKindService {
		return nil
	}
	answers := order.GetServiceAnswers()
	var slot models.ServiceBooking
	hasSlot := db.Where("order_id = ?", order.ID).First(&slot).Error == nil

	var product models.Product
	if err := db.First(&product, "id = ?", order.ProductID).Error; err != nil {
		return nil
	}
	serviceType, profile := resolveServiceTypeAndProfile(db, &product)
	if serviceType != models.ServiceTypeBooking && len(answers) == 0 {
		return nil
	}

	mode := serviceBookingMode(db, &product)
	d := &orderBookingDetails{Mode: mode}
	add := func(key, label, value string) {
		if value != "" {
			d.Lines = append(d.Lines, orderBookingLine{Key: key, Label: label, Value: value})
		}
	}

	// A stay's dates are its check-in/check-out answers, and an unslotted seat
	// booking's row holds a capacity window, not a time: only a slot is shown.
	if hasSlot && mode != models.BookingModeDateRange && profile.SlotDurationMinutes > 0 {
		add("_slot", "When", formatBookedSlot(slot.SlotStart, slot.SlotEnd, profile.Location()))
	}
	switch mode {
	case models.BookingModeQuantity:
		add("_units", unitsLabel(profile.PriceUnit, "seat"), strconv.Itoa(max(order.Quantity, 1)))
	case models.BookingModeDateRange:
		noun := "night"
		if profile.PriceUnit == models.PriceUnitPerDay || profile.PriceUnit == models.PriceUnitPerMonth {
			noun = unitNouns[profile.PriceUnit]
		}
		add("_units", unitsLabel("", noun), strconv.Itoa(max(order.Quantity, 1)))
	default:
		if hasSlot && slot.PartySize > 1 {
			add("_units", "Places", strconv.Itoa(slot.PartySize))
		}
	}

	// Answers in the order the listing's form asks them; any it no longer asks
	// (the form changed since) follow under their catalogue label.
	form, _ := serviceBookingForm(db, &product, &profile)
	seen := map[string]bool{}
	if form != nil {
		for _, f := range form.Fields {
			if v, ok := answers[f.Key]; ok {
				seen[f.Key] = true
				add(f.Key, f.Label, formatBookingAnswer(f.Type, v))
			}
		}
	}
	var rest []string
	for k := range answers {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	if len(rest) > 0 {
		sort.Strings(rest)
		catalog := loadFieldCatalog(db)
		for _, k := range rest {
			label, typ := k, models.BookingFieldText
			if def, ok := catalog[k]; ok {
				label, typ = def.Label, def.Type
			}
			add(k, label, formatBookingAnswer(typ, answers[k]))
		}
	}
	if len(d.Lines) == 0 {
		return nil
	}
	return d
}

// unitsLabel names a count of units: "Seats", "Nights", or the fallback's plural.
func unitsLabel(priceUnit, fallback string) string {
	noun, ok := unitNouns[priceUnit]
	if !ok {
		noun = fallback
	}
	return strings.ToUpper(noun[:1]) + noun[1:] + "s"
}

// formatBookedSlot reads a slot in the listing's own zone: "Tue 3 Nov 2026, 14:00–15:00".
func formatBookedSlot(start, end time.Time, loc *time.Location) string {
	s, e := start.In(loc), end.In(loc)
	if !e.After(s) {
		return s.Format("Mon 2 Jan 2006, 15:04")
	}
	if s.YearDay() == e.YearDay() && s.Year() == e.Year() {
		return s.Format("Mon 2 Jan 2006, 15:04") + "–" + e.Format("15:04")
	}
	return s.Format("Mon 2 Jan 2006, 15:04") + " – " + e.Format("Mon 2 Jan 2006, 15:04")
}

// formatBookingAnswer is one stored answer as display text, "" when unanswered.
func formatBookingAnswer(fieldType string, v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case bool:
		if x {
			return "Yes"
		}
		return "No"
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []interface{}:
		parts := make([]string, 0, len(x))
		for _, p := range x {
			if s := formatBookingAnswer(models.BookingFieldText, p); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case string:
		s := strings.TrimSpace(x)
		if fieldType == models.BookingFieldDate {
			if t, err := time.Parse("2006-01-02", s); err == nil {
				return t.Format("Mon 2 Jan 2006")
			}
		}
		return s
	default:
		return strings.TrimSpace(fmt.Sprint(x))
	}
}
