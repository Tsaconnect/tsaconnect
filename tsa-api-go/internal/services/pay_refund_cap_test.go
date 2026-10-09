package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Direct and converted: the stablecoin that left the payer for this payment
// (her deposit, sender fee inside it) less what came back, rounded down.
func TestPayRefundCap_DirectIsTheDepositLessWhatCameBackRoundedDown(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	for ref, tc := range map[string]struct {
		deposit, returned string
		want              int64
	}{
		"CAP-1": {"34.1234567", "", 34_123_456},
		"CAP-2": {"34.1234567", "1.5", 32_623_456},
		// What came back is rounded UP: the cap never exceeds the exact floor.
		"CAP-17": {"34.1234567", "1.0000001", 33_123_455},
	} {
		p := r.paidDirect(t, ref, tc.deposit, tc.returned)
		src, err := r.s.source(ctx, p)
		if err != nil || src.capMicroUSD != tc.want || src.fundingWallet != refundFundingWallet || src.depositChainID != 56 || src.depositSymbol != "USDT" {
			t.Errorf("%s: %+v err %v", ref, src, err)
		}
	}
	all := r.paidDirect(t, "CAP-3", "34.1234567", "34.1234567")
	if _, err := r.s.source(ctx, all); !errors.Is(err, ErrPayRefundNotRefundable) {
		t.Fatalf("everything came back: %v", err)
	}
}

