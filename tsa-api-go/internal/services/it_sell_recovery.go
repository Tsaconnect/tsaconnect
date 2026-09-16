package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// MCGP sell recovery.
//
// A sell moves value in two places that cannot be committed together: the
// user's MCGP is debited on-chain, then naira is paid out through Flutterwave.
// Anything that interrupts the gap — a receipt wait that times out, a crashed
// process, a payout refused on the spot — leaves one leg done and the other
// not. This file is what closes those gaps, under one rule:
//
//	Never settle or reverse without positive evidence. Reversing a sell whose
//	payout succeeded pays the user twice; settling one whose payout failed
//	takes their MCGP. When the evidence is ambiguous, leave the row, log an
//	ALERT, and surface it in the stuck-sell list.
//
// Evidence comes from exactly two places: the swap contract's per-ref debit
// record (did the debit land?) and Flutterwave's own transfer record (did the
// payout go?).

var (
	// ErrITSellDebitPending: the debit was (or may have been) broadcast but is
	// not confirmed. The sell is `debiting`; the reconciler finishes or fails it
	// from chain state.
	ErrITSellDebitPending = errors.New("your sale is processing — we are confirming it on-chain")
	// ErrITSellPayoutRejected: the payout provably did not go out, so the debit
	// is reversed (or queued for reversal).
	ErrITSellPayoutRejected = errors.New("payout rejected")
	// ErrITSellPayoutUnverified: an admin settle/reverse was refused because the
	// provider could not confirm the payout's outcome.
	ErrITSellPayoutUnverified = errors.New("payout outcome could not be verified with the provider")
	// ErrITSellPayoutConflict: the provider's record (or the row's own state)
	// contradicts the requested action (e.g. reversing a sell whose transfer
	// succeeded, or one with a payout in flight).
	ErrITSellPayoutConflict = errors.New("provider payout state does not allow this action")
)

const (
	// itSellCreateTimeout bounds a sell once it is detached from the request:
	// the receipt wait alone is capped at 60s, plus FX, fees and the payout call.
	itSellCreateTimeout = 3 * time.Minute
	// itDebitAbandonAfter is how long a debit with no on-chain record is given
	// to mine before the sell is marked failed. A later landing is still caught
	// by the late-debit scan and reversed.
	itDebitAbandonAfter = 30 * time.Minute
	// itLateDebitScanWindow bounds how far back failed sells are re-checked for
	// a debit that mined after they were marked failed.
	itLateDebitScanWindow = 48 * time.Hour
	// itPayoutAutoRetryWindow bounds automatic payout (re)submission by intent
	// age. Past it the provider's idempotency cache may have expired, so a
	// re-send could create a second transfer; only an operator may act.
	itPayoutAutoRetryWindow = 24 * time.Hour
	// itPayoutMaxAttempts caps automatic submissions of one payout reference.
	itPayoutMaxAttempts = 3
	// itPayoutPendingAlertAfter is when a payout still not final is escalated.
	itPayoutPendingAlertAfter = 24 * time.Hour
	// itPayoutAbsentSettle is how long after the last payout attempt a reference
	// missing from the provider's transfer list counts as "never created", and
	// how long a reverse waits behind a payout attempt that may be in flight.
	itPayoutAbsentSettle = 10 * time.Minute
	// itSellScanLimit is the page size of each reconciler scan.
	itSellScanLimit = 50
	// itDeepScanEvery spaces the unwindowed late-debit audit.
	itDeepScanEvery = 24 * time.Hour
)

// itPayoutTransfers reads sell payouts back from the provider.
type itPayoutTransfers interface {
	GetTransfer(ctx context.Context, id string) (FWTransfer, error)
	FindTransferByReference(ctx context.Context, reference string, around time.Time) (FWTransfer, bool, error)
}

// SetPayoutTransfers wires the provider transfer reader used as evidence by
// the sell reconciler and the admin settle/reverse guards.
func (s *ITService) SetPayoutTransfers(r itPayoutTransfers) { s.payoutTransfers = r }

// SetFWAbsenceTrusted sets whether "Flutterwave has no transfer for this
// reference" may be acted on (IT_SELL_FW_ABSENCE_TRUSTED). Off: those rows
// alert and wait for an operator.
func (s *ITService) SetFWAbsenceTrusted(trusted bool) { s.fwAbsenceTrusted = trusted }

// mcgpSells scopes a query to MCGP sells (rows predating the asset column are
// MCGP). Stablecoin sells never touch the swap contract.
func (s *ITService) mcgpSells(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("direction = ? AND (asset IS NULL OR asset = '' OR asset = ?)", models.ITDirectionSell, models.ITAssetMCGP)
}

func isMCGPSell(in *models.ITIntent) bool {
	return in.Direction == models.ITDirectionSell && models.ITIntentAsset(in) == models.ITAssetMCGP
}

// payoutAttemptTime is when the current payout reference was last sent, or
// the intent's creation for rows from before attempts were timestamped.
func payoutAttemptTime(in *models.ITIntent) time.Time {
	if in.LastPayoutAttemptAt != nil {
		return *in.LastPayoutAttemptAt
	}
	return in.CreatedAt
}

