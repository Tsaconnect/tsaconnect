package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const testConversionTreasury = "0x7777777777777777777777777777777777777777"

type fakeConvertedTreasury struct {
	laned  map[string]bool
	capErr error
}

func (f *fakeConvertedTreasury) Lane(chain string) *TreasuryLane {
	if f.laned[strings.ToLower(chain)] {
		// Tokens only: the attach-hash check reads the lane's USDC address.
		return &TreasuryLane{SponsorLane: &SponsorLane{Tokens: map[string]string{"USDC": "0xLaneUSDC"}}}
	}
	return nil
}
func (f *fakeConvertedTreasury) CheckCaps(string, float64, float64) error { return f.capErr }

// convertedVenueForTest is PriceConvertedLeg's answer for the company
// wallet's worked example (₦10,000 → D = 7.232937 USDC on Base) to USDT on
// Optimism at a 50 bps budget: T = 7.196772.
func convertedVenueForTest() PaycrestVenue {
	return PaycrestVenue{Kind: models.RouteKindConverted, ChainKey: "base", Network: "base", Token: "USDC",
		ViaChain: "base", ViaToken: "USDC", Rate: "1382.57",
		RequestedChain: "optimism", RequestedToken: "USDT", RequestedChainID: 10,
		RequestedAddress: "0xOptUSDT000000000000000000000000000001", RequestedDecimals: 6,
		TokenAmountBaseUnits: "7196772", MarkupBps: 50, EstimatedAmount: "7190000", PromisedAmount: "7196772"}
}

func wireConverted(s *ITService) {
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury: &fakeConvertedTreasury{laned: map[string]bool{"base": true}}})
}

func newConvertedTreasuryService(t *testing.T, stock int64) (*ITService, uuid.UUID) {
	t.Helper()
	s, userID, _ := newTreasuryBuyService(t, stock)
	s.SetTreasuryMarketRate(&fixedTreasuryRate{rate: treasuryMarket})
	if err := s.db.AutoMigrate(&models.ITIntentAuditLog{}, &models.RampLeg{}); err != nil {
		t.Fatal(err)
	}
	wireConverted(s)
	return s, userID
}

func conversionRecord(t *testing.T, s *ITService, intentID uuid.UUID) models.PaycrestOrderRecord {
	t.Helper()
	var rec models.PaycrestOrderRecord
	if err := s.db.First(&rec, "reference = ?", ITConversionRef(intentID)).Error; err != nil {
		t.Fatalf("conversion record: %v", err)
	}
	return rec
}

func openConvertedTreasury(t *testing.T, s *ITService, userID uuid.UUID) (models.ITIntent, models.PaycrestOrderRecord) {
	t.Helper()
	intent, _, _, err := s.OpenTreasuryConvertedBuy(context.Background(), userID, treasuryTestQuote(t, s), "base",
		ConvertedOpen{Venue: convertedVenueForTest(), WalletAddress: testRoutedEVM})
	if err != nil {
		t.Fatalf("open converted: %v", err)
	}
	return reloadIntent(t, s, intent.ID), conversionRecord(t, s, intent.ID)
}

func TestOpenTreasuryConvertedBuy_ReservesTheLegAndWritesBothRecords(t *testing.T) {
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	intent, rec := openConvertedTreasury(t, s, userID)
	if !strings.EqualFold(intent.DestAddress, testConversionTreasury) || intent.DestChain != "BASE" ||
		intent.Provider != models.ITProviderTreasury || intent.RateExpiresAt == nil || intent.AssetAmount != "7232937" {
		t.Fatalf("intent = %+v", intent)
	}
	if got := reservedNow(t, s); got != 7_232_937 {
		t.Fatalf("reserved = %d, want the leg reserved exactly like a direct buy", got)
	}
	if rec.Status != models.PaycrestStatusAwaitingPayment || rec.Holder != models.HolderCompanyWallet ||
		rec.RouteKind != models.RouteKindConverted || rec.PaycrestID != "" || rec.ChainKey != "base" || rec.Token != "USDC" ||
		rec.TokenAmount != "7.232937" || rec.QuotedAmount != "7196772" || rec.MarkupBps != 50 ||
		rec.WalletAddress != testRoutedEVM || rec.DestinationAddress != testRoutedEVM || rec.RequestedToken != "USDT" ||
		rec.UserID != userID {
		t.Fatalf("record = %+v", rec)
	}
}

