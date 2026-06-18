package jobs

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"
)

// PMNGNLedgerSource is the narrow surface the aggregate reconciler needs under
// the VA-only model: the SUM of all active PMs' off-chain available_ngn ledgers
// and the live balance of TSA's single parent Nomba account. The real
// implementation (a thin adapter over PMService + NombaClient) satisfies it;
// tests inject a fake. Injecting an interface keeps the job unit-testable
// without a DB or a live Nomba endpoint.
//
// VA-only model: corporate PMs are virtual accounts under one parent, so funds
// can't be reconciled per-PM (no per-PM sub-account balance). Instead the safety
// net compares Σ(available_ngn over active PMs) against the single parent
// balance — they should match within tolerance (Σ ledgers vs parent).
type PMNGNLedgerSource interface {
	// SumActiveAvailableNGN returns the SUM of available_ngn over all active PMs
	// (2-dp NGN decimal string) plus the count of PMs summed.
	SumActiveAvailableNGN(ctx context.Context) (sumNGN string, count int, err error)
	// FetchParentBalance returns the live NGN balance of the single parent Nomba
	// account (2-dp NGN decimal string).
	FetchParentBalance(ctx context.Context) (string, error)
}

// pmNGNDriftToleranceKobo is the absolute tolerance (in kobo, i.e. NGN×100)
// below which a ledger-vs-Nomba difference is treated as noise, not drift. One
// naira (100 kobo) absorbs rounding at the 2dp boundary without masking a real
// discrepancy.
const pmNGNDriftToleranceKobo = int64(100)

// PMNGNReconcileResult is the per-tick outcome — surfaced for testability and
// logging. Checked is 1 when the aggregate comparison ran (both amounts
// parsed), else 0; DriftCount is 1 when Σ-ledger diverged from the parent
// balance beyond tolerance; Errors is 1 when the parent balance could not be
// fetched or either amount was unparseable (the tick is skipped, not fatal).
type PMNGNReconcileResult struct {
	Checked    int
	DriftCount int
	Errors     int
	PMCount    int // number of active PMs summed (for logging/visibility)
}

// PMNGNReconciler is the corporate-PM money-safety net (design §7): once per
// tick it compares the SUM of every active PM's off-chain NGN ledger
// (available_ngn) against the live single-parent Nomba balance and logs drift
// beyond a small tolerance as pm_ngn_reconciler_drift_total (mirroring
// p2p_reconciler_drift_total). It is read-only — no ledger mutation, no
// transfers — so a missed tick is harmless and recovered next tick.
type PMNGNReconciler struct {
	src      PMNGNLedgerSource
	interval time.Duration
}

// NewPMNGNReconciler builds the worker. A non-positive interval falls back to 5m
// so the scheduler (which panics on a non-positive interval at Register) always
// gets a sane period.
func NewPMNGNReconciler(src PMNGNLedgerSource, interval time.Duration) *PMNGNReconciler {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &PMNGNReconciler{src: src, interval: interval}
}

func (w *PMNGNReconciler) Name() string            { return "pm-ngn-reconciler" }
func (w *PMNGNReconciler) Interval() time.Duration { return w.interval }

// Run is the scheduler entry point: it runs one reconcile pass and discards the
// result (already logged). It returns an error only when the PM ledger sum
// itself can't be fetched (a parent-balance fetch/parse failure is counted, not
// returned).
func (w *PMNGNReconciler) Run(ctx context.Context) error {
	_, err := w.RunOnce(ctx)
	return err
}

// RunOnce performs one aggregate reconcile pass and returns the structured
// result. Split out from Run so tests can assert on the counts.
func (w *PMNGNReconciler) RunOnce(ctx context.Context) (PMNGNReconcileResult, error) {
	var res PMNGNReconcileResult

	ledgerSum, count, err := w.src.SumActiveAvailableNGN(ctx)
	if err != nil {
		return res, err
	}
	res.PMCount = count

	parentBal, ferr := w.src.FetchParentBalance(ctx)
	if ferr != nil {
		res.Errors++
		log.Printf("[pm-ngn-reconciler] fetch parent balance failed: %v", ferr)
		return res, nil
	}

	ledgerKobo, ok1 := ngnToKoboReconcile(ledgerSum)
	parentKobo, ok2 := ngnToKoboReconcile(parentBal)
	if !ok1 || !ok2 {
		res.Errors++
		log.Printf("[pm-ngn-reconciler] unparseable amounts (ledgerSum=%q parent=%q); skipping", ledgerSum, parentBal)
		return res, nil
	}

	res.Checked = 1
	diff := ledgerKobo - parentKobo
	if abs64(diff) > pmNGNDriftToleranceKobo {
		res.DriftCount = 1
		// pm_ngn_reconciler_drift_total — log-metric token mirroring
		// p2p_reconciler_drift_total so ops can alert on drift.
		log.Printf("[pm-ngn-reconciler] pm_ngn_reconciler_drift_total=1 pms=%d ledgerSum=%s parent=%s diffKobo=%d",
			count, ledgerSum, parentBal, diff)
	}
	if res.DriftCount > 0 {
		log.Printf("[pm-ngn-reconciler] checked=%d drift=%d pms=%d", res.Checked, res.DriftCount, count)
	}
	return res, nil
}

// ngnToKoboReconcile parses a 2-dp NGN decimal string into integer kobo
// (naira×100). A self-contained copy of the services-layer ngnToKobo so the jobs
// package stays free of a services import cycle. Truncates to 2dp; returns
// ok=false on empty/non-numeric/negative input.
func ngnToKoboReconcile(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	whole := s
	frac := ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		whole = s[:i]
		frac = s[i+1:]
	}
	if whole == "" {
		return 0, false
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w < 0 {
		return 0, false
	}
	if len(frac) < 2 {
		frac = frac + strings.Repeat("0", 2-len(frac))
	} else {
		frac = frac[:2]
	}
	var f int64
	if frac != "" {
		f, err = strconv.ParseInt(frac, 10, 64)
		if err != nil || f < 0 {
			return 0, false
		}
	}
	return w*100 + f, true
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
