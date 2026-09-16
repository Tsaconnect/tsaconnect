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
	// ErrITSellPayoutConflict: the provider's record contradicts the requested
	// admin action (e.g. reversing a sell whose transfer succeeded).
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
	// itPayoutLookupSlack widens the provider search window before the intent
	// was created, for clock skew between us and the provider.
	itPayoutLookupSlack = 10 * time.Minute
	// itPayoutAbsentSettle is how long after the last payout attempt a reference
	// missing from the provider's transfer list counts as "never created" for an
	// admin reverse — a request that just timed out may not be listed yet.
	itPayoutAbsentSettle = 10 * time.Minute
)

// itPayoutTransfers reads sell payouts back from the provider.
type itPayoutTransfers interface {
	GetTransfer(ctx context.Context, id string) (FWTransfer, error)
	FindTransferByReference(ctx context.Context, reference string, from, to time.Time) (FWTransfer, bool, error)
}

// SetPayoutTransfers wires the provider transfer reader used as evidence by
// the sell reconciler and the admin settle/reverse guards.
func (s *ITService) SetPayoutTransfers(r itPayoutTransfers) { s.payoutTransfers = r }

// mcgpSells scopes a query to MCGP sells (rows predating the asset column are
// MCGP). Stablecoin sells never touch the swap contract.
func (s *ITService) mcgpSells(ctx context.Context) *gorm.DB {
	return s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("direction = ? AND (asset IS NULL OR asset = '' OR asset = ?)", models.ITDirectionSell, models.ITAssetMCGP)
}