// reverseRejectedSell reverses a sell whose payout was rejected and returns
// the cause, so the caller reports the sale as not completed. A failed reversal
// leaves the intent marked PayoutRejected for the reconciler.
func (s *ITService) reverseRejectedSell(ctx context.Context, intent *models.ITIntent, cause error) (*models.ITIntent, error) {
	out, err := s.ReverseSell(ctx, intent.ID, "")
	if err != nil {
		log.Printf("[it] ALERT it_sell_reverse_deferred intent=%s: payout rejected (%v) but reverse failed: %v — reconciler will retry",
			intent.ID, cause, err)
		if reloaded, gErr := s.getIntent(ctx, intent.ID); gErr == nil {
			return reloaded, cause
		}
		return intent, cause
	}
	return out, cause
}

// ─── Reconciler ─────────────────────────────────────────────────────────────

// ReconcileStuckSells drives MCGP sells that stopped between legs toward a
// terminal state, acting only on evidence:
//
//	processing/debiting past grace — read the debit by ref: Pending → debited;
//	    none after itDebitAbandonAfter → failed; resolved → needs_review;
//	    mismatched → ALERT
//	failed (48h), never paid out — a debit that mined late → reverse
//	debited past grace —
//	    claimed/rejected                   → reverse
//	    payout id known                    → provider SUCCESSFUL settle,
//	                                         FAILED reverse, else wait/ALERT
//	    retry refused                      → provider has it: adopt + resolve;
//	                                         absent (trusted) → reverse
//	    payout never sent                  → send it (within the window)
//	    payout sent, id unknown            → provider has it: adopt + resolve;
//	                                         absent (trusted) → re-send the same
//	                                         reference (bounded) or ALERT
//
// Scans are cursor-paged so rows that can only alert never starve new ones.
// Once a day it also audits failed sells older than the 48h window (alert
// only). Idempotent and safe on several instances: every write is guarded on
// the state it read, and every chain op is keyed by the intent's single-use ref.
func (s *ITService) ReconcileStuckSells(ctx context.Context, grace time.Duration) (int, error) {
	if !s.IsConfigured() {
		return 0, ErrITNotConfigured
	}
	now := time.Now()
	changed := 0

	inflight, err := s.scanSells(ctx, "inflight", s.mcgpSells(ctx).
		Where("status IN ? AND updated_at < ?", []string{models.ITStatusProcessing, models.ITStatusDebiting}, now.Add(-grace)))
	if err != nil {
		return changed, err
	}
	for i := range inflight {
		if s.reconcileInflightSell(ctx, &inflight[i], now) {
			changed++
		}
	}

	failed, err := s.scanSells(ctx, "failed", s.mcgpSells(ctx).
		Where("status = ? AND updated_at > ? AND COALESCE(payout_id, '') = '' AND payout_attempts = 0 AND COALESCE(debit_ref, '') <> ''",
			models.ITStatusFailed, now.Add(-itLateDebitScanWindow)))
	if err != nil {
		return changed, err
	}
	for i := range failed {
		if s.reconcileFailedSell(ctx, &failed[i]) {
			changed++
		}
	}

	debited, err := s.scanSells(ctx, "debited", s.mcgpSells(ctx).
		Where("status = ? AND updated_at < ?", models.ITStatusDebited, now.Add(-grace)))
	if err != nil {
		return changed, err
	}
	for i := range debited {
		if s.reconcileDebitedSell(ctx, &debited[i], now) {
			changed++
		}
	}

	if claimDailyRun(ctx, s.db, "it-sell-late-debit-audit", itDeepScanEvery) {
		if _, err := s.AlertLateDebitsBeyondWindow(ctx); err != nil {
			log.Printf("[it-sell-reconciler] late-debit audit failed: %v", err)
		}
	}
	return changed, nil
}

func (s *ITService) scanSells(ctx context.Context, key string, q *gorm.DB) ([]models.ITIntent, error) {
	var rows []models.ITIntent
	if err := s.sellScan.page(key, "", q, itSellScanLimit).Find(&rows).Error; err != nil {
		return nil, err
	}
	if n := len(rows); n > 0 {
		s.sellScan.advance(key, n, itSellScanLimit, rows[n-1].UpdatedAt, rows[n-1].ID)
	} else {
		s.sellScan.advance(key, 0, itSellScanLimit, time.Time{}, uuid.Nil)
	}
	return rows, nil
}

// debitMatchesIntent checks that an on-chain debit record is this intent's:
// same wallet, same amount. A mismatch is never acted on.
func debitMatchesIntent(rec DebitRecord, in *models.ITIntent) bool {
	amount, ok := parseStoredWei(in.McgpAmount)
	return ok && rec.Amount != nil && rec.Amount.Cmp(amount) == 0 &&
		rec.User == common.HexToAddress(in.WalletAddress)
}

// parseStoredWei reads a numeric(78,0) column back as an integer. PostgreSQL
// returns plain digits; SQLite (tests) can hand back exponent form ("1e+20").
// Anything that is not an exact integer is rejected.
func parseStoredWei(v string) (*big.Int, bool) {
	v = strings.TrimSpace(v)
	if n, ok := new(big.Int).SetString(v, 10); ok {
		return n, true
	}
	f, _, err := big.ParseFloat(v, 10, 256, big.ToNearestEven)
	if err != nil || !f.IsInt() {
		return nil, false
	}
	n, acc := f.Int(nil)
	return n, acc == big.Exact
}

