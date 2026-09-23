package services

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/chains/solana"
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

// fakeTreasuryLaneChecker stands in for *Treasury in ConvertedVenues' own
// gate: "does the treasury have an EXECUTABLE lane on this chain" — a
// narrower, deliberately different question from "does the chain registry
// know about it" (testChainRegistry answers that one). Every chain
// testChainRegistry configures is marked laned by default so the existing
// fixtures below are unaffected; TestQuote_RefusesAVenueOnAChainWithNoTreasuryLane
// builds its own checker that excludes one on purpose.
type fakeTreasuryLaneChecker struct {
	laned    map[string]bool
	unfunded map[string]bool // lanes that cannot pay for gas
	asked    []gasAsk
}

type gasAsk struct {
	chain string
	units uint64
}

func (f *fakeTreasuryLaneChecker) CanPayForGas(_ context.Context, chainKey string, units uint64) bool {
	f.asked = append(f.asked, gasAsk{chainKey, units})
	return f.laned[strings.ToLower(chainKey)] && !f.unfunded[strings.ToLower(chainKey)]
}

func (f *fakeTreasuryLaneChecker) Lane(chainKey string) *TreasuryLane {
	if f.laned[strings.ToLower(chainKey)] {
		return &TreasuryLane{}
	}
	return nil
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
	svc.SetTreasuryLanes(&fakeTreasuryLaneChecker{laned: map[string]bool{
		"base": true, "bsc": true, "optimism": true, "linea": true,
	}})
	return &quoteServiceRig{svc: svc, pc: pc, lifi: lifi}
}

// A route the treasury cannot pay gas for is not offered: the user would pay
// naira into an order the treasury can only strand.
func TestQuote_NoConvertedVenueWhenTheSettlementLaneCannotPayForGas(t *testing.T) {
	s := priceRig(t)
	s.svc.treasuryLanes.(*fakeTreasuryLaneChecker).unfunded = map[string]bool{"base": true}
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_200_000, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 10, false)}
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "optimism", Token: "USDT"}})
	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v err = %v; want none while base cannot pay for gas", venues, err)
	}
}

// The gas asked for is THIS route's own limit plus an approve, on the chain
// the bridge actually runs from (the settlement leg), not the destination.
func TestQuote_ConvertedVenueAsksForTheRoutesOwnGas(t *testing.T) {
	s := priceRig(t)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_200_000, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 10, false)}
	if _, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "optimism", Token: "USDT"}}); err != nil {
		t.Fatal(err)
	}
	asked := s.svc.treasuryLanes.(*fakeTreasuryLaneChecker).asked
	if len(asked) != 1 || asked[0].chain != "base" || asked[0].units != 1_200_000+treasuryERC20Gas {
		t.Fatalf("gas asks = %+v; want one for base covering the route plus an approve", asked)
	}
}

func TestQuote_ConvertedVenueIsOfferedOnlyWhenTheRouteCoversThePromise(t *testing.T) {
	s := priceRig(t) // Paycrest quotes 100 USDC on base
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, ETASeconds: 30, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 10, false)}
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "optimism", Token: "USDT"}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v, err = %v", venues, err)
	}
	v := venues[0]
	// The markup IS the round-trip budget, and a stablecoin promise is T less
	// the fixed stable headroom — neither is a function of the route's price.
	if v.Kind != models.RouteKindConverted || v.MarkupBps != 50 || v.PromisedAmount != "99700000" {
		t.Fatalf("venue = %+v", v)
	}
	if v.ViaChain != "base" || v.ViaToken != "USDC" {
		t.Fatalf("venue must say what it converts from: %+v", v)
	}
	if v.RequestedChain != "optimism" || v.RequestedToken != "USDT" {
		t.Fatalf("venue must say what it converts to: %+v", v)
	}
	// The settlement leg's Network must carry through: CreateBuy (Task 9)
	// submits this straight to Paycrest, and an empty value there means an
	// order created with no network at all.
	if v.Network == "" {
		t.Fatalf("venue must carry the settlement leg's network, got empty: %+v", v)
	}
}

