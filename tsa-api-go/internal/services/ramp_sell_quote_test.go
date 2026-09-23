package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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

// sellCalldata is a route that really does deliver to receiver, in USDC on
// Base. Every fixture carries one: the quote hands the app calldata to sign,
// so the calldata is part of the fixture, not decoration.
func sellCalldata(t *testing.T, receiver string) string {
	t.Helper()
	return testBridgeCalldata(t, receiver, rampSettlementChainID, false)
}

// sellRoute is a $10 sell landing toUSD of USDC. ToAmount and Raw are DERIVED
// from toUSD so two fixtures are never interchangeable: an assertion about
// which quote came back has to be able to fail.
func sellRoute(t *testing.T, toUSD float64) *LiFiRoute {
	t.Helper()
	to := int64(math.Round(toUSD * 1e6))
	return &LiFiRoute{
		ToAmount: big.NewInt(to), ToAmountMin: big.NewInt(to - 10_000),
		FromAmountUSD: 10, ToAmountUSD: toUSD,
		Raw:  json.RawMessage(fmt.Sprintf(`{"id":"q%d"}`, to)),
		Data: sellCalldata(t, sellTestAddress),
	}
}

func sellRawID(t *testing.T, toUSD float64) string {
	t.Helper()
	return string(sellRoute(t, toUSD).Raw)
}

// bscUSDT is the registry's BSC USDT: an EVM token that is NOT the chain's
// gas coin (so a sell from it may need a top-up) and IS a configured stable
// (so the pair it makes with Base USDC is a stable one).
const bscUSDT = "0xBscUSDT000000000000000000000000000001"

// sellTestAddress has to be a real hex address: the source address is
// validated for the chain's family before anything is quoted.
const sellTestAddress = "0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A"

// sellOtherAddress is somebody else — used to build a route whose calldata
// pays the wrong person.
const sellOtherAddress = "0x000000000000000000000000000000000000dEaD"

var sellIn = SellQuoteInput{FromChainID: 56, FromToken: evmNativeAddress, FromAmount: "16000000000000000",
	FromAddress: sellTestAddress, WalletAddress: sellTestAddress, FiatCode: "NGN"}

func TestRampSellQuote_ChargesTheUnspentBudgetAsOurFee(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
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
	// The fee-carrying route is route 2, and only route 2's own body will do.
	if want := sellRawID(t, 9.97); string(res.LiFiQuote) != want {
		t.Fatalf("the executable quote must be the fee-carrying one: got %s, want %s", res.LiFiQuote, want)
	}
}

func TestRampSellQuote_RefusesARouteOverTheBudget(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.95)) // l = 50, g = 20 -> 70 > 50
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v", err)
	}
}

// Admission is l + g ≤ B, so a route that spends the budget to the last bps
// is admitted — with no fee left to charge and therefore no second ask.
func TestRampSellQuote_AdmitsARouteThatSpendsExactlyTheBudget(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.97)) // l = 30, g = 20 -> 50 = B
	res, err := q.Quote(context.Background(), sellIn)
	if err != nil {
		t.Fatal(err)
	}
	if res.CostBps != 30 || res.GasBps != 20 || res.FeeBps != 0 {
		t.Fatalf("cost/gas/fee = %d/%d/%d, want 30/20/0", res.CostBps, res.GasBps, res.FeeBps)
	}
	if lifi.quoteCalls() != 1 {
		t.Fatalf("asked LiFi %d times; a zero fee has nothing to re-quote for", lifi.quoteCalls())
	}
	if want := sellRawID(t, 9.97); string(res.LiFiQuote) != want {
		t.Fatalf("quote = %s, want the fee-free route %s", res.LiFiQuote, want)
	}
}

// One bps over is refused: the boundary is ≤ B, not < B + 1.
func TestRampSellQuote_RefusesARouteOneBpsOverTheBudget(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.969)) // l = 31, g = 20 -> 51 > 50
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v", err)
	}
}

