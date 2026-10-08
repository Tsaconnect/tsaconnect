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
