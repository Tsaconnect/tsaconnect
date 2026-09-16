package services

import (
	"math/big"
	"strings"
)

const (
	// conversionMarkupFloorBps is Decision 4: 0.5% on converted orders, on top
	// of the Paycrest sender fee. When the conversion costs less than this, the
	// company keeps the difference.
	conversionMarkupFloorBps = 50
	// conversionMarkupCapBps is the ceiling (Decision 5). A route needing more
	// than 1% is not offered at all rather than quoted at a loss.
	conversionMarkupCapBps = 100
	// conversionMarkupHeadroomBps is what we add over a costly route's own
	// cost, so a route that drifts slightly between quote and execution still
	// covers itself.
	conversionMarkupHeadroomBps = 10
)

// ConversionMarkupBps prices a converted route from its guaranteed cost.
// offered is false when covering the cost would breach the cap.
func ConversionMarkupBps(costBps int) (int, bool) {
	if costBps < 0 {
		costBps = 0
	}
	bps := conversionMarkupFloorBps
	if costBps+conversionMarkupHeadroomBps > bps {
		bps = costBps + conversionMarkupHeadroomBps
	}
	if bps > conversionMarkupCapBps {
		return 0, false
	}
	return bps, true
}

// ConversionCostBps is the guaranteed cost of a route: how far LiFi's worst
// case (toAmountMin) falls below what Paycrest delivers, in basis points.
func ConversionCostBps(routeAmount, toAmountMin *big.Int) int {
	if routeAmount == nil || toAmountMin == nil || routeAmount.Sign() <= 0 {
		return conversionMarkupCapBps + 1 // unusable: never offered
	}
	diff := new(big.Int).Sub(routeAmount, toAmountMin)
	diff.Mul(diff, big.NewInt(10_000))
	return int(new(big.Int).Div(diff, routeAmount).Int64())
}

// PromisedAmount is what the user is guaranteed to receive: the Paycrest
// amount less the markup. Integer maths only — this is money.
func PromisedAmount(routeAmount *big.Int, markupBps int) *big.Int {
	if routeAmount == nil {
		return big.NewInt(0)
	}
	keep := big.NewInt(int64(10_000 - markupBps))
	out := new(big.Int).Mul(routeAmount, keep)
	return out.Div(out, big.NewInt(10_000))
}

// MeetsMinimumOrder applies the per-chain floor (Decision 6). Stablecoins are
// ~1 USD, so usd is the token amount.
func MeetsMinimumOrder(chainKey string, usd float64, mins map[string]float64, def float64) bool {
	min := def
	if m, ok := mins[strings.ToLower(strings.TrimSpace(chainKey))]; ok {
		min = m
	}
	return usd >= min
}
