package services

import (
	"context"
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// A foreign payout with a measured corridor fee is priced from it, not from
// the live quote (which can only price a naira transfer) nor the fallback.
func TestFeeQuote_ForeignPayoutUsesConfiguredCorridorFee(t *testing.T) {
	cfg := feeTestConfig()
	cfg.PayoutFees = map[string]config.PayoutFee{"USD": {Bps: 100, FlatNGN: 2500}}
	live := fixedProviderFee{fee: big.NewInt(1), ok: true}
	s := NewFeeQuoteService(cfg, live)

	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP, Amount: "100000", Currency: "NGN", PaymentCurrency: "USD",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	// 1% of ₦100,000 + ₦2,500 flat = ₦3,500.
	assertMinor(t, "providerFee", q.ProviderFee.Fee, "350000")
	if q.ProviderFee.Source != "configured" || q.ProviderFee.FallbackBps != 0 {
		t.Fatalf("provider provenance: %+v", q.ProviderFee)
	}
	assertMinor(t, "tax base", q.Tax.Base, "350000")
}

// A corridor nobody has measured keeps the fallback estimate — and never the
// live naira-transfer quote, which would understate a foreign payout.
func TestFeeQuote_UnmeasuredForeignPayoutKeepsFallback(t *testing.T) {
	cfg := feeTestConfig()
	cfg.PayoutFees = map[string]config.PayoutFee{"USD": {Bps: 100}}
	s := NewFeeQuoteService(cfg, fixedProviderFee{fee: big.NewInt(1), ok: true})

	q, err := s.Quote(context.Background(), FeeQuoteInput{
		Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP, Amount: "100000", Currency: "NGN", PaymentCurrency: "GBP",
	})
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	assertMinor(t, "providerFee", q.ProviderFee.Fee, "200000") // 2% fallback
	if q.ProviderFee.Source != "fallback" {
		t.Fatalf("source = %q, want fallback", q.ProviderFee.Source)
	}
}

// Naira payouts and deposits are untouched by corridor fees.
func TestFeeQuote_CorridorFeeIgnoredForNairaAndDeposits(t *testing.T) {
	cfg := feeTestConfig()
	cfg.PayoutFees = map[string]config.PayoutFee{"USD": {Bps: 100}}
	s := NewFeeQuoteService(cfg, fixedProviderFee{fee: big.NewInt(1000), ok: true})

	for _, in := range []FeeQuoteInput{
		{Flow: FeeFlowWithdraw, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN"},
		{Flow: FeeFlowDeposit, Asset: FeeAssetMCGP, Amount: "500", Currency: "NGN", PaymentCurrency: "USD"},
	} {
		q, err := s.Quote(context.Background(), in)
		if err != nil {
			t.Fatalf("quote %+v: %v", in, err)
		}
		if q.ProviderFee.Source == "configured" {
			t.Fatalf("%+v priced from a payout corridor", in)
		}
	}
}