func TestRampSellQuote_NeverAdvertisesAFeeItCannotCollect(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98))
	lifi.err, lifi.errAfter = ErrLiFiFeeUnconfigured, 1
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRampSellQuote_RechecksTheBudgetAfterAddingOurFee(t *testing.T) {
	// Fee-carrying quote lands on a pricier tool: 9.93 -> l2 = 70 > B − g.
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.93))
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v", err)
	}
}

// A quoter that answers with neither a route nor an error is a bug, but it
// must read as one — not take the process down.
func TestRampSellQuote_SurvivesAQuoterThatAnswersNothing(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, nil)
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRampSellQuote_PricesATopUpIntoTheBudget(t *testing.T) {
	r1 := sellRoute(t, 9.98)
	r1.GasCostUSD, r1.GasCostWei, r1.GasLimit = 0.05, big.NewInt(1_000), 1_000_000 // top-up ≈ 0.05 × 1.08 × 2 = $0.108 = 108 bps
	q, _ := newSellQuoterWithFakes(t, r1)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(0), true })
	in := sellIn
	in.FromToken = bscUSDT
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v; a sponsored top-up must count against B", err)
	}
}

// topUpFixture is the $100 pair the top-up tests price: r1 quotes the gas,
// r2 is the fee-carrying answer. l = 20, l2 = 26.
func topUpFixture(t *testing.T) (*LiFiRoute, *LiFiRoute) {
	t.Helper()
	r1 := &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), FromAmountUSD: 100, ToAmountUSD: 99.8,
		GasCostUSD: 0.10, GasCostWei: big.NewInt(1_000), GasLimit: 1_000_000,
		Raw: json.RawMessage(`{"id":"r1"}`), Data: sellCalldata(t, sellTestAddress)}
	r2 := &LiFiRoute{ToAmount: big.NewInt(99_740000), ToAmountMin: big.NewInt(99_640000), FromAmountUSD: 100, ToAmountUSD: 99.74,
		Raw: json.RawMessage(`{"id":"r2"}`), Data: sellCalldata(t, sellTestAddress)} // l2 = 26: 26 + 24 = 50 fits
	return r1, r2
}

// Pins the top-up formula, approve included: $0.10 × 1.08 × 2 = $0.216 on
// $100 plus the $0.02 relay = 23.6 -> 24 bps; l = 20; fee = 50 − 44 = 6. The
// formula without the approve would give 22 bps and a fee of 8.
func TestRampSellQuote_TopUpIncludesTheApprove(t *testing.T) {
	r1, r2 := topUpFixture(t)
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
	if string(res.LiFiQuote) != `{"id":"r2"}` {
		t.Fatalf("quote = %s, want the fee-carrying route", res.LiFiQuote)
	}
}

// The threshold and the price must name the same gas. She holds EXACTLY the
// swap's gas and not a wei more: she cannot also send the approve, so the
// top-up is needed — and if the threshold asked only about the swap she would
// be judged funded and the approve would be unpayable.
func TestRampSellQuote_TopUpThresholdCoversTheApprove(t *testing.T) {
	r1, r2 := topUpFixture(t)
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) {
		return new(big.Int).Set(r1.GasCostWei), true // the swap's gas exactly
	})
	in := sellIn
	in.FromToken = bscUSDT
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.GasBps != 24 || res.FeeBps != 6 {
		t.Fatalf("gas/fee = %d/%d, want 24/6 — the swap's gas alone does not fund the approve", res.GasBps, res.FeeBps)
	}
}

// A balance that comfortably covers gas plus the approve needs no top-up, and
// budgeting one anyway would quietly shrink her fee.
func TestRampSellQuote_AWellFundedWalletIsNotToppedUp(t *testing.T) {
	r1, r2 := topUpFixture(t)
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) {
		return big.NewInt(1_000_000_000), true
	})
	in := sellIn
	in.FromToken = bscUSDT
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// Only the $0.02 Base relay: 2 bps on $100. fee = 50 − 20 − 2 = 28.
	if res.GasBps != 2 || res.FeeBps != 28 {
		t.Fatalf("gas/fee = %d/%d, want 2/28", res.GasBps, res.FeeBps)
	}
}

