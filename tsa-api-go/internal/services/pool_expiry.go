package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StatementFreshness is the central-account poller (jobs.InboundCreditPoller).
type StatementFreshness interface{ LastIngestOK() time.Time }

const (
	expiryStatementStaleAfter = 10 * time.Minute
	expiryLegacyAfterNotice   = 30 * time.Minute // the watchdog told the buyer to dispute if paid (S24)
	// expiryLegacyLookback (review I-1): the legacy scope is a rolling window
	// on the expiry notice. Older trades are an ops step, never swept by a
	// deploy; a code constant, not a setting (decision 17).
	expiryLegacyLookback = 24 * time.Hour
	// expiryDeferredAlertAfter (review M-9): a stale statement defers every
	// cancel; past this it is an alert, repeated at most this often.
	expiryDeferredAlertAfter = 30 * time.Minute
	// expiryRealertEvery (re-review N-2/N-6): a per-trade alert repeats at
	// most this often — the scheduler's failure-streak cadence.
	expiryRealertEvery = time.Hour
	// expiryPayoutSweepEvery (re-review N-2): the backstop for a payout with
	// no outcome that has left the legacy window.
	expiryPayoutSweepEvery = 24 * time.Hour
	expiryPayoutSweepRun   = "p2p-payout-unresolved-alert" // its job_run_markers row
	expiryZombieWindow     = 2 * time.Hour                 // cancelled slices re-checked every minute
	expiryZombieSweep      = 7 * 24 * time.Hour
	expiryZombieEvery      = 24 * time.Hour
	expiryOpeningAfter     = 2 * time.Minute
	expiryPendingMargin    = 5 * time.Minute // past rate_expires_at + grace (§14); inside poolHonourSafety (Task 10)
	expiryPerChainDefault  = 20
	// expiryScanLimit is one page of a scope. Pages advance on a keyset and
	// wrap (review I1), so rows the canceller cannot act on yet — an open that
	// never landed waits for S44 — never hold the window.
	expiryScanLimit = 500
	// expiryResendAfter (review M10): a cancelByTimeout already sent is not
	// sent again while it may still mine, which would waste gas and revert.
	expiryResendAfter = 5 * time.Minute
)

// expiryKeyset is where a scope's next page starts.
type expiryKeyset struct {
	at time.Time
	id uuid.UUID
}

type ExpiryCancellerConfig struct {
	Trades   *P2PTradeService
	Signer   ChainSender
	Pool     *ITService // nil or without a desk: the legacy scope only
	Fresh    StatementFreshness
	PerChain int
	Now      func() time.Time
}

// ExpiryCanceller is spec §14 (plan C1 P15): always on, every minute. One
// instance per process (the scheduler runs it on one goroutine), so its
// paging cursors and send record need no lock.
type ExpiryCanceller struct {
	c                      ExpiryCancellerConfig
	lastZombie             time.Time
	poolAfter, legacyAfter expiryKeyset
	sentAt                 map[uuid.UUID]time.Time // trade → last cancelByTimeout sent (M10)
	// legacyAbsent (review I-1): legacy trades the escrow does not know (an
	// open on a retired escrow) → when they leave the window. Read once, then
	// skipped: the answer cannot change.
	legacyAbsent                     map[uuid.UUID]time.Time
	deferredSince, deferredAlertedAt time.Time
	moneyMovedAlerts                 alertThrottle
}

// alertThrottle is the scheduler's re-alert rule (jobs: alert on the first
// failure, then at most hourly) kept per key, for alerts raised on every
// tick or pass about one row. Its zero value is ready; safe for concurrent use.
type alertThrottle struct {
	mu   sync.Mutex
	last map[uuid.UUID]time.Time
}

// due reports whether id's alert should fire at now, and records it if so.
// A nil throttle never holds an alert back.
func (a *alertThrottle) due(id uuid.UUID, now time.Time) bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.last == nil {
		a.last = map[uuid.UUID]time.Time{}
	}
	if at, ok := a.last[id]; ok && now.Sub(at) < expiryRealertEvery {
		return false
	}
	for k, at := range a.last { // bounded: an entry past the interval would fire anyway
		if now.Sub(at) >= expiryRealertEvery {
			delete(a.last, k)
		}
	}
	a.last[id] = now
	return true
}

// errExpiryTradeAbsent: the escrow reports no such trade.
var errExpiryTradeAbsent = errors.New("p2p-expiry: trade not on the escrow")

