package services

import (
	"context"
	"errors"
	"math"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// ramp_sell_quote_passthrough_test.go — the marketplace's funding swap is
// priced at cost (owner decision 2026-09-29): she pays LiFi's cost plus the gas
// we pay for her and nothing else. These pin the three ways that can drift —
// the unspent budget turning into our fee, the gas recovery exceeding the gas,
// and a quoter wired without Paycrest refusing a delivery it can price.

const (
	fundingTestSonicUSDC = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
	fundingTestSonicUSDT = "0x6047828dc181963ba44974801ff68e538da5eaf9"
)

// fundingChainRegistry is testChainRegistry plus Sonic mainnet and its stables.
func fundingChainRegistry() *BlockchainService {
	return &BlockchainService{cfg: &config.Config{Networks: map[string]config.NetworkConfig{
		"mainnet": {
			Chains: map[string]config.ChainConfig{
				"base":  {ChainID: 8453},
				"bsc":   {ChainID: 56},
				"sonic": {ChainID: 146},
			},
			TokenAddresses: map[string]string{
				"base:USDC":  "0xBaseUSDC00000000000000000000000000001",
				"bsc:USDT":   bscUSDT,
				"sonic:USDC": fundingTestSonicUSDC,
				"sonic:USDT": fundingTestSonicUSDT,
			},
		},
	}}}
}

// newDeliveryQuoter is the marketplace's own quoter: no Paycrest quote
// service, no B of its own — exactly what buildMarketplaceFunding wires when
// Paycrest is off.
func newDeliveryQuoter(t *testing.T, routes ...*LiFiRoute) (*RampSellQuoter, *fakeLiFiQuoter) {
	t.Helper()
	lifi := &fakeLiFiQuoter{routes: routes}
	q := NewRampSellQuoter(signedBodyLiFi{lifi}, nil, fundingChainRegistry(), 0, "", 0)
	q.SetRouterAllowlist(map[int64][]common.Address{56: {common.HexToAddress(sellTestDiamond)}})
	return q, lifi
}

// fundingRoute is a $10 swap from BSC that lands toUSD on Sonic in her wallet.
func fundingRoute(t *testing.T, toUSD float64) *LiFiRoute {
	t.Helper()
	to := int64(math.Round(toUSD * 1e6))
	return &LiFiRoute{
		ToAmount: big.NewInt(to), ToAmountMin: big.NewInt(to - 10_000),
		FromAmountUSD: 10, ToAmountUSD: toUSD, FromAmount: "10000000000000000",
		To: sellTestDiamond, Data: testBridgeCalldata(t, sellTestAddress, 146, false), TransactionID: "0xabc",
	}
}

var fundingIn = SellQuoteInput{
	FromChainID: 56, FromToken: evmNativeAddress, FromAddress: sellTestAddress, WalletAddress: sellTestAddress,
	ToAmount: "9950000", ToChainID: 146, ToToken: fundingTestSonicUSDC, ToAddress: sellTestAddress,
	BudgetBps: 200, Delivery: true, PassThrough: true,
}

func TestPassThrough_TakesNoFeeWhenWePayNoGas(t *testing.T) {
	q, lifi := newDeliveryQuoter(t, fundingRoute(t, 9.98))
	res, err := q.Quote(context.Background(), fundingIn)
	if err != nil {
		t.Fatal(err)
	}
	// l = 20 bps of a 200 bps budget: the 180 left over is NOT charged.
	if res.FeeBps != 0 || res.CostBps != 20 || res.GasBps != 0 {
		t.Fatalf("fee/cost/gas = %d/%d/%d, want 0/20/0 — the unspent budget must never become our fee", res.FeeBps, res.CostBps, res.GasBps)
	}
	if len(lifi.all) != 1 || lifi.all[0].FeeBps != 0 {
		t.Fatalf("asks = %+v, want one fee-free ask", lifi.all)
	}
	if req := lifi.all[0]; req.ToChainID != 146 || req.ToToken != fundingTestSonicUSDC || req.ToAddress != sellTestAddress ||
		req.ToAmount != "9950000" || req.AllowDestinationCall {
		t.Fatalf("ask = %+v, want exact-output Sonic USDC to her own wallet, no destination call", req)
	}
}

func TestPassThrough_RecoversExactlyTheGasWePay(t *testing.T) {
	// $0.05 of Sonic gas on a $10 swap is 50 bps: the only fee on the route.
	in := fundingIn
	in.DestGasUSD = 0.05
	q, lifi := newDeliveryQuoter(t, fundingRoute(t, 9.98), fundingRoute(t, 9.93))
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifi.all) != 2 || lifi.all[1].FeeBps != 50 {
		t.Fatalf("asks = %+v, want the fee-carrying ask at exactly g = 50 bps", lifi.all)
	}
	// The second route costs 70 bps with our 50 inside: LiFi's own is 20.
	if res.FeeBps != 50 || res.GasBps != 50 || res.CostBps != 20 {
		t.Fatalf("fee/gas/cost = %d/%d/%d, want 50/50/20", res.FeeBps, res.GasBps, res.CostBps)
	}
}

