package models

import (
	"testing"
	"time"
)

func TestStayUnits(t *testing.T) {
	lagos, _ := time.LoadLocation("Africa/Lagos")
	day := func(s string) time.Time {
		d, _ := time.ParseInLocation("2006-01-02", s, lagos)
		return d
	}
	cases := []struct {
		unit, in, out string
		want          int
	}{
		{PriceUnitPerNight, "2026-11-10", "2026-11-13", 3},
		{"", "2026-11-10", "2026-11-11", 1},
		{PriceUnitPerDay, "2026-12-30", "2027-01-02", 3},
		{PriceUnitPerMonth, "2026-11-10", "2026-12-10", 1},
		{PriceUnitPerMonth, "2026-11-10", "2026-12-11", 2}, // a started month counts
		{PriceUnitPerMonth, "2026-01-31", "2026-02-28", 1},
	}
	for _, c := range cases {
		if got := StayUnits(c.unit, day(c.in), day(c.out)); got != c.want {
			t.Errorf("StayUnits(%q, %s, %s) = %d, want %d", c.unit, c.in, c.out, got, c.want)
		}
	}
}

func TestChargesPerUnit(t *testing.T) {
	cases := []struct {
		mode, unit string
		want       bool
	}{
		{BookingModeQuantity, PriceUnitPerSeat, true},
		{BookingModeQuantity, "", true},
		{BookingModeQuantity, PriceUnitFixed, false},
		{BookingModeDateRange, PriceUnitPerNight, true},
		{BookingModeDateRange, PriceUnitPerMonth, true},
		{BookingModeDateRange, "", true},
		{BookingModeDateRange, PriceUnitPerSeat, false},
		{BookingModeDateRange, PriceUnitFixed, false},
		{BookingModeSlot, PriceUnitPerHour, false},
	}
	for _, c := range cases {
		if got := ChargesPerUnit(c.mode, c.unit); got != c.want {
			t.Errorf("ChargesPerUnit(%s, %q) = %v, want %v", c.mode, c.unit, got, c.want)
		}
	}
}
