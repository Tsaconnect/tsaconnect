package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// newTreasuryBuyService is the routed-buy fixture with a company wallet
// holding stockUnits of USDC on Base.
func newTreasuryBuyService(t *testing.T, stockUnits int64) (*ITService, uuid.UUID, *fakeSponsorNative) {
	t.Helper()
	s, userID := newRoutedBuyService(t)
	w, native, _ := newTestCompanyWallet(t, stockUnits)
	s.SetCompanyWallet(w)
	return s, userID, native
}

// treasuryTestQuote is the company wallet's worked example: ₦10,000 → 7.27815 USDC.
func treasuryTestQuote(t *testing.T, s *ITService) *StableBuyQuote {
	t.Helper()
	q, err := s.QuoteTreasuryBuy("USDC", "10000", "NGN", treasuryMarket)
	if err != nil {
		t.Fatalf("treasury quote: %v", err)
	}
	return q
}

func reservedNow(t *testing.T, s *ITService) int64 {
	t.Helper()
	r, err := s.reservedTreasuryUnits(context.Background(), s.db, "base", "USDC", uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	return r.Int64()
}

func TestOpenTreasuryBuy_WritesAReservedHonouredBuy(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 50_000_000)
	ctx := context.Background()
	intent, pay, err := s.OpenTreasuryBuy(ctx, userID, treasuryTestQuote(t, s), "base", testRoutedEVM)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if intent.Provider != models.ITProviderTreasury || intent.DestChain != "BASE" || intent.DestAddress != testRoutedEVM ||
		intent.AssetAmount != "7278150" || intent.LockedRate != "137398" || intent.RateExpiresAt == nil {
		t.Fatalf("intent = %+v", intent)
	}
	if pay == nil || pay.AccountNumber != "1234567890" {
		t.Fatalf("pay into = %+v", pay)
	}
	if got := reservedNow(t, s); got != 7_278_150 {
		t.Fatalf("reserved = %d", got)
	}
	free, err := s.TreasuryFreeUnits(ctx, "base", "USDC")
	if err != nil || free.Int64() != 50_000_000-7_278_150 {
		t.Fatalf("free = %v %v", free, err)
	}
}

// §13 Q3: no IT agreement for the company wallet; KYC still.
func TestOpenTreasuryBuy_NeedsKYCButNotTheAgreement(t *testing.T) {
	s, _, _ := newTreasuryBuyService(t, 50_000_000)
	ctx := context.Background()
	noAgreement := seedVerifiedUser(t, s.db)
	// The fixture only gave the first user a virtual account; this one pays into
	// one too, so the open is refused (or not) on KYC and the agreement alone.
	if err := s.db.Create(&models.FiatVirtualAccount{
		ID: uuid.New(), UserID: noAgreement, Currency: "NGN",
		AccountNumber: "1234567891", BankName: "Wema Bank", AccountName: "TSA / Ada Okafor",
		Reference: "ITVA-" + noAgreement.String(),
	}).Error; err != nil {
		t.Fatalf("seed va: %v", err)
	}
	if _, _, err := s.OpenTreasuryBuy(ctx, noAgreement, treasuryTestQuote(t, s), "base", testRoutedEVM); err != nil {
		t.Fatalf("verified, no agreement: %v", err)
	}
	pending := seedUserAt(t, s.db, uuid.New(), models.VerificationStatusPending)
	if _, _, err := s.OpenTreasuryBuy(ctx, pending, treasuryTestQuote(t, s), "base", testRoutedEVM); !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("unverified: %v", err)
	}
}

func TestOpenTreasuryBuy_RefusesBeforeWriting(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 50_000_000)
	ctx := context.Background()
	q := treasuryTestQuote(t, s)
	for name, tc := range map[string]struct {
		chain, dest string
		q           *StableBuyQuote
		want        error
	}{
		"chain without a lane":   {"polygon", testRoutedEVM, q, ErrUnsupportedChainAsset},
		"token without decimals": {"base", testRoutedEVM, &StableBuyQuote{Asset: "USDT", Currency: "NGN", FiatMinor: q.FiatMinor, AssetUnits: q.AssetUnits, RatePerUnitMinor: q.RatePerUnitMinor}, ErrUnsupportedChainAsset},
		"not an EVM address":     {"base", "0xnot-an-address", q, ErrBadDeliveryAddress},
		"above $200":             {"base", testRoutedEVM, &StableBuyQuote{Asset: "USDC", Currency: "NGN", FiatMinor: big.NewInt(27_655_400), AssetUnits: big.NewInt(200_029_784), RatePerUnitMinor: big.NewInt(138257)}, ErrStableBuyAboveMaximum},
	} {
		if _, _, err := s.OpenTreasuryBuy(ctx, userID, tc.q, tc.chain, tc.dest); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", name, err, tc.want)
		}
	}
	dormant, user2 := newRoutedBuyService(t)
	if _, _, err := dormant.OpenTreasuryBuy(ctx, user2, q, "base", testRoutedEVM); !errors.Is(err, ErrTreasuryUnavailable) {
		t.Fatalf("no wallet: %v", err)
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d intents written by refused opens", n)
	}
}

