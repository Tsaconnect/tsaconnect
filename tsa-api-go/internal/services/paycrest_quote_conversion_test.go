package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// paycrest_quote_conversion_test.go — Task 8: converted venues in the quote.
//
// ConvertedVenues offers a route Paycrest cannot fill directly by settling
// through a token/chain it CAN fill, then converting via LiFi. These tests
// pin the three product decisions that make the offer safe to make:
//
//   - direct-first: never convert what Paycrest can already deliver;
//   - the promise is priced off the route's guaranteed floor (ToAmountMin),
//     never its mid-price, and a route that cannot clear the cap is not
//     offered at all;
//   - the per-chain minimum order size is enforced before spending a LiFi
//     call on an amount that could never be offered anyway.
//
// It also pins the Task 7 controller ruling: PromisedAmount is stated in the
// REQUESTED token's base units, not the settlement leg's. The settlement leg
// and the requested token are not always the same decimals (BSC stables are
// 18dp; base/polygon/optimism are 6dp), so the promise is computed by
// rescaling the settlement amount into the REQUESTED token's decimals first
// (rescaleUnits — the same helper ConversionOrchestrator uses at execution
// time) and pricing the markup off that. Getting this backwards makes a
// cross-decimal order settle wrong by a factor of 10^12, so both decimal
// directions are exercised below, not just the same-decimals case the brief's
// own fixture happens to use (base USDC, 6dp -> optimism USDT, 6dp).

// testChainRegistry gives ConvertedVenues chain IDs and token addresses
// without any live RPC — the same bypass-the-constructor pattern
// conversion_orchestrator_test.go uses for *BlockchainService.
func testChainRegistry() *BlockchainService {
	return &BlockchainService{cfg: &config.Config{Networks: map[string]config.NetworkConfig{
		"mainnet": {
			Chains: map[string]config.ChainConfig{
				"base":     {ChainID: 8453},
				"bsc":      {ChainID: 56},
				"optimism": {ChainID: 10},
				"linea":    {ChainID: 59144},
			},
			TokenAddresses: map[string]string{
				"base:USDC":     "0xBaseUSDC00000000000000000000000000001",
				"bsc:USDC":      "0xBscUSDC000000000000000000000000000001",
				"bsc:USDT":      "0xBscUSDT000000000000000000000000000001",
				"optimism:USDT": "0xOptUSDT000000000000000000000000000001",
				"linea:USDC":    "0xLineaUSDC0000000000000000000000000001",
			},
		},
	}}}
}

// quoteServiceRig bundles a PaycrestQuoteService wired for converted-buy
// pricing with fakes for its two upstreams: the Paycrest book/rates
// (stubQuoter, already used by the direct-quote tests in this package) and
// LiFi (fakeLiFiQuoter, already used by the orchestrator tests).
type quoteServiceRig struct {
	svc  *PaycrestQuoteService
	pc   *stubQuoter
	lifi *fakeLiFiQuoter
}

// newQuoteServiceWithFakes wires a service that quotes 100 USDC on base for
// NGN 150,000 (rate 1,500) — the fixture every test below starts from unless
// it needs a different settlement leg.
func newQuoteServiceWithFakes(t *testing.T) *quoteServiceRig {
	t.Helper()
	pc := &stubQuoter{market: 1500, rates: map[string]*PaycrestRates{
		"base|USDC": buy("1500"),
	}}
	svc := NewPaycrestQuoteService(pc, []string{"USDC"}, []string{"base"})
	lifi := &fakeLiFiQuoter{}
	svc.SetLiFiQuoter(lifi)
	svc.SetTreasuryAddress("0xTreasuryAddress0000000000000000000001")
	svc.SetChainRegistry(testChainRegistry())
	return &quoteServiceRig{svc: svc, pc: pc, lifi: lifi}
}

func TestQuote_ConvertedVenueIsOfferedOnlyWhenTheRouteCoversThePromise(t *testing.T) {
	s := newQuoteServiceWithFakes(t) // Paycrest quotes 100 USDC on base
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), ETASeconds: 30, Tool: "across"}
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "optimism", Token: "USDT"}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v, err = %v", venues, err)
	}
	v := venues[0]
	if v.Kind != models.RouteKindConverted || v.MarkupBps != 50 || v.PromisedAmount != "99500000" {
		t.Fatalf("venue = %+v", v)
	}
	if v.ViaChain != "base" || v.ViaToken != "USDC" {
		t.Fatalf("venue must say what it converts from: %+v", v)
	}
	if v.RequestedChain != "optimism" || v.RequestedToken != "USDT" {
		t.Fatalf("venue must say what it converts to: %+v", v)
	}
}