// ExpiryTick is one run's outcome.
type ExpiryTick struct {
	Pool, Legacy, Zombies, Opening, Failed int
	Deferred                               bool
}

func NewExpiryCanceller(c ExpiryCancellerConfig) *ExpiryCanceller {
	if c.PerChain <= 0 {
		c.PerChain = expiryPerChainDefault
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &ExpiryCanceller{c: c, sentAt: map[uuid.UUID]time.Time{}, legacyAbsent: map[uuid.UUID]time.Time{}}
}

// page reads one keyset page of a scope and moves its cursor; a short page
// wraps the cursor to the start (review I1).
func (e *ExpiryCanceller) page(q *gorm.DB, col string, cur *expiryKeyset, keyOf func(models.P2PTrade) time.Time) ([]models.P2PTrade, error) {
	if !cur.at.IsZero() {
		q = q.Where("("+col+" > ?) OR ("+col+" = ? AND id > ?)", cur.at, cur.at, cur.id)
	}
	var rows []models.P2PTrade
	if err := q.Order(col + ", id").Limit(expiryScanLimit).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) < expiryScanLimit {
		*cur = expiryKeyset{}
	} else {
		last := rows[len(rows)-1]
		*cur = expiryKeyset{at: keyOf(last), id: last.ID}
	}
	return rows, nil
}

// RunOnce: the opening sweep (DB only), then — unless the statement is stale —
// the pool scope, the zombie window, the legacy scope, and once a day the
// 7-day zombie sweep. At most PerChain sends per chain.
func (e *ExpiryCanceller) RunOnce(ctx context.Context) (ExpiryTick, error) {
	// Not UTC: the SQLite rig compares timestamps as text in the local zone.
	// Postgres is unaffected (review M-6): the columns are timestamptz and
	// every comparison is between absolute instants.
	now := e.c.Now()
	var tick ExpiryTick
	var errs []error
	pool := e.c.Pool != nil && e.c.Pool.HasPool()
	if pool {
		n, err := e.c.Pool.SweepStaleOpening(ctx, now)
		tick.Opening = n
		errs = append(errs, err)
	}
	// Pre-merge review M-4: claimed in job_run_markers, so a deploy or restart
	// does not re-alert every historical row.
	if claimRunAt(ctx, e.c.Trades.db, expiryPayoutSweepRun, expiryPayoutSweepEvery, now) {
		errs = append(errs, e.c.Trades.alertUnresolvedPayouts(ctx, now))
	}
	if e.c.Fresh != nil {
		if last := e.c.Fresh.LastIngestOK(); last.IsZero() || now.Sub(last) > expiryStatementStaleAfter {
			tick.Deferred = true
			log.Printf("[p2p-expiry] METRIC p2p_expiry_deferred reason=statement_stale last_ok=%s", last.Format(time.RFC3339))
			e.noteDeferred(now, last)
			return tick, errors.Join(errs...)
		}
	}
	e.deferredSince, e.deferredAlertedAt = time.Time{}, time.Time{}
	budget := map[int64]int{}
	if pool {
		zombieSince := now.Add(-expiryZombieWindow)
		if now.Sub(e.lastZombie) >= expiryZombieEvery {
			zombieSince, e.lastZombie = now.Add(-expiryZombieSweep), now
		}
		p, z, f, err := e.poolScope(ctx, now, zombieSince, budget)
		tick.Pool, tick.Zombies, tick.Failed = p, z, tick.Failed+f
		errs = append(errs, err)
	}
	l, f, err := e.legacyScope(ctx, now, budget)
	tick.Legacy, tick.Failed = l, tick.Failed+f
	errs = append(errs, err)
	return tick, errors.Join(errs...)
}

// noteDeferred is review M-9: a deferral that lasts is an alert.
func (e *ExpiryCanceller) noteDeferred(now, lastOK time.Time) {
	if e.deferredSince.IsZero() {
		e.deferredSince = now
	}
	if now.Sub(e.deferredSince) >= expiryDeferredAlertAfter && now.Sub(e.deferredAlertedAt) >= expiryDeferredAlertAfter {
		e.deferredAlertedAt = now
		log.Printf("[p2p-expiry] ALERT p2p_expiry_deferred_sustained since=%s last_ok=%s — the central-account statement is stale; no expired trade is being cancelled",
			e.deferredSince.Format(time.RFC3339), lastOK.Format(time.RFC3339))
	}
}

