package services

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// KeyLaneStore is where a KeyLane writes every transaction it signs before it
// is broadcast, and reads them back after a restart (key_lane_txs).
type KeyLaneStore interface {
	Record(ctx context.Context, row *models.KeyLaneTx) error
	Delete(ctx context.Context, id uuid.UUID) error
	SetStatus(ctx context.Context, id uuid.UUID, status string) error
	// Pending is the lane's rows still pending, by nonce then age.
	Pending(ctx context.Context, chainID int64, address string) ([]models.KeyLaneTx, error)
	// MarkMined marks every pending or replaced row below minedNonce mined
	// and drops its raw bytes.
	MarkMined(ctx context.Context, chainID int64, address string, minedNonce uint64) error
}

// GormKeyLaneStore is the KeyLaneStore on the API database.
type GormKeyLaneStore struct{ DB *gorm.DB }

func (s *GormKeyLaneStore) Record(ctx context.Context, row *models.KeyLaneTx) error {
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	row.Address = strings.ToLower(row.Address)
	return s.DB.WithContext(ctx).Create(row).Error
}

func (s *GormKeyLaneStore) Delete(ctx context.Context, id uuid.UUID) error {
	return s.DB.WithContext(ctx).Delete(&models.KeyLaneTx{}, "id = ?", id).Error
}

func (s *GormKeyLaneStore) SetStatus(ctx context.Context, id uuid.UUID, status string) error {
	return s.DB.WithContext(ctx).Model(&models.KeyLaneTx{}).Where("id = ?", id).Update("status", status).Error
}

func (s *GormKeyLaneStore) Pending(ctx context.Context, chainID int64, address string) ([]models.KeyLaneTx, error) {
	var rows []models.KeyLaneTx
	err := s.DB.WithContext(ctx).
		Where("chain_id = ? AND address = ? AND status = ?", chainID, strings.ToLower(address), models.KeyLaneTxPending).
		Order("nonce, created_at").Find(&rows).Error
	return rows, err
}

func (s *GormKeyLaneStore) MarkMined(ctx context.Context, chainID int64, address string, minedNonce uint64) error {
	return s.DB.WithContext(ctx).Model(&models.KeyLaneTx{}).
		Where("chain_id = ? AND address = ? AND nonce < ? AND status IN ?", chainID, strings.ToLower(address), int64(minedNonce),
			[]string{models.KeyLaneTxPending, models.KeyLaneTxReplaced, models.KeyLaneTxFilled}).
		Updates(map[string]any{"status": models.KeyLaneTxMined, "raw_tx": nil}).Error
}