func TestQuote_CostlyRouteIsNotOffered(t *testing.T) {
	s := newQuoteServiceWithFakes(t)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_000000), ToAmountMin: big.NewInt(98_900000)} // 110 bps
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "linea", Token: "USDC"}})
	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v; a route over the 1%% cap must not be offered", venues)
	}
}

func TestQuote_BelowMinimumOrderIsNotOffered(t *testing.T) {
	s := newQuoteServiceWithFakes(t)
	s.svc.SetConversionMinimums(map[string]float64{"linea": 25}, 1)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(20_000000), ToAmountMin: big.NewInt(19_950000)}
	venues, _ := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "30000", []RequestedAsset{{Chain: "linea", Token: "USDC"}})
	if len(venues) != 0 {
		t.Fatalf("$20 on linea is under its $25 minimum: %+v", venues)
	}
}

// TestQuote_DirectVenueIsNeverConverted pins Decision 3: a pair Paycrest can
// already fill directly must never be offered as a converted route, however
// good the LiFi price looks.
func TestQuote_DirectVenueIsNeverConverted(t *testing.T) {
	s := newQuoteServiceWithFakes(t)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(100_000000), ToAmountMin: big.NewInt(100_000000)}
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "base", Token: "USDC"}})
	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v, err = %v; base/USDC is a direct venue and must not be converted", venues, err)
	}
	if s.lifi.quoteCalls() != 0 {
		t.Errorf("spent a LiFi call on a pair Paycrest already fills directly")
	}
}

// TestQuote_ConvertedVenue_SixToEighteenDecimals is the destination-units
// ruling from Task 7: converting a 6dp settlement leg (base USDC) into an
// 18dp requested token (a BSC stablecoin) must promise an amount stated in
// the REQUESTED token's 18dp units, not a naive copy of the 6dp settlement
// amount less markup (which would be short by a factor of 10^12).
func TestQuote_ConvertedVenue_SixToEighteenDecimals(t *testing.T) {
	s := newQuoteServiceWithFakes(t) // settlement leg: 100 USDC on base, 6dp
	s.lifi.route = &LiFiRoute{
		ToAmount:    new(big.Int).Mul(big.NewInt(998), pow10(17)), // 99.8 * 1e18
		ToAmountMin: new(big.Int).Mul(big.NewInt(997), pow10(17)), // 99.7 * 1e18
		ETASeconds:  40, Tool: "across",
	}
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "bsc", Token: "USDT"}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v, err = %v", venues, err)
	}
	v := venues[0]
	wantPromise := new(big.Int).Mul(big.NewInt(995), pow10(17)).String() // 99.5 * 1e18
	if v.MarkupBps != 50 || v.PromisedAmount != wantPromise {
		t.Fatalf("venue = %+v, want promisedAmount %s (18dp, not the 6dp settlement figure)", v, wantPromise)
	}
	if v.ViaChain != "base" || v.ViaToken != "USDC" || v.RequestedChain != "bsc" || v.RequestedToken != "USDT" {
		t.Fatalf("venue must record both legs: %+v", v)
	}
}

// TestQuote_ConvertedVenue_EighteenToSixDecimals is the mirror image: an 18dp
// settlement leg (a BSC stablecoin) converting into a 6dp requested token
// (optimism USDT) must promise a 6dp amount, not an 18dp one scaled down by
// the wrong factor or left at 18dp entirely.
func TestQuote_ConvertedVenue_EighteenToSixDecimals(t *testing.T) {
	pc := &stubQuoter{market: 1500, rates: map[string]*PaycrestRates{
		"bnb-smart-chain|USDC": buy("1500"), // settlement leg: 100 USDC on bsc, 18dp
	}}
	svc := NewPaycrestQuoteService(pc, []string{"USDC"}, []string{"bsc"})
	lifi := &fakeLiFiQuoter{route: &LiFiRoute{
		ToAmount:    big.NewInt(99_800000),
		ToAmountMin: big.NewInt(99_700000),
		ETASeconds:  25, Tool: "across",
	}}
	svc.SetLiFiQuoter(lifi)
	svc.SetTreasuryAddress("0xTreasuryAddress0000000000000000000001")
	svc.SetChainRegistry(testChainRegistry())

	venues, err := svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "optimism", Token: "USDT"}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v, err = %v", venues, err)
	}
	v := venues[0]
	if v.MarkupBps != 50 || v.PromisedAmount != "99500000" {
		t.Fatalf("venue = %+v, want promisedAmount 99500000 (6dp, not the 18dp settlement figure)", v)
	}
	if v.ViaChain != "bsc" || v.ViaToken != "USDC" || v.RequestedChain != "optimism" || v.RequestedToken != "USDT" {
		t.Fatalf("venue must record both legs: %+v", v)
	}
}
