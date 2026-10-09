package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
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
	expiryZombieWindow        = 2 * time.Hour    // cancelled slices re-checked every minute
	expiryZombieSweep         = 7 * 24 * time.Hour
	expiryZombieEvery         = 24 * time.Hour
	expiryOpeningAfter        = 2 * time.Minute
	expiryPendingMargin       = 5 * time.Minute // past rate_expires_at + grace (§14); inside poolHonourSafety (Task 10)
	expiryPerChainDefault     = 20
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
}

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
	return &ExpiryCanceller{c: c, sentAt: map[uuid.UUID]time.Time{}}
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
	now := e.c.Now() // not UTC: the SQLite rig compares timestamps as text in the local zone
	var tick ExpiryTick
	var errs []error
	pool := e.c.Pool != nil && e.c.Pool.HasPool()
	if pool {
		n, err := e.c.Pool.SweepStaleOpening(ctx, now)
		tick.Opening = n
		errs = append(errs, err)
	}
	if e.c.Fresh != nil {
		if last := e.c.Fresh.LastIngestOK(); last.IsZero() || now.Sub(last) > expiryStatementStaleAfter {
			tick.Deferred = true
			log.Printf("[p2p-expiry] METRIC p2p_expiry_deferred reason=statement_stale last_ok=%s", last.Format(time.RFC3339))
			return tick, errors.Join(errs...)
		}
	}
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
	escrow, _, _, err := e.c.Trades.resolve(tr.ChainID)
	if err != nil {
		return false, err
	}
	onchain, err := escrow.ReadTrade(HashID(tr.ID))
	if err != nil || !onchain.Exists || onchain.Status != OnChainP2PStatusPending || onchain.CreatedAt == nil {
		return false, nil
	}
	if time.Unix(onchain.CreatedAt.Int64(), 0).Add(e.windowFor(tr.ChainID)).After(now) {
		return false, nil
	}
	if at, ok := e.sentAt[tr.ID]; ok && now.Sub(at) < expiryResendAfter {
		return false, nil // review M10: the last cancel may still mine
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
		Where("status = ? AND expired_notified_at IS NOT NULL AND expired_notified_at < ?",
			models.P2PTradeStatusPendingPayment, now.Add(-expiryLegacyAfterNotice))
	rows, err := e.page(q, "expired_notified_at", &e.legacyAfter, func(t models.P2PTrade) time.Time {
		if t.ExpiredNotifiedAt == nil {
			return time.Time{}
		}
		return *t.ExpiredNotifiedAt
	})
	if err != nil {
		return 0, 0, err
	}
	for i := range rows {
		tr := &rows[i]
		if budget[tr.ChainID] >= e.c.PerChain {
			continue
		}
		sent, cerr := e.cancelOnChain(ctx, tr, now, func() (bool, error) { return e.c.Trades.decideLegacyCancel(ctx, tr, now) })
		switch {
		case cerr != nil:
			failed++
			budget[tr.ChainID]++
			log.Printf("[p2p-expiry] %s", cerr)
		case sent:
			legacy++
			budget[tr.ChainID]++
			log.Printf("[p2p-expiry] METRIC p2p_expiry_cancelled scope=legacy chain=%d trade=%s", tr.ChainID, tr.ID)
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
	cancel := false
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
			metric := "p2p_expiry_money_moved"
			if leg == "payout" {
				metric = "p2p_expiry_payout_in_flight"
			}
			log.Printf("[p2p-expiry] ALERT %s trade=%s leg=%s: expired but naira has moved; not cancelled", metric, cur.ID, leg)
			return nil
		}
		cancel = true
		if cur.DroppedAt != nil {
			return nil
		}
		return tx.Model(&models.P2PTrade{}).Where("id = ?", cur.ID).Update("dropped_at", now).Error
	})
	return cancel, err
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