func (e *ExpiryCanceller) windowFor(chainID int64) time.Duration {
	if e.c.Trades.IsPoolChain(chainID) {
		return poolSliceWindow
	}
	return p2pPaymentWindow
}

// cancelOnChain sends cancelByTimeout through the release key's per-chain
// lock (S8, S24) once the trade is Pending past its window, then lets the
// reconciler move the row (idempotent; a second cancel reverts NotPending).
// decide (nil for a zombie, whose row is already cancelled) runs last, just
// before the send: it takes the row lock the payment paths take and records
// the decision, so a payment and the cancel can never both win.
func (e *ExpiryCanceller) cancelOnChain(ctx context.Context, tr *models.P2PTrade, now time.Time, decide func() (bool, error)) (bool, error) {
	if at, ok := e.sentAt[tr.ID]; ok && now.Sub(at) < expiryResendAfter {
		return false, nil // review M10: the last cancel may still mine (checked before any read)
	}
	escrow, _, _, err := e.c.Trades.resolve(tr.ChainID)
	if err != nil {
		return false, err
	}
	onchain, err := escrow.ReadTrade(HashID(tr.ID))
	switch {
	case err != nil:
		return false, fmt.Errorf("read %s: %s", tr.ID, RedactAlertText(err.Error())) // review M-11: never silent
	case !onchain.Exists:
		return false, errExpiryTradeAbsent
	case onchain.Status != OnChainP2PStatusPending || onchain.CreatedAt == nil:
		return false, nil
	}
	if time.Unix(onchain.CreatedAt.Int64(), 0).Add(e.windowFor(tr.ChainID)).After(now) {
		return false, nil
	}
	if decide != nil {
		ok, err := decide()
		if err != nil {
			return false, fmt.Errorf("decide %s: %s", tr.ID, RedactAlertText(err.Error()))
		}
		if !ok {
			return false, nil
		}
	}
	for id, at := range e.sentAt { // bounded: entries older than the back-off are useless
		if now.Sub(at) >= expiryResendAfter {
			delete(e.sentAt, id)
		}
	}
	e.sentAt[tr.ID] = now
	hash, err := e.c.Signer.Send(ctx, tr.ChainID, func() ([]byte, error) {
		return escrow.PrepareCancelByTimeout(HashID(tr.ID), e.c.Signer.Address())
	})
	if err != nil {
		if errors.Is(err, ErrTxNotBroadcast) {
			delete(e.sentAt, tr.ID) // nothing was sent: no back-off
			return false, fmt.Errorf("cancel %s: %w: %s", tr.ID, ErrTxNotBroadcast, RedactAlertText(err.Error()))
		}
		return false, fmt.Errorf("cancel %s: %s", tr.ID, RedactAlertText(err.Error()))
	}
	log.Printf("[p2p-expiry] trade=%s cancelByTimeout sent tx=%s", tr.ID, hash)
	if _, err := e.c.Trades.ReconcileTrade(ctx, tr.ID); err != nil {
		log.Printf("[p2p-expiry] trade=%s cancelled on-chain, reconcile deferred: %s", tr.ID, RedactAlertText(err.Error()))
	}
	return true, nil
}

func (e *ExpiryCanceller) poolScope(ctx context.Context, now, zombieSince time.Time, budget map[int64]int) (pool, zombies, failed int, err error) {
	q := e.c.Trades.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("it_intent_id IS NOT NULL AND created_at <= ?", now.Add(-poolSliceWindow)).
		Where("(status = ?) OR (status = ? AND updated_at > ?)", models.P2PTradeStatusPendingPayment, models.P2PTradeStatusCancelled, zombieSince)
	rows, err := e.page(q, "created_at", &e.poolAfter, func(t models.P2PTrade) time.Time { return t.CreatedAt })
	if err != nil {
		return 0, 0, 0, err
	}
	for i := range rows {
		tr := &rows[i]
		if budget[tr.ChainID] >= e.c.PerChain {
			continue
		}
		var decide func() (bool, error)
		if tr.Status == models.P2PTradeStatusPendingPayment {
			decide = func() (bool, error) { return e.c.Pool.decideSliceCancel(ctx, tr, now) }
		}
		sent, cerr := e.cancelOnChain(ctx, tr, now, decide)
		switch {
		case errors.Is(cerr, errExpiryTradeAbsent):
			// an open that never landed: S44's to reclaim; read again next time
		case cerr != nil:
			failed++
			budget[tr.ChainID]++
			log.Printf("[p2p-expiry] %s", cerr)
		case sent:
			budget[tr.ChainID]++
			scope := "pool"
			if tr.Status == models.P2PTradeStatusCancelled {
				zombies++
				scope = "zombie"
			} else {
				pool++
			}
			log.Printf("[p2p-expiry] METRIC p2p_expiry_cancelled scope=%s chain=%d trade=%s", scope, tr.ChainID, tr.ID)
		}
	}
	return pool, zombies, failed, nil
}

