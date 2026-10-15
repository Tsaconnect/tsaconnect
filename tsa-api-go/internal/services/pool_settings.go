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
	poolUnitUSD poolSettingUnit = "usd" // flat_amount_usd
	poolUnitBps poolSettingUnit = "bps" // bps
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

// poolSettingDefaults are the rows ensured at boot. The LP deposit limits are
// the vault's own (VaultLimits), never a row here.
var poolSettingDefaults = []poolSettingDefault{
	{feature: models.FeaturePoolSpread, unit: poolUnitBps, bps: 50, minBps: 0, maxBps: 300},
	{feature: models.FeaturePoolTake, unit: poolUnitBps, bps: 50, minBps: 0, maxBps: 300},
	{feature: models.FeaturePoolBand, unit: poolUnitBps, bps: 50, minBps: 0, maxBps: 500},
	{feature: models.FeaturePoolMinSlice, unit: poolUnitUSD, flatUSD: 2, minFlat: poolMinSliceFloorUSD, maxFlat: 100},
	{feature: models.FeaturePoolOrderMax, unit: poolUnitUSD, flatUSD: 2000, minFlat: 0, maxFlat: 2000},
	{feature: models.FeaturePoolUserDailyMax, unit: poolUnitUSD, flatUSD: 2000, minFlat: 1, maxFlat: 100000},
	{feature: models.FeaturePoolNetworkFee, unit: poolUnitUSD, flatUSD: poolNetworkFeeDefaultUSD, minFlat: 0, maxFlat: 5},
}

// poolNetworkFeeDefaultUSD is pool_network_fee's default: the two gas rows it
// replaced (pool_gas_per_slice 0.03 + pool_gas_delivery 0.01).
const poolNetworkFeeDefaultUSD = 0.04

// retiredPoolSettings are the rows the 2026-10-15 cleanup removed, deleted
// at boot once pool_network_fee holds the two gas rows' sum.
var retiredPoolSettings = []string{models.RetiredPoolLPMin, models.RetiredPoolLPMax, models.RetiredPoolSliceCap,
	models.RetiredPoolGasPerSlice, models.RetiredPoolGasDelivery}

// retiredGasDefaultsUSD are what the gas rows priced at when missing or
// inactive (the old poolSettingFlat fallback), so the fold keeps the price.
var retiredGasDefaultsUSD = map[string]float64{models.RetiredPoolGasPerSlice: 0.03, models.RetiredPoolGasDelivery: 0.01}

// atMostSixDecimals: v × 10^6 is a whole number of canonical units.
func atMostSixDecimals(v float64) bool {
	scaled := v * 1e6
	return math.Abs(scaled-math.Round(scaled)) < 1e-6
}

// poolMinSliceFloorUSD is the lowest minimum order (pool_min_slice) an admin may set (owner,
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

// poolSettingRow scopes a query to one pool row. Matched on chain and
// direction IS NULL: Postgres treats NULLs as distinct, so the unique index
// cannot be relied on here.
func poolSettingRow(db *gorm.DB, feature string) *gorm.DB {
	return db.Where("feature = ? AND token_symbol = ? AND chain_id IS NULL AND direction IS NULL", feature, models.FeeTokenAny)
}

// ensurePoolSettingRows retires the removed rows, then inserts each missing
// pool row (seedIfEmpty seeds only an empty table). A row an admin has edited
// is never touched.
func (s *FeeConfigService) ensurePoolSettingRows() error {
	if s.db == nil {
		return nil
	}
	if err := s.retirePoolSettingRows(); err != nil {
		return err
	}
	for _, d := range poolSettingDefaults {
		var n int64
		if err := poolSettingRow(s.db.Model(&models.FeeConfig{}), d.feature).Count(&n).Error; err != nil {
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

// retirePoolSettingRows (cleanup 2026-10-15, idempotent): seeds
// pool_network_fee from what the two gas rows price at today — their sum —
// when it does not exist yet, then deletes every retired row, in one
// transaction. Open orders are unaffected: an order's network fee is the fee
// line stored on its intent, never re-read from these rows.
func (s *FeeConfigService) retirePoolSettingRows() error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := poolSettingRow(tx.Model(&models.FeeConfig{}), models.FeaturePoolNetworkFee).Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			var gas []models.FeeConfig
			if err := tx.Where("feature IN ? AND token_symbol = ? AND chain_id IS NULL AND direction IS NULL",
				[]string{models.RetiredPoolGasPerSlice, models.RetiredPoolGasDelivery}, models.FeeTokenAny).Find(&gas).Error; err != nil {
				return err
			}
			if len(gas) > 0 {
				sum := retiredGasSumUSD(gas)
				row := models.FeeConfig{ID: uuid.New(), Feature: models.FeaturePoolNetworkFee, TokenSymbol: models.FeeTokenAny,
					FlatAmountUSD: sum, Active: true}
				if err := tx.Create(&row).Error; err != nil {
					return fmt.Errorf("seed %s: %w", models.FeaturePoolNetworkFee, err)
				}
				log.Printf("[FeeConfig] TSA Liquidity setting %s = %v usd (sum of %s + %s)", models.FeaturePoolNetworkFee, sum,
					models.RetiredPoolGasPerSlice, models.RetiredPoolGasDelivery)
			}
		}
		res := tx.Where("feature IN ?", retiredPoolSettings).Delete(&models.FeeConfig{})
		if res.Error != nil {
			return fmt.Errorf("retire pool settings: %w", res.Error)
		}
		if res.RowsAffected > 0 {
			log.Printf("[FeeConfig] retired %d TSA Liquidity setting rows (%v)", res.RowsAffected, retiredPoolSettings)
		}
		return nil
	})
}