// Decision C4: one transaction — never an intent without its record.
func TestOpenTreasuryConvertedBuy_NoIntentWithoutItsRecord(t *testing.T) {
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	if err := s.db.Migrator().DropTable(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.OpenTreasuryConvertedBuy(context.Background(), userID, treasuryTestQuote(t, s), "base",
		ConvertedOpen{Venue: convertedVenueForTest(), WalletAddress: testRoutedEVM}); err == nil {
		t.Fatal("open succeeded with no record table")
	}
	if n := intentCount(s); n != 0 {
		t.Fatalf("intents = %d, want none", n)
	}
}

func TestOpenTreasuryConvertedBuy_RefusesWithoutAConversionLaneOrOverTheCaps(t *testing.T) {
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	in := ConvertedOpen{Venue: convertedVenueForTest(), WalletAddress: testRoutedEVM}
	s.SetConvertedBuys(ConvertedBuys{})
	if _, _, _, err := s.OpenTreasuryConvertedBuy(context.Background(), userID, treasuryTestQuote(t, s), "base", in); !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("unwired: %v", err)
	}
	s.SetConvertedBuys(ConvertedBuys{TreasuryAddress: testConversionTreasury,
		Treasury: &fakeConvertedTreasury{laned: map[string]bool{"base": true}, capErr: errors.New("chain cap")}})
	if _, _, _, err := s.OpenTreasuryConvertedBuy(context.Background(), userID, treasuryTestQuote(t, s), "base", in); !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("over the caps: %v", err)
	}
	bad := in
	bad.Venue.TokenAmountBaseUnits = "7000000" // priced on another D
	wireConverted(s)
	if _, _, _, err := s.OpenTreasuryConvertedBuy(context.Background(), userID, treasuryTestQuote(t, s), "base", bad); !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("venue for another D: %v", err)
	}
	free := in
	free.Venue.MarkupBps, free.Venue.TokenAmountBaseUnits = 10_000, "0" // a budget that owes her nothing
	if _, _, _, err := s.OpenTreasuryConvertedBuy(context.Background(), userID, treasuryTestQuote(t, s), "base", free); !errors.Is(err, ErrConversionUnavailable) {
		t.Fatalf("a 100%% budget: %v", err)
	}
	if n := intentCount(s); n != 0 {
		t.Fatalf("intents = %d", n)
	}
}

