package services

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func reconTime(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v.UTC()
}

func reconAt(s string) *time.Time { v := reconTime(s); return &v }

// reconPayment is payIn() under ref in mode, made at created and — when paid is
// set — succeeded at paid. Times are written in UTC: SQLite compares the text.
func reconPayment(t *testing.T, s *PayPaymentService, m *models.PayMerchant, mode, ref string, created time.Time, paid *time.Time) *models.PayPayment {
	t.Helper()
	in := payIn()
	in.Reference = ref
	p, _, err := s.Create(context.Background(), m, mode, in)
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{"created_at": created.UTC()}
	if paid != nil {
		fields["status"] = models.PayStatusSucceeded
		fields["succeeded_at"] = paid.UTC()
		fields["settlement_ref"] = "ord_" + ref
	}
	if err := s.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Updates(fields).Error; err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestParsePayDateRange_ReadsWATDaysWithTheEndDayIncluded(t *testing.T) {
	start, end, err := ParsePayDateRange("2026-10-01", "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	if !start.Equal(reconTime("2026-09-30T23:00:00Z")) || !end.Equal(reconTime("2026-10-31T23:00:00Z")) {
		t.Fatalf("start %v end %v: October in Lagos is [30 Sep 23:00Z, 31 Oct 23:00Z)", start, end)
	}
	start, end, err = ParsePayDateRange(" 2026-10-08 ", "2026-10-08")
	if err != nil || end.Sub(start) != 24*time.Hour {
		t.Fatalf("one day: %v %v %v", start, end, err)
	}
	if start, end, err := ParsePayDateRange("", ""); err != nil || !start.IsZero() || !end.IsZero() {
		t.Fatalf("no dates is no range: %v %v %v", start, end, err)
	}
	for _, bad := range [][2]string{
		{"2026-10-01", ""}, {"", "2026-10-31"}, {"2026-10-31", "2026-10-01"}, {"2026-02-30", "2026-03-01"},
		{"2026-1-5", "2026-01-06"}, {"01/10/2026", "31/10/2026"}, {"2026-10-01T00:00:00Z", "2026-10-31"},
	} {
		if _, _, err := ParsePayDateRange(bad[0], bad[1]); !errors.Is(err, ErrPayDateRange) {
			t.Errorf("%q–%q: err %v, want ErrPayDateRange", bad[0], bad[1], err)
		}
	}
}

func TestPayPayment_ListFiltersByTheDayItWasMadeInWAT(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	reconPayment(t, s, m, models.PayModeLive, "SEP-30-LATE", reconTime("2026-09-30T22:59:59Z"), nil)           // 23:59:59 WAT, 30 Sep
	first := reconPayment(t, s, m, models.PayModeLive, "OCT-01-START", reconTime("2026-09-30T23:00:00Z"), nil) // 00:00 WAT, 1 Oct
	last := reconPayment(t, s, m, models.PayModeLive, "OCT-31-LATE", reconTime("2026-10-31T22:59:59Z"), nil)   // 23:59:59 WAT, 31 Oct
	reconPayment(t, s, m, models.PayModeLive, "NOV-01-START", reconTime("2026-10-31T23:00:00Z"), nil)          // 00:00 WAT, 1 Nov
	start, end, err := ParsePayDateRange("2026-10-01", "2026-10-31")
	if err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive, From: start, To: end})
	if err != nil || total != 2 || len(rows) != 2 || rows[0].ID != last.ID || rows[1].ID != first.ID {
		t.Fatalf("err %v total %d rows %+v: October is exactly the two payments made 1–31 Oct, Lagos time", err, total, rows)
	}
	if _, total, _ := s.List(ctx, PayListFilter{MerchantID: &m.ID, Mode: models.PayModeLive}); total != 4 {
		t.Fatalf("no dates: total %d", total)
	}
}
