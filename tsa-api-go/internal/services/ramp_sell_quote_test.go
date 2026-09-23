package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
)

// ramp_sell_quote_test.go — Task 12: pricing a sell that needs a swap.
//
// The price model (design §3.2) is the whole of this file. She pays F all-in
// whether or not a swap was needed: B is the slice of F a round trip may
// spend, admission is l + g ≤ B, and whatever is left (B − l − g) is charged
// as our own LiFi integrator fee. These tests pin the four things that can
// silently cost money if they drift: what counts towards B, that a fee we
// cannot collect is never advertised, that the fee-carrying re-quote is
// re-admitted, and that the naira estimate is priced at F − B.

func newSellQuoterWithFakes(t *testing.T, routes ...*LiFiRoute) (*RampSellQuoter, *fakeLiFiQuoter) {
	t.Helper()
	pc := &stubQuoter{market: 1500, rates: map[string]*PaycrestRates{"base|USDC": sell("1500")}}
	quotes := NewPaycrestQuoteService(pc, []string{"USDC"}, []string{"base"})
	quotes.SetSenderFeePercent("1")
	quotes.SetChainRegistry(testChainRegistry())
	lifi := &fakeLiFiQuoter{routes: routes}
	return NewRampSellQuoter(lifi, quotes, testChainRegistry(), 50, "0.5", 0.02), lifi
}

func sellRoute(toUSD float64) *LiFiRoute {
	return &LiFiRoute{ToAmount: big.NewInt(9_980000), ToAmountMin: big.NewInt(9_970000),
		FromAmountUSD: 10, ToAmountUSD: toUSD, Raw: json.RawMessage(`{"id":"q"}`)}
}

// bscUSDT is an EVM token that is NOT the chain's gas coin, so a sell from it
// may need a top-up.
const bscUSDT = "0x55d398326f99059fF775485246999027B3197955"

// sellTestAddress has to be a real hex address: the source address is
// validated for the chain's family before anything is quoted.
const sellTestAddress = "0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A"

var sellIn = SellQuoteInput{FromChainID: 56, FromToken: evmNativeAddress, FromAmount: "16000000000000000",
	FromAddress: sellTestAddress, WalletAddress: sellTestAddress, FiatCode: "NGN"}

func TestRampSellQuote_ChargesTheUnspentBudgetAsOurFee(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(9.98), sellRoute(9.97))
	res, err := q.Quote(context.Background(), sellIn)
	if err != nil {
		t.Fatal(err)
	}
	// l = 20 bps, g = $0.02 on $10 = 20 bps -> fee = 50 − 20 − 20 = 10.
	if res.CostBps != 20 || res.GasBps != 20 || res.FeeBps != 10 {
		t.Fatalf("cost/gas/fee = %d/%d/%d, want 20/20/10", res.CostBps, res.GasBps, res.FeeBps)
	}
	last := lifi.lastRequest()
	if last.FeeBps != 10 || last.ToChainID != 8453 || last.ToAddress != sellTestAddress || last.Slippage != 0.005 {
		t.Fatalf("second quote = %+v", last)
	}
	if string(res.LiFiQuote) != `{"id":"q"}` {
		t.Fatalf("the executable quote must be the fee-carrying one: %s", res.LiFiQuote)
	}
}

func TestRampSellQuote_RefusesARouteOverTheBudget(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(9.95)) // l = 50, g = 20 -> 70 > 50
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v", err)
	}
}

func TestRampSellQuote_NeverAdvertisesAFeeItCannotCollect(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(9.98))
	lifi.err, lifi.errAfter = ErrLiFiFeeUnconfigured, 1
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRampSellQuote_RechecksTheBudgetAfterAddingOurFee(t *testing.T) {
	// Fee-carrying quote lands on a pricier tool: 9.93 -> l2 = 70 > B − g.
	q, _ := newSellQuoterWithFakes(t, sellRoute(9.98), sellRoute(9.93))
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v", err)
	}
}

func TestRampSellQuote_PricesATopUpIntoTheBudget(t *testing.T) {
	r1 := sellRoute(9.98)
	r1.GasCostUSD, r1.GasCostWei, r1.GasLimit = 0.05, big.NewInt(1_000), 1_000_000 // top-up ≈ 0.05 × 1.08 × 2 = $0.108 = 108 bps
	q, _ := newSellQuoterWithFakes(t, r1)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(0), true })
	in := sellIn
	in.FromToken = bscUSDT
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v; a sponsored top-up must count against B", err)
	}
}

// Pins the top-up formula, approve included: $0.10 × 1.08 × 2 = $0.216 on
// $100 plus the $0.02 relay = 23.6 -> 24 bps; l = 20; fee = 50 − 44 = 6. The
// formula without the approve would give 22 bps and a fee of 8.
func TestRampSellQuote_TopUpIncludesTheApprove(t *testing.T) {
	r1 := &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), FromAmountUSD: 100, ToAmountUSD: 99.8,
		GasCostUSD: 0.10, GasCostWei: big.NewInt(1_000), GasLimit: 1_000_000, Raw: json.RawMessage(`{}`)}
	r2 := &LiFiRoute{ToAmount: big.NewInt(99_740000), ToAmountMin: big.NewInt(99_640000), FromAmountUSD: 100, ToAmountUSD: 99.74,
		Raw: json.RawMessage(`{}`)} // l2 = 26: 26 + 24 = 50 fits
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(0), true })
	in := sellIn
	in.FromToken = bscUSDT
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.GasBps != 24 || res.FeeBps != 6 {
		t.Fatalf("gas/fee = %d/%d, want 24/6", res.GasBps, res.FeeBps)
	}
}

func TestRampSellQuote_EstimatesAtTheConvertedFee(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(9.98), sellRoute(9.97))
	res, err := q.Quote(context.Background(), sellIn)
	if err != nil {
		t.Fatal(err)
	}
	// Provider sell rate 1500 at F − B = 0.5% -> 1492.50; 9.98 USDC -> ₦14,895.15.
	if res.Rate != "1492.50" || res.FiatEstimate != "14895.15" {
		t.Fatalf("rate = %s estimate = %s", res.Rate, res.FiatEstimate)
	}
}

// A source address that is not an address on its own chain never reaches
// LiFi: the quote is priced FROM it, and a malformed one is a client bug that
// should read as a bad request rather than an outage.
func TestRampSellQuote_RefusesASourceAddressOfTheWrongFamily(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(9.98), sellRoute(9.97))
	in := sellIn
	in.FromAddress = "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin" // Solana, on an EVM chain
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellBadSourceAddress) {
		t.Fatalf("err = %v", err)
	}
	if lifi.quoteCalls() != 0 {
		t.Fatalf("quoted %d times for an unusable source address", lifi.quoteCalls())
	}
}
