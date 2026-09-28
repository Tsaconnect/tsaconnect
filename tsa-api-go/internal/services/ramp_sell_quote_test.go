package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
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
	q := NewRampSellQuoter(signedBodyLiFi{lifi}, quotes, testChainRegistry(), 50, "0.5", 0.02)
	q.SetRouterAllowlist(map[int64][]common.Address{56: {common.HexToAddress(sellTestDiamond)}})
	return q, lifi
}

// sellTestDiamond is LiFi's diamond, the one router the fixtures allowlist.
const sellTestDiamond = "0x1231DEB6f5749EF6cE6943a275A1D3E7486F4EaE"

// signedBodyLiFi answers with each route's body (Raw) rebuilt from the route's
// own fields — the body a real LiFi answer carries — so a fixture that edits
// Data or To after it is built is judged on that edit, not refused for a body
// that no longer matches it. Tests of a body that disagrees with its route
// build their quoter without it.
type signedBodyLiFi struct{ *fakeLiFiQuoter }

func (f signedBodyLiFi) Quote(ctx context.Context, req LiFiQuoteRequest) (*LiFiRoute, error) {
	r, err := f.fakeLiFiQuoter.Quote(ctx, req)
	if r == nil {
		return r, err
	}
	return withSignedBodyTo(r, req.FromChainID, req.ToChainID, req.ToToken), err
}

// withSignedBody is withSignedBodyTo for an ordinary sale: USDC on Base.
func withSignedBody(r *LiFiRoute, chainID int64) *LiFiRoute {
	return withSignedBodyTo(r, chainID, rampSettlementChainID,
		testChainRegistry().TokenAddressForChainID(rampSettlementChainID, rampSettlementToken))
}

// withSignedBodyTo is r with Raw carrying r's own signable fields on chainID,
// and — as a real LiFi answer does — the destination it was asked for in
// action.toToken / action.toChainId, keeping whatever else Raw already held (the
// fixtures' "id", or an action.toToken a test plants to disagree).
func withSignedBodyTo(r *LiFiRoute, chainID, toChainID int64, toToken string) *LiFiRoute {
	body := map[string]any{}
	_ = json.Unmarshal(r.Raw, &body)
	body["transactionRequest"] = map[string]any{"to": r.To, "data": r.Data, "chainId": chainID}
	if r.ApprovalAddress != "" {
		body["estimate"] = map[string]any{"approvalAddress": r.ApprovalAddress}
	}
	action, _ := body["action"].(map[string]any)
	if action == nil {
		action = map[string]any{}
	}
	if r.ApprovalToken != "" {
		action["fromToken"] = map[string]any{"address": r.ApprovalToken}
	}
	if _, set := action["toToken"]; !set {
		action["toToken"] = map[string]any{"address": toToken}
	}
	if _, set := action["toChainId"]; !set {
		action["toChainId"] = toChainID
	}
	body["action"] = action
	raw, err := json.Marshal(body)
	if err != nil {
		panic(err)
	}
	cp := *r
	cp.Raw = raw
	return &cp
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
		To:   sellTestDiamond,
		Data: sellCalldata(t, sellTestAddress),
	}
}

// quoteBodyID is the fixture id a quote body carries — which route it is.
func quoteBodyID(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var body struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("quote body %s: %v", raw, err)
	}
	return body.ID
}

// sellRawID is the body the quote hands the app for sellRoute(toUSD).
func sellRawID(t *testing.T, toUSD float64) string {
	t.Helper()
	return string(withSignedBody(sellRoute(t, toUSD), sellIn.FromChainID).Raw)
}

// bscUSDT is the registry's BSC USDT: an EVM token that is NOT the chain's
// gas coin (so a sell from it may need a top-up) and IS a configured stable
// (so the pair it makes with Base USDC is a stable one).
const bscUSDT = "0xB5c05D7000000000000000000000000000000001"

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

