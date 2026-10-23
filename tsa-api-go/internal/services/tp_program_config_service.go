package services

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const tpProgramCacheTTL = 60 * time.Second

// TPProgramConfigService serves the admin-edited TP program with a short cache.
// A missing row, an invalid row or a DB error all fall back to the compiled-in
// defaults, so a bad edit can never stop fee settlement.
type TPProgramConfigService struct {
	db *gorm.DB

	mu       sync.RWMutex
	cache    models.TPProgramConfig
	cachedAt time.Time
}

func NewTPProgramConfigService(db *gorm.DB) *TPProgramConfigService {
	return &TPProgramConfigService{db: db, cache: models.DefaultTPProgramConfig()}
}

// Current returns the program in effect. Nil-safe: a nil service yields defaults.
func (s *TPProgramConfigService) Current() models.TPProgramConfig {
	if s == nil {
		return models.DefaultTPProgramConfig()
	}
	s.mu.RLock()
	fresh := time.Since(s.cachedAt) < tpProgramCacheTTL
	cached := s.cache
	s.mu.RUnlock()
	if fresh {
		return cached
	}

	next := models.DefaultTPProgramConfig()
	var row models.TPProgramConfig
	switch err := s.db.Order("created_at ASC").First(&row).Error; {
	case err == nil:
		if models.ValidTPProgramConfig(row) {
			next = row
		} else {
			log.Printf("[tp-program] ignoring invalid config row, using defaults")
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		log.Printf("[tp-program] load failed, serving previous config: %v", err)
		return cached
	}

	s.mu.Lock()
	s.cache = next
	s.cachedAt = time.Now()
	s.mu.Unlock()
	return next
}

// Save validates and stores the single program row, then drops the cache.
func (s *TPProgramConfigService) Save(cfg models.TPProgramConfig) (models.TPProgramConfig, error) {
	if !models.ValidTPProgramConfig(cfg) {
		return models.TPProgramConfig{}, fmt.Errorf(
			"percentages must be within 0..1 and total at most 1, zero-fee TP and the welcome bonuses must be >= 0, payout day must be 1..28")
	}
	var existing models.TPProgramConfig
	err := s.db.Order("created_at ASC").First(&existing).Error
	switch {
	case err == nil:
		if err := s.db.Model(&existing).Updates(map[string]interface{}{
			"user_pct":    cfg.UserPct,
			"upline_pct":  cfg.UplinePct,
			"pool_pct":    cfg.PoolPct,
			"zero_fee_tp": cfg.ZeroFeeTP,
			"payout_day":  cfg.PayoutDay,
			"updated_by":  cfg.UpdatedBy,

			"signup_bonus_tp":     cfg.SignupBonusTP,
			"onboarding_bonus_tp": cfg.OnboardingBonusTP,
		}).Error; err != nil {
			return models.TPProgramConfig{}, err
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		if err := s.db.Create(&cfg).Error; err != nil {
			return models.TPProgramConfig{}, err
		}
		// Create replaces a zero with the column default, which would turn an
		// admin's "no bonus" into 1 or 10: write the chosen values back exactly.
		if err := s.db.Model(&cfg).Updates(map[string]interface{}{
			"signup_bonus_tp": cfg.SignupBonusTP, "onboarding_bonus_tp": cfg.OnboardingBonusTP,
		}).Error; err != nil {
			return models.TPProgramConfig{}, err
		}
	default:
		return models.TPProgramConfig{}, err
	}

	s.mu.Lock()
	s.cachedAt = time.Time{}
	s.mu.Unlock()
	return s.Current(), nil
}