func (s *ITService) reconcileInflightSell(ctx context.Context, in *models.ITIntent, now time.Time) bool {
	rec, err := s.chain().DebitRecord(ctx, itRef("IT:debit:", in.ID))
	if err != nil {
		log.Printf("[it-sell-reconciler] intent=%s debit read failed: %v", in.ID, err)
		return false
	}
	inflight := []string{models.ITStatusProcessing, models.ITStatusDebiting}
	switch rec.State {
	case DebitStatePending:
		if !debitMatchesIntent(rec, in) {
			alertf("sell-mismatch:"+in.ID.String(),
				"[it-sell-reconciler] ALERT it_sell_debit_mismatch intent=%s on-chain user=%s amount=%s intent wallet=%s amount=%s",
				in.ID, rec.User.Hex(), rec.Amount, in.WalletAddress, in.McgpAmount)
			return false
		}
		res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("id = ? AND status IN ?", in.ID, inflight).
			Updates(map[string]any{"status": models.ITStatusDebited, "failure_reason": ""})
		if res.Error == nil && res.RowsAffected == 1 {
			log.Printf("[it-sell-reconciler] intent=%s debit confirmed on-chain → debited", in.ID)
			return true
		}
		return false
	case DebitStateNone:
		if in.UpdatedAt.After(now.Add(-itDebitAbandonAfter)) {
			return false // a broadcast may still mine
		}
		res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
			Where("id = ? AND status IN ?", in.ID, inflight).
			Updates(map[string]any{"status": models.ITStatusFailed,
				"failure_reason": truncateReason("debit never landed on-chain (was: " + in.FailureReason + ")")})
		return res.Error == nil && res.RowsAffected == 1
	default:
		s.moveToReview(ctx, in, inflight, "debit already settled or reversed on-chain while the sell read "+in.Status)
		return in.Status == models.ITStatusNeedsReview
	}
}

// reconcileFailedSell reverses a debit that mined after its sell was marked
// failed. The user was told the sale failed and no payout was ever attempted,
// so restoring the MCGP is the only correction that matches what they saw.
func (s *ITService) reconcileFailedSell(ctx context.Context, in *models.ITIntent) bool {
	rec, err := s.chain().DebitRecord(ctx, itRef("IT:debit:", in.ID))
	if err != nil || rec.State != DebitStatePending {
		return false
	}
	if !debitMatchesIntent(rec, in) {
		alertf("sell-mismatch:"+in.ID.String(),
			"[it-sell-reconciler] ALERT it_sell_debit_mismatch intent=%s (failed row) on-chain user=%s amount=%s",
			in.ID, rec.User.Hex(), rec.Amount)
		return false
	}
	// payout_rejected=true is the reverse claim: no send can take this row.
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ? AND COALESCE(payout_id, '') = '' AND payout_attempts = 0", in.ID, models.ITStatusFailed).
		Updates(map[string]any{
			"status":          models.ITStatusDebited,
			"payout_rejected": true,
			"failure_reason":  "debit landed after the sell was marked failed; reversing",
		})
	if res.Error != nil || res.RowsAffected != 1 {
		return false
	}
	log.Printf("[it-sell-reconciler] ALERT it_sell_late_debit intent=%s — debit mined after failure; reversing", in.ID)
	if _, err := s.ReverseSell(ctx, in.ID, ""); err != nil {
		log.Printf("[it-sell-reconciler] intent=%s late-debit reverse failed (will retry): %v", in.ID, err)
	}
	return true
}

func (s *ITService) reconcileDebitedSell(ctx context.Context, in *models.ITIntent, now time.Time) bool {
	if in.PayoutRejected {
		if _, err := s.ReverseSell(ctx, in.ID, ""); err != nil {
			log.Printf("[it-sell-reconciler] intent=%s reverse of rejected/claimed payout failed: %v", in.ID, err)
			return false
		}
		log.Printf("[it-sell-reconciler] intent=%s payout rejected → reversed", in.ID)
		return true
	}
	if in.PayoutID != "" {
		if s.payoutTransfers == nil {
			return false
		}
		tr, err := s.payoutTransfers.GetTransfer(ctx, in.PayoutID)
		if err != nil {
			log.Printf("[it-sell-reconciler] intent=%s transfer %s read failed: %v", in.ID, in.PayoutID, err)
			return false
		}
		return s.resolveSellFromTransfer(ctx, in, tr, now)
	}
	if in.PayoutRefusedOnRetry {
		return s.resolveRefusedRetry(ctx, in, now)
	}
	if s.payout == nil || !sellIntentHasPayoutDestination(in) {
		if in.CreatedAt.Before(now.Add(-itPayoutPendingAlertAfter)) {
			alertf("sell-manual:"+in.ID.String(),
				"[it-sell-reconciler] ALERT it_sell_manual_payout_outstanding intent=%s — debited with no automated payout", in.ID)
		}
		return false
	}
	if payoutNeverSubmitted(in) {
		if in.CreatedAt.Before(now.Add(-itPayoutAutoRetryWindow)) {
			alertf("sell-never-sent:"+in.ID.String(),
				"[it-sell-reconciler] ALERT it_sell_payout_never_sent intent=%s — too old to send automatically", in.ID)
			return false
		}
		return s.submitSellPayout(ctx, in)
	}
	withinWindow := payoutAttemptTime(in).After(now.Add(-itPayoutAutoRetryWindow))
	if !withinWindow || s.payoutTransfers == nil {
		// Past the window only an operator acts (retry-payout / reverse both
		// search the provider first); scanning every tick would be load for no
		// automatic action.
		alertf("sell-unresolved:"+in.ID.String(),
			"[it-sell-reconciler] ALERT it_sell_payout_unresolved intent=%s attempts=%d — outcome unknown; operator retry-payout or reverse required",
			in.ID, in.PayoutAttempts)
		return false
	}
	tr, found, err := s.payoutTransfers.FindTransferByReference(ctx, itPayoutRefFor(in), payoutAttemptTime(in))
	if err != nil {
		s.alertLookupFailure(in, err)
		return false
	}
	if found {
		if !s.adoptPayoutID(ctx, in, tr.ID) {
			return false
		}
		return s.resolveSellFromTransfer(ctx, in, tr, now)
	}
	if !s.fwAbsenceTrusted || in.PayoutAttempts >= itPayoutMaxAttempts {
		alertf("sell-unresolved:"+in.ID.String(),
			"[it-sell-reconciler] ALERT it_sell_payout_unresolved intent=%s attempts=%d absence_trusted=%v — no transfer found at provider; operator action required",
			in.ID, in.PayoutAttempts, s.fwAbsenceTrusted)
		return false
	}
	return s.submitSellPayout(ctx, in)
}

