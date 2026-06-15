package services

import (
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RewardPayoutConfigService owns the (reward kind, payment token) → (payout
// token, chain) mapping. It seeds the launch defaults on first boot, caches the
// active rows in memory, and resolves the payout target per reward settlement.
// The payout token is admin-configurable (PM): the SP5 reward processor calls
// Resolve to decide which token each level/cashback reward is denominated for.
type RewardPayoutConfigService struct {
	db    *gorm.DB
	mu    sync.RWMutex
	cache map[string]models.RewardPayoutConfig // key: payoutKey(kind, token)
}

// RewardPayoutResolution is the resolved payout target.
type RewardPayoutResolution struct {
	PayoutToken string
	Chain       string
}

// DefaultRewardPayoutConfigs is the launch payout mapping (PM 2026-06-15):
//   - cashback paid with USDC → USDC; with USDT or MCGP → MCGP
//   - TP-level rewards → MCGP (temporary, until the USDC reserve is funded)
//
// All on Sonic for now; admin can repoint token/chain later. The PaymentTokenAny
// rows are the fallbacks (cashback paid with any other token, and TP level which
// has no single payment token).
func DefaultRewardPayoutConfigs() []models.RewardPayoutConfig {
	const sonic = "sonic"
	return []models.RewardPayoutConfig{
		{RewardKind: models.RewardKindCashback, PaymentToken: "USDC", PayoutToken: "USDC", Chain: sonic, Active: true},
		{RewardKind: models.RewardKindCashback, PaymentToken: "USDT", PayoutToken: "MCGP", Chain: sonic, Active: true},
		{RewardKind: models.RewardKindCashback, PaymentToken: "MCGP", PayoutToken: "MCGP", Chain: sonic, Active: true},
		{RewardKind: models.RewardKindCashback, PaymentToken: models.PaymentTokenAny, PayoutToken: "MCGP", Chain: sonic, Active: true},
		{RewardKind: models.RewardKindLevel, PaymentToken: models.PaymentTokenAny, PayoutToken: "MCGP", Chain: sonic, Active: true},
	}
}

func payoutKey(kind, token string) string {
	return strings.ToLower(strings.TrimSpace(kind)) + "|" + strings.ToUpper(strings.TrimSpace(token))
}

// NewRewardPayoutConfigService seeds the defaults if the table is empty, loads
// the cache, and returns the service.
func NewRewardPayoutConfigService(db *gorm.DB) (*RewardPayoutConfigService, error) {
	s := &RewardPayoutConfigService{db: db, cache: map[string]models.RewardPayoutConfig{}}
	if err := s.seedIfEmpty(); err != nil {
		return nil, err
	}
	if err := s.Load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *RewardPayoutConfigService) seedIfEmpty() error {
	var n int64
	if err := s.db.Model(&models.RewardPayoutConfig{}).Count(&n).Error; err != nil {
		return fmt.Errorf("count reward payout configs: %w", err)
	}
	if n > 0 {
		return nil
	}
	rows := DefaultRewardPayoutConfigs()
	for i := range rows {
		rows[i].ID = uuid.New() // explicit ID — DB-agnostic (no gen_random_uuid dependency)
	}
	if err := s.db.Create(&rows).Error; err != nil {
		return fmt.Errorf("seed reward payout configs: %w", err)
	}
	log.Printf("[RewardPayout] seeded %d default payout configs", len(rows))
	return nil
}

// Load refreshes the in-memory cache of active rows from the table.
func (s *RewardPayoutConfigService) Load() error {
	var rows []models.RewardPayoutConfig
	if err := s.db.Find(&rows).Error; err != nil {
		return fmt.Errorf("load reward payout configs: %w", err)
	}
	m := make(map[string]models.RewardPayoutConfig, len(rows))
	for _, r := range rows {
		if r.Active {
			m[payoutKey(r.RewardKind, r.PaymentToken)] = r
		}
	}
	s.mu.Lock()
	s.cache = m
	s.mu.Unlock()
	return nil
}

// Resolve returns the payout token + chain for (rewardKind, paymentToken). It
// tries the exact (kind, token) row, then the kind's PaymentTokenAny fallback,
// then a final hardcoded MCGP/sonic backstop so the engine always has a target.
func (s *RewardPayoutConfigService) Resolve(rewardKind, paymentToken string) RewardPayoutResolution {
	if s == nil {
		return RewardPayoutResolution{PayoutToken: "MCGP", Chain: "sonic"}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.cache[payoutKey(rewardKind, paymentToken)]; ok {
		return RewardPayoutResolution{PayoutToken: r.PayoutToken, Chain: r.Chain}
	}
	if r, ok := s.cache[payoutKey(rewardKind, models.PaymentTokenAny)]; ok {
		return RewardPayoutResolution{PayoutToken: r.PayoutToken, Chain: r.Chain}
	}
	return RewardPayoutResolution{PayoutToken: "MCGP", Chain: "sonic"}
}

// List returns every payout config row (admin read).
func (s *RewardPayoutConfigService) List() ([]models.RewardPayoutConfig, error) {
	var rows []models.RewardPayoutConfig
	if err := s.db.Order("reward_kind, payment_token").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list reward payout configs: %w", err)
	}
	return rows, nil
}

// Upsert creates or updates the (rewardKind, paymentToken) mapping and refreshes
// the cache. Inputs are normalized (kind lower, tokens upper). Returns the row.
func (s *RewardPayoutConfigService) Upsert(rewardKind, paymentToken, payoutToken, chain string, active bool) (*models.RewardPayoutConfig, error) {
	kind := strings.ToLower(strings.TrimSpace(rewardKind))
	pay := strings.ToUpper(strings.TrimSpace(paymentToken))
	out := strings.ToUpper(strings.TrimSpace(payoutToken))
	ch := strings.ToLower(strings.TrimSpace(chain))

	if kind != models.RewardKindLevel && kind != models.RewardKindCashback {
		return nil, fmt.Errorf("invalid reward kind %q (want %q or %q)", rewardKind, models.RewardKindLevel, models.RewardKindCashback)
	}
	if pay == "" || out == "" || ch == "" {
		return nil, fmt.Errorf("paymentToken, payoutToken and chain are required")
	}
	switch out {
	case "USDC", "USDT", "MCGP":
	default:
		return nil, fmt.Errorf("unsupported payout token %q (want USDC, USDT or MCGP)", payoutToken)
	}

	row := models.RewardPayoutConfig{
		ID:           uuid.New(),
		RewardKind:   kind,
		PaymentToken: pay,
		PayoutToken:  out,
		Chain:        ch,
		Active:       active,
	}
	if err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "reward_kind"}, {Name: "payment_token"}},
		DoUpdates: clause.AssignmentColumns([]string{"payout_token", "chain", "active", "updated_at"}),
	}).Create(&row).Error; err != nil {
		return nil, fmt.Errorf("upsert reward payout config: %w", err)
	}
	if err := s.Load(); err != nil {
		return nil, err
	}
	return &row, nil
}