func TestQuote_CostlyRouteIsNotOffered(t *testing.T) {
	s := priceRig(t)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_000000), ToAmountMin: big.NewInt(98_900000),
		FromAmountUSD: 100, ToAmountUSD: 99, GasLimit: 1_000_000, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 59144, false)} // 100 bps of LiFi cost
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "linea", Token: "USDC"}})
	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v; a route costing more than the round-trip budget must not be offered", venues)
	}
}

// The route here is deliberately one that WOULD be offered — priced inside
// the budget, executable, clearing its promise — so the only thing refusing
// it is the per-chain minimum. And because that gate runs before any LiFi
// call, the refusal is free: no paid quote is spent on an amount that could
// never be offered anyway.
func TestQuote_BelowMinimumOrderIsNotOffered(t *testing.T) {
	s := priceRig(t)
	s.svc.SetConversionMinimums(map[string]float64{"linea": 25}, 1)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(19_990000), ToAmountMin: big.NewInt(19_950000),
		FromAmountUSD: 20, ToAmountUSD: 19.96, GasCostUSD: 0.002, GasLimit: 1_000_000, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 59144, false)}
	venues, _ := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "30000", []RequestedAsset{{Chain: "linea", Token: "USDC"}})
	if len(venues) != 0 {
		t.Fatalf("$20 on linea is under its $25 minimum: %+v", venues)
	}
	if s.lifi.quoteCalls() != 0 {
		t.Fatalf("spent %d LiFi calls on an amount under the chain's minimum", s.lifi.quoteCalls())
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
	s := priceRig(t) // settlement leg: 100 USDC on base, 6dp
	s.lifi.route = &LiFiRoute{
		ToAmount:      new(big.Int).Mul(big.NewInt(998), pow10(17)), // 99.8 * 1e18
		ToAmountMin:   new(big.Int).Mul(big.NewInt(997), pow10(17)), // 99.7 * 1e18
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000,
		ETASeconds: 40, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 56, false),
	}
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "bsc", Token: "USDT"}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v, err = %v", venues, err)
	}
	v := venues[0]
	wantPromise := new(big.Int).Mul(big.NewInt(997), pow10(17)).String() // 99.7 * 1e18
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
		ToAmount:      big.NewInt(99_800000),
		ToAmountMin:   big.NewInt(99_700000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000,
		ETASeconds: 25, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 10, false),
	}}
	svc.SetLiFiQuoter(lifi)
	svc.SetTreasuryAddress("0xTreasuryAddress0000000000000000000001")
	svc.SetChainRegistry(testChainRegistry())
	svc.SetTreasuryLanes(&fakeTreasuryLaneChecker{laned: map[string]bool{"bsc": true, "optimism": true}})
	svc.SetRoundTripBudgetBps(50)

	venues, err := svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{Chain: "optimism", Token: "USDT"}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v, err = %v", venues, err)
	}
	v := venues[0]
	if v.MarkupBps != 50 || v.PromisedAmount != "99700000" {
		t.Fatalf("venue = %+v, want promisedAmount 99700000 (6dp, not the 18dp settlement figure)", v)
	}
	if v.ViaChain != "bsc" || v.ViaToken != "USDC" || v.RequestedChain != "optimism" || v.RequestedToken != "USDT" {
		t.Fatalf("venue must record both legs: %+v", v)
	}
}

// ── Fix round 3: cost amplification against LiFi ────────────────────────────
//
// GET /api/paycrest/quote is deliberately public and unauthenticated (see
// the handler's own doc comment). An unresolvable requested chain or token
// must never reach LiFi: that is a live HTTP call to a paid third-party API,
// spendable once per request just by naming a chain or token that does not
// exist, and it used to be swallowed silently as "no route" only AFTER the
// call was made.