// A pay intent needs an exact amount of USDC to land, not an exact amount to
// leave her wallet: both LiFi asks (the fee-free cost probe and the fee-carrying
// executable one) are exact-output, and neither carries a fromAmount.
func TestRampSellQuote_ExactOutputAsksLiFiForTheToAmountOnBothPasses(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	in := sellIn
	in.FromAmount = ""
	in.ToAmount = "33900000"
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(lifi.all) != 2 {
		t.Fatalf("quotes asked = %d, want the cost probe and the fee-carrying ask", len(lifi.all))
	}
	for i, req := range lifi.all {
		if req.ToAmount != "33900000" || req.FromAmount != "" {
			t.Fatalf("ask %d = %+v, want toAmount only", i, req)
		}
	}
	if res.FeeBps != 10 || res.GasBps != 20 {
		t.Fatalf("budget split = %d/%d — exact output must not change how B is spent", res.FeeBps, res.GasBps)
	}
}

// A sale dearer than B is still offered, at a higher fee: our LiFi fee
// recovers the gas we pay for her (g) plus 10 bps headroom, so Paycrest's
// F − B stays ours. l = 50, g = 20 -> fee 30, all-in 50 + 50 + 30 = 130.
func TestRampSellQuote_RaisesTheFeeForADearerRoute(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.95), sellRoute(t, 9.92))
	res, err := q.Quote(context.Background(), sellIn)
	if err != nil {
		t.Fatal(err)
	}
	if res.CostBps != 50 || res.GasBps != 20 || res.FeeBps != 30 {
		t.Fatalf("cost/gas/fee = %d/%d/%d, want 50/20/30", res.CostBps, res.GasBps, res.FeeBps)
	}
	if last := lifi.lastRequest(); last.FeeBps != 30 {
		t.Fatalf("fee-carrying ask = %+v, want a 30 bps fee", last)
	}
	if want := sellRawID(t, 9.92); string(res.LiFiQuote) != want {
		t.Fatalf("quote = %s, want the fee-carrying route %s", res.LiFiQuote, want)
	}
}

// Our fee is sized on the first route's gas; a fee-carrying route that has
// us pay more gas than the fee recovers would lose money, so it is refused.
// r1: l = 50, g = 11 (top-up) + 20 (relay) = 31 -> fee 41. r2's gas makes
// g = 44 + 20 = 64 > 41.
func TestRampSellQuote_ARaisedSaleNeverPaysMoreGasThanItsFee(t *testing.T) {
	r1, r2 := sellRoute(t, 9.95), sellRoute(t, 9.91)
	r1.GasCostUSD, r1.GasCostWei, r1.GasLimit = 0.005, big.NewInt(1_000), 1_000_000
	r2.GasCostUSD, r2.GasCostWei, r2.GasLimit = 0.02, big.NewInt(1_000), 1_000_000
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(0), true })
	in := sellIn
	in.FromToken = bscUSDT
	if res, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("res = %+v err = %v; a 64 bps gas bill against a 41 bps fee must be refused", res, err)
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

// Her all-in fee — Paycrest's F − B, LiFi's cost and our fee — is capped per
// source chain. l = 31, g = 20 -> fee 30: 50 + 31 + 30 = 111 bps, one over a
// 110 bps cap. At 111 the fee-carrying route may take 61 bps (l2 = 61).
func TestRampSellQuote_RefusesARouteOneBpsOverItsChainsCap(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.969), sellRoute(t, 9.969), sellRoute(t, 9.939))
	q.quotes.SetConvertedFeeCaps(map[string]int{"bsc": 110}, 200)
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v", err)
	}
	q.quotes.SetConvertedFeeCaps(map[string]int{"bsc": 111}, 200)
	if _, err := q.Quote(context.Background(), sellIn); err != nil {
		t.Fatalf("a 111 bps sale under a 111 bps cap: %v", err)
	}
}

func TestRampSellQuote_RefusesARouteOverTheDefaultCap(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.80)) // l = 200, g = 20 -> 270 all-in
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v", err)
	}
}

