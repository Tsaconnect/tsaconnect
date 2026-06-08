package models

import (
	"time"

	"github.com/google/uuid"
)

// Fee feature keys. These name the per-feature rows in the fee_config table.
// They are the canonical identifiers the fee_config service looks up via
// RateFor(feature, tokenSymbol).
const (
	FeatureProduct         = "product"          // buyer-side product fee (currently 0%)
	FeatureProductMerchant = "product_merchant" // merchant/platform fee baked into listing price (2%)
	FeatureServiceContact  = "service_contact"  // contact-reveal flat fee ($0.10)
	FeatureServiceOther    = "service_other"    // other fixed service fee (0.5%)
	FeatureOTC             = "otc"              // OTC fee (0.5%)
	FeatureSwap            = "swap"             // internal asset swap fee (0.1% today)
	FeatureWithdrawal      = "withdrawal"       // on-chain withdrawal platform fee (0.5%)
	FeatureP2P             = "p2p"              // P2P/PM buyer fee (0.5%, on-chain) — this IS the buyer gas (100% system, no TP)
	FeatureP2PPlatform     = "p2p_platform"     // P2P/PM platform/merchant fee (1%, on-chain) — carries the carved split/TP

	// Fee features carved out by the v2 fee/TP split model (design §2). Some of
	// these name product-side features that don't have their own Go-side rate
	// math yet (spot/instant_pay/instant_transfer are forward-looking SP1 rows);
	// they exist so the per-feature split table (services.splitFor) can key on
	// them and the engine can route TP/cashback correctly per feature.
	FeatureSpot            = "spot"             // internal asset-to-asset swap on platform books (3% at cutover)
	FeatureAllswap         = "allswap"          // LiFi cross-chain bridge/swap (3% at cutover)
	FeatureInstantPay      = "instant_pay"      // instant-pay crypto (0.5%) — forward-looking
	FeatureInstantTransfer = "instant_transfer" // instant fiat transfer (2%) — forward-looking
	// FeatureGas is the buyer's stablecoin gas component on product/p2p/service
	// flows (0.5%). It is 100% system with NO TP — it must never generate cashback
	// or TP, so it carries its own split row.
	FeatureGas = "gas"
)

// FeeTokenAny is the wildcard token symbol meaning "applies to any token"
// unless a more specific (feature, token) row exists.
const FeeTokenAny = "*"

// FeeConfig is an admin-tunable per-(feature, token) fee rate. It replaces the
// scattered hardcoded fee constants with a single source of truth that can be
// edited at runtime via the admin API without a redeploy.
//
// A rate is expressed as a basis-point component (BPS, 1/10000) and/or a flat
// USD component (FlatAmountUSD). Most features use exactly one of the two
// (e.g. product_merchant is BPS-only; service_contact is flat-only), but the
// schema allows both so a feature could carry "X bps + $Y" if ever needed.
//
// MCGPExempt mirrors today's behavior where MCGP-denominated transactions are
// fee-exempt. The service applies this by returning exempt=true (and zero
// rates) whenever the looked-up token is MCGP and the matching row is flagged.
type FeeConfig struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Feature       string    `gorm:"size:32;not null;uniqueIndex:idx_fee_config_feature_token" json:"feature"`
	TokenSymbol   string    `gorm:"size:16;not null;uniqueIndex:idx_fee_config_feature_token" json:"tokenSymbol"`
	BPS           int       `gorm:"not null;default:0" json:"bps"`
	FlatAmountUSD float64   `gorm:"not null;default:0" json:"flatAmountUsd"`
	MCGPExempt    bool      `gorm:"not null;default:false" json:"mcgpExempt"`
	Active        bool      `gorm:"not null;default:true" json:"active"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// TableName overrides the default table name.
func (FeeConfig) TableName() string {
	return "fee_configs"
}
