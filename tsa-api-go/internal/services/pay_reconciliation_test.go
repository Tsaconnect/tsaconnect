package services

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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

// fakeRefundAgg stands in for Plan 3a's refund service.
type fakeRefundAgg struct {
	byPayment        map[uuid.UUID]PayRefundAgg
	totals           PaySummaryRefunds
	gotStart, gotEnd time.Time
	calls            [][]uuid.UUID
	failCall         int // the 1-based AggregateByPayment call that errors; 0 never
}

var errFakeRefundAgg = errors.New("refund lookup failed")

func (f *fakeRefundAgg) AggregateByPayment(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]PayRefundAgg, error) {
	f.calls = append(f.calls, append([]uuid.UUID(nil), ids...))
	if len(f.calls) == f.failCall {
		return nil, errFakeRefundAgg
	}
	out := map[uuid.UUID]PayRefundAgg{}
	for _, id := range ids {
		if a, ok := f.byPayment[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (f *fakeRefundAgg) TotalsConfirmed(_ context.Context, _ uuid.UUID, _ string, start, end time.Time) (PaySummaryRefunds, error) {
	f.gotStart, f.gotEnd = start, end
	return f.totals, nil
}

func TestPayReconciliation_SummaryCountsMoneyByPaidDayAndStatusesByMadeDay(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	live := models.PayModeLive
	sepMade := reconPayment(t, s, m, live, "MADE-SEP-PAID-OCT", reconTime("2026-09-30T12:00:00Z"), reconAt("2026-09-30T23:30:00Z")) // paid 00:30 WAT 1 Oct
	reconPayment(t, s, m, live, "MADE-PAID-OCT", reconTime("2026-10-05T10:00:00Z"), reconAt("2026-10-05T10:05:00Z"))
	reconPayment(t, s, m, live, "WAITING", reconTime("2026-10-20T10:00:00Z"), nil)
	called := reconPayment(t, s, m, live, "CALLED-OFF", reconTime("2026-10-21T10:00:00Z"), nil)
	s.db.Model(&models.PayPayment{}).Where("id = ?", called.ID).Updates(map[string]any{"status": models.PayStatusExpired, "failure_reason": PayReasonMerchantCancelled})
	octMade := reconPayment(t, s, m, live, "MADE-OCT-PAID-NOV", reconTime("2026-10-22T10:00:00Z"), reconAt("2026-10-31T23:30:00Z")) // paid 00:30 WAT 1 Nov
	// Distinct amounts, so the money sum says which date it was filtered on.
	s.db.Model(&models.PayPayment{}).Where("id = ?", sepMade.ID).Updates(map[string]any{"customer_total_kobo": 1_111_111, "merchant_net_kobo": 1_000_000})
	s.db.Model(&models.PayPayment{}).Where("id = ?", octMade.ID).Updates(map[string]any{"customer_total_kobo": 7_777_777, "merchant_net_kobo": 7_000_000})
	scan := reconPayment(t, s, m, live, "UNTRIED-SCAN", reconTime("2026-10-23T10:00:00Z"), nil)
	s.db.Model(&models.PayPayment{}).Where("id = ?", scan.ID).Update("source", models.PaySourceOpen)
	reconPayment(t, s, m, models.PayModeTest, "TEST", reconTime("2026-10-05T10:00:00Z"), reconAt("2026-10-05T10:05:00Z"))
	reconPayment(t, s, seedPayMerchant(t, s.db), live, "STRANGER", reconTime("2026-10-05T10:00:00Z"), reconAt("2026-10-05T10:05:00Z"))

	start, end, _ := ParsePayDateRange("2026-10-01", "2026-10-31")
	sum, err := s.Summary(ctx, m.ID, live, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Succeeded != (PaySummarySucceeded{Count: 2, CustomerTotalKobo: 5_050_506 + 1_111_111, MerchantNetKobo: 5_000_000 + 1_000_000}) {
		t.Fatalf("succeeded %+v: the two paid 1–31 Oct Lagos time, whenever they were made", sum.Succeeded)
	}
	want := map[string]int64{"succeeded": 2, "pending": 1, "cancelled": 1}
	if len(sum.ByStatus) != len(want) {
		t.Fatalf("byStatus %v, want %v", sum.ByStatus, want)
	}
	for k, v := range want {
		if sum.ByStatus[k] != v {
			t.Fatalf("byStatus %v, want %v", sum.ByStatus, want)
		}
	}
	raw, _ := json.Marshal(sum)
	if !strings.Contains(string(raw), `"refunds":{"count":0,"amount":"0.00"}`) ||
		!strings.Contains(string(raw), `"succeeded":{"count":2,"customerTotal":6161617,"merchantNet":6000000}`) {
		t.Fatalf("json %s", raw)
	}

	agg := &fakeRefundAgg{totals: PaySummaryRefunds{Count: 1, AmountMicroUSD: 12_500_000}}
	s.SetRefundAggregator(agg)
	sum, err = s.Summary(ctx, m.ID, live, start, end)
	if err != nil || sum.Refunds != agg.totals || !agg.gotStart.Equal(start) || !agg.gotEnd.Equal(end) {
		t.Fatalf("refunds %+v err %v; aggregator asked for %v–%v", sum.Refunds, err, agg.gotStart, agg.gotEnd)
	}
	if raw, _ := json.Marshal(sum); !strings.Contains(string(raw), `"refunds":{"count":1,"amount":"12.50"}`) {
		t.Fatalf("json %s", raw)
	}
	if all, _ := s.Summary(ctx, m.ID, live, time.Time{}, time.Time{}); all.Succeeded.Count != 3 {
		t.Fatalf("no dates is all time: %+v", all.Succeeded)
	}
}

func TestPayPayment_PaidDayReadsHaveTheirIndex(t *testing.T) {
	db := newPayTestDB(t)
	var ddl string
	if err := db.Raw(`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_pay_payments_merchant_mode_succeeded'`).Scan(&ddl).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ReplaceAll(ddl, "`", ""), "(merchant_id,mode,succeeded_at)") {
		t.Fatalf("index columns: %q", ddl)
	}
}

func readExport(t *testing.T, raw string) [][]string {
	t.Helper()
	if !strings.HasPrefix(raw, "\uFEFF") {
		t.Fatalf("no UTF-8 BOM at the start: %q", raw[:min(len(raw), 24)])
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(raw, "\uFEFF"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPayReconciliation_ExportIsThePaymentsPaidInTheRangeOldestFirst(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	live := models.PayModeLive
	a := reconPayment(t, s, m, live, "PNR-A", reconTime("2026-09-30T12:00:00Z"), reconAt("2026-09-30T23:30:00Z")) // paid 00:30 WAT, 1 Oct
	b := reconPayment(t, s, m, live, "PNR-B", reconTime("2026-10-02T09:00:00Z"), reconAt("2026-10-02T10:00:00Z"))
	s.db.Model(&models.PayPayment{}).Where("id = ?", b.ID).Updates(map[string]any{
		"reference": "+SUM(A1)", "description": `=HYPERLINK("https://x")`, "customer_name": "@Tolu"})
	c := reconPayment(t, s, m, live, "PNR-C", reconTime("2026-10-03T09:00:00Z"), reconAt("2026-10-03T10:00:00Z"))
	s.db.Model(&models.PayPayment{}).Where("id = ?", c.ID).Updates(map[string]any{
		"reference": "-100", "description": "Seat 4A, \"window\"\nsecond line", "customer_name": "\tAda"})
	reconPayment(t, s, m, live, "PNR-NOV", reconTime("2026-10-31T20:00:00Z"), reconAt("2026-10-31T23:00:00Z")) // paid 00:00 WAT, 1 Nov
	reconPayment(t, s, m, live, "PNR-WAITING", reconTime("2026-10-05T09:00:00Z"), nil)
	reconPayment(t, s, m, models.PayModeTest, "PNR-TEST", reconTime("2026-10-05T09:00:00Z"), reconAt("2026-10-05T09:01:00Z"))
	start, end, _ := ParsePayDateRange("2026-10-01", "2026-10-31")

	var buf bytes.Buffer
	if err := s.ExportCSV(ctx, &buf, m.ID, live, start, end); err != nil {
		t.Fatal(err)
	}
	rows := readExport(t, buf.String())
	if len(rows) != 4 || strings.Join(rows[0], ",") != strings.Join(payExportHeader, ",") {
		t.Fatalf("rows %q", rows)
	}
	want := []string{"2026-10-01T00:30:00+01:00", "2026-09-30T13:00:00+01:00", "", "PNR-A", "LOS-ABV 12 Oct", "", "succeeded",
		"50000.00", "50505.06", "50000.00", "505.06", "ord_PNR-A", "0.00", "none", "api", a.ID.String()}
	if strings.Join(rows[1], "|") != strings.Join(want, "|") {
		t.Fatalf("row A:\n got %q\nwant %q", rows[1], want)
	}
	if rows[2][15] != b.ID.String() || rows[2][3] != "'+SUM(A1)" || rows[2][4] != `'=HYPERLINK("https://x")` || rows[2][5] != "'@Tolu" {
		t.Fatalf("row B must have its formula-looking text quoted: %q", rows[2])
	}
	if rows[3][15] != c.ID.String() || rows[3][3] != "'-100" || rows[3][4] != "Seat 4A, \"window\"\nsecond line" || rows[3][5] != "'\tAda" {
		t.Fatalf("row C: commas, quotes and newlines stay one cell; a leading - or tab is quoted: %q", rows[3])
	}
}

func TestPayReconciliation_ExportRefusesTooManyRowsBeforeWritingAByte(t *testing.T) {
	prev := payExportMaxRows
	payExportMaxRows = 1
	t.Cleanup(func() { payExportMaxRows = prev })
	s, m := newPayPaymentRig(t)
	reconPayment(t, s, m, models.PayModeLive, "ONE", reconTime("2026-10-02T09:00:00Z"), reconAt("2026-10-02T10:00:00Z"))
	reconPayment(t, s, m, models.PayModeLive, "TWO", reconTime("2026-10-03T09:00:00Z"), reconAt("2026-10-03T10:00:00Z"))
	start, end, _ := ParsePayDateRange("2026-10-01", "2026-10-31")
	var buf bytes.Buffer
	if err := s.ExportCSV(context.Background(), &buf, m.ID, models.PayModeLive, start, end); !errors.Is(err, ErrPayExportTooLarge) || buf.Len() != 0 {
		t.Fatalf("err %v, %d bytes written", err, buf.Len())
	}
}

func TestPayReconciliation_ExportNeedsARangeOfAtMost366Days(t *testing.T) {
	ctx := context.Background()
	s, m := newPayPaymentRig(t)
	var buf bytes.Buffer
	if err := s.ExportCSV(ctx, &buf, m.ID, models.PayModeLive, time.Time{}, time.Time{}); !errors.Is(err, ErrPayDateRange) || buf.Len() != 0 {
		t.Fatalf("no range: err %v, %d bytes", err, buf.Len())
	}
	start, end, _ := ParsePayDateRange("2024-01-01", "2024-12-31") // 366 days: a leap year
	if err := s.ExportCSV(ctx, &buf, m.ID, models.PayModeLive, start, end); err != nil {
		t.Fatalf("366 days: %v", err)
	}
	buf.Reset()
	start, end, _ = ParsePayDateRange("2024-01-01", "2025-01-01") // 367 days
	if err := s.ExportCSV(ctx, &buf, m.ID, models.PayModeLive, start, end); !errors.Is(err, ErrPayDateRange) || buf.Len() != 0 {
		t.Fatalf("367 days: err %v, %d bytes", err, buf.Len())
	}
}

func TestPayReconciliation_ExportWalksChunksWithoutLosingTiesAndAsksRefundsPerChunk(t *testing.T) {
	prev := payExportChunk
	payExportChunk = 2
	t.Cleanup(func() { payExportChunk = prev })
	s, m := newPayPaymentRig(t)
	agg := &fakeRefundAgg{byPayment: map[uuid.UUID]PayRefundAgg{}}
	s.SetRefundAggregator(agg)
	var ids []uuid.UUID
	for i, paid := range []string{"2026-10-02T10:00:00Z", "2026-10-02T10:00:00Z", "2026-10-02T10:00:00Z", "2026-10-01T08:00:00Z", "2026-10-03T08:00:00Z"} {
		p := reconPayment(t, s, m, models.PayModeLive, fmt.Sprintf("TIE-%d", i), reconTime("2026-10-01T07:00:00Z"), reconAt(paid))
		ids = append(ids, p.ID)
	}
	agg.byPayment[ids[1]] = PayRefundAgg{RefundedMicroUSD: 2_500_000, CapMicroUSD: 50_505_060, Status: "partial"}
	start, end, _ := ParsePayDateRange("2026-10-01", "2026-10-31")
	var buf bytes.Buffer
	if err := s.ExportCSV(context.Background(), &buf, m.ID, models.PayModeLive, start, end); err != nil {
		t.Fatal(err)
	}
	rows := readExport(t, buf.String())[1:]
	if len(rows) != 5 {
		t.Fatalf("%d rows, want 5", len(rows))
	}
	seen := map[string]bool{}
	for i, r := range rows {
		if seen[r[15]] {
			t.Fatalf("payment %s exported twice", r[15])
		}
		seen[r[15]] = true
		if i > 0 && r[0] < rows[i-1][0] {
			t.Fatalf("not oldest first: %q after %q", r[0], rows[i-1][0])
		}
		if r[15] == ids[1].String() && (r[12] != "2.50" || r[13] != "partial") {
			t.Fatalf("refund columns: %q", r)
		}
	}
	if len(agg.calls) != 3 || len(agg.calls[0]) != 2 || len(agg.calls[1]) != 2 || len(agg.calls[2]) != 1 {
		t.Fatalf("refund lookups per chunk: %v", agg.calls)
	}
}

// A failure reading the first chunk (or its refunds) must leave the response
// unwritten, so the caller still answers JSON; a later one is returned after
// the file has started.
func TestPayReconciliation_ExportReadsTheFirstChunkBeforeWritingAByte(t *testing.T) {
	prev := payExportChunk
	payExportChunk = 2
	t.Cleanup(func() { payExportChunk = prev })
	s, m := newPayPaymentRig(t)
	for i, paid := range []string{"2026-10-02T10:00:00Z", "2026-10-03T10:00:00Z", "2026-10-04T10:00:00Z"} {
		reconPayment(t, s, m, models.PayModeLive, fmt.Sprintf("CHUNK-%d", i), reconTime("2026-10-01T07:00:00Z"), reconAt(paid))
	}
	start, end, _ := ParsePayDateRange("2026-10-01", "2026-10-31")

	s.SetRefundAggregator(&fakeRefundAgg{failCall: 1})
	var buf bytes.Buffer
	if err := s.ExportCSV(context.Background(), &buf, m.ID, models.PayModeLive, start, end); !errors.Is(err, errFakeRefundAgg) || buf.Len() != 0 {
		t.Fatalf("first chunk: err %v, %d bytes written; want the error and nothing written", err, buf.Len())
	}

	s.SetRefundAggregator(&fakeRefundAgg{failCall: 2})
	buf.Reset()
	if err := s.ExportCSV(context.Background(), &buf, m.ID, models.PayModeLive, start, end); !errors.Is(err, errFakeRefundAgg) || buf.Len() == 0 {
		t.Fatalf("second chunk: err %v, %d bytes written; want the error after the file started", err, buf.Len())
	}
}