func isMCGPSell(in *models.ITIntent) bool {
	return in.Direction == models.ITDirectionSell && models.ITIntentAsset(in) == models.ITAssetMCGP
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
//	    none after itDebitAbandonAfter → failed; resolved/mismatched → ALERT
//	failed (recent), never paid out — a debit that mined late → reverse
//	debited past grace —
//	    payout rejected                          → reverse
//	    payout id known                          → provider SUCCESSFUL settle,
//	                                               FAILED reverse, else wait/ALERT
//	    payout never sent                        → send it (within the window)
//	    payout sent, id unknown                  → look the reference up: found →
//	                                               adopt + resolve; absent → re-send
//	                                               (same reference, bounded) or ALERT
//
// Idempotent and safe to run on several instances: every write is guarded on
// the state it read, and every chain op is keyed by the intent's single-use ref.
func (s *ITService) ReconcileStuckSells(ctx context.Context, grace time.Duration) (int, error) {
	if !s.IsConfigured() {
		return 0, ErrITNotConfigured
	}
	now := time.Now()
	changed := 0

	var inflight []models.ITIntent
	if err := s.mcgpSells(ctx).
		Where("status IN ? AND updated_at < ?", []string{models.ITStatusProcessing, models.ITStatusDebiting}, now.Add(-grace)).
		Order("updated_at asc").Limit(50).Find(&inflight).Error; err != nil {
		return changed, err
	}
	for i := range inflight {
		if s.reconcileInflightSell(ctx, &inflight[i], now) {
			changed++
		}
	}

	var failed []models.ITIntent
	if err := s.mcgpSells(ctx).
		Where("status = ? AND updated_at > ? AND payout_id = '' AND payout_attempts = 0 AND debit_ref <> ''",
			models.ITStatusFailed, now.Add(-itLateDebitScanWindow)).
		Order("updated_at asc").Limit(50).Find(&failed).Error; err != nil {
		return changed, err
	}
	for i := range failed {
		if s.reconcileFailedSell(ctx, &failed[i]) {
			changed++
		}
	}

	var debited []models.ITIntent
	if err := s.mcgpSells(ctx).
		Where("status = ? AND updated_at < ?", models.ITStatusDebited, now.Add(-grace)).
		Order("updated_at asc").Limit(50).Find(&debited).Error; err != nil {
		return changed, err
	}
	for i := range debited {
		if s.reconcileDebitedSell(ctx, &debited[i], now) {
			changed++
		}
	}
	return changed, nil
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
			log.Printf("[it-sell-reconciler] ALERT it_sell_debit_mismatch intent=%s on-chain user=%s amount=%s intent wallet=%s amount=%s",
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
		log.Printf("[it-sell-reconciler] ALERT it_sell_debit_resolved_unrecorded intent=%s status=%s — the debit was already settled or reversed on-chain",
			in.ID, in.Status)
		return false
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
		log.Printf("[it-sell-reconciler] ALERT it_sell_debit_mismatch intent=%s (failed row) on-chain user=%s amount=%s",
			in.ID, rec.User.Hex(), rec.Amount)
		return false
	}
	res := s.db.WithContext(ctx).Model(&models.ITIntent{}).
		Where("id = ? AND status = ? AND payout_id = '' AND payout_attempts = 0", in.ID, models.ITStatusFailed).
		Updates(map[string]any{
			"status":          models.ITStatusDebited,
			"payout_rejected": true,
			"failure_reason":  "debit landed after the sell was marked failed; reversing",
		})
	if res.Error != nil || res.RowsAffected != 1 {
		return false
	}
	log.Printf("[it-sell-reconciler] ALERT it_sell_late_debit intent=%s — debit mined after failure; reversing", in.ID)
	_, err = s.ReverseSell(ctx, in.ID, "")
	if err != nil {
		log.Printf("[it-sell-reconciler] intent=%s late-debit reverse failed (will retry): %v", in.ID, err)
	}
	return true
}

func (s *ITService) reconcileDebitedSell(ctx context.Context, in *models.ITIntent, now time.Time) bool {
	if in.PayoutRejected {
		if _, err := s.ReverseSell(ctx, in.ID, ""); err != nil {
			log.Printf("[it-sell-reconciler] intent=%s reverse of rejected payout failed: %v", in.ID, err)
			return false
		}
		log.Printf("[it-sell-reconciler] intent=%s payout was rejected → reversed", in.ID)
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
	if s.payout == nil || !sellIntentHasPayoutDestination(in) {
		if in.CreatedAt.Before(now.Add(-itPayoutPendingAlertAfter)) {
			log.Printf("[it-sell-reconciler] ALERT it_sell_manual_payout_outstanding intent=%s — debited with no automated payout", in.ID)
		}
		return false
	}
	withinWindow := in.CreatedAt.After(now.Add(-itPayoutAutoRetryWindow))
	if payoutNeverSubmitted(in) {
		if !withinWindow {
			log.Printf("[it-sell-reconciler] ALERT it_sell_payout_never_sent intent=%s — too old to send automatically", in.ID)
			return false
		}
		return s.submitSellPayout(ctx, in)
	}
	if !withinWindow {
		// Past the window only an operator acts (retry-payout / reverse both
		// search the provider first); scanning every tick would be load for no
		// automatic action.
		log.Printf("[it-sell-reconciler] ALERT it_sell_payout_unresolved intent=%s attempts=%d — outcome unknown; operator retry-payout or reverse required",
			in.ID, in.PayoutAttempts)
		return false
	}
	if s.payoutTransfers == nil {
		return false
	}
	tr, found, err := s.payoutTransfers.FindTransferByReference(ctx, itPayoutRef(in.ID), in.CreatedAt.Add(-itPayoutLookupSlack), now)
	if err != nil {
		log.Printf("[it-sell-reconciler] intent=%s payout lookup failed: %v", in.ID, err)
		return false
	}
	if found {
		if !s.adoptPayoutID(ctx, in, tr.ID) {
			return false
		}
		return s.resolveSellFromTransfer(ctx, in, tr, now)
	}
	if in.PayoutAttempts >= itPayoutMaxAttempts {
		log.Printf("[it-sell-reconciler] ALERT it_sell_payout_unresolved intent=%s attempts=%d — no transfer found at provider; operator retry-payout or reverse required",
			in.ID, in.PayoutAttempts)
		return false
	}
	return s.submitSellPayout(ctx, in)
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
		log.Printf("[it-sell-reconciler] intent=%s payout outcome unknown: %v", in.ID, err)
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
		Where("id = ? AND payout_id = ''", in.ID).
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
			log.Printf("[it-sell-reconciler] ALERT it_sell_payout_not_final intent=%s transfer=%s status=%s", in.ID, tr.ID, tr.Status)
		}
		return false
	}
}

// ─── Admin ──────────────────────────────────────────────────────────────────

// AdminReverseSell reverses an MCGP sell only once the provider confirms the
// payout did not happen: the transfer is FAILED, the payout was rejected, or
// the reference was never sent / is absent from the provider's transfer list.
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
// payout succeeded. With no automated payout gateway (the manual payout flow)
// the operator's own confirmation stands, as before.
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

