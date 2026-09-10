package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// ─── test rig ──────────────────────────────────────────────────────────────

// fakeInboundCredit is one row of the DB side, as the ingestor would have
// written it: the amount and the provider-assigned transaction date.
type fakeInboundCredit struct {
	amountKobo int64
	at         time.Time
}

// fakeInboundReconcileSource models BOTH sides in memory. SumIngestedKobo
// really filters by the window it is handed rather than returning a canned
// number, so every test also asserts that the reconciler asks for the window
// it claims to be reconciling — the window is the part most likely to be
// wrong.
type fakeInboundReconcileSource struct {
	credits []fakeInboundCredit
	sumErr  error

	statement []services.FWStatementEntry
	fetchErr  error

	sumFrom, sumTo     time.Time
	fetchFrom, fetchTo time.Time
	fetchCalls         int
}

func (f *fakeInboundReconcileSource) SumIngestedKobo(_ context.Context, _ string, from, to time.Time) (int64, int, error) {
	f.sumFrom, f.sumTo = from, to
	if f.sumErr != nil {
		return 0, 0, f.sumErr
	}
	var sum int64
	var rows int
	for _, c := range f.credits {
		if !c.at.Before(from) && c.at.Before(to) {
			sum += c.amountKobo
			rows++
		}
	}
	return sum, rows, nil
}

func (f *fakeInboundReconcileSource) FetchWalletStatement(_ context.Context, _ string, from, to time.Time) ([]services.FWStatementEntry, error) {
	f.fetchCalls++
	f.fetchFrom, f.fetchTo = from, to
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	return f.statement, nil
}

func fwRow(rowType string, naira float64, at time.Time, ref string) services.FWStatementEntry {
	return services.FWStatementEntry{
		Direction:       "credit",
		Type:            rowType,
		Amount:          services.FWQuoteAmount(naira),
		Reference:       ref,
		Remarks:         "Received Money from GTBANK|ADEMU EMMANUEL",
		Currency:        "NGN",
		TransactionDate: services.FWStatementTime{Time: at},
	}
}

func funding(naira float64, at time.Time, ref string) services.FWStatementEntry {
	return fwRow("funding", naira, at, ref)
}

// newTestReconciler pins the clock so every test reasons about a fixed window.
// base is "now"; with the defaults the reconciled window is
// [now-65m, now-5m) and the fetch page runs [now-65m, now].
func newTestReconciler(src *fakeInboundReconcileSource, now time.Time) *InboundReconciler {
	rec := NewInboundReconciler(src, "NGN", 0)
	rec.now = func() time.Time { return now }
	return rec
}

// anchorRow is a row old enough to prove the fetched page reaches back past
// the start of the window. Without one the reconciler cannot tell a quiet
// account from a truncated page, and declines to compare.
func anchorRow(now time.Time) services.FWStatementEntry {
	return fwRow("settlement", 1, now.Add(-6*time.Hour), "anchor")
}

// ─── the five required behaviours ──────────────────────────────────────────

// Both sides agree → no gap, no alert, and the comparison actually ran.
func TestInboundReconciler_BothSidesAgree_NoAlert(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)

	src := &fakeInboundReconcileSource{
		credits: []fakeInboundCredit{
			{amountKobo: 200000, at: settled},
			{amountKobo: 550000, at: settled.Add(time.Minute)},
		},
		statement: []services.FWStatementEntry{
			anchorRow(now),
			funding(2000, settled, "ref-1"),
			funding(5500, settled.Add(time.Minute), "ref-2"),
		},
	}
	rec := newTestReconciler(src, now)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Checked != 1 {
		t.Fatalf("Checked = %d, want 1 (skip reason: %s)", res.Checked, res.SkipReason)
	}
	if res.GapKobo != 0 {
		t.Errorf("GapKobo = %d, want 0 (ingested=%d provider=%d)", res.GapKobo, res.IngestedKobo, res.ProviderKobo)
	}
	if res.ShortfallAlerts != 0 || res.SurplusAlerts != 0 {
		t.Errorf("alerts = %d/%d, want 0/0", res.ShortfallAlerts, res.SurplusAlerts)
	}
	if res.IngestedKobo != 750000 || res.ProviderKobo != 750000 {
		t.Errorf("sides = %d/%d, want 750000/750000", res.IngestedKobo, res.ProviderKobo)
	}
}