func TestQuote_UnknownRequestedChainNeverCallsLiFi(t *testing.T) {
	s := newQuoteServiceWithFakes(t)
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{Chain: "not-a-real-chain", Token: "USDC"}})
	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v, err = %v; an unresolvable chain must offer nothing", venues, err)
	}
	if s.lifi.quoteCalls() != 0 {
		t.Fatalf("spent a LiFi call on a chain id that does not resolve")
	}
}

func TestQuote_UnknownRequestedTokenNeverCallsLiFi(t *testing.T) {
	s := newQuoteServiceWithFakes(t)
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{Chain: "optimism", Token: "NOTREAL"}})
	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v, err = %v; an unconfigured token must offer nothing", venues, err)
	}
	if s.lifi.quoteCalls() != 0 {
		t.Fatalf("spent a LiFi call on a token address that does not resolve")
	}
}

// Conversion is a buy-only capability. The handler already gates on
// res.Side == "buy" before calling in, but ConvertedVenues must refuse on
// its own too, so a future direct caller cannot get a converted SELL venue
// just by skipping that gate.
func TestQuote_ConvertedVenuesRefusesASellSide(t *testing.T) {
	s := newQuoteServiceWithFakes(t)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000)}
	venues, err := s.svc.ConvertedVenues(context.Background(), "sell", "NGN", "150000",
		[]RequestedAsset{{Chain: "optimism", Token: "USDT"}})
	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v, err = %v; conversion is buy-only", venues, err)
	}
	if s.lifi.quoteCalls() != 0 {
		t.Fatalf("spent a LiFi call on a sell-side request")
	}
}

// ── Final review fix wave: I2 ────────────────────────────────────────────────
//
// A chain the chain registry knows about (chainID()/tokenAddress() succeed)
// is NOT the same set as a chain the treasury actually has an executable
// lane on (RAMP_TREASURY_CHAINS). Before this fix, nothing checked the
// narrower set inside ConvertedVenues itself — a venue on an unlaned chain
// was offered, quoted, and only failed later at o.destination() AFTER the
// user had already paid naira and custody had already landed. This asserts
// the actual observable behavior (no venue offered, no LiFi call spent) —
// not an internal map's keys, which is what the superseded
// rampQuotableConversionMinimums unit test asserted and which passed for
// the wrong reason (MeetsMinimumOrder falls back to a default minimum for
// an absent map key, so filtering the minimums map alone never refused
// anything).
func TestQuote_RefusesAVenueOnAChainWithNoTreasuryLane(t *testing.T) {
	s := newQuoteServiceWithFakes(t)
	// optimism is a real chain-registry entry (testChainRegistry configures
	// it) but NOT laned for this test.
	s.svc.SetTreasuryLanes(&fakeTreasuryLaneChecker{laned: map[string]bool{"base": true}})
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000)}

	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{Chain: "optimism", Token: "USDT"}})

	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v, err = %v; an unlaned requested chain must never be offered", venues, err)
	}
	if s.lifi.quoteCalls() != 0 {
		t.Fatalf("spent a LiFi call on a chain the treasury cannot actually execute on")
	}
}

// A treasury-lane checker that was simply never wired must also refuse
// everything, not fall open — same dormant-until-wired convention as a nil
// lifi/chains or an empty treasuryAddress.
func TestQuote_RefusesEveryVenueWhenTreasuryLanesWasNeverWired(t *testing.T) {
	s := newQuoteServiceWithFakes(t)
	s.svc.SetTreasuryLanes(nil)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000)}

	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{Chain: "optimism", Token: "USDT"}})

	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v, err = %v; an unwired lane checker must refuse, not fall open", venues, err)
	}
	if s.lifi.quoteCalls() != 0 {
		t.Fatalf("spent a LiFi call with no treasury-lane checker wired at all")
	}
}

