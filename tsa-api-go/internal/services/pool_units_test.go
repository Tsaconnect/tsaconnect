package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestPoolUnits_CanonicalToChainExact_18dp(t *testing.T) {
	got, err := CanonicalToChainUnits(big.NewInt(12_419_593), 18)
	if err != nil || got.String() != "12419593000000000000" {
		t.Fatalf("= %v %v", got, err)
	}
	if same, _ := CanonicalToChainUnits(big.NewInt(7), 6); same.Int64() != 7 {
		t.Fatal("6 dp is the identity")
	}
	if _, err := CanonicalToChainUnits(big.NewInt(1), 4); err == nil {
		t.Fatal("fewer than 6 decimals is refused (S7)")
	}
}

func TestPoolUnits_ChainToCanonicalFloors(t *testing.T) {
	got, err := ChainToCanonicalUnits(bigFromDecimalString("12419593999999999999"), 18)
	if err != nil || got.Int64() != 12_419_593 {
		t.Fatalf("= %v %v, want the dust floored away", got, err)
	}
}

// Spec §24.2 / S23: an 18-dp leg is priced, in its own base units.
func TestDeliveredLeg_Accepts18dp(t *testing.T) {
	s := deliveredRig(t)
	s.svc.treasuryLanes.(*fakeTreasuryLaneChecker).decimals = map[string]int{"bsc|USDT": 18}
	s.lifi.route = optimismUSDTRoute(t, 72_419_593) // bridges the whole leg
	leg := SettlementLeg{ChainKey: "bsc", Token: "USDT", Delivered: big.NewInt(72_419_593),
		FiatAmount: "100000", Rate: "1361.51", MarketRate: 1348}
	v, err := s.svc.PriceConvertedLeg(context.Background(), leg, optimismUSDT)
	if err != nil || v == nil {
		t.Fatalf("venue = %+v err = %v", v, err)
	}
	scaled, _ := CanonicalToChainUnits(big.NewInt(72_419_593), 18)
	if want := ConversionOwed(scaled, v.MarkupBps).String(); v.TokenAmountBaseUnits != want || v.ChainKey != "bsc" {
		t.Fatalf("venue owes %s on %s, want %s (18-dp leg units)", v.TokenAmountBaseUnits, v.ChainKey, want)
	}
	if s.lifi.last.FromChainID != 56 {
		t.Fatalf("bridged from chain %d, want BSC", s.lifi.last.FromChainID)
	}
}

func TestDeliveredLeg_RefusesBelowSixDecimals(t *testing.T) {
	s := deliveredRig(t)
	s.svc.treasuryLanes.(*fakeTreasuryLaneChecker).decimals = map[string]int{"bsc|USDT": 4}
	v, err := s.svc.PriceConvertedLeg(context.Background(), SettlementLeg{ChainKey: "bsc", Token: "USDT",
		Delivered: big.NewInt(1_000_000), FiatAmount: "1500", Rate: "1500", MarketRate: 1500}, optimismUSDT)
	if err != nil || v != nil {
		t.Fatalf("venue = %+v err = %v, want none", v, err)
	}
}

// Review m3: a decimals read that fails keeps today's 6-dp legs, never guesses 18.
func TestDeliveredLeg_DecimalsReadFailureKeepsSixDpLegs(t *testing.T) {
	s := deliveredRig(t)
	s.svc.treasuryLanes.(*fakeTreasuryLaneChecker).decimalsErr = errors.New("rpc blip")
	s.lifi.route = optimismUSDTRoute(t, 96_000_000)
	if v, err := s.svc.PriceConvertedLeg(context.Background(), baseLeg(96_000_000), optimismUSDT); err != nil || v == nil {
		t.Fatalf("Base USDC leg = %+v err = %v, want still priced at 6 dp", v, err)
	}
	if v, _ := s.svc.PriceConvertedLeg(context.Background(), SettlementLeg{ChainKey: "bsc", Token: "USDT",
		Delivered: big.NewInt(72_419_593), FiatAmount: "100000", Rate: "1361.51", MarketRate: 1348}, optimismUSDT); v != nil {
		t.Fatalf("an 18-dp BSC leg must not be priced on a failed read: %+v", v)
	}
}

