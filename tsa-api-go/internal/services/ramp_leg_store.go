package services

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrLegNotPending is returned by Confirm and Fail when the targeted leg is
// no longer pending: a late or duplicate call must never overwrite a row that
// already reached a terminal status, since refunds are computed from these
// rows and a self-contradicting row is a money bug.
var ErrLegNotPending = errors.New("ramp leg is no longer pending")

// rampLegStore is the ledger of on-chain movements for a converted ramp
// order. Refund amounts are computed from these rows, never typed by hand, so
// idempotency here is what stops a retried background tick moving a user's
// money twice.
type rampLegStore interface {
	Reserve(ctx context.Context, leg *models.RampLeg) (*models.RampLeg, error)
	SetTxHash(ctx context.Context, id uuid.UUID, txHash, tool string) error
	SetApproveHash(ctx context.Context, id uuid.UUID, txHash string) error
	SetAmount(ctx context.Context, id uuid.UUID, amount string) error
	Confirm(ctx context.Context, id uuid.UUID, amountReceived string, at time.Time) error
	Fail(ctx context.Context, id uuid.UUID, reason string) error
	FailUnsent(ctx context.Context, id uuid.UUID, reason string) error
	FailExpected(ctx context.Context, id uuid.UUID, txHash, reason string) error
	ForOrder(ctx context.Context, orderID uuid.UUID) ([]models.RampLeg, error)
	NextAttempt(ctx context.Context, orderID uuid.UUID, kind string) (int, error)
}

type GormRampLegStore struct{ DB *gorm.DB }

// Reserve inserts the leg, or returns the row already holding
// (order_id, kind, attempt). The unique index settles concurrent ticks; a
// pre-check could not. Same shape as GormSponsorshipStore.Reserve.
//
// A refund leg has a SECOND arbiter it can conflict against:
// uq_ramp_legs_one_pending_refund (Postgres-only, added in
// config.AutoMigrate) rejects a second PENDING refund leg for the same
// order at ANY attempt number, not just this one — which is what actually
// closes the race NextAttempt alone cannot: two callers computing different,
// non-colliding attempt numbers for the same order's refund. When the
// conflict is against THAT index rather than (order_id, kind, attempt), the
// exact-match read-back below finds nothing (this row was never inserted),
// so a broader fallback looks up whichever row actually won instead of
// surfacing a bare "not found".
func (s *GormRampLegStore) Reserve(ctx context.Context, leg *models.RampLeg) (*models.RampLeg, error) {
	if leg.Status == "" {
		leg.Status = models.RampLegStatusPending
	}
	if leg.Attempt <= 0 {
		leg.Attempt = 1
	}
	res := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(leg)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 1 {
		return nil, nil
	}
	var existing models.RampLeg
	err := s.DB.WithContext(ctx).
		Where("order_id = ? AND kind = ? AND attempt = ?", leg.OrderID, leg.Kind, leg.Attempt).
		First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	// Fallback: the row that actually blocked this insert is not the one at
	// OUR attempt number. Return the pending or confirmed row for this
	// (order_id, kind) instead, so the caller still gets a real leg back.
	if err := s.DB.WithContext(ctx).
		Where("order_id = ? AND kind = ? AND status IN ?", leg.OrderID, leg.Kind,
			[]string{models.RampLegStatusPending, models.RampLegStatusConfirmed}).
		Order("attempt DESC").First(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// SetTxHash records a pending leg's transaction. A caller runs it as its
// key lane pre-broadcast hook (ctx from the lane: LaneDB is the lane's
// allocation transaction), so the leg and the signed call commit together; a
// leg no longer pending (a refund won it) is ErrLegNotPending and nothing is
// signed.
func (s *GormRampLegStore) SetTxHash(ctx context.Context, id uuid.UUID, txHash, tool string) error {
	res := LaneDB(ctx, s.DB).Model(&models.RampLeg{}).
		Where("id = ? AND status = ?", id, models.RampLegStatusPending).
		Updates(map[string]any{"tx_hash": txHash, "lifi_tool": tool})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLegNotPending
	}
	return nil
}

// SetApproveHash records a pending bridge leg's approve, in the key lane's
// allocation transaction (the approve's pre-broadcast hook): a leg no longer
// pending is ErrLegNotPending and the approve is not signed.
func (s *GormRampLegStore) SetApproveHash(ctx context.Context, id uuid.UUID, txHash string) error {
	res := LaneDB(ctx, s.DB).Model(&models.RampLeg{}).
		Where("id = ? AND status = ?", id, models.RampLegStatusPending).
		Update("approve_tx_hash", txHash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLegNotPending
	}
	return nil
}

// SetAmount rewrites a reserved leg's amount before it is broadcast (the
// bridge amount is only known once the route is priced, and the row is
// reserved before the quote so a second tick cannot join in). A leg with a
// hash is history and never changes.
func (s *GormRampLegStore) SetAmount(ctx context.Context, id uuid.UUID, amount string) error {
	return s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ? AND status = ? AND (tx_hash = '' OR tx_hash IS NULL)", id, models.RampLegStatusPending).
		Update("amount", amount).Error
}

// Confirm records what landed, but only on a still-pending leg: scoping the
// mutation to status=pending (mirroring GormSponsorshipStore.Release's guard
// on an empty tx_hash) stops a late or duplicate call from overwriting a row
// that already reached a terminal status.
func (s *GormRampLegStore) Confirm(ctx context.Context, id uuid.UUID, amountReceived string, at time.Time) error {
	res := s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ? AND status = ?", id, models.RampLegStatusPending).
		Updates(map[string]any{
			"status":          models.RampLegStatusConfirmed,
			"amount_received": amountReceived,
			"confirmed_at":    at.UTC(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLegNotPending
	}
	return nil
}

// Fail records why a leg didn't land, but only on a still-pending leg — see
// Confirm's comment.
func (s *GormRampLegStore) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	reason = truncateRunes(reason, 512)
	res := s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ? AND status = ?", id, models.RampLegStatusPending).
		Updates(map[string]any{"status": models.RampLegStatusFailed, "error": reason})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLegNotPending
	}
	return nil
}

// FailExpected fails a pending leg only while it still records txHash (""
// for none): a leg whose recorded transaction moved on (a speed-up hash
// written, a send committed) is never failed by a caller that read it stale.
func (s *GormRampLegStore) FailExpected(ctx context.Context, id uuid.UUID, txHash, reason string) error {
	q := s.DB.WithContext(ctx).Model(&models.RampLeg{}).Where("id = ? AND status = ?", id, models.RampLegStatusPending)
	if txHash == "" {
		q = q.Where("(tx_hash = '' OR tx_hash IS NULL)")
	} else {
		q = q.Where("tx_hash = ?", txHash)
	}
	res := q.Updates(map[string]any{"status": models.RampLegStatusFailed, "error": truncateRunes(reason, 512)})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLegNotPending
	}
	return nil
}