// A TSA Pay delivery is priced on the fee it was given and never raised.
func TestRampSellQuote_ADeliveryBudgetIsNeverRaised(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.95)) // 70 bps
	in := sellIn
	in.BudgetBps = 50
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
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
	// Raised, she would pay 50 + l 20 + a fee of g 128 + 10 = 208 bps.
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v; a sponsored top-up must count against her fee and its cap", err)
	}
}

// topUpFixture is the $100 pair the top-up tests price: r1 quotes the gas,
// r2 is the fee-carrying answer. l = 20, l2 = 26.
func topUpFixture(t *testing.T) (*LiFiRoute, *LiFiRoute) {
	t.Helper()
	r1 := &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), FromAmountUSD: 100, ToAmountUSD: 99.8,
		GasCostUSD: 0.10, GasCostWei: big.NewInt(1_000), GasLimit: 1_000_000,
		Raw: json.RawMessage(`{"id":"r1"}`), To: sellTestDiamond, Data: sellCalldata(t, sellTestAddress)}
	// r2 runs on the same tool at the same gas: g is re-priced from r2, so a
	// fixture whose r2 quoted no gas would price a different sale than r1.
	r2 := &LiFiRoute{ToAmount: big.NewInt(99_740000), ToAmountMin: big.NewInt(99_640000), FromAmountUSD: 100, ToAmountUSD: 99.74,
		GasCostUSD: 0.10, GasCostWei: big.NewInt(1_000), GasLimit: 1_000_000,
		Raw: json.RawMessage(`{"id":"r2"}`), To: sellTestDiamond, Data: sellCalldata(t, sellTestAddress)} // l2 = 26: 26 + 24 = 50 fits
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
	if id := quoteBodyID(t, res.LiFiQuote); id != "r2" {
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
			r1.GasCostWei, r2.GasCostWei = nil, nil // LiFi did not answer in wei
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
			// Budgeted, with no wei to send: the send prices it live.
			if !res.GasTopUp || res.TopUpWei != "" {
				t.Fatalf("gasTopUp = %v topUpWei = %q, want a budgeted top-up with no quoted wei", res.GasTopUp, res.TopUpWei)
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

// ─── Whole-branch fix: the top-up admitted is the top-up sent ──────────────
//
// g used to be priced once, off the FIRST (fee-free) route, while the sale
// stored — and later sent — the top-up of the SECOND, fee-carrying route. The
// two are different LiFi answers and may be different tools. Every test below
// is a second route whose gas differs from the first's, the shape no earlier
// fixture had.

// The fee-carrying ask lands on a tool that costs twice the gas. Admitted on
// the first route's g (24 bps) it fits exactly — l2 26 + 24 = 50 — while the
// top-up it would actually send is priced at 46 bps: 72 against a budget of 50.
func TestRampSellQuote_AdmitsTheFeeRouteOnItsOwnGas(t *testing.T) {
	r1, r2 := topUpFixture(t)
	r2.GasCostUSD = 0.20 // $0.20 × 1.08 × 2 + $0.02 relay = 45.2 -> 46 bps
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(0), true })
	in := sellIn
	in.FromToken = bscUSDT
	if res, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("res = %+v err = %v; the route she signs must fit B on its own gas", res, err)
	}
}

// She holds enough for the first route's gas (1,080 wei with the approve) but
// not the second's (5,400). Judged off the first route she is "funded": g is
// just the relay, the whole rest of B becomes our fee — and the sale then
// needs a top-up nobody budgeted. Judged off the route she signs, it does not
// fit and is refused.
func TestRampSellQuote_AFeeRouteThatNewlyNeedsATopUpIsJudgedOnItsOwnGas(t *testing.T) {
	r1, r2 := topUpFixture(t)
	r2.GasCostWei = big.NewInt(5_000)
	r2.ToAmount, r2.ToAmountUSD = big.NewInt(99_520000), 99.52 // l2 = 48 = l + the 28 bps fee
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(2_000), true })
	in := sellIn
	in.FromToken = bscUSDT
	if res, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("res = %+v err = %v; r2 needs a top-up (48 + 24 > 50), which r1's g never counted", res, err)
	}
}

