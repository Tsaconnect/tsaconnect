package services

import (
	"fmt"
	"log"
	"math"
	"math/big"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// poolSettingDefault is one TSA Liquidity fee_configs row (spec §16.1).
type poolSettingDefault struct {
	feature          string
	flatUSD          float64
	minFlat, maxFlat float64
}

// poolSettingDefaults are the rows ensured at boot. Plan C1 appends
// pool_spread, pool_take, pool_band, pool_slice_cap, pool_min_slice,
// pool_order_max and pool_user_daily_max.
var poolSettingDefaults = []poolSettingDefault{
	{feature: models.FeaturePoolLPMin, flatUSD: 5, minFlat: 1, maxFlat: 1000},
	{feature: models.FeaturePoolLPMax, flatUSD: 5000, minFlat: 0, maxFlat: 100000},
}

func poolSettingDefaultFor(feature string) (poolSettingDefault, bool) {
	for _, d := range poolSettingDefaults {
		if d.feature == feature {
			return d, true
		}
	}
	return poolSettingDefault{}, false
}

// ensurePoolSettingRows inserts each missing pool row (seedIfEmpty seeds only an
// empty table). A row an admin has edited is never touched. Matched on chain
// and direction IS NULL: Postgres treats NULLs as distinct, so the unique index
// cannot be relied on here.
func (s *FeeConfigService) ensurePoolSettingRows() error {
	if s.db == nil {
		return nil
	}
	for _, d := range poolSettingDefaults {
		var n int64
		if err := s.db.Model(&models.FeeConfig{}).
			Where("feature = ? AND token_symbol = ? AND chain_id IS NULL AND direction IS NULL", d.feature, models.FeeTokenAny).
			Count(&n).Error; err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		row := models.FeeConfig{ID: uuid.New(), Feature: d.feature, TokenSymbol: models.FeeTokenAny, FlatAmountUSD: d.flatUSD, Active: true}
		if err := s.db.Create(&row).Error; err != nil {
			return fmt.Errorf("ensure %s: %w", d.feature, err)
		}
		log.Printf("[FeeConfig] ensured TSA Liquidity setting %s = %v", d.feature, d.flatUSD)
	}
	return nil
}

// PoolLPLimitsUSD is (pool_lp_min, pool_lp_max) in USD from the replica. A
// missing row reads its default with an ALERT: never 0, never unbounded.
func PoolLPLimitsUSD() (minUSD, maxUSD float64) {
	return poolSettingFlat(models.FeaturePoolLPMin), poolSettingFlat(models.FeaturePoolLPMax)
}

func poolSettingFlat(feature string) float64 {
	if Fees != nil {
		if _, flat, _, found := Fees.RateFor(feature, models.FeeTokenAny, nil); found {
			return flat
		}
	}
	d, _ := poolSettingDefaultFor(feature)
	log.Printf("[liquidity] ALERT liquidity_pool_setting_missing feature=%s — using the default %v", feature, d.flatUSD)
	return d.flatUSD
}

// usdToChainUnits converts USD to token base units at the 1 USDT = $1 peg
// (spec §2): 6-dp canonical units through the existing flatUSDToUnits
// (fee_manager_chain.go:77, reused), then scaled up exactly (S7).
func usdToChainUnits(usd float64, decimals int) (*big.Int, error) {
	if usd < 0 || math.IsNaN(usd) || math.IsInf(usd, 0) {
		return nil, fmt.Errorf("liquidity: %v is not a USD amount", usd)
	}
	return CanonicalToChainUnits(new(big.Int).SetUint64(flatUSDToUnits(usd)), decimals)
}

// PoolLPLimitUnits is (pool_lp_min, pool_lp_max) in the pool token's own base
// units. A token below 6 decimals is refused (S7).
func PoolLPLimitUnits(decimals int) (min, max *big.Int, err error) {
	minUSD, maxUSD := PoolLPLimitsUSD()
	if min, err = usdToChainUnits(minUSD, decimals); err != nil {
		return nil, nil, err
	}
	if max, err = usdToChainUnits(maxUSD, decimals); err != nil {
		return nil, nil, err
	}
	return min, max, nil
}

// ValidatePoolSetting is the admin edit's range check (spec §16.1). The LP min
// and max are checked against each other's CURRENT row.
func ValidatePoolSetting(db *gorm.DB, feature string, bps int, flatUSD float64) error {
	d, ok := poolSettingDefaultFor(feature)
	if !ok {
		return fmt.Errorf("unknown TSA Liquidity setting %q", feature)
	}
	if bps != 0 {
		return fmt.Errorf("%s is a flat USD amount; bps must be 0", feature)
	}
	current := func(f string) float64 {
		var row models.FeeConfig
		if err := db.Where("feature = ? AND token_symbol = ? AND chain_id IS NULL AND direction IS NULL", f, models.FeeTokenAny).
			First(&row).Error; err != nil {
			dd, _ := poolSettingDefaultFor(f)
			return dd.flatUSD
		}
		return row.FlatAmountUSD
	}
	switch feature {
	case models.FeaturePoolLPMin:
		if flatUSD < d.minFlat || flatUSD > d.maxFlat {
			return fmt.Errorf("pool_lp_min must be between 1 and 1000 USD")
		}
		if max := current(models.FeaturePoolLPMax); flatUSD > max {
			return fmt.Errorf("pool_lp_min cannot exceed the maximum (%v USD)", max)
		}
	case models.FeaturePoolLPMax:
		if min := current(models.FeaturePoolLPMin); flatUSD < min {
			return fmt.Errorf("pool_lp_max must be at least the minimum (%v USD)", min)
		}
		if flatUSD > d.maxFlat {
			return fmt.Errorf("pool_lp_max must be at most 100000 USD")
		}
	}
	return nil
}
