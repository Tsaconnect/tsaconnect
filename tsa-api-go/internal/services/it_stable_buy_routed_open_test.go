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

// seedUserAt's wallet_address — where a routed EVM buy is delivered.
const testRoutedEVM = "0x000000000000000000000000000000000000dEaD"

// newRoutedBuyService is the stable-buy fixture (VA mode, verified user, rate
// 1650.25, fx 1500) plus the tables a routed open reads, with the user's IT
// agreement accepted.
func newRoutedBuyService(t *testing.T) (*ITService, uuid.UUID) {
	t.Helper()
	s, userID := newStableBuyDBService(t)
	if err := s.db.AutoMigrate(&models.WalletAddress{}, &models.ITAgreementAcceptance{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	acceptITAgreement(t, s, userID)
	// The pricing fixtures below were all worked at the central account's
	// 1.40% funding fee; this rig has no central account (VA mode), so the VA
	// fee is pinned to the same figure. Channel pricing itself — 1.40% central,
	// 2.15% VA — is pinned in it_stable_buy_funding_channel_test.go.
	pinFundingFee(t, s)
	return s, userID
}

func acceptITAgreement(t *testing.T, s *ITService, userID uuid.UUID) {
	t.Helper()
	if err := s.AcceptITAgreement(context.Background(), userID, "127.0.0.1", "test"); err != nil {
		t.Fatalf("accept agreement: %v", err)
	}
}

// routedTestQuote is the Flutterwave worked example: ₦165,025 → 95.137885 USDC.
func routedTestQuote(t *testing.T, s *ITService) *StableBuyQuote {
	t.Helper()
	q, err := s.QuoteRoutedStableBuy("USDC", "165025", "NGN", big.NewRat(165025, 100))
	if err != nil {
		t.Fatalf("routed quote: %v", err)
	}
	return q
}

// seedTreasuryIntent writes a company-wallet buy directly: the company
// wallet's worked example (₦10,000 → 7.27815 USDC on Base).
func seedTreasuryIntent(t *testing.T, s *ITService, userID uuid.UUID, status string) *models.ITIntent {
	t.Helper()
	until := time.Now().Add(15 * time.Minute)
	i := &models.ITIntent{
		ID: uuid.New(), UserID: userID, Direction: models.ITDirectionBuy, Status: status,
		Provider: models.ITProviderTreasury, Asset: models.ITAssetUSDC, AssetAmount: "7278150",
		Currency: "NGN", DestChain: "BASE", DestAddress: testRoutedEVM,
		LockedRate: "137398", DisplayRate: "135474", RateExpiresAt: &until,
		NgnKobo: "1000000", AmountMinor: "1000000", RateKobo: "137398", RateMinor: "137398",
		McgpAmount: "0", FeeNgnKobo: "0", NetNgnKobo: "1000000", PlatformKeptKobo: "0", ReceivedAmount: "0",
	}
	if err := s.db.Create(i).Error; err != nil {
		t.Fatalf("seed treasury intent: %v", err)
	}
	return i
}

func TestITIntentProvider_ReadsLegacyRowsAsFlutterwave(t *testing.T) {
	if models.ITIntentProvider(&models.ITIntent{}) != models.ITProviderFlutterwave ||
		models.ITIntentProvider(nil) != models.ITProviderFlutterwave ||
		models.ITIntentProvider(&models.ITIntent{Provider: "treasury"}) != models.ITProviderTreasury {
		t.Fatal("provider reader")
	}
}

func TestOpenRoutedStableBuy_HonoursTheQuoteAndKeepsItsBreakdown(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	before := time.Now()
	intent, pay, err := s.OpenRoutedStableBuy(context.Background(), userID, routedTestQuote(t, s), "polygon")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if intent.RateExpiresAt == nil || intent.RateExpiresAt.Before(before.Add(14*time.Minute)) ||
		intent.RateExpiresAt.After(time.Now().Add(16*time.Minute)) {
		t.Fatalf("rate_expires_at = %v, want ~now+15m (IT_STABLE_RATE_LOCK_SECONDS)", intent.RateExpiresAt)
	}
	if intent.LockedRate != "173459" || intent.AssetAmount != "95137885" || intent.AmountMinor != "16502500" {
		t.Fatalf("locked=%s asset=%s amount=%s", intent.LockedRate, intent.AssetAmount, intent.AmountMinor)
	}
	if intent.Provider != models.ITProviderFlutterwave || intent.DestChain != "POLYGON" || intent.DestAddress != testRoutedEVM {
		t.Fatalf("provider=%s delivery=%s %s", intent.Provider, intent.DestChain, intent.DestAddress)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if StableBuyDisplayRate(&got) != "165850" {
		t.Fatalf("display rate = %q, want 165850 (spec §14: stored with the intent)", StableBuyDisplayRate(&got))
	}
	if fees := StableBuyFees(&got); len(fees) != 3 || fees[0].Units != "1393035" || fees[1].Units != "1471642" || fees[2].Units != "1500000" {
		t.Fatalf("fees = %+v", fees)
	}
	if pay.AccountNumber != "1234567890" {
		t.Fatalf("pay into = %+v", pay)
	}
	if !IsRoutedStableBuy(intent) || StableBuyHonourUntil(intent) == nil || StableBuyRateUpdated(intent) {
		t.Fatal("a freshly opened routed buy is routed, held, not re-priced")
	}
}

// Spec §3: the old endpoint keeps its NGN routing check and now names its
// provider; the router path has no routing check.
func TestOpenRoutedStableBuy_SkipsTheRoutingCheckTheOldPathKeeps(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	s.SetCurrencyRegistry(NewCurrencyRegistry(newCurrencyTestDB(t))) // NGN → paycrest, the prod shape
	if _, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM); !errors.Is(err, ErrStableBuyWrongRamp) {
		t.Fatalf("old path: %v, want ErrStableBuyWrongRamp", err)
	}
	if _, _, err := s.OpenRoutedStableBuy(context.Background(), userID, routedTestQuote(t, s), "POLYGON"); err != nil {
		t.Fatalf("routed path: %v", err)
	}
}

func TestOpenRoutedStableBuy_UsesTheRoutedFloorNotTheSellFloor(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	if err := s.SetStableMinSell("100"); err != nil {
		t.Fatal(err)
	}
	tiny := routedTestQuote(t, s)
	tiny.AssetUnits = big.NewInt(9_000_000)
	if _, _, err := s.OpenRoutedStableBuy(context.Background(), userID, tiny, "POLYGON"); !errors.Is(err, ErrStableBuyBelowMinimum) {
		t.Fatalf("9 tokens: %v, want under the routed 10", err)
	}
	if _, _, err := s.OpenRoutedStableBuy(context.Background(), userID, routedTestQuote(t, s), "POLYGON"); err != nil {
		t.Fatalf("94.5 under a 100 sell floor: %v, want accepted", err)
	}
}

func TestOpenRoutedStableBuy_RefusesBeforeWriting(t *testing.T) {
	s, _ := newRoutedBuyService(t)
	ctx := context.Background()
	pending := seedUserAt(t, s.db, uuid.New(), models.VerificationStatusPending)
	if _, _, err := s.OpenRoutedStableBuy(ctx, pending, routedTestQuote(t, s), "POLYGON"); !errors.Is(err, ErrKYCVerificationRequired) {
		t.Fatalf("unverified: %v", err)
	}
	verified := seedVerifiedUser(t, s.db)
	if _, _, err := s.OpenRoutedStableBuy(ctx, verified, routedTestQuote(t, s), "POLYGON"); !errors.Is(err, ErrITAgreementRequired) {
		t.Fatalf("no agreement: %v", err)
	}
	acceptITAgreement(t, s, verified)
	for _, chain := range []string{"SOLANA", "ETHEREUM", "BASE"} {
		if _, _, err := s.OpenRoutedStableBuy(ctx, verified, routedTestQuote(t, s), chain); !errors.Is(err, ErrUnsupportedChainAsset) {
			t.Fatalf("%s: %v, want Polygon only", chain, err)
		}
	}
	var n int64
	s.db.Model(&models.ITIntent{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d intents written by refused opens", n)
	}
}

// Decision D26: the refusal names the open purchase, wording unchanged —
// whichever provider holds it.
func TestStableBuyOpenRefusal_NamesTheOpenPurchase(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ctx := context.Background()
	first := seedTreasuryIntent(t, s, userID, models.ITStatusPendingPayment)
	for name, open := range map[string]func() error{
		"routed": func() error {
			_, _, err := s.OpenRoutedStableBuy(ctx, userID, routedTestQuote(t, s), "POLYGON")
			return err
		},
		"old": func() error {
			_, _, err := s.CreateStableBuyIntent(ctx, userID, testBuyQuote(), "POLYGON", testBuyEVM)
			return err
		},
	} {
		err := open()
		var named *StableBuyOpenError
		if !errors.As(err, &named) || named.IntentID != first.ID {
			t.Fatalf("%s: err = %v, want StableBuyOpenError naming %s", name, err, first.ID)
		}
		if !errors.Is(err, ErrStableBuyAlreadyOpen) || err.Error() != ErrStableBuyAlreadyOpen.Error() {
			t.Fatalf("%s: wording changed: %q", name, err.Error())
		}
	}
}

func TestOpenStableBuy_ExpiresStaleBeforeReporting(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ctx := context.Background()
	intent, _, err := s.OpenRoutedStableBuy(ctx, userID, routedTestQuote(t, s), "POLYGON")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if open, err := s.OpenStableBuy(ctx, userID); err != nil || open == nil || open.ID != intent.ID {
		t.Fatalf("open = %v err = %v, want the fresh purchase", open, err)
	}
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).
		Update("created_at", time.Now().Add(-25*time.Hour)).Error; err != nil {
		t.Fatalf("age it: %v", err)
	}
	logs := captureLog(t)
	if open, err := s.OpenStableBuy(ctx, userID); err != nil || open != nil {
		t.Fatalf("open = %v err = %v, want nil after expiry", open, err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.Status != models.ITStatusCancelled {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	if !strings.Contains(logs.String(), "ramp_it_open_unpaid intent="+intent.ID.String()) ||
		!strings.Contains(logs.String(), "provider=flutterwave reason=expired") {
		t.Fatalf("log = %q", logs.String())
	}
}

// Spec §7: the abandon cap is shared — both providers' routed cancels count,
// an old-path cancel does not.
func TestCountAbandonedRoutedStableBuys_CountsBothProvidersOnly(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ctx := context.Background()
	routed, _, err := s.OpenRoutedStableBuy(ctx, userID, routedTestQuote(t, s), "POLYGON")
	if err != nil {
		t.Fatalf("routed: %v", err)
	}
	if _, err := s.CancelBuy(ctx, userID, routed.ID); err != nil {
		t.Fatalf("cancel routed: %v", err)
	}
	tr := seedTreasuryIntent(t, s, userID, models.ITStatusPendingPayment)
	if _, err := s.CancelBuy(ctx, userID, tr.ID); err != nil {
		t.Fatalf("cancel treasury: %v", err)
	}
	old, _, err := s.CreateStableBuyIntent(ctx, userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("old: %v", err)
	}
	if _, err := s.CancelBuy(ctx, userID, old.ID); err != nil {
		t.Fatalf("cancel old: %v", err)
	}
	if n, err := s.CountAbandonedRoutedStableBuys(ctx, userID, time.Now().Add(-24*time.Hour)); err != nil || n != 2 {
		t.Fatalf("count = %d err = %v, want 2", n, err)
	}
	if n, _ := s.CountAbandonedRoutedStableBuys(ctx, seedVerifiedUser(t, s.db), time.Now().Add(-24*time.Hour)); n != 0 {
		t.Fatalf("another user's count = %d", n)
	}
}

// D1/D35: the old path names its provider and never sets the routed marker.
func TestCreateStableBuyIntent_NeverSetsTheRoutedMarker(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	intent, _, err := s.CreateStableBuyIntent(context.Background(), userID, testBuyQuote(), "POLYGON", testBuyEVM)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var got models.ITIntent
	s.db.First(&got, "id = ?", intent.ID)
	if got.RateExpiresAt != nil || IsRoutedStableBuy(&got) || got.Provider != models.ITProviderFlutterwave {
		t.Fatalf("rate_expires_at = %v provider = %q on an old-path buy", got.RateExpiresAt, got.Provider)
	}
}

func TestCancelBuy_LogsAnUnpaidRoutedBuy(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ctx := context.Background()
	intent := seedTreasuryIntent(t, s, userID, models.ITStatusPendingPayment)
	logs := captureLog(t)
	if _, err := s.CancelBuy(ctx, userID, intent.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !strings.Contains(logs.String(), "ramp_it_open_unpaid intent="+intent.ID.String()) ||
		!strings.Contains(logs.String(), "provider=treasury reason=cancelled") {
		t.Fatalf("log = %q", logs.String())
	}
}

// Spec §4.2: reads are filtered by user AND provider.
func TestGetStableBuyForUser_ScopedByUserAndProvider(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ctx := context.Background()
	intent, _, err := s.OpenRoutedStableBuy(ctx, userID, routedTestQuote(t, s), "POLYGON")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.GetStableBuyForUser(ctx, seedVerifiedUser(t, s.db), intent.ID, models.ITProviderFlutterwave); !errors.Is(err, ErrStableIntentNotFound) {
		t.Fatalf("foreign: %v", err)
	}
	if _, err := s.GetStableBuyForUser(ctx, userID, intent.ID, models.ITProviderTreasury); !errors.Is(err, ErrStableIntentNotFound) {
		t.Fatalf("other provider: %v", err)
	}
	got, err := s.GetStableBuyForUser(ctx, userID, intent.ID, models.ITProviderFlutterwave)
	if err != nil || got.ID != intent.ID {
		t.Fatalf("own: %v %v", got, err)
	}
	pay, err := s.StableBuyPayInto(ctx, got)
	if err != nil || pay == nil || pay.AccountNumber != "1234567890" {
		t.Fatalf("pay into = %+v err = %v", pay, err)
	}
	got.Status = models.ITStatusPaymentReceived
	if pay, err := s.StableBuyPayInto(ctx, got); err != nil || pay != nil {
		t.Fatalf("after payment: %+v %v, want nil — never invite a second transfer", pay, err)
	}
}

// Spec §5.4: a company-wallet buy can never be converted again at
// Flutterwave with the user's naira.
func TestTreasuryRowsStayOffTheFlutterwaveRail(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	ctx := context.Background()
	received := seedTreasuryIntent(t, s, userID, models.ITStatusPaymentReceived)
	if err := s.ConvertStableBuy(ctx, received.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("convert: %v, want ErrITBadState", err)
	}
	delivering := seedTreasuryIntent(t, s, seedVerifiedUser(t, s.db), models.ITStatusDelivering)
	if err := s.DeliverStableBuy(ctx, delivering.ID); !errors.Is(err, ErrITBadState) {
		t.Fatalf("deliver: %v, want ErrITBadState", err)
	}
	r, a, c, d, err := s.PendingStableBuys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r)+len(a)+len(c)+len(d) != 0 {
		t.Fatalf("Flutterwave lists carry company-wallet rows: %v %v %v %v", r, a, c, d)
	}
}