// The quote carries the top-up it priced, from the route she signs: r2 quotes
// its gas at 2,000 wei for 1M gas, so the top-up is 2,000 × 1.08M/1M × 2.
func TestRampSellQuote_CarriesTheTopUpItPricedFromTheSignedRoute(t *testing.T) {
	r1, r2 := topUpFixture(t)
	r2.GasCostWei = big.NewInt(2_000)
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(0), true })
	in := sellIn
	in.FromToken = bscUSDT
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !res.GasTopUp || res.TopUpWei != "4320" || res.GasBps != 24 {
		t.Fatalf("gasTopUp = %v topUpWei = %q gas = %d; want r2's top-up, 4320 wei, in 24 bps", res.GasTopUp, res.TopUpWei, res.GasBps)
	}
	if want := quotedTopUpWei(r2); res.TopUpWei != want {
		t.Fatalf("topUpWei = %s, want r2's own %s", res.TopUpWei, want)
	}
}

// A funded wallet was priced no top-up, so the quote says so and carries no
// wei — even though the route quoted its gas in wei and one COULD be derived.
func TestRampSellQuote_AFundedWalletCarriesNoTopUp(t *testing.T) {
	r1, r2 := topUpFixture(t)
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	q.SetNativeBalanceReader(func(context.Context, int64, string) (*big.Int, bool) { return big.NewInt(1_000_000_000), true })
	in := sellIn
	in.FromToken = bscUSDT
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.GasTopUp || res.TopUpWei != "" {
		t.Fatalf("gasTopUp = %v topUpWei = %q; nothing was budgeted, so nothing may be sent", res.GasTopUp, res.TopUpWei)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), `"gasTopUp":false`) {
		t.Fatalf("json = %s; the app reads gasTopUp", raw)
	}
}

// Bitcoin, Sui and Solana are named for what they are and refused as
// unsupported — never as "not a valid address", which a real address on them
// is not. Solana because nothing can read what she would sign there (see
// ErrSellSourceUnsupported): refused before LiFi is even asked.
func TestRampSellQuote_RefusesNonEVMSourcesAsUnsupported(t *testing.T) {
	for name, tc := range map[string]struct {
		chain int64
		addr  string
	}{
		"bitcoin": {bitcoinLiFiChainID, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"},
		"sui":     {suiLiFiChainID, "0x" + strings.Repeat("ab", 32)},
		"solana":  {solanaLiFiChainID, "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"},
	} {
		t.Run(name, func(t *testing.T) {
			q, lifi := newSellQuoterWithFakes(t, sellRoute(t, 9.98))
			in := sellIn
			in.FromChainID, in.FromAddress = tc.chain, tc.addr
			_, err := q.Quote(context.Background(), in)
			if !errors.Is(err, ErrSellSourceUnsupported) || errors.Is(err, ErrSellBadSourceAddress) {
				t.Fatalf("err = %v, want ErrSellSourceUnsupported", err)
			}
			if lifi.quoteCalls() != 0 {
				t.Fatalf("asked LiFi %d times for a source we cannot sell from", lifi.quoteCalls())
			}
		})
	}
}

// ─── Five-axis review: where her signature goes ────────────────────────────
//
// The calldata check decodes a payload "as if" it were a diamond call; it
// never confirmed the call goes to the diamond. The app signs a transaction to
// transactionRequest.to and approves estimate.approvalAddress for an unlimited
// amount, so both must be allowlisted routers — the treasury's own check,
// reused.

func TestRampSellQuote_RefusesARouteOffTheRouterAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name  string
		route func(*LiFiRoute)
	}{
		{"sends her transaction elsewhere", func(r *LiFiRoute) { r.To = sellOtherAddress }},
		{"has her approve someone else", func(r *LiFiRoute) {
			r.ApprovalAddress, r.ApprovalToken = sellOtherAddress, evmNativeAddress
		}},
		{"has her approve another token", func(r *LiFiRoute) {
			r.ApprovalAddress, r.ApprovalToken = sellTestDiamond, bscUSDT // she is selling BNB
		}},
		{"names no target at all", func(r *LiFiRoute) { r.To = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r1, r2 := sellRoute(t, 9.98), sellRoute(t, 9.97)
			tc.route(r2)
			q, _ := newSellQuoterWithFakes(t, r1, r2)
			if res, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellUnavailable) {
				t.Fatalf("res = %+v err = %v; a route off the allowlist must never be offered", res, err)
			}
		})
	}
}