// We ingested LESS than Flutterwave credited → a customer paid and nobody has
// their money. The urgent direction.
func TestInboundReconciler_MissedCredit_AlertsShortfall(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)

	src := &fakeInboundReconcileSource{
		credits: []fakeInboundCredit{
			{amountKobo: 200000, at: settled},
		},
		statement: []services.FWStatementEntry{
			anchorRow(now),
			funding(2000, settled, "ref-1"),
			funding(5500, settled.Add(time.Minute), "ref-2"), // never ingested
		},
	}
	rec := newTestReconciler(src, now)

	// Tick 1 observes the gap but must not alert on it yet.
	first, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #1: %v", err)
	}
	if first.GapKobo != -550000 {
		t.Fatalf("GapKobo = %d, want -550000", first.GapKobo)
	}
	if first.ShortfallAlerts != 0 {
		t.Errorf("tick 1 ShortfallAlerts = %d, want 0 (unconfirmed)", first.ShortfallAlerts)
	}

	// Tick 2 confirms it.
	second, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	if second.ShortfallAlerts != 1 {
		t.Errorf("tick 2 ShortfallAlerts = %d, want 1", second.ShortfallAlerts)
	}
	if second.SurplusAlerts != 0 {
		t.Errorf("SurplusAlerts = %d, want 0 (wrong direction)", second.SurplusAlerts)
	}
	if second.Streak != 2 {
		t.Errorf("Streak = %d, want 2", second.Streak)
	}
}

// We ingested MORE than Flutterwave credited → one payment recorded twice, so
// someone may be credited twice. The other direction, and it must alert too —
// this is where pm_ngn_reconciler deliberately stays silent and we must not.
func TestInboundReconciler_DoubleIngest_AlertsSurplus(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)

	src := &fakeInboundReconcileSource{
		credits: []fakeInboundCredit{
			{amountKobo: 200000, at: settled},
			{amountKobo: 200000, at: settled}, // same money, two rows
		},
		statement: []services.FWStatementEntry{
			anchorRow(now),
			funding(2000, settled, "ref-1"),
		},
	}
	rec := newTestReconciler(src, now)

	if _, err := rec.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce #1: %v", err)
	}
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	if res.GapKobo != 200000 {
		t.Fatalf("GapKobo = %d, want +200000", res.GapKobo)
	}
	if res.SurplusAlerts != 1 {
		t.Errorf("SurplusAlerts = %d, want 1", res.SurplusAlerts)
	}
	if res.ShortfallAlerts != 0 {
		t.Errorf("ShortfallAlerts = %d, want 0 (wrong direction)", res.ShortfallAlerts)
	}
}

