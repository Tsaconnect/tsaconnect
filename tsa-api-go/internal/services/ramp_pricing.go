package services

import (
	"math"
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
	if routeAmount == nil || routeAmount.Sign() <= 0 {
		return big.NewInt(0)
	}
	return bpsShare(routeAmount, int64(10_000-markupBps))
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

// stableHeadroomBps is the room a stablecoin promise leaves under what she's
// owed: LiFi's 0.1% stable slippage plus 20 bps for USDC/USDT price drift, so
// an honest route clears it.
const stableHeadroomBps = 30

// ceilBps rounds a bps figure up — never under-price a route — after shaving
// float noise, and floors it at zero.
func ceilBps(x float64) int {
	v := int(math.Ceil(x - 1e-6))
	if v < 0 {
		return 0
	}
	return v
}

// LiFiCostBps is LiFi's own cost of a route: USD in less USD out. Gas is paid
// separately (TreasuryGasBps / USDGasBps) and slippage tolerance is not a cost.
func LiFiCostBps(route *LiFiRoute) (int, bool) {
	if route == nil || route.FromAmountUSD <= 0 || route.ToAmountUSD <= 0 {
		return 0, false
	}
	return ceilBps((route.FromAmountUSD - route.ToAmountUSD) / route.FromAmountUSD * 10_000), true
}

// RouteGasUSD is what a route's gas costs in dollars including extraGasUnits
// (the approve), scaled from the route's own quote. A route with no gas limit
// is priced at the ceiling, the same figure execution would be held to.
func RouteGasUSD(route *LiFiRoute, extraGasUnits uint64) float64 {
	if route == nil {
		return 0
	}
	routeGas := route.GasLimit
	if routeGas == 0 {
		routeGas = maxRouteGasLimit
	}
	return route.GasCostUSD * float64(routeGas+extraGasUnits) / float64(routeGas)
}

// TreasuryGasBps is the gas the treasury pays for a route plus extraGasUnits
// (the approve), priced from the route's own gas cost, as bps of the input.
func TreasuryGasBps(route *LiFiRoute, extraGasUnits uint64) int {
	if route == nil || route.FromAmountUSD <= 0 {
		return 0
	}
	return USDGasBps(RouteGasUSD(route, extraGasUnits), route.FromAmountUSD)
}

// TopUpUSDForRoute is what a sponsored source-chain gas top-up for this route
// costs us: its gas plus an approve, times topUpGasBuffer — exactly what the
// top-up sends. The quote prices it into the round-trip budget and the
// intent later spends it, so both read this one function: a quote that
// budgets less than the top-up actually costs is money leaking per sell.
func TopUpUSDForRoute(route *LiFiRoute) float64 {
	return RouteGasUSD(route, treasuryERC20Gas) * topUpGasBuffer
}

// USDGasBps is a USD gas cost as bps of an order's USD value.
func USDGasBps(usd, fromUSD float64) int {
	if fromUSD <= 0 {
		return 0
	}
	return ceilBps(usd / fromUSD * 10_000)
}

// BridgeAmount is what the treasury sends so LiFi delivers about `entitled`
// in value: entitled / (1 − l). Refused when that exceeds what was received.
func BridgeAmount(entitled, received *big.Int, costBps int) (*big.Int, bool) {
	if entitled == nil || received == nil || entitled.Sign() <= 0 || costBps < 0 || costBps >= 10_000 {
		return nil, false
	}
	a := new(big.Int).Mul(entitled, big.NewInt(10_000))
	a.Quo(a, big.NewInt(int64(10_000-costBps)))
	if a.Cmp(received) > 0 {
		return nil, false
	}
	return a, true
}

// StablePromise is the floor a stablecoin destination must reach: what she's
// owed in destination units, less slippage and price-drift headroom.
func StablePromise(entitled *big.Int, srcDecimals, dstDecimals int) *big.Int {
	return rescaleUnits(bpsShare(entitled, 10_000-stableHeadroomBps), srcDecimals, dstDecimals)
}

// QuotedFromOrder is T from Paycrest's own order amount: what a direct order
// at F would have delivered, given this order was sent at F − B. Using the
// order's amount (not the venue's quote) keeps T consistent with what the
// treasury actually receives even if the provider's rate moved.
func QuotedFromOrder(orderAmount *big.Int, feeBps, budgetBps int) *big.Int {
	out := new(big.Int).Mul(orderAmount, big.NewInt(int64(10_000+feeBps-budgetBps)))
	return out.Quo(out, big.NewInt(int64(10_000+feeBps)))
}

// EntitledAmount is what she is owed from a delivery: the quoted amount, cut
// in proportion when Paycrest delivered less than the order expected.
func EntitledAmount(quoted, expected, received *big.Int) *big.Int {
	if expected == nil || expected.Sign() <= 0 || received.Cmp(expected) >= 0 {
		return new(big.Int).Set(quoted)
	}
	out := new(big.Int).Mul(quoted, received)
	return out.Quo(out, expected)
}