// resolveRefusedRetry handles a sell whose payout retry was refused. The first
// attempt may have gone out (a refusal can mean "reference already used"), so
// the provider is asked: a transfer is adopted and resolved on its status; no
// transfer (only when absence is trusted) means nothing was paid and the sell
// is reversed. It is never re-sent.
func (s *ITService) resolveRefusedRetry(ctx context.Context, in *models.ITIntent, now time.Time) bool {
	if s.payoutTransfers == nil {
		return false
	}
	tr, found, err := s.payoutTransfers.FindTransferByReference(ctx, itPayoutRefFor(in), payoutAttemptTime(in))
	if err != nil {
		s.alertLookupFailure(in, err)
		return false
	}
	if found {
		if !s.adoptPayoutID(ctx, in, tr.ID) {
			return false
		}
		return s.resolveSellFromTransfer(ctx, in, tr, now)
	}
	if !s.fwAbsenceTrusted {
		alertf("sell-refused:"+in.ID.String(),
			"[it-sell-reconciler] ALERT it_sell_retry_refused_unconfirmed intent=%s — provider refused a retry and absence is not trusted; operator action required", in.ID)
		return false
	}
	if _, err := s.ReverseSell(ctx, in.ID, ""); err != nil {
		log.Printf("[it-sell-reconciler] intent=%s reverse after confirmed absence failed: %v", in.ID, err)
		return false
	}
	return true
}

// alertLookupFailure alerts on a provider lookup that did not complete. A
// payout whose outcome cannot be established is money in limbo, so it is never
// just a log line — and a response shape this code does not recognise is
// called out by name, because that is exactly what IT_SELL_FW_ABSENCE_TRUSTED
// is waiting to be confirmed.
func (s *ITService) alertLookupFailure(in *models.ITIntent, err error) {
	if errors.Is(err, ErrFWTransferShape) {
		alertf("fw-shape:"+in.ID.String(),
			"[it-sell-reconciler] ALERT it_fw_transfer_shape_unrecognised intent=%s: %v — the /transfers listing does not match what this code parses; absence can never be trusted until it does",
			in.ID, err)
		return
	}
	alertf("sell-lookup:"+in.ID.String(),
		"[it-sell-reconciler] ALERT it_sell_payout_lookup_failed intent=%s: %v", in.ID, err)
}

func (s *ITService) submitSellPayout(ctx context.Context, in *models.ITIntent) bool {
	err := s.AutoPayout(ctx, in)
	switch {
	case err == nil:
		return true
	case errors.Is(err, ErrITSellPayoutRejected):
		// Recording the rejection is itself progress; a failed reverse is
		// retried from the marker on the next tick.
		_, _ = s.reverseRejectedSell(ctx, in, err)
		return true
	default:
		log.Printf("[it-sell-reconciler] intent=%s payout not sent / outcome unknown: %v", in.ID, err)
		return false
	}
}

// adoptPayoutID records a transfer found by reference, guarded so it never
// overwrites an id something else already recorded.
func (s *ITService) adoptPayoutID(ctx context.Context, in *models.ITIntent, id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND COALESCE(payout_id, '') = ''", in.ID).
		Updates(map[string]any{"payout_id": id, "failure_reason": ""})
	if res.Error != nil || res.RowsAffected != 1 {
		return false
	}
	in.PayoutID = id
	return true
}

func (s *ITService) resolveSellFromTransfer(ctx context.Context, in *models.ITIntent, tr FWTransfer, now time.Time) bool {
	switch {
	case fwSucceeded(tr.Status):
		if _, err := s.SettleSell(ctx, in.ID, ""); err != nil {
			log.Printf("[it-sell-reconciler] intent=%s settle after SUCCESSFUL transfer %s failed: %v", in.ID, tr.ID, err)
			return false
		}
		return true
	case strings.EqualFold(tr.Status, "FAILED"):
		if _, err := s.ReverseSell(ctx, in.ID, ""); err != nil {
			log.Printf("[it-sell-reconciler] intent=%s reverse after FAILED transfer %s failed: %v", in.ID, tr.ID, err)
			return false
		}
		return true
	default:
		if in.CreatedAt.Before(now.Add(-itPayoutPendingAlertAfter)) {
			alertf("sell-not-final:"+in.ID.String(),
				"[it-sell-reconciler] ALERT it_sell_payout_not_final intent=%s transfer=%s status=%s", in.ID, tr.ID, tr.Status)
		}
		return false
	}
}