// A credit that landed mid-poll is on the provider's side and not yet on ours.
// That is normal, not drift: the settling lag must hold it out of BOTH sides,
// so the gap stays zero however many ticks run.
func TestInboundReconciler_MidWindowCredit_DoesNotFire(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)

	src := &fakeInboundReconcileSource{
		credits: []fakeInboundCredit{
			{amountKobo: 200000, at: settled},
		},
		statement: []services.FWStatementEntry{
			anchorRow(now),
			funding(2000, settled, "ref-1"),
			// 40 seconds old: inside the settling lag, and the 30s ingest poll
			// has not necessarily even seen it.
			funding(9999, now.Add(-40*time.Second), "ref-fresh"),
		},
	}
	rec := newTestReconciler(src, now)

	for tick := 1; tick <= 3; tick++ {
		res, err := rec.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("RunOnce #%d: %v", tick, err)
		}
		if res.Checked != 1 {
			t.Fatalf("tick %d Checked = %d, want 1 (skip reason: %s)", tick, res.Checked, res.SkipReason)
		}
		if res.GapKobo != 0 {
			t.Fatalf("tick %d GapKobo = %d, want 0 — a mid-poll credit is not drift", tick, res.GapKobo)
		}
		if res.ShortfallAlerts != 0 || res.SurplusAlerts != 0 {
			t.Fatalf("tick %d alerted on a mid-poll credit", tick)
		}
	}

	// And prove the window it asked for actually excludes the fresh credit.
	if !src.sumTo.Equal(now.Add(-inboundReconcileDefaultSettle)) {
		t.Errorf("window end = %v, want %v (now minus the settling lag)", src.sumTo, now.Add(-inboundReconcileDefaultSettle))
	}
	// The page, however, must run all the way to now, or a row that only
	// exists in the unsettled tail could never be inspected at all.
	if src.fetchTo.Before(now) {
		t.Errorf("fetch to = %v, want >= now (%v)", src.fetchTo, now)
	}
}

// A provider fetch error must not look like a missing credit. No comparison
// runs, no alert fires, and the tick is not fatal.
func TestInboundReconciler_ProviderFetchError_NoFalseAlert(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	src := &fakeInboundReconcileSource{
		credits:  []fakeInboundCredit{{amountKobo: 200000, at: now.Add(-30 * time.Minute)}},
		fetchErr: errors.New("flutterwave 503"),
	}
	rec := newTestReconciler(src, now)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce must not return a provider error: %v", err)
	}
	if res.Errors != 1 {
		t.Errorf("Errors = %d, want 1", res.Errors)
	}
	if res.Checked != 0 {
		t.Errorf("Checked = %d, want 0 (no comparison ran)", res.Checked)
	}
	if res.ShortfallAlerts != 0 || res.SurplusAlerts != 0 {
		t.Errorf("alerted on a provider outage: %d/%d", res.ShortfallAlerts, res.SurplusAlerts)
	}
}

// ─── window, tolerance and comparability ───────────────────────────────────

// A provider blip in the middle of a real gap must not reset the confirmation
// streak — otherwise a flaky provider hides a missing credit forever.
func TestInboundReconciler_FetchErrorPreservesStreak(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)
	src := &fakeInboundReconcileSource{
		statement: []services.FWStatementEntry{anchorRow(now), funding(2000, settled, "ref-1")},
	}
	rec := newTestReconciler(src, now)

	if _, err := rec.RunOnce(context.Background()); err != nil { // streak 1
		t.Fatalf("RunOnce #1: %v", err)
	}
	src.fetchErr = errors.New("flutterwave 503")
	if _, err := rec.RunOnce(context.Background()); err != nil { // no evidence
		t.Fatalf("RunOnce #2: %v", err)
	}
	src.fetchErr = nil
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #3: %v", err)
	}
	if res.ShortfallAlerts != 1 {
		t.Errorf("ShortfallAlerts = %d, want 1 — the outage must not have cleared the streak", res.ShortfallAlerts)
	}
}

// A gap that clears is not a gap. The streak resets, so an unrelated later gap
// still needs its own confirmation.
func TestInboundReconciler_GapClears_ResetsStreak(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)
	src := &fakeInboundReconcileSource{
		statement: []services.FWStatementEntry{anchorRow(now), funding(2000, settled, "ref-1")},
	}
	rec := newTestReconciler(src, now)

	if _, err := rec.RunOnce(context.Background()); err != nil { // gap, streak 1
		t.Fatalf("RunOnce #1: %v", err)
	}
	// The ingestor catches up.
	src.credits = []fakeInboundCredit{{amountKobo: 200000, at: settled}}
	clean, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	if clean.Streak != 0 {
		t.Fatalf("Streak = %d, want 0 after a clean tick", clean.Streak)
	}

	// A brand-new gap must start counting from scratch.
	src.statement = append(src.statement, funding(1000, settled.Add(time.Minute), "ref-2"))
	again, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #3: %v", err)
	}
	if again.ShortfallAlerts != 0 {
		t.Errorf("ShortfallAlerts = %d, want 0 — a fresh gap needs its own confirmation", again.ShortfallAlerts)
	}
}

