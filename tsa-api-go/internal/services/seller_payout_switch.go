package services

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// payoutSwitch is the operator's on/off for seller payouts.
type payoutSwitch interface {
	Enabled(ctx context.Context) (bool, error)
}

// payoutSwitchTTL bounds how stale a read may be: a flip reaches every process
// within it.
const payoutSwitchTTL = 3 * time.Second

// SellerPayoutSwitch reads and sets the persisted switch. It is on until an
// operator turns it off, and an unreadable switch reads as an error, never on.
type SellerPayoutSwitch struct {
	db  *gorm.DB
	now func() time.Time

	mu      sync.Mutex
	at      time.Time
	enabled bool
}

func NewSellerPayoutSwitch(db *gorm.DB) *SellerPayoutSwitch {
	return &SellerPayoutSwitch{db: db, now: time.Now}
}

func (s *SellerPayoutSwitch) Enabled(ctx context.Context) (bool, error) {
	if s == nil {
		return true, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.at.IsZero() && s.now().Sub(s.at) < payoutSwitchTTL {
		return s.enabled, nil
	}
	var row models.SellerPayoutSetting
	err := s.db.WithContext(ctx).First(&row, "key = ?", models.SellerPayoutSettingKey).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		s.enabled = true
	case err != nil:
		return false, err
	default:
		s.enabled = row.Enabled
	}
	s.at = s.now()
	return s.enabled, nil
}

// Set turns the payouts on or off, recording who and why.
func (s *SellerPayoutSwitch) Set(ctx context.Context, enabled bool, by uuid.UUID, note string) error {
	row := models.SellerPayoutSetting{Key: models.SellerPayoutSettingKey, Enabled: enabled, UpdatedBy: &by, Note: truncateRunes(note, 255)}
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"enabled", "updated_by", "note", "updated_at"}),
	}).Create(&row).Error
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.at = time.Time{} // this process reads the new value at once
	s.mu.Unlock()
	return nil
}
