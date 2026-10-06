package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// deliveredRig is priceRig with Polygon known and laned, so a delivered leg
// can settle on Base (route 1) or Polygon (route 2).
func deliveredRig(t *testing.T) *quoteServiceRig {
	t.Helper()
	s := priceRig(t)
	reg := testChainRegistry()
	net := reg.cfg.Networks["mainnet"]
	net.Chains["polygon"] = config.ChainConfig{ChainID: 137}
	net.TokenAddresses["polygon:USDC"] = "0xPolyUSDC0000000000000000000000000000001"
	s.svc.SetChainRegistry(reg)
	s.svc.treasuryLanes.(*fakeTreasuryLaneChecker).laned["polygon"] = true
	return s
}

// optimismUSDTRoute costs 20 bps; its minimum sits just under toAmount.
func optimismUSDTRoute(t *testing.T, toAmount int64) *LiFiRoute {
	t.Helper()
	return &LiFiRoute{ToAmount: big.NewInt(toAmount), ToAmountMin: big.NewInt(toAmount - 100_000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_200_000, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 10, false)}
}

func baseLeg(delivered int64) SettlementLeg {
	return SettlementLeg{ChainKey: "base", Token: "USDC", Delivered: big.NewInt(delivered),
		FiatAmount: "150000", Rate: "1382.57", MarketRate: 1348}
}

var optimismUSDT = RequestedAsset{Chain: "optimism", Token: "USDT", Recipient: sponsorTestUser}

// Route 1 prices from the leg's own D, whatever Paycrest's book says.
func TestPriceConvertedLeg_OwesDTimesOneMinusTheBudget(t *testing.T) {
	s := deliveredRig(t)
	s.pc.rates = map[string]*PaycrestRates{} // Paycrest's naira book is empty
	s.lifi.route = optimismUSDTRoute(t, 96_000_000)
	v, err := s.svc.PriceConvertedLeg(context.Background(), baseLeg(96_000_000), optimismUSDT)
	if err != nil || v == nil {
		t.Fatalf("venue = %+v err = %v", v, err)
	}
	// T = 96 × (1 − 0.005) = 95.52 at B = 50 bps.
	if v.Kind != models.RouteKindConverted || v.ChainKey != "base" || v.Token != "USDC" || v.ViaChain != "base" ||
		v.TokenAmountBaseUnits != "95520000" || v.MarkupBps != 50 || v.Rate != "1382.57" ||
		v.RequestedChain != "optimism" || v.RequestedToken != "USDT" || v.PromisedAmount == "" {
		t.Fatalf("venue = %+v", v)
	}
	if s.lifi.last.FromChainID != 8453 || s.lifi.last.FromAddress != s.svc.treasuryAddress {
		t.Fatalf("bridged from chain %d by %s, want Base from the conversion treasury", s.lifi.last.FromChainID, s.lifi.last.FromAddress)
	}
}

// D29: the cap limits B′ alone. A 150 bps route raises B′ to 160: offered
// under a 200 cap (a margin + B′ total would not be), refused under 150.
func TestPriceConvertedLeg_CapIsOnTheBudgetOnly(t *testing.T) {
	dear := func(t *testing.T) *LiFiRoute {
		r := optimismUSDTRoute(t, 96_000_000)
		r.ToAmountUSD = 98.5
		return r
	}
	s := deliveredRig(t)
	s.svc.SetConvertedFeeCaps(nil, 200)
	s.lifi.route = dear(t)
	if v, err := s.svc.PriceConvertedLeg(context.Background(), baseLeg(96_000_000), optimismUSDT); err != nil || v == nil || v.MarkupBps != 160 {
		t.Fatalf("venue = %+v err = %v, want offered at 160 bps under a 200 cap", v, err)
	}
	s2 := deliveredRig(t)
	s2.svc.SetConvertedFeeCaps(nil, 150)
	s2.lifi.route = dear(t)
	if v, err := s2.svc.PriceConvertedLeg(context.Background(), baseLeg(96_000_000), optimismUSDT); err != nil || v != nil {
		t.Fatalf("venue = %+v err = %v, want none over a 150 cap", v, err)
	}
}

func TestPriceConvertedLeg_NeedsAConversionLaneOnTheLegChain(t *testing.T) {
	s := deliveredRig(t)
	delete(s.svc.treasuryLanes.(*fakeTreasuryLaneChecker).laned, "polygon")
	leg := baseLeg(96_000_000)
	leg.ChainKey = "polygon"
	if v, err := s.svc.PriceConvertedLeg(context.Background(), leg, optimismUSDT); err != nil || v != nil || s.lifi.calls != 0 {
		t.Fatalf("venue = %+v err = %v lifi = %d, want nothing and no LiFi call", v, err, s.lifi.calls)
	}
}

func TestPriceConvertedLeg_NeverConvertsTheLegIntoItself(t *testing.T) {
	s := deliveredRig(t)
	if v, err := s.svc.PriceConvertedLeg(context.Background(), baseLeg(96_000_000),
		RequestedAsset{Chain: "base", Token: "USDC", Recipient: sponsorTestUser}); err != nil || v != nil || s.lifi.calls != 0 {
		t.Fatalf("venue = %+v err = %v lifi = %d", v, err, s.lifi.calls)
	}
}

func TestPriceConvertedLeg_DormantUntilConversionIsWired(t *testing.T) {
	var s *PaycrestQuoteService
	if v, err := s.PriceConvertedLeg(context.Background(), baseLeg(96_000_000), optimismUSDT); err != nil || v != nil {
		t.Fatalf("venue = %+v err = %v", v, err)
	}
}

func TestConversionOwed(t *testing.T) {
	if got := ConversionOwed(big.NewInt(7_232_937), 50); got.String() != "7196772" {
		t.Fatalf("owed = %s, want 7196772", got)
	}
}