// FailUnsent is Fail for a leg that recorded no transaction hash: a leg
// whose send committed (its hook wrote the hash) is never freed by it
// (ErrLegNotPending).
func (s *GormRampLegStore) FailUnsent(ctx context.Context, id uuid.UUID, reason string) error {
	res := s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("id = ? AND status = ? AND (tx_hash = '' OR tx_hash IS NULL)", id, models.RampLegStatusPending).
		Updates(map[string]any{"status": models.RampLegStatusFailed, "error": truncateRunes(reason, 512)})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrLegNotPending
	}
	return nil
}

// truncateRunes trims s to at most n runes, so a cut never splits a multi-byte
// UTF-8 rune (Postgres sizes varchar in characters and rejects invalid UTF-8).
// The one truncation every stored reason, note and payload goes through.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}

func (s *GormRampLegStore) ForOrder(ctx context.Context, orderID uuid.UUID) ([]models.RampLeg, error) {
	var legs []models.RampLeg
	err := s.DB.WithContext(ctx).Where("order_id = ?", orderID).
		Order("created_at ASC").Find(&legs).Error
	return legs, err
}

// NextAttempt is the attempt number a retry should claim: one past the
// highest attempt this order has for that kind, or 1 when there is none.
func (s *GormRampLegStore) NextAttempt(ctx context.Context, orderID uuid.UUID, kind string) (int, error) {
	var max *int
	err := s.DB.WithContext(ctx).Model(&models.RampLeg{}).
		Where("order_id = ? AND kind = ?", orderID, kind).
		Select("MAX(attempt)").Scan(&max).Error
	if err != nil {
		return 0, err
	}
	if max == nil {
		return 1, nil
	}
	return *max + 1, nil
}
