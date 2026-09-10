package jobs

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// InboundReconcileSource is the narrow surface the inbound reconciler needs:
// our own side (Σ of what the ingestor actually recorded) and the provider's
// side (the wallet statement itself). NewInboundReconcileSource adapts a
// *gorm.DB plus the Flutterwave client onto it; tests inject a fake, so the
// job needs neither a database nor a live provider to be exercised.
type InboundReconcileSource interface {
	// SumIngestedKobo returns Σ amount_kobo and the row count over
	// inbound_credits whose transaction_date falls in [from, to).
	SumIngestedKobo(ctx context.Context, currency string, from, to time.Time) (sumKobo int64, rows int, err error)

	// FetchWalletStatement returns the provider's own statement rows. It is
	// asked for a window that runs to *now*, deliberately wider than the
	// window being reconciled — see the settling lag in RunOnce.
	FetchWalletStatement(ctx context.Context, currency string, from, to time.Time) ([]services.FWStatementEntry, error)
}

// InboundGap is one confirmed disagreement between what we recorded and what
// Flutterwave says it credited, handed to whatever wants to page a human.
type InboundGap struct {
	Currency string
	From, To time.Time
	// IngestedKobo is Σ inbound_credits.amount_kobo over the window.
	IngestedKobo int64
	// ProviderKobo is Σ of the statement's own funding credits over the window.
	ProviderKobo int64
	// GapKobo is IngestedKobo - ProviderKobo. NEGATIVE means we missed a
	// credit (a customer paid and nobody has their money — the urgent one);
	// POSITIVE means we ingested the same payment twice.
	GapKobo int64
	// IngestedRows/ProviderRows narrow the search for whoever picks this up:
	// a gap with equal row counts is a wrong amount, an unequal one a whole
	// missing or duplicated row.
	IngestedRows int
	ProviderRows int
	// Streak is how many consecutive ticks have seen this gap.
	Streak int
}

// InboundGapNotifier is the consumer side of the INBOUND_ALERT_EMAILS notifier
// (design §8), which a separate change owns. It is declared here, narrowly,
// rather than reaching into that change's files: this worker needs exactly one
// verb, and depending on one verb means the two can land in either order.
//
// It is optional. A nil notifier leaves the structured log line as the alert,
// which is what ops already scrapes for every other reconciler in this package.
type InboundGapNotifier interface {
	NotifyInboundGap(ctx context.Context, gap InboundGap) error
}

const (
	// inboundReconcileDefaultInterval is the tick period. This is a checker,
	// not the detection path — InboundCreditPoller at 30s is what actually
	// notices money — so it runs at the same 5m cadence as pm_ngn_reconciler.
	inboundReconcileDefaultInterval = 5 * time.Minute

	// inboundReconcileDefaultSettle is the settling lag: how far in the past
	// the reconciled window ENDS.
	//
	// This is the answer to the mid-poll problem. A credit that landed seconds
	// ago is on Flutterwave's statement and not yet in inbound_credits, because
	// the ingestor polls every 30 seconds — that is the system working, not
	// drift, and comparing right up to `now` would report it as a missing
	// credit on every single tick. So the window ends five minutes ago: ten
	// ingest polls, which is long enough that anything still absent from our
	// side is absent for a reason. Both sides then window on the SAME field —
	// the provider's own transaction_date, which the ingestor persists verbatim
	// — so a credit is in both windows or in neither, whatever moment either
	// side happens to be read at.
	inboundReconcileDefaultSettle = 5 * time.Minute

	// inboundReconcileDefaultWindow is how much settled history each tick
	// compares. Wide enough that a real gap stays visible for twelve
	// consecutive ticks (so the confirmation rule below has plenty of
	// observations), narrow enough that the statement page plausibly covers it.
	//
	// It also bounds how long a gap can be seen at all: a credit we never
	// ingest stops being reported once it ages past this, so the alert is a
	// prompt to act now, not a durable record. The log line is the record.
	inboundReconcileDefaultWindow = time.Hour

	// inboundReconcileConfirmTicks is how many consecutive ticks must see a
	// gap before it is alerted.
	//
	// The settling lag removes the ordinary race; this removes the residual
	// ones — a statement page that briefly omits a row, a clock skew at a
	// window edge, a provider that reorders. One tick of disagreement is an
	// observation; two is a fact. The cost is ~5 minutes of extra latency on a
	// real alert, which is nothing next to an alert ops has learned to ignore.
	inboundReconcileConfirmTicks = 2

	// inboundReconcileToleranceKobo is ZERO, and that is a deliberate
	// departure from pm_ngn_reconciler's flat ₦1.
	//
	// That reconciler compares two independently-maintained 2dp decimal
	// balances, so a naira of slack absorbs real rounding. Here both sides are
	// integer kobo derived from the SAME provider numbers by the SAME rounding
	// (inboundKoboFromMajor mirrors the ingestor's koboFromMajor), so a
	// faithfully ingested row contributes exactly zero difference. There is
	// nothing for a tolerance to absorb — it could only hide something. And
	// the smallest real discrepancy is a whole credit: orders are minted at
	// whole naira (design §5.4) with a ₦100 floor, so the first thing a
	// tolerance would ever mask is ₦100 of somebody's money.
	inboundReconcileToleranceKobo = int64(0)

	// inboundReconcileDefaultCurrency — the central account collects NGN only.
	inboundReconcileDefaultCurrency = "NGN"

	// Statement row types. Only `funding` is customer money arriving; every
	// other type is our own movement across the same account.
	inboundRowTypeFunding = "funding"
)