// RetrySellPayout re-drives the payout of a debited MCGP sell. A reference that
// may already have been sent is looked up first and adopted if it exists; only
// a reference the provider has no record of is sent again (reusing the same
// reference, which is the provider's idempotency key).
func (s *ITService) RetrySellPayout(ctx context.Context, intentID uuid.UUID) (*models.ITIntent, error) {
	in, err := s.adminMCGPSell(ctx, intentID)
	if err != nil {
		return nil, err
	}
	if in.Status != models.ITStatusDebited || in.PayoutID != "" {
		return nil, fmt.Errorf("%w: only a debited sell with no recorded payout can be retried", ErrITBadState)
	}
	if in.PayoutRejected {
		return nil, fmt.Errorf("%w: the payout was rejected; reverse this sell instead", ErrITBadState)
	}
	if s.payout == nil {
		return nil, errors.New("it: no payout gateway configured")
	}
	if !payoutNeverSubmitted(in) {
		if s.payoutTransfers == nil {
			return nil, fmt.Errorf("%w: no provider reader configured", ErrITSellPayoutUnverified)
		}
		tr, found, err := s.payoutTransfers.FindTransferByReference(ctx, itPayoutRef(in.ID), in.CreatedAt.Add(-itPayoutLookupSlack), time.Now())
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrITSellPayoutUnverified, err)
		}
		if found {
			if !s.adoptPayoutID(ctx, in, tr.ID) {
				return nil, fmt.Errorf("%w: could not record transfer %s", ErrITBadState, tr.ID)
			}
			return s.getIntent(ctx, in.ID)
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
	// Manual payout flow: no automated transfer was ever made.
	if s.payout == nil || payoutNeverSubmitted(in) {
		return nil
	}
	if time.Since(in.UpdatedAt) < itPayoutAbsentSettle {
		return fmt.Errorf("%w: the last payout attempt is too recent to rule out; try again in a few minutes", ErrITSellPayoutUnverified)
	}
	tr, found, err := s.findPayout(ctx, in)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	s.adoptPayoutID(ctx, in, tr.ID)
	if !strings.EqualFold(tr.Status, "FAILED") {
		return fmt.Errorf("%w: transfer %s is %s — reversing would pay the user twice", ErrITSellPayoutConflict, tr.ID, tr.Status)
	}
	return nil
}

func (s *ITService) requirePayoutSucceeded(ctx context.Context, in *models.ITIntent) error {
	if in.PayoutRejected {
		return fmt.Errorf("%w: the payout was rejected — settling would take the user's MCGP", ErrITSellPayoutConflict)
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
	if s.payout == nil {
		return nil // manual payout flow: the operator paid and confirms it
	}
	if payoutNeverSubmitted(in) {
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
	tr, found, err := s.payoutTransfers.FindTransferByReference(ctx, itPayoutRef(in.ID), in.CreatedAt.Add(-itPayoutLookupSlack), time.Now())
	if err != nil {
		return FWTransfer{}, false, fmt.Errorf("%w: %v", ErrITSellPayoutUnverified, err)
	}
	return tr, found, nil
}

// StuckMCGPSell is an MCGP sell that has not reached a terminal state, with
// the evidence an operator needs to resolve it.
type StuckMCGPSell struct {
	IntentID       uuid.UUID `json:"intentId"`
	UserID         uuid.UUID `json:"userId"`
	Status         string    `json:"status"`
	WalletAddress  string    `json:"walletAddress"`
	McgpAmount     string    `json:"mcgpAmount"`
	NgnKobo        string    `json:"ngnKobo"`
	NetNgnKobo     string    `json:"netNgnKobo,omitempty"`
	DebitRef       string    `json:"debitRef,omitempty"`
	TxHash         string    `json:"txHash,omitempty"`
	PayoutID       string    `json:"payoutId,omitempty"`
	PayoutAttempts int       `json:"payoutAttempts"`
	PayoutRejected bool      `json:"payoutRejected"`
	FailureReason  string    `json:"failureReason,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	StuckSince     time.Time `json:"stuckSince"`
}

// ListStuckMCGPSells lists MCGP sells still processing, debiting or debited
// after olderThan — each one is MCGP held in pendingSettle, or a debit whose
// fate is not yet known.
func (s *ITService) ListStuckMCGPSells(ctx context.Context, olderThan time.Duration) ([]StuckMCGPSell, error) {
	var rows []models.ITIntent
	if err := s.mcgpSells(ctx).
		Where("status IN ? AND updated_at < ?",
			[]string{models.ITStatusProcessing, models.ITStatusDebiting, models.ITStatusDebited}, time.Now().Add(-olderThan)).
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
			FailureReason: in.FailureReason, CreatedAt: in.CreatedAt, StuckSince: in.UpdatedAt,
		})
	}
	return out, nil
}