// A route that did not quote its gas in wei cannot be compared against a
// balance at all. That is an unknown, the same unknown as a balance we could
// not read — and an unknown is budgeted for, never silently resolved by the
// comparison it broke.
func TestRampSellQuote_AnUnreadableRouteGasIsTreatedAsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name    string
		balance func() (*big.Int, bool)
	}{
		{"unknown balance", func() (*big.Int, bool) { return nil, false }},
		{"ample balance, unpriced gas", func() (*big.Int, bool) { return big.NewInt(1_000_000_000), true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r1, r2 := topUpFixture(t)
			r1.GasCostWei = nil // LiFi did not answer in wei
			q, _ := newSellQuoterWithFakes(t, r1, r2)
			q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return tc.balance() })
			in := sellIn
			in.FromToken = bscUSDT
			res, err := q.Quote(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if res.GasBps != 24 || res.FeeBps != 6 {
				t.Fatalf("gas/fee = %d/%d, want 24/6 — an unknown must budget the top-up", res.GasBps, res.FeeBps)
			}
		})
	}
}

// A stable pair should not move on price, so it is quoted at LiFi's tight
// default — the same call ConvertedVenues makes for a stable destination.
// Quoting BSC USDT -> Base USDC at 50 bps of tolerance doubles the worst case
// of a route admitted at 50 bps of total cost, and ToAmount hides it.
func TestRampSellQuote_QuotesAStablePairAtTheTightSlippage(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	in := sellIn
	in.FromToken = bscUSDT
	if _, err := q.Quote(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if got := lifi.lastRequest().Slippage; got != 0.001 {
		t.Fatalf("slippage = %v, want 0.001 for a stable pair", got)
	}
}

// The calldata is what she signs. A route that names anyone but her, or any
// destination but USDC on Base, is never handed back: she would sign her
// tokens away into a swap §5.3's binding check then refuses to offramp.
func TestRampSellQuote_RefusesARouteThatDoesNotDeliverToHer(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
	}{
		{"pays someone else", testBridgeCalldata(t, sellOtherAddress, rampSettlementChainID, false)},
		{"delivers on another chain", testBridgeCalldata(t, sellTestAddress, 56, false)},
		{"hands the funds to a destination call", testBridgeCalldata(t, sellTestAddress, rampSettlementChainID, true)},
		{"is not decodable calldata", "0xdeadbeef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r1, r2 := sellRoute(t, 9.98), sellRoute(t, 9.97)
			r2.Data = tc.data
			q, _ := newSellQuoterWithFakes(t, r1, r2)
			if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellUnavailable) {
				t.Fatalf("err = %v; an unsignable route must never be offered", err)
			}
		})
	}
}

func TestRampSellQuote_EstimatesAtTheConvertedFee(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	res, err := q.Quote(context.Background(), sellIn)
	if err != nil {
		t.Fatal(err)
	}
	// Her USDC is what the FEE-CARRYING route lands: 9.97, not route 1's 9.98.
	// Provider sell rate 1500 at F − B = 0.5% -> 1492.50; 9.97 × 1492.50 =
	// ₦14,880.225, rounded to the nearest kobo away from zero -> 14880.23.
	if res.USDCAmount != "9.97" {
		t.Fatalf("usdc = %s, want the fee-carrying route's landing", res.USDCAmount)
	}
	if res.Rate != "1492.50" || res.FiatEstimate != "14880.23" {
		t.Fatalf("rate = %s estimate = %s", res.Rate, res.FiatEstimate)
	}
}

// A source address that is not an address on its own chain never reaches
// LiFi: the quote is priced FROM it, and a malformed one is a client bug that
// should read as a bad request rather than an outage.
func TestRampSellQuote_RefusesASourceAddressOfTheWrongFamily(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	in := sellIn
	in.FromAddress = "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin" // Solana, on an EVM chain
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellBadSourceAddress) {
		t.Fatalf("err = %v", err)
	}
	if lifi.quoteCalls() != 0 {
		t.Fatalf("quoted %d times for an unusable source address", lifi.quoteCalls())
	}
}
