package jobs

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"
)

// PMLedgerRow is the projection the reconciler compares against Nomba: a PM's
// off-chain NGN ledger (available_ngn) plus the Nomba sub-account id whose live
// balance it should track.
type PMLedgerRow struct {
	Name         string
	SubAccountID string
	AvailableNGN string
}

// PMNGNLedgerSource is the narrow surface the reconciler needs — listing active
// PMs with a sub-account and fetching the live Nomba sub-account balance. The
// real implementation (a thin adapter over PMService + NombaClient) satisfies
// it; tests inject a fake. Injecting an interface keeps the job unit-testable
// without a DB or a live Nomba endpoint.
type PMNGNLedgerSource interface {
	// ActiveLedgerRows returns one row per active PM that has a Nomba
	// sub-account id (PMs without one are skipped — nothing to compare against).
	ActiveLedgerRows(ctx context.Context) ([]PMLedgerRow, error)
	// FetchSubAccountBalance returns the live Nomba available balance (2-dp NGN
	// decimal string) for a sub-account.
	FetchSubAccountBalance(ctx context.Context, subAccountID string) (string, error)
}

// pmNGNDriftToleranceKobo is the absolute tolerance (in kobo, i.e. NGN×100)
// below which a ledger-vs-Nomba difference is treated as noise, not drift. One
// naira (100 kobo) absorbs rounding at the 2dp boundary without masking a real
// discrepancy.
const pmNGNDriftToleranceKobo = int64(100)

// PMNGNReconcileResult is the per-tick outcome — surfaced for testability and
// logging. Checked counts PMs whose balance was successfully fetched and
// compared; DriftCount the subset whose ledger diverged from Nomba beyond
// tolerance; Errors the PMs whose balance could not be fetched/parsed (skipped,
// not fatal).
type PMNGNReconcileResult struct {
	Checked    int
	DriftCount int
	Errors     int
}

// PMNGNReconciler is the corporate-PM money-safety net (design §7): once per
// tick it compares each active PM's off-chain NGN ledger (available_ngn) against
// the live Nomba sub-account balance and logs drift beyond a small tolerance as
// pm_ngn_reconciler_drift_total (mirroring p2p_reconciler_drift_total). It is
// read-only — no ledger mutation, no transfers — so a missed tick is harmless
// and recovered next tick. A per-PM fetch/parse failure is counted and skipped
// so one bad PM can't stop the sweep.
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
// result (already logged). It returns an error only when the PM list itself
// can't be fetched (a per-PM failure is counted, not returned).
func (w *PMNGNReconciler) Run(ctx context.Context) error {
	_, err := w.RunOnce(ctx)
	return err
}

// RunOnce performs one reconcile pass and returns the structured result. Split
// out from Run so tests can assert on the counts.
func (w *PMNGNReconciler) RunOnce(ctx context.Context) (PMNGNReconcileResult, error) {
	var res PMNGNReconcileResult
	rows, err := w.src.ActiveLedgerRows(ctx)
	if err != nil {
		return res, err
	}
	for _, r := range rows {
		if strings.TrimSpace(r.SubAccountID) == "" {
			continue // nothing to compare against
		}
		nombaBal, ferr := w.src.FetchSubAccountBalance(ctx, r.SubAccountID)
		if ferr != nil {
			res.Errors++
			log.Printf("[pm-ngn-reconciler] pm=%q sub=%s fetch balance failed: %v", r.Name, r.SubAccountID, ferr)
			continue
		}
		ledgerKobo, ok1 := ngnToKoboReconcile(r.AvailableNGN)
		nombaKobo, ok2 := ngnToKoboReconcile(nombaBal)
		if !ok1 || !ok2 {
			res.Errors++
			log.Printf("[pm-ngn-reconciler] pm=%q sub=%s unparseable amounts (ledger=%q nomba=%q); skipping",
				r.Name, r.SubAccountID, r.AvailableNGN, nombaBal)
			continue
		}
		res.Checked++
		diff := ledgerKobo - nombaKobo
		if abs64(diff) > pmNGNDriftToleranceKobo {
			res.DriftCount++
			// pm_ngn_reconciler_drift_total — log-metric token mirroring
			// p2p_reconciler_drift_total so ops can alert on drift.
			log.Printf("[pm-ngn-reconciler] pm_ngn_reconciler_drift_total=1 pm=%q sub=%s ledger=%s nomba=%s diffKobo=%d",
				r.Name, r.SubAccountID, r.AvailableNGN, nombaBal, diff)
		}
	}
	if res.DriftCount > 0 || res.Errors > 0 {
		log.Printf("[pm-ngn-reconciler] checked=%d drift=%d errors=%d", res.Checked, res.DriftCount, res.Errors)
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
