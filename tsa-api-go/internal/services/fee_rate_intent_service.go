package services

import (
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// FeeRateIntentService is the persistence layer for DESIRED-state fee rate
// intents (configurable-FeeManager design §4/§5).
//
// TODO(#43): this is a minimal store. The admin set-fee flow (POST
// /api/admin/fees/intent | /plan | /confirm) — resolution of per-chain
// effective rates, diffing against on-chain rateFor, building the unsigned
// FeeManager.setRate tx(s), and the confirm-driven RefreshFromConfirmedRate
// call — is built in task #43 on top of these primitives. Do NOT add the
// endpoints here. An intent NEVER charges; it is desired-state only.
type FeeRateIntentService struct {
	db *gorm.DB
}

// NewFeeRateIntentService builds a FeeRateIntentService.
func NewFeeRateIntentService(db *gorm.DB) *FeeRateIntentService {
	return &FeeRateIntentService{db: db}
}

// Create persists a new intent in the draft state and returns it.
func (s *FeeRateIntentService) Create(feature, token string, chainID *int64, bps uint16, flatUSD float64, mcgpExempt, active bool) (*models.FeeRateIntent, error) {
	intent := &models.FeeRateIntent{
		ID:         uuid.New(),
		Feature:    feature,
		Token:      token,
		ChainID:    chainID,
		BPS:        bps,
		FlatUSD:    flatUSD,
		MCGPExempt: mcgpExempt,
		Active:     active,
		State:      models.FeeRateIntentDraft,
	}
	if err := s.db.Create(intent).Error; err != nil {
		return nil, err
	}
	return intent, nil
}

// Get loads an intent by ID.
func (s *FeeRateIntentService) Get(id uuid.UUID) (*models.FeeRateIntent, error) {
	var intent models.FeeRateIntent
	if err := s.db.First(&intent, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &intent, nil
}

// List returns all intents ordered by most-recent-first.
func (s *FeeRateIntentService) List() ([]models.FeeRateIntent, error) {
	var intents []models.FeeRateIntent
	if err := s.db.Order("created_at DESC").Find(&intents).Error; err != nil {
		return nil, err
	}
	return intents, nil
}

// UpdateState advances an intent's lifecycle state (design §5 state machine).
// #43 drives the legal transitions; this primitive just persists the value.
func (s *FeeRateIntentService) UpdateState(id uuid.UUID, state models.FeeRateIntentState) error {
	return s.db.Model(&models.FeeRateIntent{}).
		Where("id = ?", id).
		Update("state", state).Error
}