func (e *ExpiryCanceller) legacyScope(ctx context.Context, now time.Time, budget map[int64]int) (legacy, failed int, err error) {
	q := e.c.Trades.db.WithContext(ctx).Model(&models.P2PTrade{}).Scopes(notPoolSlice).
		Where("status = ? AND expired_notified_at IS NOT NULL AND expired_notified_at < ? AND expired_notified_at >= ?",
			models.P2PTradeStatusPendingPayment, now.Add(-expiryLegacyAfterNotice), now.Add(-expiryLegacyLookback))
	rows, err := e.page(q, "expired_notified_at", &e.legacyAfter, func(t models.P2PTrade) time.Time {
		if t.ExpiredNotifiedAt == nil {
			return time.Time{}
		}
		return *t.ExpiredNotifiedAt
	})
	if err != nil {
		return 0, 0, err
	}
	for id, until := range e.legacyAbsent {
		if !now.Before(until) {
			delete(e.legacyAbsent, id)
		}
	}
	for i := range rows {
		tr := &rows[i]
		if budget[tr.ChainID] >= e.c.PerChain {
			continue
		}
		if _, absent := e.legacyAbsent[tr.ID]; absent {
			continue
		}
		sent, cerr := e.cancelOnChain(ctx, tr, now, func() (bool, error) {
			cancel, leg, err := e.c.Trades.decideLegacyCancelLeg(ctx, tr, now)
			if leg != "" && e.moneyMovedAlerts.due(tr.ID, now) {
				metric := "p2p_expiry_money_moved"
				if leg == "payout" {
					metric = "p2p_expiry_payout_in_flight"
				}
				log.Printf("[p2p-expiry] ALERT %s trade=%s leg=%s: expired but naira has moved; not cancelled (repeats hourly)", metric, tr.ID, leg)
			}
			return cancel, err
		})
		switch {
		case errors.Is(cerr, errExpiryTradeAbsent):
			e.legacyAbsent[tr.ID] = tr.ExpiredNotifiedAt.Add(expiryLegacyLookback)
			log.Printf("[p2p-expiry] METRIC p2p_expiry_legacy_absent chain=%d trade=%s — not on the current escrow; skipped", tr.ChainID, tr.ID)
		case cerr != nil:
			failed++
			budget[tr.ChainID]++
			log.Printf("[p2p-expiry] %s", cerr)
			if errors.Is(cerr, ErrTxNotBroadcast) {
				e.c.Trades.undoLegacyDecision(ctx, tr.ID, now) // review M-7
			}
		case sent:
			legacy++
			budget[tr.ChainID]++
			log.Printf("[p2p-expiry] METRIC p2p_expiry_cancelled scope=legacy chain=%d trade=%s", tr.ChainID, tr.ID)
			e.c.Trades.alertHeldCreditOnCancel(ctx, tr)
		}
	}
	return legacy, failed, nil
}

// poolSliceCancellable is §14's rule (plan C1 P15). Never a slice that still
// backs a paid order.
func poolSliceCancellable(intent *models.ITIntent, slice *models.P2PTrade, now time.Time, grace time.Duration) bool {
	if slice.DroppedAt != nil {
		return true
	}
	switch intent.Status {
	case models.ITStatusCancelled, models.ITStatusRefunded, models.ITStatusOpening:
		return true
	case models.ITStatusBuyAwaitingRate:
		return strings.HasPrefix(intent.FailureReason, models.ITFailurePoolSlicesShort) // never a rate_unavailable park (M1)
	case models.ITStatusPendingPayment:
		return intent.RateExpiresAt != nil && now.After(intent.RateExpiresAt.Add(grace+expiryPendingMargin))
	}
	return false // payment_received, delivering, settled
}

