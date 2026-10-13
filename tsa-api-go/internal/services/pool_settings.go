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

// poolSettingUnit is how a pool row is edited and read (spec §16.2).
type poolSettingUnit string

const (
	poolUnitUSD   poolSettingUnit = "usd"   // flat_amount_usd
	poolUnitBps   poolSettingUnit = "bps"   // bps
	poolUnitCount poolSettingUnit = "count" // a whole number in the bps column (S25)
)

// poolSettingDefault is one TSA Liquidity fee_configs row (spec §16.1).
type poolSettingDefault struct {
	feature          string
	unit             poolSettingUnit
	flatUSD          float64
	minFlat, maxFlat float64
	bps              int
	minBps, maxBps   int
}

// poolSettingDefaults are the rows ensured at boot (Plan B's two, C1's seven,
// then the two per-order gas costs the buy price passes on).
var poolSettingDefaults = []poolSettingDefault{
	{feature: models.FeaturePoolLPMin, unit: poolUnitUSD, flatUSD: 5, minFlat: 1, maxFlat: 1000},
	{feature: models.FeaturePoolLPMax, unit: poolUnitUSD, flatUSD: 5000, minFlat: 0, maxFlat: 100000},
	{feature: models.FeaturePoolSpread, unit: poolUnitBps, bps: 50, minBps: 0, maxBps: 300},
	{feature: models.FeaturePoolTake, unit: poolUnitBps, bps: 50, minBps: 0, maxBps: 300},
	{feature: models.FeaturePoolBand, unit: poolUnitBps, bps: 50, minBps: 0, maxBps: 500},
	{feature: models.FeaturePoolSliceCap, unit: poolUnitCount, bps: 3, minBps: 1, maxBps: 5},
	{feature: models.FeaturePoolMinSlice, unit: poolUnitUSD, flatUSD: 2, minFlat: poolMinSliceFloorUSD, maxFlat: 100},
	{feature: models.FeaturePoolOrderMax, unit: poolUnitUSD, flatUSD: 2000, minFlat: 0, maxFlat: 2000},
	{feature: models.FeaturePoolUserDailyMax, unit: poolUnitUSD, flatUSD: 2000, minFlat: 1, maxFlat: 100000},
	{feature: models.FeaturePoolGasPerSlice, unit: poolUnitUSD, flatUSD: 0.03, minFlat: 0, maxFlat: 5},
	{feature: models.FeaturePoolGasDelivery, unit: poolUnitUSD, flatUSD: 0.01, minFlat: 0, maxFlat: 5},
}

// atMostSixDecimals: v × 10^6 is a whole number of canonical units.
func atMostSixDecimals(v float64) bool {
	scaled := v * 1e6
	return math.Abs(scaled-math.Round(scaled)) < 1e-6
}

// poolMinSliceFloorUSD is the lowest pool_min_slice an admin may set (owner,
// 2026-10-13: orders down to about ₦500). Every cost is a fee line, so a small
// slice no longer loses money; an LP payout under the ₦100 transfer floor is
// refused at quote time (poolQuoteAt).
const poolMinSliceFloorUSD = 0.25

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
		row := models.FeeConfig{ID: uuid.New(), Feature: d.feature, TokenSymbol: models.FeeTokenAny,
			BPS: d.bps, FlatAmountUSD: d.flatUSD, Active: true}
		if err := s.db.Create(&row).Error; err != nil {
			return fmt.Errorf("ensure %s: %w", d.feature, err)
		}
		log.Printf("[FeeConfig] ensured TSA Liquidity setting %s = %v %s", d.feature, poolDefaultValue(d), d.unit)
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

func poolDefaultValue(d poolSettingDefault) float64 {
	if d.unit == poolUnitUSD {
		return d.flatUSD
	}
	return float64(d.bps)
}

// poolSettingBps reads a bps/count row from the replica; a missing row reads
// its default with an ALERT, never 0.
func poolSettingBps(feature string) int64 {
	if Fees != nil {
		if bps, _, _, found := Fees.RateFor(feature, models.FeeTokenAny, nil); found {
			return int64(bps)
		}
	}
	d, _ := poolSettingDefaultFor(feature)
	log.Printf("[liquidity] ALERT liquidity_pool_setting_missing feature=%s — using the default %d", feature, d.bps)
	return int64(d.bps)
}

// poolOrderMaxCeilingUSD bounds pool_order_max: the treasury's per-order
// in-flight cap (spec §16.1; RAMP_TREASURY_MAX_INFLIGHT_USD_PER_ORDER).
var poolOrderMaxCeilingUSD = 2000.0

// SetPoolOrderMaxCeilingUSD is called once at boot with the treasury's
// per-order cap. A non-positive value keeps the default.
func SetPoolOrderMaxCeilingUSD(usd float64) {
	if usd > 0 {
		poolOrderMaxCeilingUSD = usd
	}
}

