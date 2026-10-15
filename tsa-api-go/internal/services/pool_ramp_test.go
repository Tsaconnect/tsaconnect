package services

import (
	"context"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Plan A I1 / P18: a converted pool order's record carries the 18-dp leg.
func TestPoolConverted_RecordLegDecimals18(t *testing.T) {
	r := newVaultRig(t, 100, 25)
	r.it.SetConvertedBuys(ConvertedBuys{TreasuryAddress: poolTestTreasury, Treasury: r.desk.c.Treasury})
	p := r.price(t, "5000")
	leg, _ := CanonicalToChainUnits(p.Quote.AssetUnits, 18)
	v := PaycrestVenue{Kind: models.RouteKindConverted, ChainKey: "bsc", Token: "USDT", MarkupBps: 100, LegDecimals: 18,
		RequestedChain: "base", RequestedToken: "USDC", TokenAmountBaseUnits: ConversionOwed(leg, 100).String()}
	intent, pay, rec, err := r.it.OpenPoolConvertedBuy(context.Background(), r.userID, p, ConvertedOpen{Venue: v, WalletAddress: testRoutedEVM})
	if err != nil || pay == nil || rec == nil {
		t.Fatalf("converted open = %v", err)
	}
	var got models.PaycrestOrderRecord
	r.db.First(&got, "reference = ?", ITConversionRef(intent.ID))
	if got.Holder != models.HolderPool || got.LegDecimals == nil || *got.LegDecimals != 18 || got.ValidUntil == nil ||
		!strings.EqualFold(intent.DestAddress, poolTestTreasury) {
		t.Fatalf("record = %+v intent dest = %s", got, intent.DestAddress)
	}
}

// §15 pool-card: rate and limits without an amount; never a provider name or
// a voucher signature.
func TestPoolCard_RateAndLimits(t *testing.T) {
	r := newVaultRig(t, 125, 25) // 125 USDT liquid: all deliverable (the spread needs no backing)
	r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	card, err := r.it.PoolCard(context.Background())
	if err != nil || !card.Enabled || !card.Available || card.Rate != "1361.51" || card.FundingFeePercent != "1.4" ||
		card.MinNgn != "2817.00" || card.Name != "TSA Connect" || card.Badge != "Recommended" || card.Token.Decimals != 18 {
		t.Fatalf("card = %+v %v", card, err)
	}
	maxNgn, _ := new(big.Rat).SetString(card.MaxNgn)
	if maxNgn.Cmp(big.NewRat(172_000, 1)) < 0 || maxNgn.Cmp(big.NewRat(173_000, 1)) > 0 { // 125 ÷ 0.986 × ₦1,361.51 ≈ ₦172,605
		t.Fatalf("maxNgn = %s, want ≈ the naira for 125 USDT net", card.MaxNgn)
	}
	b, _ := json.Marshal(card)
	if strings.Contains(strings.ToLower(string(b)), "voucher") {
		t.Fatalf("card leaks voucher data: %s", b)
	}
	dormant, _ := newRoutedBuyService(t)
	if c, _ := dormant.PoolCard(context.Background()); c.Enabled {
		t.Fatal("a dormant pool shows a card (P17)")
	}
}