// S4: a concurrent open that won the last stock leaves this one short — no write.
func TestOpenTreasuryBuy_StockShortUnderTheLockWritesNothing(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 7_000_000)
	logs := captureLog(t)
	_, _, err := s.OpenTreasuryBuy(context.Background(), userID, treasuryTestQuote(t, s), "base", testRoutedEVM)
	if !errors.Is(err, ErrTreasuryStockShort) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(logs.String(), "ramp_treasury_stock_short_at_open chain=base token=USDC") {
		t.Fatalf("log = %q", logs.String())
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatal("wrote an order the stock cannot cover")
	}
}

// §5.3 reservation table: released on cancel, on expiry of the honour window
// (time only, no write), on park, on settle and on refund; kept while paid.
func TestTreasuryReservation_LifeCycle(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 50_000_000)
	ctx := context.Background()
	a, _, err := s.OpenTreasuryBuy(ctx, userID, treasuryTestQuote(t, s), "base", testRoutedEVM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelBuy(ctx, userID, a.ID); err != nil {
		t.Fatal(err)
	}
	if got := reservedNow(t, s); got != 0 {
		t.Fatalf("after cancel: %d", got)
	}
	b := seedTreasuryIntent(t, s, seedVerifiedUser(t, s.db), models.ITStatusPendingPayment)
	set := func(cols map[string]any) {
		t.Helper()
		if err := s.db.Model(&models.ITIntent{}).Where("id = ?", b.ID).Updates(cols).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		cols map[string]any
		want int64
	}{
		{"open, inside the window", map[string]any{"rate_expires_at": time.Now().Add(time.Minute)}, 7_278_150},
		{"window over, inside the grace", map[string]any{"rate_expires_at": time.Now().Add(-9 * time.Minute)}, 7_278_150},
		{"past window + grace (no write needed)", map[string]any{"rate_expires_at": time.Now().Add(-11 * time.Minute)}, 0},
		{"paid", map[string]any{"status": models.ITStatusPaymentReceived}, 7_278_150},
		{"delivering", map[string]any{"status": models.ITStatusDelivering}, 7_278_150},
		{"parked", map[string]any{"status": models.ITStatusBuyAwaitingRate}, 0},
		{"refunded", map[string]any{"status": models.ITStatusRefunded}, 0},
		{"settled", map[string]any{"status": models.ITStatusSettled}, 0},
	} {
		set(tc.cols)
		if got := reservedNow(t, s); got != tc.want {
			t.Fatalf("%s: reserved = %d, want %d", tc.name, got, tc.want)
		}
	}
	if r, _ := s.reservedTreasuryUnits(ctx, s.db, "base", "USDC", b.ID); r.Sign() != 0 {
		t.Fatal("exclude did not exclude")
	}
}

// §11: an operator withdrawal → free clamps at 0 and alerts.
func TestTreasuryFreeStock_ClampsAtZeroAndAlerts(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 1_000_000)
	seedTreasuryIntent(t, s, userID, models.ITStatusPaymentReceived)
	logs := captureLog(t)
	free, bal, err := s.freeTreasuryStock(context.Background(), s.db, "base", "USDC", uuid.Nil, true)
	if err != nil || free.Sign() != 0 || bal.Int64() != 1_000_000 {
		t.Fatalf("free=%v bal=%v err=%v", free, bal, err)
	}
	if !strings.Contains(logs.String(), "ALERT ramp_treasury_stock_overcommitted chain=base token=USDC") {
		t.Fatalf("log = %q", logs.String())
	}
}

// Review minor: the public quote path reads free stock at most once per 5s
// per token; the money paths (open, claim, send) never use this cache.
func TestTreasuryFreeUnits_CachedFiveSeconds(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 50_000_000)
	clock := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	s.CompanyWallet().now = func() time.Time { return clock }
	ctx := context.Background()
	if free, err := s.TreasuryFreeUnits(ctx, "base", "USDC"); err != nil || free.Int64() != 50_000_000 {
		t.Fatalf("first read: %v %v", free, err)
	}
	seedTreasuryIntent(t, s, userID, models.ITStatusDelivering) // reserves 7.27815
	clock = clock.Add(4 * time.Second)
	if free, _ := s.TreasuryFreeUnits(ctx, "base", "USDC"); free.Int64() != 50_000_000 {
		t.Fatalf("inside 5s: %v, want the cached reading", free)
	}
	clock = clock.Add(2 * time.Second)
	if free, _ := s.TreasuryFreeUnits(ctx, "base", "USDC"); free.Int64() != 50_000_000-7_278_150 {
		t.Fatalf("after 5s: %v, want the reservation counted", free)
	}
}