func TestPassThrough_RefusesARouteOverBudget(t *testing.T) {
	for name, tc := range map[string]struct {
		toUSD   float64
		destGas float64
	}{
		"LiFi's cost alone":      {toUSD: 9.79}, // 210 bps
		"cost plus gas we'd pay": {toUSD: 9.85, destGas: 0.06},
	} {
		t.Run(name, func(t *testing.T) {
			in := fundingIn
			in.DestGasUSD = tc.destGas
			q, _ := newDeliveryQuoter(t, fundingRoute(t, tc.toUSD), fundingRoute(t, tc.toUSD))
			if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
				t.Fatalf("err = %v, want ErrSellRouteTooCostly", err)
			}
		})
	}
}

func TestPassThrough_FeeCarryingRouteIsReadmitted(t *testing.T) {
	in := fundingIn
	in.DestGasUSD = 0.05
	// Cheap enough at first; the fee-carrying ask lands on a dearer tool.
	q, _ := newDeliveryQuoter(t, fundingRoute(t, 9.98), fundingRoute(t, 9.75))
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v, want the dearer fee-carrying route refused", err)
	}
}

func TestPassThrough_IsADeliverysPricingOnly(t *testing.T) {
	q, _ := newDeliveryQuoter(t, fundingRoute(t, 9.98))
	in := fundingIn
	in.Delivery = false
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v, want an offramp never priced at cost", err)
	}
	in = fundingIn
	in.BudgetBps = 0
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v, want a pass-through with no budget refused", err)
	}
}

// A quoter wired without Paycrest still prices a delivery, and refuses an
// offramp (which needs the Paycrest quote and the quoter's own B).
func TestDeliveryQuoter_NeedsNoPaycrest(t *testing.T) {
	q, _ := newDeliveryQuoter(t, fundingRoute(t, 9.98))
	if _, err := q.Quote(context.Background(), fundingIn); err != nil {
		t.Fatalf("delivery without Paycrest: %v", err)
	}
	offramp := sellIn
	if _, err := q.Quote(context.Background(), offramp); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v, want an offramp refused by a quoter with no Paycrest", err)
	}
}

// The route she signs must deliver to her own wallet with no destination call:
// LiFi is asked allowDestinationCall=false, and the calldata is still checked.
func TestPassThrough_RefusesADestinationCallOrAnotherReceiver(t *testing.T) {
	for name, data := range map[string]func(t *testing.T) string{
		"destination call": func(t *testing.T) string { return testBridgeCalldata(t, sellTestAddress, 146, true) },
		"another receiver": func(t *testing.T) string { return testBridgeCalldata(t, sellOtherAddress, 146, false) },
		"another chain":    func(t *testing.T) string { return testBridgeCalldata(t, sellTestAddress, 8453, false) },
	} {
		t.Run(name, func(t *testing.T) {
			r := fundingRoute(t, 9.98)
			r.Data = data(t)
			q, _ := newDeliveryQuoter(t, r)
			if _, err := q.Quote(context.Background(), fundingIn); !errors.Is(err, ErrSellUnavailable) {
				t.Fatalf("err = %v: she would sign a route that does not pay her own wallet", err)
			}
		})
	}
}

// A route LiFi did not price in USD has no cost we can judge. It is reported
// as unpriced — still a "too costly" refusal to a sale, which shows that copy,
// but distinguishable so a checkout can say "unavailable" instead.
func TestPassThrough_UnpricedRouteIsReportedAsUnpriced(t *testing.T) {
	route := fundingRoute(t, 9.98)
	route.FromAmountUSD = 0
	q, _ := newDeliveryQuoter(t, route)
	_, err := q.Quote(context.Background(), fundingIn)
	if !errors.Is(err, ErrSellRouteUnpriced) || !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v, want ErrSellRouteUnpriced (wrapping ErrSellRouteTooCostly)", err)
	}
}

// End to end through Quote: a route LiFi priced for signing carries a top-up
// sized from what the node reserves (routeGasReserveWei), priced at the gas's
// own USD-per-wei — not the estimate that left a buyer short on 2026-10-01.
func TestPassThrough_TopUpIsSizedFromTheSignedReserve(t *testing.T) {
	priced := func(toUSD float64) *LiFiRoute {
		r := fundingRoute(t, toUSD)
		r.GasLimit, r.TxGasPriceWei = 300_000, big.NewInt(10_000)
		r.GasCostWei, r.GasCostUSD = big.NewInt(1_000_000_000), 0.001 // the estimate: ~100k gas
		return r
	}
	first, second := priced(9.98), priced(9.93)
	q, _ := newDeliveryQuoter(t, first, second)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(0), true })
	in := fundingIn
	in.FromToken = bscUSDT
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	want := routeGasReserveWei(second)
	if !res.GasTopUp || res.TopUpWei != want.String() {
		t.Fatalf("topUp = %v wei = %q, want %s from the signed route", res.GasTopUp, res.TopUpWei, want)
	}
	limits := new(big.Int).Mul(big.NewInt(300_000+lifiApproveGasLimit), big.NewInt(10_000))
	if want.Cmp(limits) <= 0 {
		t.Fatalf("reserve %v does not cover the signed limits %v", want, limits)
	}
	if res.GasBps != USDGasBps(TopUpUSDForRoute(second), second.FromAmountUSD) {
		t.Fatalf("gas = %d bps, want the reserve priced: %d", res.GasBps, USDGasBps(TopUpUSDForRoute(second), second.FromAmountUSD))
	}
}
