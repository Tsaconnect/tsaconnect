package services

import (
	"math"
	"math/big"
	"strings"
)

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
	// A cost that is not a number is never cheap. int(NaN) is 0 on some
	// platforms and int(+Inf) wraps negative on others; either would admit
	// the route. Price it past every budget instead. So is a finite cost too
	// large for an int: converting it is undefined and reads as 0 on amd64.
	if math.IsNaN(x) || x >= math.MaxInt32 {
		return math.MaxInt32
	}
	v := int(math.Ceil(x - 1e-6))
	if v < 0 {
		return 0
	}
	return v
}

// LiFiCostBps is LiFi's own cost of a route: USD in less USD out. Gas is paid
// separately (TreasuryGasBps / USDGasBps) and slippage tolerance is not a cost.
func LiFiCostBps(route *LiFiRoute) (int, bool) {
	// Written as "not > 0" so NaN, which compares false with everything, is
	// refused too; +Inf is no price either.
	if route == nil || !(route.FromAmountUSD > 0) || !(route.ToAmountUSD > 0) ||
		math.IsInf(route.FromAmountUSD, 1) || math.IsInf(route.ToAmountUSD, 1) {
		return 0, false
	}
	return ceilBps((route.FromAmountUSD - route.ToAmountUSD) / route.FromAmountUSD * 10_000), true
}

// routeGasUnits is the gas a route is held to: its own limit, or the ceiling
// when it quoted none — the same figure execution refuses to go past. One
// function so a price and the check that admits it cannot disagree.
func routeGasUnits(route *LiFiRoute) uint64 {
	if route == nil || route.GasLimit == 0 {
		return maxRouteGasLimit
	}
	return route.GasLimit
}

// RouteGasUSD is what a route's gas costs in dollars including extraGasUnits
// (the approve), scaled from the route's own quote. A route with no gas limit
// is priced at the ceiling, the same figure execution would be held to.
func RouteGasUSD(route *LiFiRoute, extraGasUnits uint64) float64 {
	if route == nil {
		return 0
	}
	routeGas := routeGasUnits(route)
	return route.GasCostUSD * float64(routeGas+extraGasUnits) / float64(routeGas)
}

// RouteGasWei is RouteGasUSD's twin in the coin the gas is actually paid in:
// the same gas, the same approve, the same scaling. It is what a wallet's
// native balance has to be compared against, because the dollar figure cannot
// answer "can she pay for this" — it prices the gas, not the coin.
//
// nil means the route did not quote its gas in wei, which is an UNKNOWN and
// not a zero: the caller decides what to do about it rather than having the
// answer fall out of a comparison against nil.
func RouteGasWei(route *LiFiRoute, extraGasUnits uint64) *big.Int {
	if route == nil || route.GasCostWei == nil {
		return nil
	}
	routeGas := routeGasUnits(route)
	need := new(big.Int).Mul(route.GasCostWei, new(big.Int).SetUint64(routeGas+extraGasUnits))
	return need.Quo(need, new(big.Int).SetUint64(routeGas))
}