// ── Part 2: converted venues priced on the round-trip budget ────────────────
//
// A converted venue is now offered only when the WHOLE round trip fits the
// budget B (LiFi's own cost plus the gas the treasury pays), and only when
// the route it was priced from is one execution would actually accept: a
// known calldata layout, no destination call, and the destination chain the
// user asked for. A stablecoin destination additionally carries a promise,
// and a route whose own guaranteed minimum falls under that promise would be
// refused on every execution attempt — so it is not offered either.

func priceRig(t *testing.T) *quoteServiceRig {
	t.Helper()
	s := newQuoteServiceWithFakes(t)
	s.svc.SetRoundTripBudgetBps(50)
	return s
}

func bnbRoute(t *testing.T) *LiFiRoute {
	t.Helper()
	return &LiFiRoute{ToAmount: big.NewInt(220_000_000_000_000_000), ToAmountMin: big.NewInt(219_000_000_000_000_000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasCostUSD: 0.01, GasLimit: 1_000_000, ToTokenDecimals: 18, ToTokenSymbol: "BNB",
		Tool: "across", Data: testBridgeCalldata(t, sponsorTestUser, 56, false)}
}

func TestConvertedVenues_BuysANativeCoinByAddress(t *testing.T) {
	s := priceRig(t)
	s.svc.treasuryLanes.(*fakeTreasuryLaneChecker).laned = map[string]bool{"base": true} // no bsc lane needed
	s.lifi.route = bnbRoute(t)
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{ChainID: 56, Address: evmNativeAddress, Token: "BN8-spoof", Recipient: sponsorTestUser}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v err = %v", venues, err)
	}
	v := venues[0]
	// The symbol comes from LiFi, never the client.
	if v.RequestedChain != "bsc" || v.RequestedAddress != evmNativeAddress || v.RequestedDecimals != 18 || v.RequestedToken != "BNB" {
		t.Fatalf("venue = %+v", v)
	}
	if v.PromisedAmount != "" {
		t.Fatalf("a volatile destination has no promise at quote time: %q", v.PromisedAmount)
	}
	if v.CostBps != 20 || v.EstimatedAmount != bnbRoute(t).ToAmount.String() {
		t.Fatalf("cost = %d estimate = %s", v.CostBps, v.EstimatedAmount)
	}
	// Second call prices what the treasury bridges: T/(1 − l) = 100000000 × 10000/9980 = 100200400.
	req := s.lifi.lastRequest()
	if req.ToToken != evmNativeAddress || req.ToChainID != 56 || req.FromAmount != "100200400" || req.Slippage != 0.005 {
		t.Fatalf("LiFi request = %+v", req)
	}
}

func TestConvertedVenues_RefusesWhenCostPlusGasExceedsTheBudget(t *testing.T) {
	s := priceRig(t)
	r := bnbRoute(t)
	r.ToAmountUSD, r.GasCostUSD = 99.55, 0.10 // l = 45 bps, g = 11 bps
	s.lifi.route = r
	venues, _ := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{ChainID: 56, Address: evmNativeAddress}})
	if len(venues) != 0 {
		t.Fatalf("a 56 bps round trip was offered against a 50 bps budget: %+v", venues)
	}
}

func TestConvertedVenues_RefusesARouteWithADestinationCall(t *testing.T) {
	s := priceRig(t)
	r := bnbRoute(t)
	r.Data = testBridgeCalldata(t, sponsorTestUser, 56, true)
	s.lifi.route = r
	venues, _ := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{ChainID: 56, Address: evmNativeAddress}})
	if len(venues) != 0 {
		t.Fatal("a route execution would refuse must not be offered")
	}
}