func TestPayRefundCap_FindsTheOrderByItsReverseLink(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidDirect(t, "CAP-4", "20", "")
	if err := r.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Update("paycrest_order_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	p.PaycrestOrderID = nil
	if src, err := r.s.source(context.Background(), p); err != nil || src.capMicroUSD != 20_000_000 {
		t.Fatalf("%+v %v", src, err)
	}
}

// Crypto: the owed floor in the listed stablecoin, at its on-chain decimals,
// scaled up from what the merchant was owed to what the customer paid.
func TestPayRefundCap_CryptoScalesTheOwedFloorToWhatTheCustomerPaid(t *testing.T) {
	r := newRefundRig(t)
	base := r.paidCrypto(t, "CAP-5", 8453, refundBaseUSDC, "USDC", "33000000")
	bsc := r.paidCrypto(t, "CAP-6", 56, refundBSCUSDT, "USDT", "33000000000000000000")
	for _, p := range []*models.PayPayment{base, bsc} {
		src, err := r.s.source(context.Background(), p)
		if err != nil || src.capMicroUSD != 33_330_000 || src.fundingWallet != refundFundingWallet || src.depositChainID != p.SettleChainID {
			t.Errorf("%s: %+v err %v", p.Reference, src, err)
		}
	}
}

// An admin can settle a merchant in anything; a refund only in a listed stablecoin.
func TestPayRefundCap_CryptoRefusesAnUnlistedSettlementToken(t *testing.T) {
	r := newRefundRig(t)
	weth := r.paidCrypto(t, "CAP-7", 8453, "0x4200000000000000000000000000000000000006", "USDC", "33000000")
	mislabelled := r.paidCrypto(t, "CAP-8", 56, refundBSCUSDT, "USDC", "1")
	for _, p := range []*models.PayPayment{weth, mislabelled} {
		if _, err := r.s.source(context.Background(), p); !errors.Is(err, ErrPayRefundNotRefundable) {
			t.Errorf("%s: %v", p.Reference, err)
		}
	}
}

func TestPayRefundCap_CryptoDecimalsUnreadableIsUnavailable(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidCrypto(t, "CAP-9", 56, refundBSCUSDT, "USDT", "33000000000000000000")
	r.net.decimalsErr = errors.New("rpc down")
	if _, err := r.s.source(context.Background(), p); !errors.Is(err, ErrPayRefundUnavailable) {
		t.Fatalf("%v", err)
	}
}

// Test payments moved no money: a fixed nominal ₦1,500/USD sizes them.
func TestPayRefundCap_TestModeUsesTheNominalRate(t *testing.T) {
	r := newRefundRig(t)
	p := r.paid(t, models.PayModeTest, "CAP-10", map[string]any{"customer_total_kobo": int64(1_500_000)})
	if src, err := r.s.source(context.Background(), p); err != nil || src.capMicroUSD != 10_000_000 || src.fundingWallet != "" {
		t.Fatalf("%+v %v", src, err)
	}
}

func TestPayRefundMoney_FloorsToMicroUSD(t *testing.T) {
	for in, want := range map[string]int64{"": 0, "0": 0, "1": 1_000_000, "34.1234567": 34_123_456, "0.0000009": 0, "12.5": 12_500_000} {
		if got, err := microUSDFloor(in); err != nil || got != want {
			t.Errorf("microUSDFloor(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for in, want := range map[string]int64{"": 0, "0": 0, "1.5": 1_500_000, "1.0000001": 1_000_001, "1.0000000": 1_000_000, "0.0000009": 1} {
		if got, err := microUSDCeil(in); err != nil || got != want {
			t.Errorf("microUSDCeil(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"1e5", "-1", "34.", ".5"} {
		if _, err := microUSDFloor(bad); err == nil {
			t.Errorf("microUSDFloor(%q) must be refused: only a plain non-negative decimal is an amount", bad)
		}
		if _, err := microUSDCeil(bad); err == nil {
			t.Errorf("microUSDCeil(%q) must be refused: only a plain non-negative decimal is an amount", bad)
		}
	}
	if _, err := microUSDCeil("9223372036854.775807999"); err == nil {
		t.Error("rounding up past int64 must be refused, not wrapped")
	}
	if got, err := usdMicrosFloor(new(big.Int).Add(bscAmountForTest(12_500_000), big.NewInt(1)), 18); err != nil || got != 12_500_000 {
		t.Errorf("18 dp: %d %v", got, err)
	}
	if got, err := usdMicrosFloor(big.NewInt(1234), 2); err != nil || got != 12_340_000 {
		t.Errorf("2 dp: %d %v", got, err)
	}
	// 10^19 micro-USD is past int64: an error, never a silently wrapped amount.
	huge := new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil)
	if got, err := usdMicrosFloor(huge, 6); err == nil {
		t.Errorf("6 dp overflow: %d", got)
	}
	if got, err := usdMicrosFloor(huge, 2); err == nil {
		t.Errorf("2 dp overflow: %d", got)
	}
}

func bscAmountForTest(micro int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(micro), big.NewInt(1_000_000_000_000))
}

func TestBlockchainRefundNetwork_AChainWithoutAClientIsAnError(t *testing.T) {
	n := NewBlockchainRefundNetwork(NewBlockchainService(&config.Config{}))
	if _, err := n.TokenDecimals("bsc", refundBSCUSDT); err == nil {
		t.Error("TokenDecimals with no client")
	}
	if err := n.Broadcast(context.Background(), "bsc", nil); err == nil {
		t.Error("Broadcast with no client")
	}
	if _, err := n.ConfirmedNonce(context.Background(), "bsc", common.Address{}, 5); err == nil {
		t.Error("ConfirmedNonce with no client")
	}
	if n.ChainName("bsc") != "bsc" {
		t.Error("ChainName falls back to the key")
	}
}

// Negative or empty inputs never size a refund: a negative AmountReturned would
// raise the cap, an empty deposit would make one out of nothing.
func TestPayRefundCap_DirectRefusesAnEmptyOrMalformedOrder(t *testing.T) {
	r := newRefundRig(t)
	for ref, tc := range map[string][2]string{
		"CAP-11": {"", ""},
		"CAP-12": {"1e5", ""},
		"CAP-13": {"20", "-1"},
		"CAP-14": {"-20", ""},
	} {
		p := r.paidDirect(t, ref, tc[0], tc[1])
		if _, err := r.s.source(context.Background(), p); !errors.Is(err, ErrPayRefundNotRefundable) {
			t.Errorf("%s (%q less %q): %v", ref, tc[0], tc[1], err)
		}
	}
}

func TestPayRefundCap_TestModeWithNothingPaidIsNotRefundable(t *testing.T) {
	r := newRefundRig(t)
	p := r.paid(t, models.PayModeTest, "CAP-15", map[string]any{"customer_total_kobo": int64(0)})
	if _, err := r.s.source(context.Background(), p); !errors.Is(err, ErrPayRefundNotRefundable) {
		t.Fatalf("%v", err)
	}
}

func TestPayCryptoRefundCap_IsExactAndRefusesBadInputs(t *testing.T) {
	// $33 owed at 6 dp, ₦50,500 paid for ₦50,000: 33 × 1.01 exactly.
	if got, err := payCryptoRefundCap("33000000", 6, 5_050_000, 5_000_000); err != nil || got != 33_330_000 {
		t.Errorf("6 dp: %d %v", got, err)
	}
	// floor, never round: 1 base unit at 18 dp is far below a micro-USD.
	if got, err := payCryptoRefundCap("1000000000001", 18, 3, 2); err != nil || got != 1 {
		t.Errorf("18 dp floor: %d %v", got, err)
	}
	for name, tc := range map[string]struct {
		dest            string
		decimals        int
		customer, owner int64
	}{
		"empty dest":        {"", 6, 5_050_000, 5_000_000},
		"negative dest":     {"-33000000", 6, 5_050_000, 5_000_000},
		"decimal dest":      {"33.5", 6, 5_050_000, 5_000_000},
		"zero net":          {"33000000", 6, 5_050_000, 0},
		"negative customer": {"33000000", 6, -5_050_000, 5_000_000},
		"negative decimals": {"33000000", -1, 5_050_000, 5_000_000},
		"rounds to nothing": {"1", 18, 5_050_000, 5_000_000},
	} {
		if _, err := payCryptoRefundCap(tc.dest, tc.decimals, tc.customer, tc.owner); !errors.Is(err, ErrPayRefundNotRefundable) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An RPC error can carry the node URL and its key: it leaves source redacted.
func TestPayRefundCap_CryptoDecimalsErrorIsRedacted(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidCrypto(t, "CAP-16", 56, refundBSCUSDT, "USDT", "33000000000000000000")
	r.net.decimalsErr = errors.New(`Post "https://bsc-mainnet.example.com/v2/sk_live_abc123secret": dial tcp: timeout`)
	_, err := r.s.source(context.Background(), p)
	if !errors.Is(err, ErrPayRefundUnavailable) || strings.Contains(err.Error(), "sk_live_abc123secret") {
		t.Fatalf("%v", err)
	}
}

// Converted: the swap's Base-USDC deposit (6 dp) into the order handed over to
// the payment sizes the refund, and is where it defaults.
func TestPayRefundCap_ConvertedIsTheBaseUSDCDeposit(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidByOrder(t, "CAP-18", "base", "USDC", "33.123456", "")
	src, err := r.s.source(context.Background(), p)
	if err != nil || src.capMicroUSD != 33_123_456 || src.fundingWallet != refundFundingWallet || src.depositChainID != 8453 || src.depositSymbol != "USDC" {
		t.Fatalf("%+v %v", src, err)
	}
}

// No settled order — none at all, or only one still open — sizes nothing.
func TestPayRefundCap_DirectWithoutASettledOrderIsNotRefundable(t *testing.T) {
	ctx := context.Background()
	r := newRefundRig(t)
	none := r.paid(t, models.PayModeLive, "CAP-19", nil)
	if _, err := r.s.source(ctx, none); !errors.Is(err, ErrPayRefundNotRefundable) {
		t.Fatalf("no order: %v", err)
	}
	open := r.paidDirect(t, "CAP-20", "20", "")
	r.db.Model(&models.PayPayment{}).Where("id = ?", open.ID).Update("paycrest_order_id", nil)
	r.db.Model(&models.PaycrestOrderRecord{}).Where("pay_payment_id = ?", open.ID).Update("status", models.PaycrestStatusPending)
	open.PaycrestOrderID = nil
	if _, err := r.s.source(ctx, open); !errors.Is(err, ErrPayRefundNotRefundable) {
		t.Fatalf("only an open order: %v", err)
	}
}

func TestPayRefundCap_CryptoWithoutAnIntentIsNotRefundable(t *testing.T) {
	r := newRefundRig(t)
	p := r.paid(t, models.PayModeLive, "CAP-21", map[string]any{"settlement_mode": models.PaySettlementCrypto,
		"settle_chain_id": int64(8453), "settle_token_address": refundBaseUSDC, "settle_token_symbol": "USDC",
		"customer_total_kobo": int64(5_050_000), "merchant_net_kobo": int64(5_000_000)})
	if _, err := r.s.source(context.Background(), p); !errors.Is(err, ErrPayRefundNotRefundable) {
		t.Fatalf("%v", err)
	}
}

// Without a direct link the intent is found by its reverse link — and only a
// delivered one counts: a newer attempt that failed delivered nothing.
func TestPayRefundCap_CryptoFindsTheDeliveredIntentByItsReverseLink(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidCrypto(t, "CAP-22", 8453, refundBaseUSDC, "USDC", "33000000")
	if err := r.db.Model(&models.RampSellIntent{}).Where("id = ?", *p.SellIntentID).Update("pay_payment_id", p.ID).Error; err != nil {
		t.Fatal(err)
	}
	failed := models.RampSellIntent{ID: uuid.New(), UserID: r.payer, SourceAddress: refundFundingWallet, WalletAddress: refundFundingWallet,
		SourceChainID: 8453, SourceTokenAddress: refundBaseUSDC, SourceAmount: "1", FiatCode: "NGN", Status: models.SellIntentFailed,
		DestChainID: 8453, DestTokenAddress: refundBaseUSDC, DestAddress: "0x2222222222222222222222222222222222222222", DestAmount: "99000000",
		PayPaymentID: &p.ID, CreatedAt: time.Now().Add(time.Hour)}
	if err := r.db.Create(&failed).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.db.Model(&models.PayPayment{}).Where("id = ?", p.ID).Update("sell_intent_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	p.SellIntentID = nil
	src, err := r.s.source(context.Background(), p)
	if err != nil || src.capMicroUSD != 33_330_000 {
		t.Fatalf("%+v %v", src, err)
	}
	if err := r.db.Model(&models.RampSellIntent{}).Where("id <> ?", failed.ID).Update("status", models.SellIntentFailed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.source(context.Background(), p); !errors.Is(err, ErrPayRefundNotRefundable) {
		t.Fatalf("only failed intents: %v", err)
	}
}

// A directly linked intent counts only once it delivered: an attempt that
// failed paid the merchant nothing.
func TestPayRefundCap_CryptoDirectLinkMustBeDelivered(t *testing.T) {
	r := newRefundRig(t)
	p := r.paidCrypto(t, "CAP-23", 8453, refundBaseUSDC, "USDC", "33000000")
	if err := r.db.Model(&models.RampSellIntent{}).Where("id = ?", *p.SellIntentID).Update("status", models.SellIntentFailed).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.source(context.Background(), p); !errors.Is(err, ErrPayRefundNotRefundable) {
		t.Fatalf("%v", err)
	}
}
