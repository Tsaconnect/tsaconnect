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

type fixedTreasuryRate struct {
	rate *big.Rat
	err  error
}

func (f *fixedTreasuryRate) BuyRate(context.Context, string) (*big.Rat, error) { return f.rate, f.err }

// newTreasurySettleService opens the company wallet's worked example
// (₦10,000 → 7.232937 USDC on Base) with stockUnits in the wallet.
func newTreasurySettleService(t *testing.T, stockUnits int64) (*ITService, uuid.UUID, *models.ITIntent, *fakeSponsorNative) {
	t.Helper()
	s, userID, native := newTreasuryBuyService(t, stockUnits)
	s.SetTreasuryMarketRate(&fixedTreasuryRate{rate: treasuryMarket})
	if err := s.db.AutoMigrate(&models.ITIntentAuditLog{}); err != nil {
		t.Fatal(err)
	}
	intent, _, err := s.OpenTreasuryBuy(context.Background(), userID, treasuryTestQuote(t, s), "base", testRoutedEVM)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, userID, intent, native
}

func setStock(native *fakeSponsorNative, units int64) {
	native.tokenBalances[strings.ToLower(sponsorTestUSDC)] = big.NewInt(units)
}

// §5.2: an honoured company-wallet payment needs no stock check — its
// reservation is still live.
func TestTreasurySettle_HonouredNeedsNoStockCheck(t *testing.T) {
	s, userID, intent, native := newTreasurySettleService(t, 50_000_000)
	setStock(native, 0) // even an emptied wallet does not un-honour her
	logs := captureLog(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_t_exact", "1000000"); err != nil {
		t.Fatal(err)
	}
	got := reloadIntent(t, s, intent.ID)
	if got.Status != models.ITStatusPaymentReceived || got.AssetAmount != "7232937" || got.Repriced {
		t.Fatalf("status=%s asset=%s repriced=%v", got.Status, got.AssetAmount, got.Repriced)
	}
	if !strings.Contains(logs.String(), "ramp_treasury_honoured intent="+intent.ID.String()) {
		t.Fatalf("log = %q", logs.String())
	}
}

func TestTreasurySettle_OffAmountIsPricedLiveAtMarketPlusMargin(t *testing.T) {
	s, userID, intent, _ := newTreasurySettleService(t, 50_000_000)
	told := watchRateUpdates(t, s)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_t_off", "900000"); err != nil {
		t.Fatal(err)
	}
	want, _ := s.treasuryQuoteAt("USDC", "NGN", big.NewInt(900_000), treasuryMarket, true)
	got := reloadIntent(t, s, intent.ID)
	if got.Status != models.ITStatusPaymentReceived || got.AssetAmount != want.AssetUnits.String() || !got.Repriced {
		t.Fatalf("status=%s asset=%s, want %s live", got.Status, got.AssetAmount, want.AssetUnits)
	}
	expectTold(t, told, userID, want.AssetUnits)
}