// Both sides are integer kobo derived from the same numbers, so there is
// nothing for a tolerance to absorb. One kobo of disagreement is two sides
// disagreeing about a row, and it must be reported.
func TestInboundReconciler_ZeroTolerance_OneKoboCounts(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)
	src := &fakeInboundReconcileSource{
		credits:   []fakeInboundCredit{{amountKobo: 199999, at: settled}},
		statement: []services.FWStatementEntry{anchorRow(now), funding(2000, settled, "ref-1")},
	}
	rec := newTestReconciler(src, now)

	if _, err := rec.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce #1: %v", err)
	}
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	if res.GapKobo != -1 {
		t.Fatalf("GapKobo = %d, want -1", res.GapKobo)
	}
	if res.ShortfallAlerts != 1 {
		t.Errorf("ShortfallAlerts = %d, want 1 — there is no rounding here to forgive", res.ShortfallAlerts)
	}
}

// Only `funding` rows are customer money. Our own payouts, fees, settlements,
// reversals and chargebacks share the statement and must not enter the
// provider side — otherwise the two sides are not comparable at all.
func TestInboundReconciler_CountsOnlyCustomerFunding(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)

	src := &fakeInboundReconcileSource{
		credits: []fakeInboundCredit{{amountKobo: 200000, at: settled}},
		statement: []services.FWStatementEntry{
			anchorRow(now),
			funding(2000, settled, "ref-1"),
			fwRow("transfer", 90000, settled, "t-1"),
			fwRow("fee", 28, settled, "f-1"),
			fwRow("settlement", 400000, settled, "s-1"),
			fwRow("reversal", 3000, settled, "r-1"),
			fwRow("chargeback", 1500, settled, "c-1"),
			fwRow("bill", 500, settled, "b-1"),
		},
	}
	rec := newTestReconciler(src, now)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.ProviderKobo != 200000 {
		t.Errorf("ProviderKobo = %d, want 200000 (only the funding row)", res.ProviderKobo)
	}
	if res.GapKobo != 0 {
		t.Errorf("GapKobo = %d, want 0", res.GapKobo)
	}
}

// An explicitly-debit funding row is our own money leaving, not a credit.
func TestInboundReconciler_SkipsDebitFundingRow(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)

	debit := funding(7500, settled, "d-1")
	debit.Direction = "debit"

	src := &fakeInboundReconcileSource{
		credits:   []fakeInboundCredit{{amountKobo: 200000, at: settled}},
		statement: []services.FWStatementEntry{anchorRow(now), funding(2000, settled, "ref-1"), debit},
	}
	rec := newTestReconciler(src, now)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.GapKobo != 0 {
		t.Errorf("GapKobo = %d, want 0 — a debit is not an inbound credit", res.GapKobo)
	}
}

// Rows outside the window belong to a different tick's comparison and must be
// excluded from the provider side even though they are in the fetched page.
func TestInboundReconciler_ExcludesRowsOutsideWindow(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	src := &fakeInboundReconcileSource{
		credits: []fakeInboundCredit{{amountKobo: 200000, at: now.Add(-30 * time.Minute)}},
		statement: []services.FWStatementEntry{
			anchorRow(now),
			funding(2000, now.Add(-30*time.Minute), "in-window"),
			funding(4000, now.Add(-5*time.Hour), "too-old"),
		},
	}
	rec := newTestReconciler(src, now)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.ProviderKobo != 200000 {
		t.Errorf("ProviderKobo = %d, want 200000", res.ProviderKobo)
	}
}

// ─── the cases where comparing is not defensible ───────────────────────────