// AlertLateDebitsBeyondWindow audits failed MCGP sells older than the 48h
// late-debit window whose debit is nonetheless pending on-chain. Alert only:
// that far out an operator decides. Returns how many it reported.
func (s *ITService) AlertLateDebitsBeyondWindow(ctx context.Context) (int, error) {
	if !s.IsConfigured() {
		return 0, ErrITNotConfigured
	}
	cutoff := time.Now().Add(-itLateDebitScanWindow)
	reported := 0
	var cursor reconcileCursor
	const batch = 200
	for page := 0; page < 50; page++ {
		var rows []models.ITIntent
		q := s.mcgpSells(ctx).Where("status = ? AND updated_at <= ? AND COALESCE(payout_id, '') = '' AND payout_attempts = 0 AND COALESCE(debit_ref, '') <> ''",
			models.ITStatusFailed, cutoff)
		if err := cursor.page("deep", "", q, batch).Find(&rows).Error; err != nil {
			return reported, err
		}
		for i := range rows {
			in := &rows[i]
			rec, err := s.chain().DebitRecord(ctx, itRef("IT:debit:", in.ID))
			if err != nil || rec.State != DebitStatePending {
				continue
			}
			reported++
			log.Printf("[it-sell-reconciler] ALERT it_sell_late_debit_beyond_window intent=%s failed_at=%s on-chain user=%s amount=%s — operator reverse required",
				in.ID, in.UpdatedAt.Format(time.RFC3339), rec.User.Hex(), rec.Amount)
		}
		if len(rows) < batch {
			return reported, nil
		}
		cursor.advance("deep", len(rows), batch, rows[len(rows)-1].UpdatedAt, rows[len(rows)-1].ID)
	}
	return reported, nil
}

// ─── Admin ──────────────────────────────────────────────────────────────────