// The control for the cases above: the same route, to the diamond and
// approving the diamond for the token she sells, is offered.
func TestRampSellQuote_OffersARouteToTheAllowlistedDiamond(t *testing.T) {
	r1, r2 := sellRoute(t, 9.98), sellRoute(t, 9.97)
	r2.ApprovalAddress, r2.ApprovalToken = sellTestDiamond, "0x0000000000000000000000000000000000000000" // LiFi's native spelling
	q, _ := newSellQuoterWithFakes(t, r1, r2)
	res, err := q.Quote(context.Background(), sellIn)
	if err != nil {
		t.Fatalf("err = %v; the diamond is the one router a sell may target", err)
	}
	var body struct {
		TransactionRequest struct {
			To string `json:"to"`
		} `json:"transactionRequest"`
	}
	if err := json.Unmarshal(res.LiFiQuote, &body); err != nil || body.TransactionRequest.To != sellTestDiamond {
		t.Fatalf("handed over %s (%v)", res.LiFiQuote, err)
	}
}

// Fail closed: a chain with no allowlist has nothing to check `to` against.
func TestRampSellQuote_RefusesEverySellWhenNoAllowlistIsWired(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	q.SetRouterAllowlist(nil)
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v; an unwired allowlist must refuse, not allow", err)
	}
}

