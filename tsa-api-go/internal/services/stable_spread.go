package services

import (
	"errors"
	"fmt"
	"math/big"
)

// ErrBadFxProbe means Flutterwave's rate response cannot be used to derive a
// rate — a zero or negative amount on either side.
var ErrBadFxProbe = errors.New("stable: unusable FX probe from Flutterwave")

// ApplySellSpread reduces a gross fiat amount by the company spread, flooring.
//
// The spread is the ONLY margin on a brokered stablecoin trade — the swap and
// routing bps legs are not charged on these flows (see FeeQuoteService.swapLegs)
// because the user already pays the bridge directly out of what arrives.
//
// Flooring is not a rounding preference. The platform holds no position on
// these trades, so paying out a rounded-up unit means paying out money it never
// received.
func ApplySellSpread(gross *big.Int, spreadBps int64) *big.Int {
	if gross == nil {
		return big.NewInt(0)
	}
	if spreadBps <= 0 {
		return new(big.Int).Set(gross)
	}
	out := new(big.Int).Mul(gross, big.NewInt(10000-spreadBps))
	return out.Quo(out, big.NewInt(10000)) // Quo truncates toward zero
}

// DeriveFiatPerUnit returns the fiat units per one whole asset unit, computed
// from the amounts Flutterwave echoes back.
//
// FWRate.Rate is ignored on purpose. Its unit direction is undocumented and the
// vendor's own example is self-contradictory, whereas the Source/Destination
// pair is unambiguous: "to receive Destination.Amount of the destination
// currency you pay Source.Amount of the source currency". This mirrors the
// derivation ITService.QuoteWithdrawal already relies on.
func DeriveFiatPerUnit(rate FWRate) (*big.Rat, error) {
	if rate.Source.Amount <= 0 || rate.Destination.Amount <= 0 {
		return nil, fmt.Errorf("%w: source=%v destination=%v",
			ErrBadFxProbe, rate.Source.Amount, rate.Destination.Amount)
	}
	src := new(big.Rat).SetFloat64(rate.Source.Amount)
	dst := new(big.Rat).SetFloat64(rate.Destination.Amount)
	if src == nil || dst == nil {
		return nil, ErrBadFxProbe
	}
	return new(big.Rat).Quo(dst, src), nil
}