// InboundReconcileResult is one tick's outcome, surfaced for logging and tests.
type InboundReconcileResult struct {
	Checked int // 1 when the two sides were actually compared
	Skipped int // 1 when the tick declined to compare (SkipReason says why)
	Errors  int // 1 when the provider side could not be read

	IngestedKobo int64
	ProviderKobo int64
	IngestedRows int
	ProviderRows int
	// GapKobo is IngestedKobo - ProviderKobo. Negative = missed credit.
	GapKobo int64

	// Undatable counts funding rows the provider gave no usable date.
	Undatable int
	// Streak is how many consecutive compared ticks have seen a gap.
	Streak int

	ShortfallAlerts int // 1 when a confirmed "we missed a credit" was raised
	SurplusAlerts   int // 1 when a confirmed "we double-ingested" was raised

	WindowFrom, WindowTo time.Time
	SkipReason           string
}

// InboundReconciler is the money-safety net over central-account attribution
// (design §7). Once per tick it compares
//
//	Σ inbound_credits.amount_kobo   (every status: see sumStatuses below)
//	Σ statement funding credits     (same window, same provider timestamps)
//
// and alerts on any gap, in BOTH directions.
//
// Why both directions, when pm_ngn_reconciler deliberately alerts on only one.
// That job compares Σ PM ledgers against a commingled parent balance which
// also holds TSA's own float, so its two sides can never be equal and flagging
// the under direction would fire on every tick forever — alarm fatigue that
// tells ops nothing. Here the two sides are the same set of statement rows
// counted twice, once by us and once by the provider, so equality is the
// normal state and each direction names a distinct, real failure:
//
//   - ingested < credited — we MISSED a credit. Attribution is now a heuristic
//     over free text and detection is polling with no webhook backstop, so a
//     row silently dropped by a parse, a filter or a failed insert is entirely
//     possible, and its owner has paid and has nothing. This is the urgent one.
//   - ingested > credited — we DOUBLE-INGESTED. The dedupe key is a provider
//     reference we do not control, and a synthesized one when the provider
//     omits it; two rows for one payment is a user credited twice.
//
// Neither is noise, so neither is suppressed.
//
// It is read-only. It writes no ledger, moves no money, and never touches a
// credit's status — a missed tick is harmless and recovered on the next one.
type InboundReconciler struct {
	src      InboundReconcileSource
	notifier InboundGapNotifier

	currency string
	interval time.Duration
	settle   time.Duration
	window   time.Duration

	// Streak state. The scheduler runs one goroutine per worker and never
	// overlaps ticks, so this needs no lock.
	streak        int
	streakGap     int64
	notifiedGap   int64
	haveNotified  bool
	lastSkipLogAt time.Time

	// now is injectable so tests can pin the window without sleeping.
	now func() time.Time
}

// NewInboundReconciler builds the worker. A blank currency or non-positive
// interval falls back to the documented defaults, so the scheduler (which
// panics on a non-positive interval at Register) always gets a sane period.
func NewInboundReconciler(src InboundReconcileSource, currency string, interval time.Duration) *InboundReconciler {
	if strings.TrimSpace(currency) == "" {
		currency = inboundReconcileDefaultCurrency
	}
	if interval <= 0 {
		interval = inboundReconcileDefaultInterval
	}
	return &InboundReconciler{
		src:      src,
		currency: strings.ToUpper(strings.TrimSpace(currency)),
		interval: interval,
		settle:   inboundReconcileDefaultSettle,
		window:   inboundReconcileDefaultWindow,
		now:      time.Now,
	}
}