func TestRampSellQuote_RefusesARouteForAnotherChain(t *testing.T) {
	r2 := sellRoute(t, 9.97)
	r2 = withSignedBody(r2, 137) // she sells on BSC; the body would have her sign on Polygon
	lifi := &fakeLiFiQuoter{routes: []*LiFiRoute{withSignedBody(sellRoute(t, 9.98), 56), r2}}
	q, _ := newSellQuoterWithFakes(t)
	q.lifi = lifi
	if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

// encoding/json matches keys case-insensitively and keeps the LAST match;
// JSON.parse on her phone reads each key exactly. A body carrying both
// spellings reads one way here and another way when she signs it — so every
// signable field is read by its exact name, and a body that disagrees with
// the route this server parsed from it is refused. Parsed through the real
// client, so each premise is the production parser's.
func TestRampSellQuote_RefusesABodyThatReadsDifferentlyOnHerPhone(t *testing.T) {
	// Her real route, stamped with transactionId 0x01…: the id the sale binds to.
	bound := testBridgeCalldataWithID(t, sellTestAddress, 1)
	for _, tc := range []struct {
		name  string
		txReq string
		// premise: what the server's own parse must read for the case to matter
		premise func(*LiFiRoute) bool
	}{
		{
			// Caught by the exact-key read alone: the allowlist sees "to".
			name:    "to: she signs to someone else",
			txReq:   fmt.Sprintf(`"to":%q,"To":%q,"data":%q`, sellOtherAddress, sellTestDiamond, bound),
			premise: func(r *LiFiRoute) bool { return strings.EqualFold(r.To, sellTestDiamond) },
		},
		{
			// Both calldatas pay her, so only the consistency check can see
			// it: the sale would bind to transactionId 0x02… while she signs
			// 0x01…, and her sale could never be finished.
			name:    "data: she signs a swap the sale is not bound to",
			txReq:   fmt.Sprintf(`"to":%q,"data":%q,"Data":%q`, sellTestDiamond, bound, testBridgeCalldataWithID(t, sellTestAddress, 2)),
			premise: func(r *LiFiRoute) bool { return strings.HasSuffix(r.TransactionID, "02") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"estimate":{"toAmount":"9970000","toAmountMin":"9960000"},"transactionRequest":{%s,"chainId":56}}`, tc.txReq)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer srv.Close()
			route, err := NewLiFiQuoteClient(srv.URL, "", "tsa").Quote(context.Background(), LiFiQuoteRequest{})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.premise(route) {
				t.Fatalf("premise: the server parsed to=%s transactionId=%s", route.To, route.TransactionID)
			}
			q, _ := newSellQuoterWithFakes(t)
			if err := q.checkSignable(route, sellIn, "0xBaseUSDC00000000000000000000000000001"); !errors.Is(err, ErrTreasuryInvalidRoute) {
				t.Fatalf("err = %v; a body that reads two ways must be refused", err)
			}
		})
	}
}

// testBridgeCalldataWithID is testBridgeCalldata stamped with a transactionId
// whose last byte is id, so two otherwise identical routes can be told apart.
func testBridgeCalldataWithID(t *testing.T, receiver string, id byte) string {
	t.Helper()
	var txID [32]byte
	txID[31] = id
	packed, err := abi.Arguments{{Type: lifiBridgeDataType}}.Pack(lifiBridgeData{
		TransactionId: txID, Bridge: "across", Integrator: "tsa",
		Receiver: common.HexToAddress(receiver), MinAmount: big.NewInt(1),
		DestinationChainId: big.NewInt(rampSettlementChainID),
	})
	if err != nil {
		t.Fatal(err)
	}
	return hexutil.Encode(append([]byte{0x17, 0x94, 0x95, 0x8f}, packed...))
}

// A real LiFi answer (BSC USDT -> Base USDC, captured 2026-09-23 from
// li.quest) passes every check: diamond target, diamond spender, the token
// she sells, her wallet on Base. Without this the checks above could be
// refusing everything.
func TestRampSellQuote_ARealLiFiRoutePassesTheSignableCheck(t *testing.T) {
	body, err := os.ReadFile("testdata/lifi_quote/bsc_usdt_to_base_usdc.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	route, err := NewLiFiQuoteClient(srv.URL, "", "tsa").Quote(context.Background(), LiFiQuoteRequest{})
	if err != nil {
		t.Fatal(err)
	}
	q, _ := newSellQuoterWithFakes(t)
	in := sellIn
	in.FromToken = "0x55d398326f99059fF775485246999027B3197955" // BSC USDT
	if err := q.checkSignable(route, in, "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"); err != nil {
		t.Fatalf("a genuine LiFi route was refused: %v", err)
	}
	q.SetRouterAllowlist(map[int64][]common.Address{56: {common.HexToAddress(sellOtherAddress)}})
	if err := q.checkSignable(route, in, "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"); !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("err = %v; the same route must be refused once the diamond is off the list", err)
	}
}

// ── A target that is not "USDC on Base in her wallet": TSA Pay's crypto route ──

const (
	// payMerchantWallet is a merchant's settlement wallet; payMerchantToken the
	// token it wants, on Optimism — neither is her wallet nor Base USDC.
	payMerchantWallet  = "0x00000000000000000000000000000000000b0b0b"
	payMerchantToken   = "0x94b008aA00579c1307B0EF2c499aD98a8ce58e58"
	payMerchantChainID = int64(10)
)

// payRoute is a sell route whose calldata delivers to receiver on chainID.
func payRoute(t *testing.T, toUSD float64, receiver string, chainID int64) *LiFiRoute {
	t.Helper()
	r := sellRoute(t, toUSD)
	r.Data = testBridgeCalldata(t, receiver, chainID, false)
	return r
}

func merchantTargetIn() SellQuoteInput {
	in := sellIn
	in.FromAmount = ""
	in.ToChainID, in.ToToken, in.ToAddress = payMerchantChainID, payMerchantToken, payMerchantWallet
	in.ToAmount, in.BudgetBps = "50500000", 100
	return in
}

func TestRampSellQuote_ADeliveryTargetReachesLiFiAndChargesTheBudgetItWasGiven(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t,
		payRoute(t, 9.98, payMerchantWallet, payMerchantChainID), payRoute(t, 9.97, payMerchantWallet, payMerchantChainID))
	res, err := q.Quote(context.Background(), merchantTargetIn())
	if err != nil {
		t.Fatal(err)
	}
	// l = 20 bps; there is no Paycrest leg, so no Base relay is sponsored and
	// g = 0 (a native sell needs no top-up either). Fee = 100 − 20 − 0.
	if res.CostBps != 20 || res.GasBps != 0 || res.FeeBps != 80 {
		t.Fatalf("cost/gas/fee = %d/%d/%d, want 20/0/80", res.CostBps, res.GasBps, res.FeeBps)
	}
	if len(lifi.all) != 2 {
		t.Fatalf("asks = %d", len(lifi.all))
	}
	for i, req := range lifi.all {
		if req.ToChainID != payMerchantChainID || req.ToToken != payMerchantToken || req.ToAddress != payMerchantWallet ||
			req.ToAmount != "50500000" || req.FromAmount != "" {
			t.Fatalf("ask %d = %+v — the merchant is the receiver, never her", i, req)
		}
	}
	if lifi.all[0].FeeBps != 0 || lifi.all[1].FeeBps != 80 {
		t.Fatalf("fees asked = %d then %d, want 0 then 80", lifi.all[0].FeeBps, lifi.all[1].FeeBps)
	}
	// There is no naira and no USDC on Base to speak of.
	if res.FiatEstimate != "" || res.Rate != "" || res.USDCAmount != "" {
		t.Fatalf("a non-Base-USDC target must carry no naira/USDC figures: %+v", res)
	}
}

func TestRampSellQuote_ADeliveryTargetIsRefusedWhenRoundTripCostsMoreThanItsBudget(t *testing.T) {
	// l = 50 bps against BudgetBps 40 (the quoter's own B of 50 would admit it).
	q, _ := newSellQuoterWithFakes(t, payRoute(t, 9.95, payMerchantWallet, payMerchantChainID))
	in := merchantTargetIn()
	in.BudgetBps = 40
	if _, err := q.Quote(context.Background(), in); !errors.Is(err, ErrSellRouteTooCostly) {
		t.Fatalf("err = %v, want ErrSellRouteTooCostly", err)
	}
	// Exactly the budget is admitted, with no fee left and so no second ask.
	q, lifi := newSellQuoterWithFakes(t, payRoute(t, 9.95, payMerchantWallet, payMerchantChainID))
	in.BudgetBps = 50
	res, err := q.Quote(context.Background(), in)
	if err != nil || res.FeeBps != 0 || len(lifi.all) != 1 {
		t.Fatalf("err %v res %+v asks %d", err, res, len(lifi.all))
	}
}

// She signs the route with her own key. One that pays HER (or anyone but the
// merchant), or delivers on another chain, would let a payment be reported
// delivered that never reached the merchant — it is never offered.
func TestRampSellQuote_ADeliveryTargetRefusesARouteThatPaysAnyoneElse(t *testing.T) {
	for name, data := range map[string]string{
		"pays her instead":                      testBridgeCalldata(t, sellTestAddress, payMerchantChainID, false),
		"pays a stranger":                       testBridgeCalldata(t, sellOtherAddress, payMerchantChainID, false),
		"delivers to Base, not the target":      testBridgeCalldata(t, payMerchantWallet, rampSettlementChainID, false),
		"hands the funds to a destination call": testBridgeCalldata(t, payMerchantWallet, payMerchantChainID, true),
	} {
		t.Run(name, func(t *testing.T) {
			r1, r2 := payRoute(t, 9.98, payMerchantWallet, payMerchantChainID), payRoute(t, 9.97, payMerchantWallet, payMerchantChainID)
			r2.Data = data
			q, _ := newSellQuoterWithFakes(t, r1, r2)
			if _, err := q.Quote(context.Background(), merchantTargetIn()); !errors.Is(err, ErrSellUnavailable) {
				t.Fatalf("err = %v; a route that does not pay the merchant must never be offered", err)
			}
		})
	}
}

func TestRampSellQuote_ADeliveryTargetMustBeAnAddressOnItsOwnChain(t *testing.T) {
	q, lifi := newSellQuoterWithFakes(t, payRoute(t, 9.98, payMerchantWallet, payMerchantChainID))
	for name, mutate := range map[string]func(*SellQuoteInput){
		"not an address":   func(in *SellQuoteInput) { in.ToAddress = "merchant" },
		"a Solana target":  func(in *SellQuoteInput) { in.ToChainID = solanaLiFiChainID },
		"a Bitcoin target": func(in *SellQuoteInput) { in.ToChainID = bitcoinLiFiChainID },
	} {
		t.Run(name, func(t *testing.T) {
			in := merchantTargetIn()
			mutate(&in)
			if _, err := q.Quote(context.Background(), in); err == nil {
				t.Fatal("quoted to a target that cannot be delivered to")
			}
			if lifi.calls != 0 {
				t.Fatalf("LiFi was asked (%d calls) about an undeliverable target", lifi.calls)
			}
		})
	}
}

// Spelling out the default target is the default: Base USDC to her own wallet
// is still an offramp sale, priced in naira.
func TestRampSellQuote_NamingBaseUSDCToHerWalletIsTheOrdinarySale(t *testing.T) {
	q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.98), sellRoute(t, 9.97))
	in := sellIn
	in.ToChainID, in.ToToken, in.ToAddress = rampSettlementChainID, "0xBaseUSDC00000000000000000000000000001", sellTestAddress
	res, err := q.Quote(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.GasBps != 20 || res.FeeBps != 10 || res.FiatEstimate != "14880.23" || res.USDCAmount != "9.97" {
		t.Fatalf("res = %+v — must equal the ordinary sale (gas 20, fee 10, ₦14,880.23)", res)
	}
}

// A delivery is never an offramp — even to the payer's own wallet, in Base
// USDC. (Naming the default target on an ordinary sale stays that sale: see
// TestRampSellQuote_NamingBaseUSDCToHerWalletIsTheOrdinarySale.)
func TestRampSellQuote_AnExplicitDeliveryTargetIsNeverAnOfframp(t *testing.T) {
	q := &RampSellQuoter{budgetBps: 50}
	own := SellQuoteInput{WalletAddress: sellTestAddress}
	if !q.targetFor(own, sellUSDC).offramp {
		t.Fatal("an ordinary sale is an offramp")
	}
	self := SellQuoteInput{WalletAddress: sellTestAddress, ToChainID: rampSettlementChainID, ToToken: sellUSDC, ToAddress: sellTestAddress, Delivery: true}
	if q.targetFor(self, sellUSDC).offramp {
		t.Fatal("a delivery to the payer's own wallet was priced as an offramp: it would get Base relay gas and a naira estimate")
	}
}

// LiFi's calldata names the receiver and chain but not the asset: a route that
// pays her wallet on Base in some OTHER token (a bridged USDC.e) must be
// refused before she signs it, on what the quote itself says it delivers.
func TestRampSellQuote_RefusesARouteThatDeliversAnotherToken(t *testing.T) {
	for name, action := range map[string]string{
		"another token":         `{"toToken":{"address":"0xd9aAEc86B65D86f6A7B5B1b0c42FFA531710b6CA"},"toChainId":8453}`,
		"another chain":         `{"toChainId":42161}`,
		"no destination at all": `{"toToken":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			bad := sellRoute(t, 9.97)
			bad.Raw = json.RawMessage(`{"id":"bad","action":` + action + `}`)
			q, _ := newSellQuoterWithFakes(t, sellRoute(t, 9.98), bad)
			if _, err := q.Quote(context.Background(), sellIn); !errors.Is(err, ErrSellUnavailable) {
				t.Fatalf("err = %v: she would sign a route that delivers the wrong asset", err)
			}
		})
	}
}
