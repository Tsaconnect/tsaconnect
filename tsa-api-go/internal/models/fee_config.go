package models

import (
	"strings"
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

// TSA Liquidity settings (spec §16.1, S25): off-chain fee_configs rows
// (token '*', chain and direction NULL), never authored on-chain and never in
// OnChainFeeFeatures. Plan B owns the LP limits; Plan C1 adds the rest.
const (
	FeaturePoolLPMin = "pool_lp_min" // flat USD: minimum first deposit (decision 10)
	FeaturePoolLPMax = "pool_lp_max" // flat USD: stake ceiling (decision 10)
	// Plan C1 (spec §16.1, S31–S33, decision 19).
	FeaturePoolSpread       = "pool_spread"         // bps: LP earning over mid
	FeaturePoolTake         = "pool_take"           // bps: company take
	FeaturePoolBand         = "pool_band"           // bps: IT preference band
	FeaturePoolSliceCap     = "pool_slice_cap"      // count in the bps column: max LPs per order
	FeaturePoolMinSlice     = "pool_min_slice"      // flat USD: minimum slice and minimum order
	FeaturePoolOrderMax     = "pool_order_max"      // flat USD: per order; 0 = the incident stop (S46)
	FeaturePoolUserDailyMax = "pool_user_daily_max" // flat USD: rolling 24h per user
)

// IsPoolSetting reports whether feature is a TSA Liquidity setting row.
func IsPoolSetting(feature string) bool { return strings.HasPrefix(feature, "pool_") }

// OnChainFeeFeatures are the features a deployed contract reads from
// FeeManager at transaction time (ProductEscrowV2: product_merchant, product;
// P2PEscrowV3: p2p_platform, p2p). Their fee_config rows are a replica of chain
// truth, written only by the confirmed on-chain flow — editing one directly
// makes the backend quote and approve one amount while the contract pulls
// another.
var OnChainFeeFeatures = map[string]bool{
	FeatureProductMerchant: true,
	FeatureProduct:         true,
	FeatureP2PPlatform:     true,
	FeatureP2P:             true,
}

// FeeTokenAny is the wildcard token symbol meaning "applies to any token"
// unless a more specific (feature, token) row exists.
const FeeTokenAny = "*"

// Fee directions. The gasless/transaction-tax schedule prices a flow by the
// direction money moves as well as by (feature, token, chain): an MCGP buy is
// untaxed where an MCGP sell is not, and OTC/spot carry separate buy and sell
// rates. A row with a NULL direction applies to every direction — that is the
// legacy shape, and every row predating this dimension has it.
const (
	FeeDirectionBuy      = "buy"
	FeeDirectionSell     = "sell"
	FeeDirectionSend     = "send"
	FeeDirectionPay      = "pay"
	FeeDirectionWithdraw = "withdraw"
)

// IsValidFeeDirection reports whether d is one of the known directions. The
// empty string is NOT valid: it is the in-memory spelling of the NULL "applies
// to any direction" row, so accepting it here would let an admin write a row
// that looks direction-scoped but silently matches everything.
func IsValidFeeDirection(d string) bool {
	switch d {
	case FeeDirectionBuy, FeeDirectionSell, FeeDirectionSend, FeeDirectionPay, FeeDirectionWithdraw:
		return true
	default:
		return false
	}
}

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
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Feature     string    `gorm:"size:32;not null;uniqueIndex:idx_fee_config_feature_token_chain" json:"feature"`
	TokenSymbol string    `gorm:"size:16;not null;uniqueIndex:idx_fee_config_feature_token_chain" json:"tokenSymbol"`
	// ChainID scopes the row to a single chain. NULL = the legacy GLOBAL row:
	// the default for a (feature, token) used when no chain-specific row exists,
	// and the only row backend-charged fees (which don't yet thread chain) ever
	// read. A non-NULL ChainID is that chain's override of the global default.
	// (Configurable-FeeManager design §4: fee_config is the READ REPLICA of the
	// confirmed on-chain rates; the chain dimension mirrors the FeeManager keys.)
	ChainID *int64 `gorm:"uniqueIndex:idx_fee_config_feature_token_chain" json:"chainId,omitempty"`
	// Direction scopes the row to one leg of a flow (buy/sell/send/pay/withdraw).
	// NULL = applies to every direction, which is what every row written before
	// the transaction-tax work carries. A non-NULL Direction is that leg's
	// override, and it is the OUTERMOST resolution dimension — a directional
	// wildcard-token row outranks a direction-agnostic exact-token row, because
	// the tax schedule is authored per leg first and per asset second.
	Direction     *string   `gorm:"size:16;uniqueIndex:idx_fee_config_feature_token_chain" json:"direction,omitempty"`
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
