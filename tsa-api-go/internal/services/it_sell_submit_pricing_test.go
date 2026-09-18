package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The sell's pricing moved into priceSellAt (shared with the pre-trade quote).
// These pin the submit path end to end: it prices exactly as the quote does,
// and every pricing refusal happens BEFORE the on-chain debit and before any
// intent row exists.

func assertNothingMoved(t *testing.T, f *sellFixture, userID uuid.UUID) {
	t.Helper()
	if f.chain.debitCalls != 0 {
		t.Fatalf("%d on-chain debits for a refused sell", f.chain.debitCalls)
	}
	var n int64
	f.s.db.Model(&models.ITIntent{}).Where("user_id = ?", userID).Count(&n)
	if n != 0 {
		t.Fatalf("%d intents recorded for a refused sell", n)
	}
}

func TestCreateSell_RecordsTheQuotedPricing(t *testing.T) {
	f := newSellFixture(t)
	ctx := context.Background()
	amount, _ := new(big.Int).SetString(hundredMCGP, 10)
	want, err := f.s.QuoteSell(ctx, amount, "")
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	intent, err := f.s.CreateSellIntent(ctx, uuid.New(), sellWallet, hundredMCGP, sellBank())
	if err != nil {
		t.Fatalf("sell: %v", err)
	}
	if intent.NgnKobo != want.Fees.Gross.String() || intent.RateKobo != want.RateKobo.String() ||
		intent.FeeNgnKobo != want.Fees.Fee.String() || intent.NetNgnKobo != want.Fees.Net.String() ||
		intent.PlatformKeptKobo != want.Fees.PlatformKept.String() {
		t.Fatalf("intent gross/rate/fee/net/kept = %s/%s/%s/%s/%s, quote %s/%s/%s/%s/%s",
			intent.NgnKobo, intent.RateKobo, intent.FeeNgnKobo, intent.NetNgnKobo, intent.PlatformKeptKobo,
			want.Fees.Gross, want.RateKobo, want.Fees.Fee, want.Fees.Net, want.Fees.PlatformKept)
	}
	// 100 MCGP at 2850 kobo is ₦2,850 gross — the fixture's own arithmetic.
	if intent.NgnKobo != "285000" {
		t.Fatalf("gross = %s kobo, want 285000", intent.NgnKobo)
	}
}

func TestCreateSell_BelowFloorRefusedBeforeDebit(t *testing.T) {
	f := newSellFixture(t)
	userID := uuid.New()
	two := new(big.Int).Mul(big.NewInt(2), oneE18).String() // ₦57 gross, under ₦100
	_, err := f.s.CreateSellIntent(context.Background(), userID, sellWallet, two, sellBank())
	if err == nil || !strings.Contains(err.Error(), "minimum payout") {
		t.Fatalf("err = %v, want the minimum-payout refusal", err)
	}
	assertNothingMoved(t, f, userID)
}

func TestCreateSell_StaleFxRefusedBeforeDebit(t *testing.T) {
	f := newSellFixture(t)
	f.s.fx = stubFx{ngn: 1500, stale: true}
	userID := uuid.New()
	_, err := f.s.CreateSellIntent(context.Background(), userID, sellWallet, hundredMCGP, sellBank())
	if !errors.Is(err, ErrITFxUnavailable) {
		t.Fatalf("err = %v, want ErrITFxUnavailable", err)
	}
	assertNothingMoved(t, f, userID)
}

func TestCreateSell_UnpayableCurrencyRefusedBeforeDebit(t *testing.T) {
	f := newSellFixture(t)
	db := newCurrencyTestDB(t)
	reg := NewCurrencyRegistry(db) // seeds the default rows
	if err := db.Model(&models.Currency{}).Where("code = ?", "GHS").Update("payout_enabled", false).Error; err != nil {
		t.Fatalf("disable GHS: %v", err)
	}
	if err := reg.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	f.s.SetCurrencyRegistry(reg)
	userID := uuid.New()
	bank := sellBank()
	bank.DestinationCurrency = "GHS"
	bank.RailType = "bank"
	_, err := f.s.CreateSellIntent(context.Background(), userID, sellWallet, hundredMCGP, bank)
	if !errors.Is(err, ErrCurrencyNotPayable) {
		t.Fatalf("err = %v, want ErrCurrencyNotPayable", err)
	}
	assertNothingMoved(t, f, userID)
}

func TestCreateSell_FeeEngineDownRefusedBeforeDebit(t *testing.T) {
	f := newSellFixture(t)
	f.s.SetFeeQuoter(nil)
	userID := uuid.New()
	_, err := f.s.CreateSellIntent(context.Background(), userID, sellWallet, hundredMCGP, sellBank())
	if !errors.Is(err, ErrITFeeQuoteUnavailable) {
		t.Fatalf("err = %v, want ErrITFeeQuoteUnavailable", err)
	}
	assertNothingMoved(t, f, userID)
}
