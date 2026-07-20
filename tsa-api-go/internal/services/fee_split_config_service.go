package services

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// fee_split_config_service.go — DB-backed overrides for the per-feature fee
// split, cached in memory because splitFor() runs on every settled fee.
//
// Fail-safe by construction: a missing row, an inactive row, a row that doesn't
// sum to 1.0, or a DB error all fall back to the compiled-in featureSplits
// default. A misconfigured override can therefore never drop or duplicate value
// — the worst case is that an admin's edit doesn't take effect and is logged.

const feeSplitCacheTTL = 60 * time.Second

type FeeSplitConfigService struct {
	db *gorm.DB

	mu       sync.RWMutex
	cache    map[string]FeeShares
	cachedAt time.Time
}

func NewFeeSplitConfigService(db *gorm.DB) *FeeSplitConfigService {
	return &FeeSplitConfigService{db: db, cache: map[string]FeeShares{}}
}

// DefaultFeeSplits exposes the compiled-in table so admin reads can show what a
// feature falls back to, and so the seeder can populate the DB from it.
func DefaultFeeSplits() map[string]FeeShares {
	out := make(map[string]FeeShares, len(featureSplits))
	for k, v := range featureSplits {
		out[k] = v
	}
	return out
}

// Overrides returns the cached active overrides, refreshing past the TTL. On a
// DB error it serves the previous cache (or empty), so a database blip degrades
// to compiled-in defaults rather than failing the fee settlement.
func (s *FeeSplitConfigService) Overrides() map[string]FeeShares {
	s.mu.RLock()
	fresh := time.Since(s.cachedAt) < feeSplitCacheTTL
	cached := s.cache
	s.mu.RUnlock()
	if fresh {
		return cached
	}

	var rows []models.FeeSplitConfig
	if err := s.db.Where("active = ?", true).Find(&rows).Error; err != nil {
		log.Printf("[fee-split] load overrides failed, using defaults: %v", err)
		return cached
	}

	next := make(map[string]FeeShares, len(rows))
	for _, r := range rows {
		// Re-validate on apply: a row hand-edited in the DB bypasses the write
		// path's check, and a split that doesn't sum to 1.0 would leak value.
		if !models.ValidFeeSplit(r) {
			log.Printf("[fee-split] ignoring invalid override for %q (sum %.6f)", r.Feature, r.Sum())
			continue
		}
		next[r.Feature] = FeeShares{
			System:   r.SystemShare,
			Buyer:    r.BuyerShare,
			Upline:   r.UplineShare,
			Provider: r.ProviderShare,
			TP:       r.TPShare,
		}
	}

	s.mu.Lock()
	s.cache = next
	s.cachedAt = time.Now()
	s.mu.Unlock()
	return next
}

// Invalidate drops the cache so the next read reflects a just-saved edit.
func (s *FeeSplitConfigService) Invalidate() {
	s.mu.Lock()
	s.cachedAt = time.Time{}
	s.mu.Unlock()
}

// Effective merges overrides over the compiled-in defaults — what the engine
// actually applies, and what the admin UI should display.
func (s *FeeSplitConfigService) Effective() map[string]FeeShares {
	out := DefaultFeeSplits()
	for feature, shares := range s.Overrides() {
		out[feature] = shares
	}
	return out
}

// Upsert validates and saves one feature's split, then invalidates the cache.
func (s *FeeSplitConfigService) Upsert(cfg models.FeeSplitConfig) (models.FeeSplitConfig, error) {
	if !models.ValidFeeSplit(cfg) {
		return models.FeeSplitConfig{}, fmt.Errorf(
			"shares must each be within 0..1 and sum to 1.0 (got %.6f)", cfg.Sum())
	}
	var existing models.FeeSplitConfig
	err := s.db.Where("feature = ?", cfg.Feature).First(&existing).Error
	switch {
	case err == nil:
		updates := map[string]interface{}{
			"system_share":   cfg.SystemShare,
			"buyer_share":    cfg.BuyerShare,
			"upline_share":   cfg.UplineShare,
			"provider_share": cfg.ProviderShare,
			"tp_share":       cfg.TPShare,
			"active":         cfg.Active,
			"updated_by":     cfg.UpdatedBy,
		}
		if err := s.db.Model(&existing).Updates(updates).Error; err != nil {
			return models.FeeSplitConfig{}, err
		}
	case err == gorm.ErrRecordNotFound:
		// Select("*"): the model's `default:true` tag makes GORM omit a false
		// Active on insert and fall back to the DB default (true), so creating an
		// inactive override would silently persist as active. Force every column.
		if err := s.db.Select("*").Create(&cfg).Error; err != nil {
			return models.FeeSplitConfig{}, err
		}
		existing = cfg
	default:
		return models.FeeSplitConfig{}, err
	}

	s.Invalidate()
	var saved models.FeeSplitConfig
	s.db.Where("feature = ?", cfg.Feature).First(&saved)
	return saved, nil
}