// SetNotifier wires the email alert. Until it is called the reconciler alerts
// by log line only, which is a valid production state: the check is worth
// running before anyone has built somewhere to send it.
func (w *InboundReconciler) SetNotifier(n InboundGapNotifier) { w.notifier = n }

func (w *InboundReconciler) Name() string            { return "inbound-reconciler" }
func (w *InboundReconciler) Interval() time.Duration { return w.interval }

// Run is the scheduler entry point: one pass, result discarded (already
// logged). It returns an error only when OUR OWN side can't be read — a
// provider failure is counted, not returned, because a Flutterwave outage is
// not a reason to make the log look like a code fault.
func (w *InboundReconciler) Run(ctx context.Context) error {
	_, err := w.RunOnce(ctx)
	return err
}

// RunOnce performs one reconcile pass and returns the structured result.
// Split out from Run so tests can assert on the counts.
func (w *InboundReconciler) RunOnce(ctx context.Context) (InboundReconcileResult, error) {
	var res InboundReconcileResult
	if w == nil || w.src == nil {
		return res, fmt.Errorf("inbound reconciler: not configured")
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}

	now := w.now().UTC()
	to := now.Add(-w.settle)
	from := to.Add(-w.window)
	res.WindowFrom, res.WindowTo = from, to

	// Our side first: if the database can't be read there is nothing to say
	// about the provider's.
	ingestedKobo, ingestedRows, err := w.src.SumIngestedKobo(ctx, w.currency, from, to)
	if err != nil {
		return res, fmt.Errorf("inbound reconcile ledger sum: %w", err)
	}
	res.IngestedKobo, res.IngestedRows = ingestedKobo, ingestedRows

	// The provider's page is fetched all the way to `now`, wider than the
	// window being compared, for two reasons: a row that only exists in the
	// unsettled tail still has to be inspectable (that is how an undatable row
	// is spotted at all), and the endpoint takes date-only bounds anyway, so
	// asking for a narrower slice would buy nothing.
	rows, ferr := w.src.FetchWalletStatement(ctx, w.currency, from, now)
	if ferr != nil {
		// A provider outage is NOT a missing credit. No comparison runs and,
		// critically, the streak is left alone: a flaky provider must not be
		// able to clear the confirmation on a gap that is really there.
		res.Errors = 1
		log.Printf("[inbound-reconciler] fetch statement failed (window %s..%s): %v",
			from.Format(time.RFC3339), to.Format(time.RFC3339), ferr)
		return res, nil
	}

	providerKobo, providerRows, undatable, oldest := summarizeStatement(rows, from, to)
	res.ProviderKobo, res.ProviderRows, res.Undatable = providerKobo, providerRows, undatable

	if reason := w.refuseReason(rows, undatable, oldest, from); reason != "" {
		// Declining is not the same as passing. The two sides are not
		// comparable this tick, so the streak is neither advanced nor cleared.
		res.Skipped, res.SkipReason, res.Streak = 1, reason, w.streak
		w.logSkip(now, reason, from, to, len(rows), undatable)
		return res, nil
	}

	res.Checked = 1
	res.GapKobo = ingestedKobo - providerKobo
	w.trackStreak(res.GapKobo)
	res.Streak = w.streak

	if abs64(res.GapKobo) <= inboundReconcileToleranceKobo {
		return res, nil
	}

	// A gap exists. Say so every tick — ops dashboards count the token — but
	// only escalate once it has been seen inboundReconcileConfirmTicks times.
	direction := "shortfall"
	metric := "inbound_reconciler_shortfall_total"
	detail := "a customer paid and nobody has their money"
	if res.GapKobo > 0 {
		direction, metric, detail = "surplus", "inbound_reconciler_surplus_total", "one payment recorded twice — a user may be credited twice"
	}
	if w.streak < inboundReconcileConfirmTicks {
		log.Printf("[inbound-reconciler] provisional %s gapKobo=%d streak=%d/%d window=%s..%s ingested=%d/%d provider=%d/%d",
			direction, res.GapKobo, w.streak, inboundReconcileConfirmTicks,
			from.Format(time.RFC3339), to.Format(time.RFC3339),
			ingestedKobo, ingestedRows, providerKobo, providerRows)
		return res, nil
	}

	if res.GapKobo < 0 {
		res.ShortfallAlerts = 1
	} else {
		res.SurplusAlerts = 1
	}
	log.Printf("[inbound-reconciler] ALERT %s=1 direction=%s gapKobo=%d streak=%d window=%s..%s ingestedKobo=%d rows=%d providerKobo=%d rows=%d — %s",
		metric, direction, res.GapKobo, w.streak,
		from.Format(time.RFC3339), to.Format(time.RFC3339),
		ingestedKobo, ingestedRows, providerKobo, providerRows, detail)

	w.notify(ctx, InboundGap{
		Currency:     w.currency,
		From:         from,
		To:           to,
		IngestedKobo: ingestedKobo,
		ProviderKobo: providerKobo,
		GapKobo:      res.GapKobo,
		IngestedRows: ingestedRows,
		ProviderRows: providerRows,
		Streak:       w.streak,
	})
	return res, nil
}