// AdminReverseSell reverses an MCGP sell only once the payout is shown not to
// have happened: the transfer is FAILED, the payout was rejected, the
// reference was never sent, or (only while absence is trusted) the provider
// has no transfer for it well after the last attempt.
func (s *ITService) AdminReverseSell(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	in, err := s.adminMCGPSell(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if in.Status == models.ITStatusDebited {
		if err := s.requirePayoutDidNotHappen(ctx, in); err != nil {
			return nil, err
		}
	}
	return s.ReverseSell(ctx, intentID, "")
}

// AdminSettleSell settles an MCGP sell only once the provider confirms the
// payout succeeded. With no automated payout gateway and no payout ever sent
// (the manual flow) the operator's own confirmation stands, as before.
func (s *ITService) AdminSettleSell(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	in, err := s.adminMCGPSell(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if in.Status == models.ITStatusDebited {
		if err := s.requirePayoutSucceeded(ctx, in); err != nil {
			return nil, err
		}
	}
	return s.SettleSell(ctx, intentID, "")
}

// RetrySellPayout re-drives the payout of a debited MCGP sell. A reference
// that may already have been sent is looked up first and adopted if it
// exists; it is only sent again when the provider has no record of it and
// that absence is trusted.
func (s *ITService) RetrySellPayout(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	in, err := s.adminMCGPSell(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if in.Status != models.ITStatusDebited || in.PayoutID != "" {
		return nil, fmt.Errorf("%w: only a debited sell with no recorded payout can be retried", ErrITBadState)
	}
	if in.PayoutRejected {
		return nil, fmt.Errorf("%w: the payout was rejected or the sell is being reversed; reverse it instead", ErrITBadState)
	}
	if s.payout == nil {
		return nil, errors.New("it: no payout gateway configured")
	}
	if !payoutNeverSubmitted(in) {
		tr, found, err := s.findPayout(ctx, in)
		if err != nil {
			return nil, err
		}
		if found {
			if !s.adoptPayoutID(ctx, in, tr.ID) {
				return nil, fmt.Errorf("%w: could not record transfer %s", ErrITBadState, tr.ID)
			}
			return s.getIntent(ctx, in.ID)
		}
		if in.PayoutRefusedOnRetry {
			return nil, fmt.Errorf("%w: the provider refused this reference; reverse the sell instead", ErrITBadState)
		}
		if !s.fwAbsenceTrusted {
			return nil, fmt.Errorf("%w: no transfer found at Flutterwave, but absence is not trusted (IT_SELL_FW_ABSENCE_TRUSTED)", ErrITSellPayoutUnverified)
		}
	}
	if err := s.AutoPayout(ctx, in); err != nil {
		if errors.Is(err, ErrITSellPayoutRejected) {
			return s.reverseRejectedSell(ctx, in, err)
		}
		return nil, err
	}
	return s.getIntent(ctx, in.ID)
}

func (s *ITService) adminMCGPSell(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	if !s.IsConfigured() {
		return nil, ErrITNotConfigured
	}
	in, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if !isMCGPSell(in) {
		return nil, fmt.Errorf("%w: not an MCGP sell", ErrITBadState)
	}
	return in, nil
}

func (s *ITService) requirePayoutDidNotHappen(ctx context.Context, in *models.ITIntent) error {
	if in.PayoutRejected {
		return nil
	}
	if in.PayoutID != "" {
		tr, err := s.readTransfer(ctx, in.PayoutID)
		if err != nil {
			return err
		}
		if !strings.EqualFold(tr.Status, "FAILED") {
			return fmt.Errorf("%w: transfer %s is %s — reversing would pay the user twice", ErrITSellPayoutConflict, tr.ID, tr.Status)
		}
		return nil
	}
	// Only a payout provably never sent needs no provider evidence — with or
	// without a gateway wired.
	if payoutNeverSubmitted(in) {
		return nil
	}
	if time.Since(payoutAttemptTime(in)) < itPayoutAbsentSettle || time.Since(in.UpdatedAt) < itPayoutAbsentSettle {
		return fmt.Errorf("%w: the last payout attempt is too recent to rule out; try again in a few minutes", ErrITSellPayoutUnverified)
	}
	tr, found, err := s.findPayout(ctx, in)
	if err != nil {
		return err
	}
	if found {
		s.adoptPayoutID(ctx, in, tr.ID)
		if !strings.EqualFold(tr.Status, "FAILED") {
			return fmt.Errorf("%w: transfer %s is %s — reversing would pay the user twice", ErrITSellPayoutConflict, tr.ID, tr.Status)
		}
		return nil
	}
	if !s.fwAbsenceTrusted {
		return fmt.Errorf("%w: no transfer found at Flutterwave, but absence is not trusted (IT_SELL_FW_ABSENCE_TRUSTED)", ErrITSellPayoutUnverified)
	}
	return nil
}

func (s *ITService) requirePayoutSucceeded(ctx context.Context, in *models.ITIntent) error {
	if in.PayoutRejected {
		return fmt.Errorf("%w: the payout was rejected or the sell is being reversed — settling would take the user's MCGP", ErrITSellPayoutConflict)
	}
	if in.PayoutID != "" {
		tr, err := s.readTransfer(ctx, in.PayoutID)
		if err != nil {
			return err
		}
		if !fwSucceeded(tr.Status) {
			return fmt.Errorf("%w: transfer %s is %s, not successful", ErrITSellPayoutConflict, tr.ID, tr.Status)
		}
		return nil
	}
	if payoutNeverSubmitted(in) {
		if s.payout == nil {
			return nil // manual payout flow: the operator paid and confirms it
		}
		return fmt.Errorf("%w: no payout was ever sent for this sell", ErrITSellPayoutConflict)
	}
	tr, found, err := s.findPayout(ctx, in)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: the provider has no transfer for this sell", ErrITSellPayoutConflict)
	}
	s.adoptPayoutID(ctx, in, tr.ID)
	if !fwSucceeded(tr.Status) {
		return fmt.Errorf("%w: transfer %s is %s, not successful", ErrITSellPayoutConflict, tr.ID, tr.Status)
	}
	return nil
}

func (s *ITService) readTransfer(ctx context.Context, id string) (FWTransfer, error) {
	if s.payoutTransfers == nil {
		return FWTransfer{}, fmt.Errorf("%w: no provider reader configured", ErrITSellPayoutUnverified)
	}
	tr, err := s.payoutTransfers.GetTransfer(ctx, id)
	if err != nil {
		return FWTransfer{}, fmt.Errorf("%w: %v", ErrITSellPayoutUnverified, err)
	}
	return tr, nil
}

func (s *ITService) findPayout(ctx context.Context, in *models.ITIntent) (FWTransfer, bool, error) {
	if s.payoutTransfers == nil {
		return FWTransfer{}, false, fmt.Errorf("%w: no provider reader configured", ErrITSellPayoutUnverified)
	}
	tr, found, err := s.payoutTransfers.FindTransferByReference(ctx, itPayoutRefFor(in), payoutAttemptTime(in))
	if err != nil {
		return FWTransfer{}, false, fmt.Errorf("%w: %v", ErrITSellPayoutUnverified, err)
	}
	return tr, found, nil
}

// RepayStableSell pays out a parked stablecoin sell again on a fresh payout
// reference. Allowed only once the provider confirms the previous transfer
// FAILED — the user's fiat is then still in our balance, and re-sending the old
// reference would only replay the failure.
func (s *ITService) RepayStableSell(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	in, err := s.getIntent(ctx, intentID)
	if err != nil {
		return nil, err
	}
	asset := models.ITIntentAsset(in)
	if in.Direction != models.ITDirectionSell || (asset != models.ITAssetUSDC && asset != models.ITAssetUSDT) {
		return nil, fmt.Errorf("%w: not a stablecoin sell", ErrITBadState)
	}
	// Two ways in: a sell PARKED by a provider-confirmed FAILED transfer, and
	// one whose current reference the provider keeps refusing outright (the
	// watcher can only replay that refusal forever). Both need the same
	// evidence below: whatever the provider holds for this reference must not
	// be a payout that could still land.
	refused := strings.HasPrefix(in.FailureReason, itPayoutReasonPrefix+" refused") && in.PayoutAttempts > 0
	if in.Status != models.ITStatusDebited || (!in.PayoutRejected && !refused) {
		return nil, fmt.Errorf("%w: only a parked or repeatedly refused stablecoin sell can be repaid", ErrITBadState)
	}
	if s.payout == nil {
		return nil, errors.New("it: no payout gateway configured")
	}
	if in.PayoutRefGen >= itPayoutRefMaxGen {
		return nil, fmt.Errorf("%w: payout reference generations exhausted", ErrITBadState)
	}
	if in.PayoutID != "" {
		tr, terr := s.readTransfer(ctx, in.PayoutID)
		if terr != nil {
			return nil, terr
		}
		if !strings.EqualFold(tr.Status, "FAILED") {
			return nil, fmt.Errorf("%w: previous transfer %s is %s, not FAILED", ErrITSellPayoutConflict, tr.ID, tr.Status)
		}
	} else {
		tr, found, ferr := s.findPayout(ctx, in)
		if ferr != nil {
			return nil, ferr
		}
		switch {
		case found && strings.EqualFold(tr.Status, "FAILED"):
			// Confirmed failed: safe to pay again on a fresh reference.
		case found:
			// A transfer exists that is not failed — adopt it and let the normal
			// settle/reverse path handle it rather than paying twice.
			s.adoptPayoutID(ctx, in, tr.ID)
			return nil, fmt.Errorf("%w: transfer %s for this reference is %s", ErrITSellPayoutConflict, tr.ID, tr.Status)
		case !s.fwAbsenceTrusted:
			return nil, fmt.Errorf("%w: no transfer found at Flutterwave, but absence is not trusted (IT_SELL_FW_ABSENCE_TRUSTED)", ErrITSellPayoutUnverified)
		}
		// Absent and absence trusted: the refused reference never created a
		// transfer, so a fresh one cannot double-pay.
	}
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ? AND payout_attempts = ? AND payout_ref_gen = ? AND COALESCE(payout_id, '') = ?",
			in.ID, models.ITStatusDebited, in.PayoutAttempts, in.PayoutRefGen, in.PayoutID).
		Updates(map[string]any{
			"payout_ref_gen":          in.PayoutRefGen + 1,
			"payout_id":               "",
			"payout_rejected":         false,
			"payout_refused_on_retry": false,
			"payout_attempts":         0,
			"last_payout_attempt_at":  nil,
			"failure_reason":          "",
		})
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, fmt.Errorf("%w: sell %s changed; not repaying", ErrITSellPayoutConflict, in.ID)
	}
	fresh, err := s.getIntent(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	log.Printf("[it] stable sell intent=%s repaid on reference generation %d", in.ID, fresh.PayoutRefGen)
	if err := s.AutoPayout(ctx, fresh); err != nil {
		return nil, err
	}
	return s.getIntent(ctx, in.ID)
}

// ReevaluateSell is the exit door for a sell parked in `needs_review`: it
// re-reads the chain and, where a payout reference exists, the provider, then
// moves the row to whatever the evidence supports.
//
//	debit Pending (matching)  → debited; the sell carries on normally
//	debit Resolved            → settled or reversed, decided by which operation
//	                            did not verify (recorded when it was parked) and
//	                            cross-checked against the transfer; with no
//	                            recorded operation the transfer decides
//	debit None / unreadable   → refused: a lagging node cannot close a sell
//	contradiction             → left parked, ALERT
//
// It never broadcasts anything: the debit is already resolved on-chain, so
// this only records which way it went.
func (s *ITService) ReevaluateSell(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	in, err := s.adminMCGPSell(ctx, intentID)
	if err != nil {
		return nil, err
	}
	switch in.Status {
	case models.ITStatusNeedsReview, models.ITStatusDebited, models.ITStatusDebiting, models.ITStatusProcessing:
	default:
		return nil, fmt.Errorf("%w: sell is %s; nothing to reevaluate", ErrITBadState, in.Status)
	}
	rec, err := s.chain().DebitRecord(ctx, itRef("IT:debit:", in.ID))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrITSellPayoutUnverified, err)
	}
	from := []string{models.ITStatusNeedsReview, models.ITStatusDebited, models.ITStatusDebiting, models.ITStatusProcessing}
	switch rec.State {
	case DebitStatePending:
		if !debitMatchesIntent(rec, in) {
			alertf("reevaluate-mismatch:"+in.ID.String(),
				"[it] ALERT it_sell_reevaluate_mismatch intent=%s on-chain user=%s amount=%s — left parked", in.ID, rec.User.Hex(), rec.Amount)
			return nil, fmt.Errorf("%w: the on-chain debit is for a different wallet or amount", ErrITSellPayoutConflict)
		}
		if !s.moveSellStatus(ctx, in, from, models.ITStatusDebited, "") {
			return nil, fmt.Errorf("%w: sell %s changed while it was being reevaluated", ErrITSellPayoutConflict, in.ID)
		}
		return s.getIntent(ctx, in.ID)
	case DebitStateNone:
		return nil, fmt.Errorf("%w: no debit at this ref — the node may be behind; try again shortly", ErrITSellPayoutUnverified)
	}

	// Resolved: settled or reversed on-chain, and the row never recorded which.
	settled, reversed := strings.HasPrefix(in.FailureReason, reviewReasonSettle), strings.HasPrefix(in.FailureReason, reviewReasonReverse)
	transfer, haveTransfer, err := s.reevaluateTransfer(ctx, in)
	if err != nil {
		return nil, err
	}
	paid := haveTransfer && fwSucceeded(transfer.Status)
	notPaid := haveTransfer && strings.EqualFold(transfer.Status, "FAILED")

	switch {
	case settled && notPaid, reversed && paid:
		alertf("reevaluate-conflict:"+in.ID.String(),
			"[it] ALERT it_sell_reevaluate_conflict intent=%s attempted=%q transfer=%s status=%s — left parked",
			in.ID, in.FailureReason, transfer.ID, transfer.Status)
		return nil, fmt.Errorf("%w: the on-chain outcome and transfer %s (%s) contradict each other", ErrITSellPayoutConflict, transfer.ID, transfer.Status)
	case settled, paid:
		if !s.moveSellStatus(ctx, in, from, models.ITStatusSettled, "closed by operator reevaluation") {
			return nil, fmt.Errorf("%w: sell %s changed while it was being reevaluated", ErrITSellPayoutConflict, in.ID)
		}
		s.settleITFee(ctx, in)
		return s.getIntent(ctx, in.ID)
	case reversed, notPaid, !haveTransfer && payoutNeverSubmitted(in):
		if !s.moveSellStatus(ctx, in, from, models.ITStatusReversed, "closed by operator reevaluation") {
			return nil, fmt.Errorf("%w: sell %s changed while it was being reevaluated", ErrITSellPayoutConflict, in.ID)
		}
		return s.getIntent(ctx, in.ID)
	default:
		alertf("reevaluate-unknown:"+in.ID.String(),
			"[it] ALERT it_sell_reevaluate_inconclusive intent=%s — debit resolved on-chain but neither the row nor the provider says which way; left parked", in.ID)
		return nil, fmt.Errorf("%w: cannot tell whether this sell settled or reversed", ErrITSellPayoutUnverified)
	}
}