// decideSliceCancel decides under the intent's row lock (the claim takes the
// same lock), and marks the slice dropped in that transaction: whoever locks
// the intent first wins, deterministically (P1).
func (s *ITService) decideSliceCancel(ctx context.Context, slice *models.P2PTrade, now time.Time) (bool, error) {
	if slice.ITIntentID == nil {
		return false, nil
	}
	cancel := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var intent models.ITIntent
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&intent, "id = ?", *slice.ITIntentID).Error; err != nil {
			return err
		}
		var cur models.P2PTrade
		if err := tx.First(&cur, "id = ?", slice.ID).Error; err != nil {
			return err
		}
		cancel = cur.Status == models.P2PTradeStatusPendingPayment && poolSliceCancellable(&intent, &cur, now, s.rampTerms().honourGrace)
		if cancel && cur.DroppedAt == nil {
			return tx.Model(&models.P2PTrade{}).Where("id = ?", cur.ID).Update("dropped_at", now).Error
		}
		return nil
	})
	return cancel, err
}

// decideLegacyCancel is the legacy scope's decision (S24), taken under the
// trade's row lock: still pending_payment, its expiry notice 30 minutes old,
// and no naira has moved for it off-chain (legacyMoneyMoved). It records the
// decision in dropped_at in the same transaction. Every payment path refuses a
// trade with dropped_at set — the credit (which takes the same lock), the sell
// payout (which commits its started marker under it before the gateway call), the operator's and the
// buyer's mark-paid, and the release — so whoever locks the row first wins.
func (s *P2PTradeService) decideLegacyCancel(ctx context.Context, tr *models.P2PTrade, now time.Time) (bool, error) {
	cancel, _, err := s.decideLegacyCancelLeg(ctx, tr, now)
	return cancel, err
}

// decideLegacyCancelLeg is decideLegacyCancel, also naming the off-chain leg
// that refused it ("" when none did), for the canceller's throttled alert.
func (s *P2PTradeService) decideLegacyCancelLeg(ctx context.Context, tr *models.P2PTrade, now time.Time) (bool, string, error) {
	cancel, moved := false, ""
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var cur models.P2PTrade
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, "id = ?", tr.ID).Error; err != nil {
			return err
		}
		if cur.IsPoolSlice() || cur.Status != models.P2PTradeStatusPendingPayment ||
			cur.ExpiredNotifiedAt == nil || !cur.ExpiredNotifiedAt.Before(now.Add(-expiryLegacyAfterNotice)) {
			return nil
		}
		leg, err := legacyMoneyMoved(tx, &cur)
		if err != nil {
			return err
		}
		if leg != "" {
			moved = leg
			return nil
		}
		cancel = true
		if cur.DroppedAt != nil {
			return nil
		}
		return tx.Model(&models.P2PTrade{}).Where("id = ?", cur.ID).Update("dropped_at", now).Error
	})
	return cancel, moved, err
}

// alertUnresolvedPayouts is re-review N-2's backstop: a sell payout started
// (or recorded) with no outcome — no success settled the trade, no failure
// was recorded — whose trade has left the legacy window is never silent. The
// trade waits for its webhook or an operator (provider lookup, then replay
// the webhook); this names it once a day until then.
func (s *P2PTradeService) alertUnresolvedPayouts(ctx context.Context, now time.Time) error {
	var rows []models.P2PTrade
	if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).Scopes(notPoolSlice).
		Select("id", "chain_id", "nomba_payout_id").
		Where("status = ? AND (payout_started_at IS NOT NULL OR nomba_payout_id <> '')", models.P2PTradeStatusPendingPayment).
		Where("cancel_reason IS NULL OR cancel_reason NOT LIKE ?", pmPayoutFailedReason+"%").
		Where("expired_notified_at IS NOT NULL AND expired_notified_at < ?", now.Add(-expiryLegacyLookback)).
		Order("expired_notified_at").Limit(100).Find(&rows).Error; err != nil {
		return fmt.Errorf("unresolved payouts: %w", err)
	}
	for _, r := range rows {
		log.Printf("[p2p-expiry] ALERT p2p_payout_outcome_unresolved trade=%s chain=%d payout=%q — a sell payout with no outcome, past the canceller's window; look it up at the provider and replay its webhook (repeats daily)",
			r.ID, r.ChainID, r.NombaPayoutID)
	}
	return nil
}