// refuseReason names the cases where comparing the two sides would report
// bookkeeping as money. It returns "" when the comparison is defensible.
func (w *InboundReconciler) refuseReason(rows []services.FWStatementEntry, undatable int, oldest time.Time, from time.Time) string {
	// (1) A funding row with no usable transaction_date. The ingestor stamps
	// such a row with OUR clock instead (it has to — the column is NOT NULL),
	// so the two sides file the same credit under different timestamps and
	// therefore different windows. The difference that produces is real
	// arithmetic about nothing, so the whole tick is skipped. It is a blunt
	// response, and deliberately so: the only way this happens at all is that
	// Flutterwave changed its date format, in which case every row is
	// undatable and the reconciler has no ground to stand on.
	if undatable > 0 {
		return "undatable-funding-row"
	}

	// (2) A page that does not reach back past the start of the window. The
	// endpoint takes no page or limit parameter and its bounds are unverified
	// (design §11.2), so a short page is indistinguishable from a quiet
	// account — and every row a truncated page omits would read as a credit we
	// missed. An older row of ANY type is the proof that the page really does
	// cover the window; without it we decline rather than invent a shortfall.
	// The cost is that a genuinely quiet account is never reconciled, which is
	// the harmless direction: there is nothing there to reconcile.
	if len(rows) == 0 || oldest.IsZero() || oldest.After(from) {
		return "page-does-not-cover-window"
	}
	return ""
}

// trackStreak advances or clears the confirmation counter. A gap that changes
// sign is a different fact and starts counting again.
func (w *InboundReconciler) trackStreak(gapKobo int64) {
	if abs64(gapKobo) <= inboundReconcileToleranceKobo {
		w.streak, w.streakGap = 0, 0
		w.haveNotified, w.notifiedGap = false, 0
		return
	}
	if w.streak > 0 && sign64(gapKobo) == sign64(w.streakGap) {
		w.streak++
	} else {
		w.streak = 1
	}
	w.streakGap = gapKobo
}

// notify pages a human, at most once per distinct confirmed gap. Re-sending
// the same figure every five minutes is how an alert stops being read; a gap
// that CHANGES, though, is new money and is sent again.
func (w *InboundReconciler) notify(ctx context.Context, gap InboundGap) {
	if w.notifier == nil {
		return
	}
	if w.haveNotified && w.notifiedGap == gap.GapKobo {
		return
	}
	w.haveNotified, w.notifiedGap = true, gap.GapKobo
	if err := w.notifier.NotifyInboundGap(ctx, gap); err != nil {
		// Never fatal. The ALERT log line above is already written and is the
		// record of last resort; a dead SMTP relay must not also cost us the
		// tick.
		log.Printf("[inbound-reconciler] gap notification failed (gapKobo=%d): %v", gap.GapKobo, err)
	}
}

// logSkip reports a declined tick, rate-limited to once a minute. A skip is
// the reconciler admitting it cannot see, which must be visible — but a
// persistent cause (a provider that changed its date format) would otherwise
// fill the log with the same line forever.
func (w *InboundReconciler) logSkip(now time.Time, reason string, from, to time.Time, rows, undatable int) {
	if !w.lastSkipLogAt.IsZero() && now.Sub(w.lastSkipLogAt) < time.Minute {
		return
	}
	w.lastSkipLogAt = now
	log.Printf("[inbound-reconciler] inbound_reconciler_skipped_total=1 reason=%s window=%s..%s pageRows=%d undatable=%d — the invariant is UNCHECKED while this persists",
		reason, from.Format(time.RFC3339), to.Format(time.RFC3339), rows, undatable)
}