func TestConvertedVenues_StableDestinationKeepsAPromise(t *testing.T) {
	s := priceRig(t)
	r := bnbRoute(t)
	r.Data = testBridgeCalldata(t, sponsorTestUser, 10, false)
	r.ToTokenDecimals = 0 // stable decimals come from config, not LiFi
	// USDT priced 3 bps above USDC: LiFi's minimum is T/(1−l)·(1−l)·0.9997·0.999.
	r.ToAmount, r.ToAmountMin = big.NewInt(99_970000), big.NewInt(99_870000)
	s.lifi.route = r
	usdtOP := testChainRegistry().TokenAddressForChainID(10, "USDT")
	venues, _ := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{ChainID: 10, Address: usdtOP}})
	if len(venues) != 1 || venues[0].PromisedAmount != "99700000" || venues[0].MarkupBps != 50 {
		t.Fatalf("venues = %+v, want promise 99700000 (T less 30 bps) and markup = B", venues)
	}
	if req := s.lifi.lastRequest(); req.Slippage != 0.001 {
		t.Fatalf("stable slippage = %v, want 0.001", req.Slippage)
	}
}

// Offer only what execution accepts: a stable route whose own minimum is under
// the promise would be refused on every attempt, so it is not offered.
func TestConvertedVenues_RefusesAStableRouteBelowThePromise(t *testing.T) {
	s := priceRig(t)
	r := bnbRoute(t)
	r.Data = testBridgeCalldata(t, sponsorTestUser, 10, false)
	r.ToAmount, r.ToAmountMin = big.NewInt(99_600000), big.NewInt(99_500000)
	s.lifi.route = r
	usdtOP := testChainRegistry().TokenAddressForChainID(10, "USDT")
	if venues, _ := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{ChainID: 10, Address: usdtOP}}); len(venues) != 0 {
		t.Fatalf("venues = %+v; toAmountMin under the promise must not be offered", venues)
	}
}

// ── a Solana destination ───────────────────────────────────────────────────

// A Solana route is priced to HER SOLANA ADDRESS, never to the EVM wallet the
// rest of the request is keyed on: nothing on Solana can be delivered to a
// 20-byte address, so quoting one would price a route that could never run.
func TestQuote_SolanaVenueIsPricedToHerSolanaAddress(t *testing.T) {
	const solRecipient = "5tzFkiKscXHK5ZXCGbXZxdw7gTjjD1mBwuoFbhUvuAi9"
	s := priceRig(t)
	s.svc.SetSolanaValidator(solana.ValidateAddress)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(1_000_000_000), ToAmountMin: big.NewInt(990_000_000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, ToTokenDecimals: 9, ToTokenSymbol: "SOL",
		Tool: "near", Data: loadLiFiFixtures(t)["near_base_solana_sol"].Data}

	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{
		ChainID: solanaLiFiChainID, Address: solanaNativeToken,
		Recipient: sponsorTestUser, Destination: solRecipient,
	}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v err = %v; want one Solana venue", venues, err)
	}
	v := venues[0]
	if v.RequestedChain != "solana" || v.RequestedChainID != solanaLiFiChainID || v.RequestedDestination != solRecipient {
		t.Fatalf("venue = %+v; want a solana destination carrying her address", v)
	}
	// A Solana stablecoin would be just as unpromisable: the treasury holds no
	// float there, so nothing is ever promised on this chain.
	if v.PromisedAmount != "" {
		t.Fatalf("promised %q on a chain with no treasury lane", v.PromisedAmount)
	}
	if got := s.lifi.lastRequest(); got.ToAddress != solRecipient || got.ToChainID != solanaLiFiChainID {
		t.Fatalf("quote = %+v; want it priced to her Solana address", got)
	}
}