// Review Focus 4: an overpayment above $200 parks; nothing sends it.
func TestTreasurySettle_LiveAboveTheMaximumParks(t *testing.T) {
	s, userID, intent, _ := newTreasurySettleService(t, 500_000_000)
	logs := captureLog(t)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_t_big", "30000000"); err != nil {
		t.Fatal(err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusBuyAwaitingRate || got.AssetAmount != "7232937" {
		t.Fatalf("status=%s asset=%s, want parked with the quote untouched", got.Status, got.AssetAmount)
	}
	if !strings.Contains(logs.String(), "ramp_treasury_parked intent="+intent.ID.String()+" reason=maximum") {
		t.Fatalf("log = %q", logs.String())
	}
}

// Review Focus 4: live within the maximum but beyond free stock parks.
func TestTreasurySettle_LiveBeyondFreeStockParks(t *testing.T) {
	s, userID, intent, _ := newTreasurySettleService(t, 8_232_937)
	logs := captureLog(t)
	// ₦12,000 → 8.679525 USDC live > 8.232937 free (own reservation excluded).
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_t_stock", "1200000"); err != nil {
		t.Fatal(err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %s, want parked", got.Status)
	}
	if !strings.Contains(logs.String(), "reason=stock") {
		t.Fatalf("log = %q", logs.String())
	}
}

// §5.2: live pricing excludes the order's own reservation — with stock
// exactly its own 7.232937, a slightly smaller live amount still fits.
func TestTreasurySettle_LivePricingExcludesItsOwnReservation(t *testing.T) {
	s, userID, intent, _ := newTreasurySettleService(t, 7_232_937)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_t_own", "999000"); err != nil {
		t.Fatal(err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusPaymentReceived || !got.Repriced {
		t.Fatalf("status=%s repriced=%v, want priced against its own stock", got.Status, got.Repriced)
	}
}

func TestTreasurySettle_BelowTheMinimumParks(t *testing.T) {
	s, userID, intent, _ := newTreasurySettleService(t, 50_000_000)
	if _, err := s.settleStableBuyFromCharge(context.Background(), userID, "chg_t_tiny", "5000"); err != nil {
		t.Fatal(err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatalf("status = %s", got.Status)
	}
}

func TestTreasurySettle_CentralRateOutageRetries(t *testing.T) {
	s, _, intent, _ := newTreasurySettleService(t, 50_000_000)
	s.SetTreasuryMarketRate(&fixedTreasuryRate{err: errors.New("paycrest and flutterwave down")})
	err := NewITStableClaimSource(s).Accept(context.Background(), intent.ID,
		centralCredit("100004261003120000000000000091", 900_000, time.Now()))
	if err == nil {
		t.Fatal("want an error so the poller retries")
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusPendingPayment {
		t.Fatalf("status = %s, want untouched", got.Status)
	}
}

func parkTreasury(t *testing.T, s *ITService, intent *models.ITIntent, received string) {
	t.Helper()
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Updates(map[string]any{
		"status": models.ITStatusBuyAwaitingRate, "payment_ref": "chg_" + received, "received_amount": received,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestRepriceParkedStableBuy_CompanyWalletNeedsStockAndKeepsTheMaximum(t *testing.T) {
	// Open with stock (₦10,000 needs 7.232937), then let the wallet run low.
	s, _, intent, native := newTreasurySettleService(t, 50_000_000)
	setStock(native, 1_000_000)
	parkTreasury(t, s, intent, "1200000") // ₦12,000 → 8.679525 USDC
	if _, err := s.RepriceParkedStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrTreasuryStockShort) {
		t.Fatalf("short: %v", err)
	}
	if got := reloadIntent(t, s, intent.ID); got.Status != models.ITStatusBuyAwaitingRate {
		t.Fatal("a failed re-price must leave it parked")
	}
	setStock(native, 50_000_000) // refilled
	got, err := s.RepriceParkedStableBuy(context.Background(), intent.ID)
	if err != nil || got.Status != models.ITStatusPaymentReceived || got.AssetAmount != "8679525" {
		t.Fatalf("after refill: %+v %v", got, err)
	}
	parkTreasury(t, s, intent, "30000000") // ₦300,000 → above $200
	if _, err := s.RepriceParkedStableBuy(context.Background(), intent.ID); !errors.Is(err, ErrStableBuyAboveMaximum) {
		t.Fatalf("plain re-price keeps the maximum: %v", err)
	}
}

// Review Focus 4 / §13 Q9: "deliver anyway" waives the maximum, never the stock.
func TestDeliverParkedTreasuryBuy_WaivesTheMaximumButNotTheStock(t *testing.T) {
	s, _, intent, native := newTreasurySettleService(t, 100_000_000)
	parkTreasury(t, s, intent, "30000000") // 216.988129 USDC live
	admin := uuid.New()
	if _, err := s.DeliverParkedTreasuryBuy(context.Background(), admin, intent.ID, ""); !errors.Is(err, ErrITOperatorNoteRequired) {
		t.Fatalf("no note: %v", err)
	}
	if _, err := s.DeliverParkedTreasuryBuy(context.Background(), admin, intent.ID, "customer confirmed"); !errors.Is(err, ErrTreasuryStockShort) {
		t.Fatalf("100 USDC in stock: %v, want short", err)
	}
	setStock(native, 300_000_000)
	got, err := s.DeliverParkedTreasuryBuy(context.Background(), admin, intent.ID, "customer confirmed")
	if err != nil || got.Status != models.ITStatusPaymentReceived || got.AssetAmount != "216988129" || !got.MaxWaived {
		t.Fatalf("deliver anyway: %+v %v, want the waiver recorded on the intent", got, err)
	}
	var audit models.ITIntentAuditLog
	if err := s.db.First(&audit, "intent_id = ? AND action = ?", intent.ID, models.ITAuditStableBuyDeliveredAnyway).Error; err != nil ||
		audit.ActorID == nil || *audit.ActorID != admin || audit.Note != "customer confirmed" {
		t.Fatalf("audit = %+v %v", audit, err)
	}
}

// §5.2 / D56: mark refunded closes a parked buy of either provider, audited.
func TestMarkStableBuyRefunded(t *testing.T) {
	s, _, intent, _ := newTreasurySettleService(t, 50_000_000)
	admin := uuid.New()
	if _, err := s.MarkStableBuyRefunded(context.Background(), admin, intent.ID, "sent back"); !errors.Is(err, ErrITBadState) {
		t.Fatalf("pending order: %v, want ErrITBadState", err)
	}
	parkTreasury(t, s, intent, "30000000")
	if _, err := s.MarkStableBuyRefunded(context.Background(), admin, intent.ID, "  "); !errors.Is(err, ErrITOperatorNoteRequired) {
		t.Fatalf("blank note: %v", err)
	}
	got, err := s.MarkStableBuyRefunded(context.Background(), admin, intent.ID, "GTB transfer 0123")
	if err != nil || got.Status != models.ITStatusRefunded {
		t.Fatalf("refund: %+v %v", got, err)
	}
	var n int64
	s.db.Model(&models.ITIntentAuditLog{}).Where("intent_id = ? AND action = ?", intent.ID, models.ITAuditStableBuyRefunded).Count(&n)
	if n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	if r, _ := s.reservedTreasuryUnits(context.Background(), s.db, "base", "USDC", uuid.Nil); r.Sign() != 0 {
		t.Fatal("a refunded order still holds stock")
	}
}