// §13 Q2 / D59: $500 per rolling 24h, over buys that were not cancelled or refunded.
func TestCheckTreasuryDailyCap(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 50_000_000)
	ctx := context.Background()
	big495 := seedTreasuryIntent(t, s, userID, models.ITStatusSettled)
	s.db.Model(big495).Update("asset_amount", "495000000")
	for _, st := range []string{models.ITStatusCancelled, models.ITStatusRefunded} {
		i := seedTreasuryIntent(t, s, userID, st)
		s.db.Model(i).Update("asset_amount", "400000000")
	}
	if err := s.CheckTreasuryDailyCap(ctx, userID, big.NewInt(5_000_000)); err != nil {
		t.Fatalf("495 + 5 = 500: %v", err)
	}
	if err := s.CheckTreasuryDailyCap(ctx, userID, big.NewInt(5_000_001)); !errors.Is(err, ErrTreasuryDailyCapReached) {
		t.Fatalf("over 500: %v", err)
	}
	s.db.Model(big495).Update("created_at", time.Now().Add(-25*time.Hour))
	if err := s.CheckTreasuryDailyCap(ctx, userID, big.NewInt(200_000_000)); err != nil {
		t.Fatalf("yesterday's buy still counted: %v", err)
	}
	if err := s.CheckTreasuryDailyCap(ctx, seedVerifiedUser(t, s.db), big.NewInt(200_000_000)); err != nil {
		t.Fatalf("another user: %v", err)
	}
}

func intentCount(s *ITService) int64 {
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	return n
}

// An exported money-writing function enforces its own limits: a hand-built
// quote that skipped QuoteTreasuryBuy is refused, and nothing is written.
func TestOpenTreasuryBuy_EnforcesItsOwnLimits(t *testing.T) {
	ctx := context.Background()

	s, userID, _ := newTreasuryBuyService(t, 50_000_000)
	below := &StableBuyQuote{Asset: "USDC", Currency: "NGN", FiatMinor: big.NewInt(5_000), AssetUnits: big.NewInt(1_000_000), RatePerUnitMinor: big.NewInt(138257)}
	if _, _, err := s.OpenTreasuryBuy(ctx, userID, below, "base", testRoutedEVM); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("below the naira minimum: %v", err)
	}
	if n := intentCount(s); n != 0 {
		t.Fatalf("%d rows written below the minimum", n)
	}

	big495 := seedTreasuryIntent(t, s, userID, models.ITStatusSettled)
	s.db.Model(big495).Update("asset_amount", "495000000")
	before := intentCount(s)
	if _, _, err := s.OpenTreasuryBuy(ctx, userID, treasuryTestQuote(t, s), "base", testRoutedEVM); !errors.Is(err, ErrTreasuryDailyCapReached) {
		t.Fatalf("over the daily cap: %v", err)
	}
	if n := intentCount(s); n != before {
		t.Fatalf("%d rows written over the daily cap", n-before)
	}

	w0, _, _ := newTestCompanyWallet(t, 50_000_000)
	closed := NewCompanyWallet(CompanyWalletConfig{
		Treasury: w0.Treasury(), Decimals: map[string]map[string]int{"base": {"USDC": 6}},
		ReceiptsOK: map[string]bool{"base": false},
	})
	s2, user2 := newRoutedBuyService(t)
	s2.SetCompanyWallet(closed)
	if _, _, err := s2.OpenTreasuryBuy(ctx, user2, treasuryTestQuote(t, s), "base", testRoutedEVM); !errors.Is(err, ErrTreasuryUnavailable) {
		t.Fatalf("receipts unreadable: %v", err)
	}
	if n := intentCount(s2); n != 0 {
		t.Fatalf("%d rows written on an unreadable chain", n)
	}
}

// A hand-built lowercase asset or chain must still be reserved.
func TestInsertTreasuryIntent_NormalisesSoItIsReserved(t *testing.T) {
	s, userID, _ := newTreasuryBuyService(t, 50_000_000)
	i := seedTreasuryIntent(t, s, userID, models.ITStatusPendingPayment)
	s.db.Delete(i)
	i.ID = uuid.New()
	i.Asset, i.DestChain = "usdc", "base"
	if err := s.insertTreasuryIntent(context.Background(), i); err != nil {
		t.Fatal(err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", i.ID)
	if got.Asset != "USDC" || got.DestChain != "BASE" {
		t.Fatalf("stored %q %q", got.Asset, got.DestChain)
	}
	if r := reservedNow(t, s); r != 7_278_150 {
		t.Fatalf("reserved = %d, the lowercase order was not reserved", r)
	}
}

func TestITService_DormantWalletAccessorsAreNilSafe(t *testing.T) {
	s, _ := newRoutedBuyService(t)
	if s.TreasuryChains() != nil || s.TreasuryTokens("base") != nil || s.TreasuryCanPayGas(context.Background(), "base") {
		t.Fatal("a dormant wallet must offer nothing")
	}
}