// Without a Solana address there is nowhere to deliver, so there is nothing to
// offer — a quote is never priced to a recipient we had to invent.
func TestQuote_SolanaVenueIsNotOfferedWithoutARecipient(t *testing.T) {
	s := priceRig(t)
	s.svc.SetSolanaValidator(solana.ValidateAddress)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(1_000_000_000), ToAmountMin: big.NewInt(990_000_000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, ToTokenDecimals: 9, ToTokenSymbol: "SOL",
		Tool: "near", Data: loadLiFiFixtures(t)["near_base_solana_sol"].Data}

	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{
		ChainID: solanaLiFiChainID, Address: solanaNativeToken, Recipient: sponsorTestUser,
	}})
	if err != nil || len(venues) != 0 {
		t.Fatalf("venues = %+v err = %v; want none without a Solana recipient", venues, err)
	}
	if s.lifi.quoteCalls() != 0 {
		t.Fatalf("LiFi was called %d times for an asset that cannot be delivered", s.lifi.quoteCalls())
	}
}

// solanaUSDCMint is the real USDC mint on Solana — the destination this whole
// corridor exists for, and the one the polymerCCTP capture was quoted to.
const solanaUSDCMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"

// A Solana venue is only worth offering if execution would accept it. The
// quote-time structural check (known layout, no destination call, right chain)
// passes calldata that startBridge then refuses every time — and by then she
// has paid naira for it, so the order can only sit through the retry window
// and refund. So the Solana family runs the SAME check here that execution
// runs, against the recipient the quote is priced to.
//
// Both cases below are real captured LiFi calldata (Task 10 fixtures):
//
//   - the NEAR facet carries no token-account word, so it cannot say where an
//     SPL delivery lands; checkLiFiDeliveryTo refuses an SPL route on it.
//   - the polymerCCTP capture pays a specific Solana address; quoting it to
//     anyone else is a route that pays a stranger.
func TestQuote_SolanaVenueExecutionWouldRefuseIsNotOffered(t *testing.T) {
	const solRecipient = "5tzFkiKscXHK5ZXCGbXZxdw7gTjjD1mBwuoFbhUvuAi9"
	const someoneElse = "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"
	cases := []struct {
		name        string
		fixture     string
		token       string
		destination string
	}{
		{
			name:    "an SPL destination on a facet with no token-account word",
			fixture: "near_base_solana_sol", token: solanaUSDCMint, destination: solRecipient,
		},
		{
			name:    "a route that pays a different Solana address",
			fixture: "polymer_base_solana_usdc", token: solanaUSDCMint, destination: someoneElse,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := priceRig(t)
			s.svc.SetSolanaValidator(solana.ValidateAddress)
			s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(100_000_000), ToAmountMin: big.NewInt(99_000_000),
				FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, ToTokenDecimals: 6, ToTokenSymbol: "USDC",
				Data: loadLiFiFixtures(t)[tc.fixture].Data}

			venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{
				ChainID: solanaLiFiChainID, Address: tc.token,
				Recipient: sponsorTestUser, Destination: tc.destination,
			}})
			if err != nil || len(venues) != 0 {
				t.Fatalf("venues = %+v err = %v; want none — execution would refuse this route", venues, err)
			}
		})
	}
}

// The other half of the same rule: an SPL delivery on a facet that DOES carry
// the token-account word, priced to the address the calldata pays, is still
// offered. SPL-on-Solana is the corridor this feature is for; refusing it
// would hide it behind a silent "unsupported".
func TestQuote_SolanaSPLVenueExecutionAcceptsIsOffered(t *testing.T) {
	const solRecipient = "5tzFkiKscXHK5ZXCGbXZxdw7gTjjD1mBwuoFbhUvuAi9"
	s := priceRig(t)
	s.svc.SetSolanaValidator(solana.ValidateAddress)
	s.lifi.route = &LiFiRoute{ToAmount: big.NewInt(100_000_000), ToAmountMin: big.NewInt(99_000_000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, ToTokenDecimals: 6, ToTokenSymbol: "USDC",
		Data: loadLiFiFixtures(t)["polymer_base_solana_usdc"].Data}

	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000", []RequestedAsset{{
		ChainID: solanaLiFiChainID, Address: solanaUSDCMint,
		Recipient: sponsorTestUser, Destination: solRecipient,
	}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v err = %v; want the SPL Solana venue offered", venues, err)
	}
	if v := venues[0]; v.RequestedDestination != solRecipient || v.RequestedAddress != solanaUSDCMint {
		t.Fatalf("venue = %+v; want USDC delivered to her Solana address", v)
	}
}