// A funding row the provider gave no usable date is stamped with OUR clock by
// the ingestor, so the two sides window it differently. That is a bookkeeping
// difference, not money, and must never be reported as one.
func TestInboundReconciler_UndatableFundingRow_SkipsTick(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)

	src := &fakeInboundReconcileSource{
		credits: []fakeInboundCredit{{amountKobo: 200000, at: settled}},
		statement: []services.FWStatementEntry{
			anchorRow(now),
			funding(2000, settled, "ref-1"),
			funding(3300, time.Time{}, "no-date"),
		},
	}
	rec := newTestReconciler(src, now)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Checked != 0 || res.Skipped != 1 {
		t.Errorf("Checked/Skipped = %d/%d, want 0/1", res.Checked, res.Skipped)
	}
	if res.Undatable != 1 {
		t.Errorf("Undatable = %d, want 1", res.Undatable)
	}
	if res.ShortfallAlerts != 0 || res.SurplusAlerts != 0 {
		t.Errorf("alerted on an undatable row: %d/%d", res.ShortfallAlerts, res.SurplusAlerts)
	}
}

// A page that does not reach back past the start of the window may simply have
// been truncated, and every row it is missing would read as a missed credit.
// We cannot tell, so we decline to compare rather than invent a shortfall.
func TestInboundReconciler_PageDoesNotCoverWindow_SkipsTick(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	src := &fakeInboundReconcileSource{
		credits: []fakeInboundCredit{{amountKobo: 200000, at: now.Add(-30 * time.Minute)}},
		// Nothing older than the window start: the page could be complete, or
		// it could be page 1 of many.
		statement: []services.FWStatementEntry{funding(2000, now.Add(-30*time.Minute), "ref-1")},
	}
	rec := newTestReconciler(src, now)

	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if res.Checked != 0 || res.Skipped != 1 {
		t.Errorf("Checked/Skipped = %d/%d, want 0/1 (%s)", res.Checked, res.Skipped, res.SkipReason)
	}
	if res.ShortfallAlerts != 0 {
		t.Errorf("ShortfallAlerts = %d, want 0 on an unprovable page", res.ShortfallAlerts)
	}
}

// A skipped tick is no evidence either way, so it must not advance a streak.
func TestInboundReconciler_SkippedTickDoesNotConfirmGap(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)
	src := &fakeInboundReconcileSource{
		statement: []services.FWStatementEntry{anchorRow(now), funding(2000, settled, "ref-1")},
	}
	rec := newTestReconciler(src, now)

	if _, err := rec.RunOnce(context.Background()); err != nil { // streak 1
		t.Fatalf("RunOnce #1: %v", err)
	}
	src.statement = append(src.statement, funding(3300, time.Time{}, "no-date"))
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #2: %v", err)
	}
	if res.ShortfallAlerts != 0 {
		t.Errorf("ShortfallAlerts = %d, want 0 — a skipped tick confirms nothing", res.ShortfallAlerts)
	}
	if res.Streak != 1 {
		t.Errorf("Streak = %d, want 1 (unchanged)", res.Streak)
	}
}

// ─── notifier seam ─────────────────────────────────────────────────────────

type fakeInboundGapNotifier struct {
	calls []InboundGap
	err   error
}

func (f *fakeInboundGapNotifier) NotifyInboundGap(_ context.Context, gap InboundGap) error {
	f.calls = append(f.calls, gap)
	return f.err
}