// summarizeStatement reduces a statement page to the provider side of the
// invariant: Σ of the funding credits whose transaction_date falls in
// [from, to), how many there were, how many funding rows carried no usable
// date, and the oldest datable row of any type on the page.
func summarizeStatement(rows []services.FWStatementEntry, from, to time.Time) (sumKobo int64, count, undatable int, oldest time.Time) {
	for _, row := range rows {
		at := row.TransactionDate.UTC()
		if !at.IsZero() && (oldest.IsZero() || at.Before(oldest)) {
			oldest = at
		}
		if !inboundIsCustomerFunding(row) {
			continue
		}
		if at.IsZero() {
			undatable++
			continue
		}
		if at.Before(from) || !at.Before(to) {
			continue
		}
		sumKobo += inboundKoboFromMajor(row.Amount.Float64())
		count++
	}
	return sumKobo, count, undatable, oldest
}

// inboundIsCustomerFunding decides which statement rows are the customer money
// this invariant is about.
//
// It MUST agree exactly with services.isCustomerFunding, which is what the
// ingestor writes rows from. If the two ever disagree the reconciler compares
// two different sets and alerts on every tick forever — the failure mode this
// whole job exists to avoid. It is duplicated rather than shared only because
// the ingestor keeps it unexported; when that predicate changes, this changes
// in the same commit.
func inboundIsCustomerFunding(row services.FWStatementEntry) bool {
	if !strings.EqualFold(strings.TrimSpace(row.Type), inboundRowTypeFunding) {
		return false
	}
	if d := strings.TrimSpace(row.Direction); d != "" && strings.EqualFold(d, "debit") {
		return false
	}
	return true
}

// inboundKoboFromMajor mirrors the ingestor's koboFromMajor exactly: rounding
// half-away-from-zero, because the provider sends 2dp values as float64 and
// 19.72 arrives as 19.719999…. Truncating instead would shave a kobo off every
// row on the provider side only, manufacturing a shortfall out of arithmetic.
func inboundKoboFromMajor(v float64) int64 {
	return int64(math.Round(v * 100))
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func sign64(v int64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

// ─── production adapter ────────────────────────────────────────────────────

// inboundReconcileSource joins our ledger to the provider's statement.
type inboundReconcileSource struct {
	db   *gorm.DB
	stmt services.InboundStatementSource
}

// NewInboundReconcileSource adapts the inbound_credits table plus the
// Flutterwave client onto InboundReconcileSource.
func NewInboundReconcileSource(db *gorm.DB, stmt services.InboundStatementSource) InboundReconcileSource {
	return &inboundReconcileSource{db: db, stmt: stmt}
}

func (s *inboundReconcileSource) FetchWalletStatement(ctx context.Context, currency string, from, to time.Time) ([]services.FWStatementEntry, error) {
	return s.stmt.FetchWalletStatement(ctx, currency, from, to)
}

// SumIngestedKobo sums OUR side of the invariant.
//
// Three choices here decide whether the comparison means anything:
//
//  1. amount_kobo, NOT gross_kobo. The provider side is the statement's own
//     credit figures; gross_kobo adds a netted fee back on top of them so that
//     claims can match what the sender typed (design §4). Summing gross here
//     would inflate our side by ~1.4% of every credit and report it as a double
//     ingest on every tick.
//
//  2. EVERY status, including `ignored` and `refunded`. The spec's §7 formula
//     counts only matched + unmatched + held, and that is wrong: `ignored` is
//     precisely the status for a funding row that turned out to be company
//     float or a test credit, and the provider's statement still counts that
//     row. Excluding it here while it stays on the provider side would
//     manufacture a permanent phantom shortfall that grows every time an
//     operator marks something ignored — pm_ngn_reconciler's alarm-fatigue trap,
//     rebuilt by hand. Same for `refunded`: the money still arrived, and the
//     refund is a separate debit row that the funding side never sees.
//     This invariant is "did we faithfully record what landed", and every row
//     in the table is part of that answer.
//
//  3. transaction_date, not created_at. It is the provider's own timestamp,
//     stored verbatim, which is the only field both sides agree on.
func (s *inboundReconcileSource) SumIngestedKobo(ctx context.Context, currency string, from, to time.Time) (int64, int, error) {
	var agg struct {
		SumKobo  int64
		RowCount int
	}
	err := s.db.WithContext(ctx).
		Model(&models.InboundCredit{}).
		Select("COALESCE(SUM(amount_kobo), 0) AS sum_kobo, COUNT(*) AS row_count").
		Where("currency = ?", strings.ToUpper(strings.TrimSpace(currency))).
		Where("transaction_date >= ? AND transaction_date < ?", from, to).
		Scan(&agg).Error
	if err != nil {
		return 0, 0, fmt.Errorf("sum inbound credits: %w", err)
	}
	return agg.SumKobo, agg.RowCount, nil
}