// routeSlippage is the tolerance a route is quoted at: LiFi's tight default
// for a stable pair, which should not move on price, and the room a normal
// swap takes for anything else. Buys and sells share it so the same pair
// cannot be offered two different worst cases.
func routeSlippage(stable bool) float64 {
	if stable {
		return 0.001
	}
	return 0.005
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
// costs us at most: routeGasReserveWei in dollars — exactly what the top-up
// sends to an empty wallet. The quote prices it into the round-trip budget and the
// intent later spends it, so both read this one function: a quote that
// budgets less than the top-up actually costs is money leaking per sell.
func TopUpUSDForRoute(route *LiFiRoute) float64 {
	reserve := routeGasReserveWei(route)
	if reserve == nil {
		return RouteGasUSD(route, treasuryERC20Gas) * topUpGasBuffer
	}
	// The gas's own USD-per-wei (a non-nil reserve implies a wei cost), so
	// what we price is what we send.
	usd, _ := new(big.Rat).Mul(new(big.Rat).SetInt(reserve),
		new(big.Rat).Quo(new(big.Rat).SetFloat64(route.GasCostUSD), new(big.Rat).SetInt(route.GasCostWei))).Float64()
	return usd
}

// lifiApproveGasLimit is the gas limit the app signs a route's approve with
// (tsa-app services/lifiFlow.ts, approveTx.gasLimit). Keep the two in step.
const lifiApproveGasLimit = 65_000

// routeGasReserveMarginPct covers what gas limit × price leaves out: an
// OP-stack chain's L1 data fee, charged on top (measured ~3% of an approve's
// L2 gas on Base, 2026-10-01; a swap's longer calldata costs more).
const routeGasReserveMarginPct = 25

// routeGasReserveWei is the native coin her wallet must hold for a node to
// accept the approve and the swap — the one figure that both decides a
// source-chain top-up is needed and sizes it. nil when the route quoted no
// gas in wei: with no USD-per-wei to price it at, the top-up is an unknown
// on both sides, never sized one way and priced another.
func routeGasReserveWei(route *LiFiRoute) *big.Int {
	if route == nil || route.GasCostWei == nil || route.GasCostWei.Sign() <= 0 {
		return nil
	}
	return routeReserveWei(route, lifiApproveGasLimit, treasuryERC20Gas)
}

// routeSwapReserveWei is what a node reserves for the swap alone: paying with
// a chain's own coin has no approve, and the coin itself pays the gas.
func routeSwapReserveWei(route *LiFiRoute) *big.Int {
	return routeReserveWei(route, 0, 0)
}

// routeReserveWei is the route's signed gas limit plus extraLimit at the price
// LiFi signs it at, plus the L1-fee margin. A node admits a tx only when the
// wallet holds limit × price — LiFi's estimate of what it will USE is ~3.8x
// lower on Base (2026-10-01), and a wallet holding the estimate is refused
// ("insufficient funds for gas * price"). With no signed price it falls back
// to the estimate (plus extraEstimate gas), times topUpGasBuffer.
func routeReserveWei(route *LiFiRoute, extraLimit, extraEstimate uint64) *big.Int {
	if route != nil && route.GasLimit > 0 && route.TxGasPriceWei != nil && route.TxGasPriceWei.Sign() > 0 {
		r := new(big.Int).SetUint64(route.GasLimit + extraLimit)
		r.Mul(r, route.TxGasPriceWei)
		r.Mul(r, big.NewInt(100+routeGasReserveMarginPct))
		return r.Quo(r, big.NewInt(100))
	}
	est := RouteGasWei(route, extraEstimate)
	if est == nil {
		return nil
	}
	return est.Mul(est, big.NewInt(topUpGasBuffer))
}

// quotedTopUpWei is what a source-chain gas top-up for this route would cost,
// in wei: exactly the figure TopUpUSDForRoute priced into the round-trip
// budget, expressed in the coin the gas is actually paid in. Both read
// RouteGasWei/RouteGasUSD over the same units and multiply by the same
// buffer, so the price and the spend cannot disagree.
//
// "" when the route quoted no gas in wei. That is an unknown, and a caller
// must decide what to do about it rather than read a zero as "free".
func quotedTopUpWei(route *LiFiRoute) string {
	need := routeGasReserveWei(route)
	if need == nil {
		return ""
	}
	return need.String()
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

// ConversionOwed is T, what a converted buy owes in the settlement token once
// the round-trip budget B′ is held back: D × (10 000 − B′) ÷ 10 000, floored
// (spec §15.7). The pricer, the open and the handoff all use it.
func ConversionOwed(delivered *big.Int, markupBps int) *big.Int {
	return bpsShare(delivered, int64(10_000-markupBps))
}
