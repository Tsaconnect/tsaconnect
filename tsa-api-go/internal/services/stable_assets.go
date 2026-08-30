package services

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"strings"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// StableAssetDecimals is the base-unit exponent for USDC and USDT. Both are
// 6-decimal on every chain Flutterwave supports, which is what lets one
// constant serve the whole ramp.
const StableAssetDecimals = 6

var (
	ErrUnsupportedStableChain = errors.New("stable: unsupported chain")
	ErrUnsupportedChainAsset  = errors.New("stable: asset not supported on this chain")
	ErrBadDeliveryAddress     = errors.New("stable: malformed address for this chain")
	ErrStablePrecision        = errors.New("stable: amount has more than 6 decimal places")
)

// deliveryMatrix is Flutterwave's supported (chain, token) pairs for outbound
// crypto transfers, verified against developer.flutterwave.com 2026-08-27.
// Anything absent here FAILS at Flutterwave — transfers to Tron, Stellar, BSC
// and Arbitrum are rejected — and a rejected transfer to a live address is not
// recoverable, so this gate runs before an intent exists.
var deliveryMatrix = map[string]map[string]bool{
	"POLYGON":  {models.ITAssetUSDC: true, models.ITAssetUSDT: true},
	"ETHEREUM": {models.ITAssetUSDC: true, models.ITAssetUSDT: true},
	"SOLANA":   {models.ITAssetUSDC: true, models.ITAssetUSDT: true},
	"BASE":     {models.ITAssetUSDC: true}, // USDC only
}

var (
	evmAddressRe    = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	base58AddressRe = regexp.MustCompile(`^[1-9A-HJ-NP-Za-km-z]{32,44}$`)
)

// IsStableAsset reports whether the asset is one this ramp brokers.
func IsStableAsset(asset string) bool {
	switch strings.ToUpper(strings.TrimSpace(asset)) {
	case models.ITAssetUSDC, models.ITAssetUSDT:
		return true
	}
	return false
}

// SupportsDelivery reports whether Flutterwave can send `asset` on `chain`.
func SupportsDelivery(chain, asset string) bool {
	tokens, ok := deliveryMatrix[strings.ToUpper(strings.TrimSpace(chain))]
	if !ok {
		return false
	}
	return tokens[strings.ToUpper(strings.TrimSpace(asset))]
}

// ValidateDeliveryAddress checks an address against its chain's format. It is
// deliberately strict: a malformed or wrong-chain address means funds are gone.
func ValidateDeliveryAddress(chain, address string) error {
	chain = strings.ToUpper(strings.TrimSpace(chain))
	address = strings.TrimSpace(address)
	switch chain {
	case "POLYGON", "ETHEREUM", "BASE":
		if !evmAddressRe.MatchString(address) {
			return fmt.Errorf("%w: %s expects a 0x-prefixed 40-hex-digit address", ErrBadDeliveryAddress, chain)
		}
		return nil
	case "SOLANA":
		if !base58AddressRe.MatchString(address) {
			return fmt.Errorf("%w: SOLANA expects a 32-44 character base58 address", ErrBadDeliveryAddress)
		}
		return nil
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedStableChain, chain)
	}
}

// StableToBaseUnits converts a decimal major-unit string ("100.123456") to
// integer base units. Excess precision is an error, never a truncation —
// silently dropping a digit loses the user's money.
func StableToBaseUnits(major string) (*big.Int, error) {
	major = strings.TrimSpace(major)
	if major == "" {
		return nil, fmt.Errorf("%w: empty amount", ErrStablePrecision)
	}
	whole, frac, _ := strings.Cut(major, ".")
	if len(frac) > StableAssetDecimals {
		return nil, fmt.Errorf("%w: %q", ErrStablePrecision, major)
	}
	frac += strings.Repeat("0", StableAssetDecimals-len(frac))
	v, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok || v.Sign() < 0 {
		return nil, fmt.Errorf("%w: %q", ErrStablePrecision, major)
	}
	return v, nil
}

// ValidateBuyDelivery gates a buy's delivery target before an intent exists.
//
// Delivery is the one irreversible step in the buy flow: Flutterwave rejects
// an unsupported (chain, asset) pair, and a rejected transfer to a live
// address is not recoverable. Both checks therefore run at intent creation,
// not at delivery time.
func ValidateBuyDelivery(chain, asset, address string) error {
	chain = strings.ToUpper(strings.TrimSpace(chain))
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if !SupportsDelivery(chain, asset) {
		return fmt.Errorf("%w: Flutterwave does not deliver %s on %s",
			ErrUnsupportedChainAsset, asset, chain)
	}
	return ValidateDeliveryAddress(chain, address)
}

// DeliverableChains returns the chains this user can be paid on for `asset`,
// sorted for a stable UI ordering.
//
// Takes a plain chain->address map rather than the wallet itself so it stays
// pure and testable; the service layer builds that map from the jsonb entry
// list (see userDeliveryAddresses), filtering out external and inactive
// addresses on the way.
//
// A chain is offered ONLY when Flutterwave delivers the pair AND the user
// actually holds an address on it. Wallet.Addresses is a sparse map — a Solana
// address exists only if the wallet derived one — and offering a chain the
// user cannot receive on would strand funds somewhere they cannot see.
func DeliverableChains(asset string, addresses map[string]string) []string {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	out := make([]string, 0, len(deliveryMatrix))
	for chain := range deliveryMatrix {
		if !SupportsDelivery(chain, asset) {
			continue
		}
		if strings.TrimSpace(addresses[chain]) == "" {
			continue
		}
		out = append(out, chain)
	}
	sort.Strings(out)
	return out
}

// StableFromBaseUnits converts base units to the float Flutterwave's API takes.
// Float appears ONLY at that boundary — never in internal arithmetic.
func StableFromBaseUnits(units *big.Int) float64 {
	if units == nil {
		return 0
	}
	q := new(big.Rat).SetFrac(units, big.NewInt(1000000))
	f, _ := q.Float64()
	return f
}
