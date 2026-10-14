package services

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sponsorshipStore interface {
	Reserve(ctx context.Context, row *models.GasSponsorship) (*models.GasSponsorship, error)
	SetTxHash(ctx context.Context, id uuid.UUID, txHash, amountWei string) error
	Release(ctx context.Context, id uuid.UUID) error
	// ForgetCancelled drops a row whose sent transaction the key lane proved
	// cancelled (it never executed), only while it still records that hash.
	ForgetCancelled(ctx context.Context, id uuid.UUID, txHash string) (bool, error)
	CountUnusedTopUpsSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error)
}

type GormSponsorshipStore struct{ DB *gorm.DB }

// Reserve inserts the row, or returns the row already holding (order, kind).
// The unique index settles concurrent requests; a pre-check could not.
func (s *GormSponsorshipStore) Reserve(ctx context.Context, row *models.GasSponsorship) (*models.GasSponsorship, error) {
	res := s.DB.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 1 {
		return nil, nil
	}
	var existing models.GasSponsorship
	if err := s.DB.WithContext(ctx).
		Where("order_id = ? AND kind = ?", row.OrderID, row.Kind).
		First(&existing).Error; err != nil {
		return nil, err
	}
	return &existing, nil
}

// SetTxHash writes the sent tx on the row; inside a key lane hook (ctx from
// the lane) it is part of the lane's allocation transaction.
func (s *GormSponsorshipStore) SetTxHash(ctx context.Context, id uuid.UUID, txHash, amountWei string) error {
	// Exactly one row, still unsent: the hook's guard (else nothing signs).
	res := LaneDB(ctx, s.DB).Model(&models.GasSponsorship{}).
		Where("id = ? AND (tx_hash = '' OR tx_hash IS NULL)", id).
		Updates(map[string]any{"tx_hash": txHash, "amount_wei": amountWei})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return fmt.Errorf("sponsorship %s already records a transaction", id)
	}
	return nil
}

func (s *GormSponsorshipStore) ForgetCancelled(ctx context.Context, id uuid.UUID, txHash string) (bool, error) {
	res := s.DB.WithContext(ctx).Where("id = ? AND tx_hash = ? AND tx_hash <> ''", id, txHash).Delete(&models.GasSponsorship{})
	return res.RowsAffected == 1, res.Error
}

func (s *GormSponsorshipStore) Release(ctx context.Context, id uuid.UUID) error {
	return s.DB.WithContext(ctx).Where("id = ? AND tx_hash = ''", id).Delete(&models.GasSponsorship{}).Error
}

// CountUnusedTopUpsSince counts the user's top-ups created since `since`
// whose gas went unused: the sell order they were sent for ended expired,
// cancelled or failed with nothing paid (see topUpUnused). Top-ups on settled
// or still-open orders never count, so users who complete sales are never
// capped.
func (s *GormSponsorshipStore) CountUnusedTopUpsSince(ctx context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	var outcomes []struct {
		Status     string
		AmountPaid string
	}
	err := s.DB.WithContext(ctx).Table("gas_sponsorships").
		Select("paycrest_order_records.status, paycrest_order_records.amount_paid").
		Joins("JOIN paycrest_order_records ON paycrest_order_records.id = gas_sponsorships.order_id").
		Where("gas_sponsorships.user_id = ? AND gas_sponsorships.kind = ? AND gas_sponsorships.created_at >= ?",
			userID, models.GasSponsorshipKindTopUp, since).
		Scan(&outcomes).Error
	if err != nil {
		return 0, err
	}
	var n int64
	for _, o := range outcomes {
		if topUpUnused(o.Status, o.AmountPaid) {
			n++
		}
	}
	return n, nil
}