// The notifier fires once when the gap is confirmed, and not again while the
// same gap persists — a hold-the-line email every five minutes is how an alert
// stops being read.
func TestInboundReconciler_NotifiesOncePerDistinctGap(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)
	src := &fakeInboundReconcileSource{
		statement: []services.FWStatementEntry{anchorRow(now), funding(2000, settled, "ref-1")},
	}
	notifier := &fakeInboundGapNotifier{}
	rec := newTestReconciler(src, now)
	rec.SetNotifier(notifier)

	for i := 0; i < 4; i++ {
		if _, err := rec.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce #%d: %v", i+1, err)
		}
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("notifier called %d times, want 1", len(notifier.calls))
	}
	got := notifier.calls[0]
	if got.GapKobo != -200000 {
		t.Errorf("GapKobo = %d, want -200000", got.GapKobo)
	}
	if got.Currency != "NGN" {
		t.Errorf("Currency = %q, want NGN", got.Currency)
	}
	if !got.To.Equal(now.Add(-inboundReconcileDefaultSettle)) {
		t.Errorf("To = %v, want the settled window end %v", got.To, now.Add(-inboundReconcileDefaultSettle))
	}

	// More money goes missing → that is a new fact and must be re-sent.
	src.statement = append(src.statement, funding(1000, settled.Add(time.Minute), "ref-2"))
	if _, err := rec.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce #5: %v", err)
	}
	if len(notifier.calls) != 2 {
		t.Fatalf("notifier called %d times, want 2 after the gap grew", len(notifier.calls))
	}
}

// A notifier that fails must not break the tick — the log line is the record
// of last resort and it has already been written.
func TestInboundReconciler_NotifierErrorIsNotFatal(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	settled := now.Add(-30 * time.Minute)
	src := &fakeInboundReconcileSource{
		statement: []services.FWStatementEntry{anchorRow(now), funding(2000, settled, "ref-1")},
	}
	rec := newTestReconciler(src, now)
	rec.SetNotifier(&fakeInboundGapNotifier{err: errors.New("smtp down")})

	if _, err := rec.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce #1: %v", err)
	}
	res, err := rec.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce #2 must not fail on a notifier error: %v", err)
	}
	if res.ShortfallAlerts != 1 {
		t.Errorf("ShortfallAlerts = %d, want 1", res.ShortfallAlerts)
	}
}

// ─── plumbing ──────────────────────────────────────────────────────────────

// A ledger-sum failure is our own DB and IS returned — unlike a provider
// failure, there is no reading of it that leaves the reconciler useful.
func TestInboundReconciler_LedgerSumError_Returns(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	src := &fakeInboundReconcileSource{sumErr: errors.New("db down")}
	rec := newTestReconciler(src, now)

	if _, err := rec.RunOnce(context.Background()); err == nil {
		t.Fatal("expected an error when SumIngestedKobo fails, got nil")
	}
}

func TestInboundReconciler_WorkerMetadata(t *testing.T) {
	rec := NewInboundReconciler(&fakeInboundReconcileSource{}, "", 0)
	if rec.Name() != "inbound-reconciler" {
		t.Errorf("Name = %q", rec.Name())
	}
	if rec.Interval() <= 0 {
		t.Errorf("Interval = %v, want a positive default", rec.Interval())
	}
	if rec.currency != "NGN" {
		t.Errorf("currency = %q, want the NGN default", rec.currency)
	}
	if err := rec.Run(context.Background()); err != nil {
		t.Errorf("Run(empty): %v", err)
	}
}

// A cancelled context must abort before the provider is called, not after.
func TestInboundReconciler_ContextCancelled(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	src := &fakeInboundReconcileSource{}
	rec := newTestReconciler(src, now)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rec.RunOnce(ctx); err == nil {
		t.Fatal("expected a context error, got nil")
	}
}

// The reconciler must convert provider naira to kobo exactly as the ingestor
// did, or every row would contribute a phantom kobo of "drift".
func TestInboundReconcileKoboRounding_MatchesIngestor(t *testing.T) {
	cases := []struct {
		naira float64
		want  int64
	}{
		{0, 0},
		{19.72, 1972}, // arrives as 19.719999…; truncation would lose a kobo
		{2000, 200000},
		{0.01, 1},
		{123456.78, 12345678},
	}
	for _, tc := range cases {
		if got := services.KoboFromMajor(tc.naira); got != tc.want {
			t.Errorf("services.KoboFromMajor(%v) = %d, want %d", tc.naira, got, tc.want)
		}
	}
}