// Spec §24.2 / §5: the handoff restates owed units in the leg's own units.
func TestHandoff_18dpLegOwedInLegUnits(t *testing.T) {
	eighteen := 18
	u, err := handoffUpdates(&models.PaycrestOrderRecord{MarkupBps: 50, LegDecimals: &eighteen}, big.NewInt(72_419_593), "0xhash")
	if err != nil {
		t.Fatalf("updates: %v", err)
	}
	if u["quoted_amount"] != "72057495035000000000" || u["token_amount"] != FormatStableUnits("72419593") {
		t.Fatalf("updates = %v", u)
	}
}

func TestHandoff_LegacyRecordStaysSixDecimals(t *testing.T) {
	u, err := handoffUpdates(&models.PaycrestOrderRecord{MarkupBps: 50}, big.NewInt(7_232_937), "0xhash")
	if err != nil || u["quoted_amount"] != "7196772" {
		t.Fatalf("updates = %v err = %v", u, err)
	}
}

// Review I1: an 18-dp converted leg round-trips quote → open → handoff
// exactly — the opener checks T in the leg's units and records them.
func TestConvertedOpen_18dpLegRoundTripsQuoteOpenHandoff(t *testing.T) {
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	q := treasuryTestQuote(t, s)
	rig := deliveredRig(t)
	rig.svc.treasuryLanes.(*fakeTreasuryLaneChecker).decimals = map[string]int{"base|USDC": 18}
	rig.lifi.route = &LiFiRoute{ToAmount: big.NewInt(7_278_150), ToAmountMin: big.NewInt(7_250_000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_200_000, Tool: "across",
		Data: testBridgeCalldata(t, sponsorTestUser, 10, false)}
	v, err := rig.svc.PriceConvertedLeg(context.Background(), SettlementLeg{ChainKey: "base", Token: "USDC",
		Delivered: q.AssetUnits, FiatAmount: "10000", Rate: "1374", MarketRate: 1348}, optimismUSDT)
	if err != nil || v == nil {
		t.Fatalf("venue = %+v err = %v", v, err)
	}
	scaled, _ := CanonicalToChainUnits(q.AssetUnits, 18)
	if want := ConversionOwed(scaled, v.MarkupBps).String(); v.TokenAmountBaseUnits != want || v.LegDecimals != 18 {
		t.Fatalf("venue T = %s at %d dp, want %s at 18", v.TokenAmountBaseUnits, v.LegDecimals, want)
	}
	intent, _, rec, err := s.OpenTreasuryConvertedBuy(context.Background(), userID, q, "base",
		ConvertedOpen{Venue: *v, WalletAddress: testRoutedEVM})
	if err != nil {
		t.Fatalf("open an 18-dp converted leg: %v", err)
	}
	stored := conversionRecord(t, s, intent.ID)
	if stored.QuotedAmount != v.TokenAmountBaseUnits || stored.LegDecimals == nil || *stored.LegDecimals != 18 ||
		rec.QuotedAmount != stored.QuotedAmount {
		t.Fatalf("record T = %s legDecimals = %v, want %s at 18", stored.QuotedAmount, stored.LegDecimals, v.TokenAmountBaseUnits)
	}
	u, err := handoffUpdates(&stored, q.AssetUnits, "0xhash")
	if err != nil || u["quoted_amount"] != v.TokenAmountBaseUnits || u["token_amount"] != FormatStableUnits(q.AssetUnits.String()) {
		t.Fatalf("handoff = %v err = %v, want T %s unchanged", u, err, v.TokenAmountBaseUnits)
	}
}

// Review I1: a 6-dp converted open writes no leg decimals, as before the pool.
func TestConvertedOpen_SixDpRecordLeavesLegDecimalsNull(t *testing.T) {
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	six := convertedVenueForTest()
	six.LegDecimals = 6
	_, _, _, err := s.OpenTreasuryConvertedBuy(context.Background(), userID, treasuryTestQuote(t, s), "base",
		ConvertedOpen{Venue: six, WalletAddress: testRoutedEVM})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var n int64
	s.db.Model(&models.PaycrestOrderRecord{}).Where("leg_decimals IS NOT NULL").Count(&n)
	if n != 0 {
		t.Fatalf("%d records carry leg decimals, want none for a 6-dp leg", n)
	}
}