// alertHeldCreditOnCancel is pre-merge review M-2. The canceller cannot see a
// central-account credit the matcher held (O-C1-2: ops refund it rather than
// bind it to a cancelled trade), so when it cancels a merchant buy trade it
// names any held or unmatched credit of the trade's amount that arrived since
// the trade opened. Ids and the hold code only: never the payer's name.
func (s *P2PTradeService) alertHeldCreditOnCancel(ctx context.Context, tr *models.P2PTrade) {
	kobo, ok := ngnToKobo(tr.FiatAmount)
	if !ok || kobo <= 0 {
		return
	}
	var merchant int64
	if err := s.db.WithContext(ctx).Model(&models.P2PAd{}).
		Where("id = ? AND pm_id IS NOT NULL AND side = ?", tr.AdID, models.P2PAdSideSell).Count(&merchant).Error; err != nil || merchant == 0 {
		return
	}
	var credits []models.InboundCredit
	if err := s.db.WithContext(ctx).Select("id", "status", "hold_reason_code").
		Where("status IN ? AND currency = ? AND gross_kobo IN ? AND transaction_date >= ?",
			[]string{models.InboundCreditHeld, models.InboundCreditUnmatched}, normaliseCurrency(tr.FiatCurrency),
			[]int64{kobo, RoundKoboToNaira(kobo)}, tr.CreatedAt).
		Limit(5).Find(&credits).Error; err != nil {
		log.Printf("[p2p-expiry] trade=%s held-credit check failed: %s", tr.ID, RedactAlertText(err.Error()))
		return
	}
	for _, c := range credits {
		log.Printf("[p2p-expiry] ALERT p2p_expiry_cancelled_with_held_credit trade=%s credit=%s status=%s hold=%s — the trade was cancelled with a credit of its amount waiting; refund the credit, never assign it to this trade",
			tr.ID, c.ID, c.Status, c.HoldReasonCode)
	}
}

// undoLegacyDecision is review M-7: the cancel this tick decided provably
// never broadcast, so its decision comes off and the trade's payment paths
// open again until a later tick cancels it. Guarded to this tick's decision
// (a decision whose cancel may have broadcast keeps its older dropped_at).
func (s *P2PTradeService) undoLegacyDecision(ctx context.Context, id uuid.UUID, decidedAt time.Time) {
	if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("id = ? AND status = ? AND dropped_at >= ? AND it_intent_id IS NULL", id, models.P2PTradeStatusPendingPayment, decidedAt.Add(-time.Second)).
		Update("dropped_at", nil).Error; err != nil {
		log.Printf("[p2p-expiry] ALERT p2p_expiry_undo_failed trade=%s: %s — decided but not cancelled; payments refused until a later cancel lands",
			id, RedactAlertText(err.Error()))
	}
}

// legacyMoneyMoved is the legacy scope's one positive predicate (review C-1):
// has naira moved — or started to move — for this ordinary trade off-chain?
// It names the leg, or "" when none has. cancelByTimeout refunds the crypto,
// so any of these refuses it:
//   - payout: the sell payout to the user's bank was started — its marker is
//     committed before the gateway call, so this holds even when the payout
//     id was never recorded — unless the failure handler recorded it failed
//     (then nothing left and the deposit is owed back);
//   - inbound: a merchant-account inbound was matched to the trade;
//   - forward: a central-account credit claimed the trade (the forward row
//     lands before the trade is marked paid).
//
// A new off-chain leg belongs here.
func legacyMoneyMoved(tx *gorm.DB, tr *models.P2PTrade) (string, error) {
	if (tr.NombaPayoutID != "" || tr.PayoutStartedAt != nil) && !strings.HasPrefix(tr.CancelReason, pmPayoutFailedReason) {
		return "payout", nil
	}
	if tr.NombaInboundEventID != "" {
		return "inbound", nil
	}
	var credited int64
	if err := tx.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", tr.ID).Count(&credited).Error; err != nil {
		return "", err
	}
	if credited > 0 {
		return "forward", nil
	}
	return "", nil
}

// SweepStaleOpening is F3: an opening order older than 2 minutes was left by a
// crash; cancel it as a failed open (hidden, never an abandon), free its slices.
func (s *ITService) SweepStaleOpening(ctx context.Context, now time.Time) (int, error) {
	if s.poolDesk == nil {
		return 0, nil
	}
	var stale []models.ITIntent
	if err := s.db.WithContext(ctx).Where("status = ? AND created_at < ?", models.ITStatusOpening, now.Add(-expiryOpeningAfter)).
		Limit(100).Find(&stale).Error; err != nil {
		return 0, err
	}
	for i := range stale {
		s.poolDesk.fail(ctx, &stale[i], nil)
		log.Printf("[liquidity] METRIC ramp_pool_open outcome=swept intent=%s", stale[i].ID)
	}
	return len(stale), nil
}