// PoolSettings is what one quote or open prices at (spec §16.1). Read once per
// call and snapshotted into pool_orders at open. Units are canonical 6 dp.
type PoolSettings struct {
	SpreadBps, TakeBps, BandBps int64
	SliceCap                    int
	MinSliceUnits               *big.Int
	OrderMaxUnits               *big.Int
	UserDailyMaxUnits           *big.Int
	// OrdersStopped: pool_order_max = 0, the incident stop (S46). Quotes and
	// opens close; claims, expiry and (C2) settlement keep running.
	OrdersStopped bool
	// Per-order gas the buy price passes on as its "Network fee" line:
	// slices × GasPerSliceUnits + GasDeliveryUnits. nil reads as zero.
	GasPerSliceUnits, GasDeliveryUnits *big.Int
}

func usdToCanonical(usd float64) *big.Int {
	if usd <= 0 {
		return new(big.Int)
	}
	return new(big.Int).SetUint64(flatUSDToUnits(usd))
}

// CurrentPoolSettings reads the C1 rows from the replica. The order maximum is
// clamped to the treasury's per-order ceiling at read time (review M9): a row
// ensured at 2,000 must not outlive a lower env ceiling.
func CurrentPoolSettings() PoolSettings {
	orderMax := poolSettingFlat(models.FeaturePoolOrderMax)
	if orderMax > poolOrderMaxCeilingUSD {
		orderMax = poolOrderMaxCeilingUSD
	}
	return PoolSettings{
		SpreadBps:         poolSettingBps(models.FeaturePoolSpread),
		TakeBps:           poolSettingBps(models.FeaturePoolTake),
		BandBps:           poolSettingBps(models.FeaturePoolBand),
		SliceCap:          int(poolSettingBps(models.FeaturePoolSliceCap)),
		MinSliceUnits:     usdToCanonical(poolSettingFlat(models.FeaturePoolMinSlice)),
		OrderMaxUnits:     usdToCanonical(orderMax),
		UserDailyMaxUnits: usdToCanonical(poolSettingFlat(models.FeaturePoolUserDailyMax)),
		OrdersStopped:     orderMax <= 0,
		GasPerSliceUnits:  usdToCanonical(poolSettingFlat(models.FeaturePoolGasPerSlice)),
		GasDeliveryUnits:  usdToCanonical(poolSettingFlat(models.FeaturePoolGasDelivery)),
	}
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

// ValidatePoolSetting is the admin edit's range check (spec §16.1). Rows that
// bound each other (LP min/max; min slice, order max, daily max) are checked
// against the other row's CURRENT value.
func ValidatePoolSetting(db *gorm.DB, feature string, bps int, flatUSD float64) error {
	d, ok := poolSettingDefaultFor(feature)
	if !ok {
		return fmt.Errorf("unknown TSA Liquidity setting %q", feature)
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
	switch d.unit {
	case poolUnitBps, poolUnitCount:
		if flatUSD != 0 {
			return fmt.Errorf("%s is a rate; flatAmountUsd must be 0", feature)
		}
		if bps < d.minBps || bps > d.maxBps {
			return fmt.Errorf("%s must be between %d and %d", feature, d.minBps, d.maxBps)
		}
		return nil
	}
	if bps != 0 {
		return fmt.Errorf("%s is a flat USD amount; bps must be 0", feature)
	}
	if !atMostSixDecimals(flatUSD) {
		// Stored as a float but used in 6-dp units (flatUSDToUnits): a finer
		// value would silently round to another amount.
		return fmt.Errorf("%s takes at most 6 decimal places", feature)
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
	case models.FeaturePoolMinSlice:
		if flatUSD < d.minFlat || flatUSD > d.maxFlat {
			return fmt.Errorf("pool_min_slice must be between %v and %v USD", d.minFlat, d.maxFlat)
		}
		if max := current(models.FeaturePoolOrderMax); max > 0 && flatUSD > max {
			return fmt.Errorf("pool_min_slice cannot exceed the order maximum (%v USD)", max)
		}
	case models.FeaturePoolOrderMax:
		if flatUSD == 0 {
			return nil // the incident stop (S46)
		}
		if flatUSD < 0 || flatUSD > poolOrderMaxCeilingUSD {
			return fmt.Errorf("pool_order_max must be 0 (stop) or at most %v USD", poolOrderMaxCeilingUSD)
		}
		if min := current(models.FeaturePoolMinSlice); flatUSD < min {
			return fmt.Errorf("pool_order_max must be at least the minimum slice (%v USD) or 0", min)
		}
		if daily := current(models.FeaturePoolUserDailyMax); flatUSD > daily {
			return fmt.Errorf("pool_order_max cannot exceed the daily maximum (%v USD)", daily)
		}
	case models.FeaturePoolGasPerSlice, models.FeaturePoolGasDelivery:
		if flatUSD < d.minFlat || flatUSD > d.maxFlat {
			return fmt.Errorf("%s must be between %v and %v USD", feature, d.minFlat, d.maxFlat)
		}
	case models.FeaturePoolUserDailyMax:
		if flatUSD < d.minFlat || flatUSD > d.maxFlat {
			return fmt.Errorf("pool_user_daily_max must be between 1 and at most 100000 USD")
		}
		if max := current(models.FeaturePoolOrderMax); max > 0 && flatUSD < max {
			return fmt.Errorf("pool_user_daily_max must be at least the order maximum (%v USD)", max)
		}
	}
	return nil
}