// retiredGasSumUSD is what the gas rows price an order at: an active row's
// amount, else (inactive or absent) the old default — then rounded to 6 dp
// (float sums) and held to pool_network_fee's range.
func retiredGasSumUSD(rows []models.FeeConfig) float64 {
	got := map[string]float64{}
	for _, r := range rows {
		if r.Active {
			got[r.Feature] = r.FlatAmountUSD
		}
	}
	sum := 0.0
	for feature, def := range retiredGasDefaultsUSD {
		if v, ok := got[feature]; ok {
			sum += v
		} else {
			sum += def
		}
	}
	sum = math.Round(sum*1e6) / 1e6
	d, _ := poolSettingDefaultFor(models.FeaturePoolNetworkFee)
	if sum > d.maxFlat {
		log.Printf("[FeeConfig] ALERT liquidity_pool_network_fee_clamped sum=%v — stored %v", sum, d.maxFlat)
		sum = d.maxFlat
	}
	return sum
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

// poolSettingBps reads a bps row from the replica; a missing row reads
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
	// MinSliceUnits is the minimum order (pool_min_slice).
	MinSliceUnits     *big.Int
	OrderMaxUnits     *big.Int
	UserDailyMaxUnits *big.Int
	// OrdersStopped: pool_order_max = 0, the incident stop (S46). Quotes and
	// opens close; claims, expiry and (C2) settlement keep running.
	OrdersStopped bool
	// NetworkFeeUnits is the per-order gas the buy price passes on as its
	// "Network fee" line (pool_network_fee). nil reads as zero.
	NetworkFeeUnits *big.Int
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
		MinSliceUnits:     usdToCanonical(poolSettingFlat(models.FeaturePoolMinSlice)),
		OrderMaxUnits:     usdToCanonical(orderMax),
		UserDailyMaxUnits: usdToCanonical(poolSettingFlat(models.FeaturePoolUserDailyMax)),
		OrdersStopped:     orderMax <= 0,
		NetworkFeeUnits:   usdToCanonical(poolSettingFlat(models.FeaturePoolNetworkFee)),
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

// ValidatePoolSetting is the admin edit's range check (spec §16.1). Rows that
// bound each other (minimum order, order max, daily max) are checked against
// the other row's CURRENT value.
func ValidatePoolSetting(db *gorm.DB, feature string, bps int, flatUSD float64) error {
	d, ok := poolSettingDefaultFor(feature)
	if !ok {
		return fmt.Errorf("unknown TSA Liquidity setting %q", feature)
	}
	current := func(f string) float64 {
		var row models.FeeConfig
		if err := poolSettingRow(db, f).First(&row).Error; err != nil {
			dd, _ := poolSettingDefaultFor(f)
			return dd.flatUSD
		}
		return row.FlatAmountUSD
	}
	switch d.unit {
	case poolUnitBps:
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
	case models.FeaturePoolMinSlice:
		if flatUSD < d.minFlat || flatUSD > d.maxFlat {
			return fmt.Errorf("the minimum order must be between %v and %v USD", d.minFlat, d.maxFlat)
		}
		if max := current(models.FeaturePoolOrderMax); max > 0 && flatUSD > max {
			return fmt.Errorf("the minimum order cannot exceed the order maximum (%v USD)", max)
		}
	case models.FeaturePoolOrderMax:
		if flatUSD == 0 {
			return nil // the incident stop (S46)
		}
		if flatUSD < 0 || flatUSD > poolOrderMaxCeilingUSD {
			return fmt.Errorf("pool_order_max must be 0 (stop) or at most %v USD", poolOrderMaxCeilingUSD)
		}
		if min := current(models.FeaturePoolMinSlice); flatUSD < min {
			return fmt.Errorf("pool_order_max must be at least the minimum order (%v USD) or 0", min)
		}
		if daily := current(models.FeaturePoolUserDailyMax); flatUSD > daily {
			return fmt.Errorf("pool_order_max cannot exceed the daily maximum (%v USD)", daily)
		}
	case models.FeaturePoolNetworkFee:
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