// reevaluateTransfer reads whatever the provider holds for the sell's payout
// reference. found=false means there is nothing to read (no reference sent, or
// absence the deployment does not trust).
func (s *ITService) reevaluateTransfer(ctx context.Context, in *models.ITIntent) (FWTransfer, bool, error) {
	if in.PayoutID != "" {
		tr, err := s.readTransfer(ctx, in.PayoutID)
		return tr, err == nil, err
	}
	if payoutNeverSubmitted(in) || s.payoutTransfers == nil {
		return FWTransfer{}, false, nil
	}
	tr, found, err := s.findPayout(ctx, in)
	if err != nil {
		return FWTransfer{}, false, err
	}
	if !found && !s.fwAbsenceTrusted {
		return FWTransfer{}, false, nil
	}
	return tr, found, nil
}

// moveSellStatus applies a status change guarded on the statuses it was read in.
func (s *ITService) moveSellStatus(ctx context.Context, in *models.ITIntent, from []string, to, reason string) bool {
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status IN ?", in.ID, from).
		Updates(map[string]any{"status": to, "failure_reason": truncateReason(reason)})
	if res.Error != nil || res.RowsAffected != 1 {
		return false
	}
	in.Status, in.FailureReason = to, reason
	log.Printf("[it] intent=%s reevaluated → %s", in.ID, to)
	return true
}

