package services

import (
	"context"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestCompanyWalletFloats_ReportFreeStockAndGas(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 60_000_000)
	seedTreasuryIntent(t, s, userID, models.ITStatusPaymentReceived) // 7.232937 reserved
	f := s.CompanyWalletFloats()
	if !f.Enabled() {
		t.Fatal("enabled")
	}
	floats, err := f.FloatBalances(context.Background())
	if err != nil || len(floats) != 1 {
		t.Fatalf("floats = %+v err = %v", floats, err)
	}
	got := floats[0]
	if got.Chain != "base" || got.Address != sponsorTestSigner || got.BalanceWei == nil || got.FloorWei == nil {
		t.Fatalf("native = %+v", got)
	}
	if usd := got.Tokens["USDC"]; usd < 52.767 || usd > 52.768 {
		t.Fatalf("free USDC = %v, want 60 − 7.232937", usd)
	}
	dormant, _ := newRoutedBuyService(t)
	if dormant.CompanyWalletFloats().Enabled() {
		t.Fatal("no wallet must read as disabled")
	}
}

func TestInboundHoldNotifier_NotifyStockLow(t *testing.T) {
	mailer := &fakeAlertMailer{}
	n := NewInboundHoldNotifier(mailer, []string{"ops@tsa.test"}, "https://admin.tsa.test")
	a := StockLowAlert{Chain: "base", Asset: "USDC", Address: "0xabc", Have: "41.20 USDC free", Threshold: "$50.00"}
	if err := n.NotifyStockLow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	a.Recovered, a.Have = true, "250.00 USDC free"
	_ = n.NotifyStockLow(context.Background(), a)
	n.Flush()
	sent := mailer.records()
	if len(sent) != 2 {
		t.Fatalf("sent = %d", len(sent))
	}
	if !strings.Contains(sent[0].subject, "Company wallet stock low") || !strings.Contains(sent[0].message, "0xabc") ||
		!strings.Contains(sent[0].message, "41.20 USDC free") || sent[0].ctaURL != "https://admin.tsa.test/company-wallet" {
		t.Fatalf("low = %+v", sent[0])
	}
	if !strings.Contains(sent[1].subject, "refilled") {
		t.Fatalf("recovered = %+v", sent[1])
	}
}