// bestSourceVenue used to scan every configured chain for whichever quoted
// cheapest and settle through THAT one — meaning the treasury needed
// standing gas on every chain Paycrest might ever pick, and which chain that
// was could change from one quote to the next. One chain running dry took
// every converted buy down with it, regardless of destination, and there was
// no way to tell from the outside which chain was actually in play. Base is
// the one chain both buy and sell already need funded (the sell leg is
// always Base), so it is the only chain settlement is pinned to now — other
// chains are consulted only when Base itself has no direct listing at all.
func rigWithTwoSettlementChains(t *testing.T) *quoteServiceRig {
	t.Helper()
	pc := &stubQuoter{market: 1500, rates: map[string]*PaycrestRates{
		"base|USDC":            buy("1500"), // worse rate for her: more naira per token
		"bnb-smart-chain|USDC": buy("1400"), // cheaper — this is the one bestSourceVenue must NOT pick
	}}
	svc := NewPaycrestQuoteService(pc, []string{"USDC"}, []string{"base", "bsc"})
	lifi := &fakeLiFiQuoter{}
	svc.SetLiFiQuoter(lifi)
	svc.SetTreasuryAddress("0xTreasuryAddress0000000000000000000001")
	svc.SetChainRegistry(testChainRegistry())
	svc.SetTreasuryLanes(&fakeTreasuryLaneChecker{laned: map[string]bool{"base": true, "bsc": true}})
	svc.SetRoundTripBudgetBps(50)
	return &quoteServiceRig{svc: svc, pc: pc, lifi: lifi}
}

func TestConvertedVenues_PinsSettlementToBaseEvenWhenAnotherChainIsCheaper(t *testing.T) {
	s := rigWithTwoSettlementChains(t)
	s.lifi.route = bnbRoute(t)
	venues, err := s.svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{ChainID: 56, Address: evmNativeAddress}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v err = %v", venues, err)
	}
	// The settlement leg the treasury actually bridges from is whatever
	// chain the LiFi request was priced from — Base's id, not BSC's, even
	// though BSC quoted the cheaper rate.
	if req := s.lifi.lastRequest(); req.FromChainID != 8453 {
		t.Fatalf("settled from chain %d, want Base (8453) even though bsc was cheaper", req.FromChainID)
	}
}

func TestConvertedVenues_FallsBackToAnotherChainWhenBaseHasNoDirectListing(t *testing.T) {
	pc := &stubQuoter{market: 1500, rates: map[string]*PaycrestRates{
		"bnb-smart-chain|USDC": buy("1400"), // no "base|USDC" at all
	}}
	svc := NewPaycrestQuoteService(pc, []string{"USDC"}, []string{"base", "bsc"})
	lifi := &fakeLiFiQuoter{}
	svc.SetLiFiQuoter(lifi)
	svc.SetTreasuryAddress("0xTreasuryAddress0000000000000000000001")
	svc.SetChainRegistry(testChainRegistry())
	svc.SetTreasuryLanes(&fakeTreasuryLaneChecker{laned: map[string]bool{"base": true, "bsc": true}})
	svc.SetRoundTripBudgetBps(50)
	lifi.route = bnbRoute(t)

	venues, err := svc.ConvertedVenues(context.Background(), "buy", "NGN", "150000",
		[]RequestedAsset{{ChainID: 56, Address: evmNativeAddress}})
	if err != nil || len(venues) != 1 {
		t.Fatalf("venues = %+v err = %v; base has nothing to quote, bsc must still fill it", venues, err)
	}
	if req := lifi.lastRequest(); req.FromChainID != 56 {
		t.Fatalf("settled from chain %d, want bsc (56) since base has no direct listing at all", req.FromChainID)
	}
}