// StuckMCGPSell is an MCGP sell that has not reached a terminal state, with
// the evidence an operator needs to resolve it.
type StuckMCGPSell struct {
	IntentID             uuid.UUID  `json:"intentId"`
	UserID               uuid.UUID  `json:"userId"`
	Status               string     `json:"status"`
	WalletAddress        string     `json:"walletAddress"`
	McgpAmount           string     `json:"mcgpAmount"`
	NgnKobo              string     `json:"ngnKobo"`
	NetNgnKobo           string     `json:"netNgnKobo,omitempty"`
	DebitRef             string     `json:"debitRef,omitempty"`
	TxHash               string     `json:"txHash,omitempty"`
	PayoutID             string     `json:"payoutId,omitempty"`
	PayoutAttempts       int        `json:"payoutAttempts"`
	PayoutRejected       bool       `json:"payoutRejected"`
	PayoutRefusedOnRetry bool       `json:"payoutRefusedOnRetry"`
	LastPayoutAttemptAt  *time.Time `json:"lastPayoutAttemptAt,omitempty"`
	FailureReason        string     `json:"failureReason,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	StuckSince           time.Time  `json:"stuckSince"`
}

// ListStuckMCGPSells lists MCGP sells still processing, debiting, debited or
// needing review after olderThan — each one is MCGP held in pendingSettle, or a
// debit whose fate is not yet recorded.
func (s *ITService) ListStuckMCGPSells(ctx context.Context, olderThan time.Duration) ([]StuckMCGPSell, error) {
	var rows []models.ITIntent
	if err := s.mcgpSells(ctx).
		Where("status IN ? AND updated_at < ?",
			[]string{models.ITStatusProcessing, models.ITStatusDebiting, models.ITStatusDebited, models.ITStatusNeedsReview}, time.Now().Add(-olderThan)).
		Order("updated_at asc").Limit(200).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]StuckMCGPSell, 0, len(rows))
	for i := range rows {
		in := &rows[i]
		out = append(out, StuckMCGPSell{
			IntentID: in.ID, UserID: in.UserID, Status: in.Status, WalletAddress: in.WalletAddress,
			McgpAmount: in.McgpAmount, NgnKobo: in.NgnKobo, NetNgnKobo: in.NetNgnKobo,
			DebitRef: in.DebitRef, TxHash: in.TxHash, PayoutID: in.PayoutID,
			PayoutAttempts: in.PayoutAttempts, PayoutRejected: in.PayoutRejected,
			PayoutRefusedOnRetry: in.PayoutRefusedOnRetry, LastPayoutAttemptAt: in.LastPayoutAttemptAt,
			FailureReason: in.FailureReason, CreatedAt: in.CreatedAt, StuckSince: in.UpdatedAt,
		})
	}
	return out, nil
}