func TestConvertedRecord_FollowsTheIntent(t *testing.T) {
	ctx := context.Background()
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	intent, _ := openConvertedTreasury(t, s, userID)
	if _, err := s.settleStableBuyFromCharge(ctx, userID, "chg_conv_paid", "1000000"); err != nil {
		t.Fatalf("pay: %v", err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusPending {
		t.Fatalf("paid: %s", rec.Status)
	}

	s2, u2 := newConvertedTreasuryService(t, 50_000_000)
	i2, _ := openConvertedTreasury(t, s2, u2)
	if _, err := s2.CancelBuy(ctx, u2, i2.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if rec := conversionRecord(t, s2, i2.ID); rec.Status != models.PaycrestStatusCancelled {
		t.Fatalf("cancelled: %s", rec.Status)
	}

	s3, u3 := newConvertedTreasuryService(t, 50_000_000)
	i3, _ := openConvertedTreasury(t, s3, u3)
	s3.db.Model(&models.ITIntent{}).Where("id = ?", i3.ID).Update("created_at", time.Now().Add(-25*time.Hour))
	if _, err := s3.OpenStableBuy(ctx, u3); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if rec := conversionRecord(t, s3, i3.ID); rec.Status != models.PaycrestStatusExpired {
		t.Fatalf("expired: %s", rec.Status)
	}
}

// N1's authority: the lookup reads only the database.
func TestConversionFor_IsALookupByReference(t *testing.T) {
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	intent, _ := openConvertedTreasury(t, s, userID)
	s.SetConvertedBuys(ConvertedBuys{})
	rec, err := s.ConversionFor(context.Background(), &intent)
	if err != nil || rec == nil || rec.Reference != ITConversionRef(intent.ID) {
		t.Fatalf("rec = %+v err = %v", rec, err)
	}
	direct := seedTreasuryIntent(t, s, userID, models.ITStatusPaymentReceived) // a direct company-wallet buy
	if rec, err := s.ConversionFor(context.Background(), direct); err != nil || rec != nil {
		t.Fatalf("a direct buy feeds no conversion: %+v %v", rec, err)
	}
}

// N2: every routed entry point, adapter or not.
func TestOpenStableBuy_AbandonCapIsEnforcedInTheService(t *testing.T) {
	ctx := context.Background()
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	if err := s.SetRampBuyTerms(RampBuyTerms{DailyAbandonCap: 1}); err != nil {
		t.Fatal(err)
	}
	i, _, err := s.OpenTreasuryBuy(ctx, userID, treasuryTestQuote(t, s), "base", testRoutedEVM)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if _, err := s.CancelBuy(ctx, userID, i.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, _, err := s.OpenTreasuryBuy(ctx, userID, treasuryTestQuote(t, s), "base", testRoutedEVM); !errors.Is(err, ErrStableBuyAbandonCapReached) {
		t.Fatalf("direct open past the cap: %v", err)
	}
	if _, _, _, err := s.OpenTreasuryConvertedBuy(ctx, userID, treasuryTestQuote(t, s), "base",
		ConvertedOpen{Venue: convertedVenueForTest(), WalletAddress: testRoutedEVM}); !errors.Is(err, ErrStableBuyAbandonCapReached) {
		t.Fatalf("converted open past the cap: %v", err)
	}
}

func TestNewConvertedTerms_OwedAllInRateAndTheLiFiLine(t *testing.T) {
	s, _ := newConvertedTreasuryService(t, 50_000_000)
	v := convertedVenueForTest()
	terms, err := NewConvertedTerms(treasuryTestQuote(t, s), &v)
	if err != nil {
		t.Fatal(err)
	}
	// T = 7.196772; ⌈1,000,000 kobo × 10⁶ ÷ 7,196,772⌉ = 138,952 → ₦1,389.52.
	if terms.OwedUnits.String() != "7196772" || terms.EffectiveRateMinor.String() != "138952" {
		t.Fatalf("terms = %+v", terms)
	}
	last := terms.Fees[len(terms.Fees)-1]
	// D − T = 0.036165 USDC at ₦1,382.57 = ₦50.00.
	if last.Kind != FeeKindConversion || last.Label != ConversionLiFiLabel || last.Provider != FeeProviderLiFi ||
		last.Units != "36165" || last.NgnMinor != "5000" {
		t.Fatalf("fee line = %+v", last)
	}
	all := PaycrestVenue{MarkupBps: 10_000}
	if _, err := NewConvertedTerms(treasuryTestQuote(t, s), &all); err == nil {
		t.Fatal("a 100% budget owes nothing and must be refused")
	}
}

// The record is invisible to Paycrest's own flows.
func TestPaycrestOrders_ITConversionRowsStayOutOfPaycrestFlows(t *testing.T) {
	svc, _ := newPaycrestOrderTestService(t)
	ctx := context.Background()
	user := uuid.New()
	itc := &models.PaycrestOrderRecord{UserID: user, Direction: models.PaycrestDirectionBuy,
		Status: models.PaycrestStatusAwaitingPayment, Reference: ITConversionRef(uuid.New())}
	own := &models.PaycrestOrderRecord{UserID: user, Direction: models.PaycrestDirectionSell,
		Status: models.PaycrestStatusSettled, Reference: "PCS-own"}
	for _, r := range []*models.PaycrestOrderRecord{itc, own} {
		if err := svc.db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}
	if rows, err := svc.List(ctx, user, 25); err != nil || len(rows) != 1 || rows[0].ID != own.ID {
		t.Fatalf("list = %+v %v", rows, err)
	}
	if err := svc.AssertNoOpenOrder(ctx, user, models.PaycrestDirectionBuy); err != nil {
		t.Fatalf("the conversion row blocked a Paycrest buy: %v", err)
	}
	if _, err := svc.Cancel(ctx, user, itc.ID); !errors.Is(err, ErrPaycrestNotCancellable) {
		t.Fatalf("cancel: %v", err)
	}
}

// A parked converted buy refunded in naira closes its record in the same
// transaction: no conversion will ever run for it.
func TestConvertedRecord_ClosesWhenRefundedInNaira(t *testing.T) {
	ctx := context.Background()
	s, userID := newConvertedTreasuryService(t, 50_000_000)
	intent, _ := openConvertedTreasury(t, s, userID)
	// Claimed and parked: what a payment that could not be priced leaves.
	s.db.Model(&models.ITIntent{}).Where("id = ?", intent.ID).Update("status", models.ITStatusBuyAwaitingRate)
	s.db.Model(&models.PaycrestOrderRecord{}).Where("reference = ?", ITConversionRef(intent.ID)).
		Update("status", models.PaycrestStatusPending)
	if _, err := s.MarkStableBuyRefunded(ctx, uuid.New(), intent.ID, "returned by bank transfer"); err != nil {
		t.Fatalf("refund: %v", err)
	}
	if rec := conversionRecord(t, s, intent.ID); rec.Status != models.PaycrestStatusCancelled ||
		rec.FailureReason != "refunded in naira by an operator" {
		t.Fatalf("refunded: %s %q", rec.Status, rec.FailureReason)
	}
}

// Decision C4 on PostgreSQL: the record is written in the intent's own
// transaction under the stock lock — both commit, or neither does.
func TestInsertTreasuryIntentWith_RecordCommitsWithTheIntent_Postgres(t *testing.T) {
	db, s := newLockedStockService(t, 50_000_000)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := ConvertedOpen{Venue: convertedVenueForTest(), WalletAddress: testRoutedEVM}
	withRecord := func(tx *gorm.DB, i *models.ITIntent) error {
		return tx.Create(newConversionRecord(i, in, models.HolderCompanyWallet)).Error
	}
	first := newUSDCOrder()
	if err := s.insertTreasuryIntentWith(ctx, first, withRecord); err != nil {
		t.Fatalf("open: %v", err)
	}
	var rec models.PaycrestOrderRecord
	if err := db.First(&rec, "reference = ?", ITConversionRef(first.ID)).Error; err != nil ||
		rec.Holder != models.HolderCompanyWallet || rec.QuotedAmount != "7196772" {
		t.Fatalf("record = %+v err = %v", rec, err)
	}
	// A record that cannot be written (its reference is already taken) takes
	// the intent down with it.
	second := newUSDCOrder()
	err := s.insertTreasuryIntentWith(ctx, second, func(tx *gorm.DB, i *models.ITIntent) error {
		dup := newConversionRecord(i, in, models.HolderCompanyWallet)
		dup.Reference = ITConversionRef(first.ID)
		return tx.Create(dup).Error
	})
	if err == nil {
		t.Fatal("a duplicate record was written")
	}
	var intents, records int64
	db.Model(&models.ITIntent{}).Count(&intents)
	db.Model(&models.PaycrestOrderRecord{}).Count(&records)
	if intents != 1 || records != 1 {
		t.Fatalf("intents=%d records=%d, want the failed open to leave nothing", intents, records)
	}
}
