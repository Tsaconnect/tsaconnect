package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// markAdAsPMWithBalance stamps a real PaymentMerchant row (with the given
// available_ngn) onto a buy-ad so the open path treats it as a PM sell and the
// NGN reservation has a backing row. Returns the PM id.
func markAdAsPMWithBalance(t *testing.T, db *gorm.DB, adID uuid.UUID, availableNGN string) uuid.UUID {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID:            uuid.New(),
		UserID:        uuid.New(),
		Name:          "TSA",
		Status:        models.PaymentMerchantStatusActive,
		NombaWalletID: "wallet-" + uuid.NewString(),
		AvailableNGN:  availableNGN,
		ReservedNGN:   "0",
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM: %v", err)
	}
	if err := db.Model(&models.P2PAd{}).Where("id = ?", adID).
		Update("pm_id", pm.ID).Error; err != nil {
		t.Fatalf("stamp pm_id: %v", err)
	}
	return pm.ID
}

// ngnBalances reads back a PM's available/reserved NGN ledger columns.
func ngnBalances(t *testing.T, svc *PMService, pmID uuid.UUID) (available, reserved string) {
	t.Helper()
	var pm models.PaymentMerchant
	if err := svc.db.First(&pm, "id = ?", pmID).Error; err != nil {
		t.Fatalf("load PM: %v", err)
	}
	return pm.AvailableNGN, pm.ReservedNGN
}

// ─── Credit on settled inbound ───────────────────────────────────────────────

// TestHandleInbound_SettledCreditsLedger verifies every settled inbound credits
// the PM's available_ngn — here the inbound matches an open buy trade and the
// ledger still grows by the credited amount.
func TestHandleInbound_SettledCreditsLedger(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, _ := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		AccountRef:        pm.NombaAccountRef,
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_credit",
	}); err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}

	avail, _ := ngnBalances(t, auto.pm, pm.ID)
	if avail != "1500" && avail != "1500.00" {
		t.Errorf("available_ngn = %q, want 1500.00", avail)
	}
}

// TestHandleInbound_UnmatchedTopUpStillCredits verifies an inbound that matches
// NO trade (a PM top-up) still credits the ledger. No trade is seeded; only the
// PM exists.
func TestHandleInbound_UnmatchedTopUpStillCredits(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(nil))

	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		AccountRef:        pm.NombaAccountRef,
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "NOBODY",
		AmountNGN:         "2500.00",
		EventID:           "evt_topup",
	}); err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}

	// No trade matched → no release.
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0 (unmatched top-up)", len(signer.calls))
	}
	// Ledger still credited.
	avail, _ := ngnBalances(t, auto.pm, pm.ID)
	if avail != "2500" && avail != "2500.00" {
		t.Errorf("available_ngn = %q, want 2500.00 (top-up credit)", avail)
	}
}

// TestHandleInbound_NonSettled_NoCredit verifies a non-settled inbound does NOT
// touch the ledger.
func TestHandleInbound_NonSettled_NoCredit(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	auto, _ := newPMAutomationService(t, db, esc, svc, nameMap(nil))

	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:     "pending",
		AccountRef: pm.NombaAccountRef,
		WalletID:   pm.NombaWalletID,
		AmountNGN:  "999.00",
		EventID:    "evt_pending",
	}); err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}
	avail, _ := ngnBalances(t, auto.pm, pm.ID)
	if avail != "0" && avail != "0.00" {
		t.Errorf("available_ngn = %q, want 0 (non-settled must not credit)", avail)
	}
}

// ─── Consume on payout success / Release on payout failure ───────────────────

// seedPMSellTradeWithReservation seeds a PM sell trade (deposited-by-taker buy-ad)
// whose ad is owned by pm, with the PM holding a matching NGN reservation. Returns
// the trade. available/reserved are set so a Consume (success) drops both and a
// Release (failure) drops only reserved.
func seedPMSellTradeWithReservation(t *testing.T, db *gorm.DB, pm *models.PaymentMerchant, fiat string) *models.P2PTrade {
	t.Helper()
	// Stamp the PM with the reservation: available=reserved=fiat (a clean
	// reservation against a freshly-credited balance).
	if err := db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).
		Updates(map[string]any{"available_ngn": fiat, "reserved_ngn": fiat}).Error; err != nil {
		t.Fatalf("set ledger: %v", err)
	}
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	// Make it a deposited-by-taker sell trade on the PM's ad.
	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), fiat)
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("ad_id", ad.ID).Error; err != nil {
		t.Fatalf("link trade to ad: %v", err)
	}
	trade.AdID = ad.ID
	return trade
}

func TestHandlePayoutStatus_Success_ConsumesNGN(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	trade := seedPMSellTradeWithReservation(t, db, pm, "1500.00")
	setReleasedEvent(esc, trade)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	if err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_ok", MerchantTxRef: trade.ID.String(), Status: "success",
	}); err != nil {
		t.Fatalf("HandlePayoutStatus: %v", err)
	}

	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1 (release on success)", len(signer.calls))
	}
	avail, reserved := ngnBalances(t, auto.pm, pm.ID)
	if avail != "0" && avail != "0.00" {
		t.Errorf("available_ngn = %q, want 0 (consumed)", avail)
	}
	if reserved != "0" && reserved != "0.00" {
		t.Errorf("reserved_ngn = %q, want 0 (consumed)", reserved)
	}
}

func TestHandlePayoutStatus_Failed_ReleasesNGN(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	trade := seedPMSellTradeWithReservation(t, db, pm, "1500.00")
	setReleasedEvent(esc, trade)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	if err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_fail", MerchantTxRef: trade.ID.String(), Status: "failed",
	}); err != nil {
		t.Fatalf("HandlePayoutStatus: %v", err)
	}

	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0 (no release on failed)", len(signer.calls))
	}
	avail, reserved := ngnBalances(t, auto.pm, pm.ID)
	// Release un-reserves only: reserved drops, available is unchanged.
	if avail != "1500" && avail != "1500.00" {
		t.Errorf("available_ngn = %q, want 1500.00 (released, not consumed)", avail)
	}
	if reserved != "0" && reserved != "0.00" {
		t.Errorf("reserved_ngn = %q, want 0 (released)", reserved)
	}
}

// ─── Cancel releases the reservation ─────────────────────────────────────────

// TestConfirmCancel_PMSell_ReleasesNGN verifies a user-cancelled PM sell trade
// releases the held NGN reservation back to the PM's free balance.
func TestConfirmCancel_PMSell_ReleasesNGN(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))

	pm := seedActivePM(t, db, "1234567890", 0)
	if err := db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).
		Updates(map[string]any{"available_ngn": "1500.00", "reserved_ngn": "1500.00"}).Error; err != nil {
		t.Fatalf("set ledger: %v", err)
	}
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	// Set the ad's reserved capacity to match the trade so the cancel restore is valid.
	if err := db.Model(&models.P2PAd{}).Where("id = ?", ad.ID).
		Updates(map[string]any{"remaining": "0", "reserved": "1000000"}).Error; err != nil {
		t.Fatalf("set ad capacity: %v", err)
	}
	userID := uuid.New()
	trade := seedSellTradeOnAd(t, db, uuid.New(), userID, "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("ad_id", ad.ID).Error; err != nil {
		t.Fatalf("link trade: %v", err)
	}

	// Wire the fake escrow's TradeCancelled receipt for a deposited-by-taker trade
	// (ReturnedToPool=false).
	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	esc.tradeCancelled = &P2PTradeCancelledEvent{
		TradeID:        HashID(trade.ID),
		Amount:         tokenAmt,
		ReturnedToPool: false,
	}

	if _, err := svc.ConfirmCancel(context.Background(), trade.ID, userID, "0xcancel"); err != nil {
		t.Fatalf("ConfirmCancel: %v", err)
	}

	pmSvc := NewPMService(db)
	avail, reserved := ngnBalances(t, pmSvc, pm.ID)
	if avail != "1500" && avail != "1500.00" {
		t.Errorf("available_ngn = %q, want 1500.00 (unchanged by release)", avail)
	}
	if reserved != "0" && reserved != "0.00" {
		t.Errorf("reserved_ngn = %q, want 0 (released on cancel)", reserved)
	}
}

// ─── Regression: failed-payout then cancel must NOT double-release ───────────

// TestFailedPayoutThenCancel_NoDoubleRelease is the regression for the
// cross-cutting double-release bug: HandlePayoutStatus("failed") releases a PM
// sell trade's NGN reservation but leaves the trade in pending_payment for the
// watchdog; the subsequent ConfirmCancel would then release the SAME reservation
// a second time. With two concurrent reservations on one PM, the second release
// would silently STEAL the other live trade's reservation (reserved_ngn goes to
// 0 instead of staying at the second trade's amount).
//
// Setup: PM with available=3000, two sell trades each reserving 1500 → reserved=3000.
// Trade A's payout fails (release A → reserved=1500, marker set on A), then
// Trade A is cancelled. Correct outcome: reserved stays 1500 (B's reservation
// intact). The buggy outcome was reserved=0 (B's reservation stolen).
func TestFailedPayoutThenCancel_NoDoubleRelease(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))

	pm := seedActivePM(t, db, "1234567890", 0)
	// Two reservations of 1500 each against a 3000 balance.
	if err := db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).
		Updates(map[string]any{"available_ngn": "3000.00", "reserved_ngn": "3000.00"}).Error; err != nil {
		t.Fatalf("set ledger: %v", err)
	}

	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	// Ad capacity to satisfy the cancel restore for trade A's 1000000 token amount.
	if err := db.Model(&models.P2PAd{}).Where("id = ?", ad.ID).
		Updates(map[string]any{"remaining": "0", "reserved": "1000000"}).Error; err != nil {
		t.Fatalf("set ad capacity: %v", err)
	}

	userA := uuid.New()
	tradeA := seedSellTradeOnAd(t, db, uuid.New(), userA, "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", tradeA.ID).
		Update("ad_id", ad.ID).Error; err != nil {
		t.Fatalf("link trade A: %v", err)
	}
	tradeA.AdID = ad.ID

	gw := &fakeNombaGateway{}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	// 1) Trade A's payout fails → releases A's 1500 reservation, sets the marker,
	//    leaves the trade in pending_payment.
	if err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_fail_A", MerchantTxRef: tradeA.ID.String(), Status: "failed",
	}); err != nil {
		t.Fatalf("HandlePayoutStatus(failed): %v", err)
	}
	_, reservedAfterFail := ngnBalances(t, auto.pm, pm.ID)
	if reservedAfterFail != "1500" && reservedAfterFail != "1500.00" {
		t.Fatalf("reserved_ngn after failed payout = %q, want 1500 (only A released)", reservedAfterFail)
	}
	// The marker must be persisted on trade A.
	var markerA bool
	if err := db.Model(&models.P2PTrade{}).Select("ngn_reservation_released").
		Where("id = ?", tradeA.ID).Scan(&markerA).Error; err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !markerA {
		t.Fatalf("ngn_reservation_released = false after failed payout, want true")
	}

	// 2) Trade A is now cancelled (timeout watchdog → ConfirmCancel). This must
	//    NOT release a second time.
	tokenAmt, _ := new(big.Int).SetString(tradeA.TokenAmount, 10)
	esc.tradeCancelled = &P2PTradeCancelledEvent{
		TradeID:        HashID(tradeA.ID),
		Amount:         tokenAmt,
		ReturnedToPool: false,
	}
	if _, err := svc.ConfirmCancel(context.Background(), tradeA.ID, userA, "0xcancelA"); err != nil {
		t.Fatalf("ConfirmCancel: %v", err)
	}

	avail, reserved := ngnBalances(t, NewPMService(db), pm.ID)
	if avail != "3000" && avail != "3000.00" {
		t.Errorf("available_ngn = %q, want 3000 (release un-reserves only)", avail)
	}
	// THE ASSERTION: reserved stays at 1500 (trade B's reservation), NOT 0.
	if reserved != "1500" && reserved != "1500.00" {
		t.Errorf("reserved_ngn = %q, want 1500 (B's reservation intact — NOT double-released)", reserved)
	}
}

// ─── Sell-open reserves atomically, rolls back on insufficient liquidity ──────

// TestOpenTradeWithDeposit_PMBuyAd_ReservesNGN verifies opening a PM sell trade
// reserves the fiat payout from the PM's NGN, and the WHOLE open rolls back
// (no trade, no crypto capacity change) when the PM has insufficient liquidity.
func TestOpenTradeWithDeposit_PMBuyAd_InsufficientNGN_RollsBack(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t)
	createPaymentMerchantsTable(t, db)
	pmID := markAdAsPMWithBalance(t, db, adID, "0.00") // no NGN at all

	svc.SetPMService(NewPMService(db))

	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")

	var before models.P2PAd
	db.First(&before, "id = ?", adID)

	_, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{
			TokenAmount:         "50000000",
			PaymentMethodID:     userPm,
			PayoutAccountNumber: "0123456789",
			PayoutBank:          "058",
			PayoutAccountName:   "JOHN DOE",
		})
	if !errors.Is(err, ErrInsufficientPMLiquidity) {
		t.Fatalf("expected ErrInsufficientPMLiquidity, got %v", err)
	}

	// Whole open rolled back: no trade row.
	var count int64
	db.Model(&models.P2PTrade{}).Where("ad_id = ?", adID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 trades (rolled back), got %d", count)
	}
	// Crypto capacity unchanged.
	var after models.P2PAd
	db.First(&after, "id = ?", adID)
	if after.Remaining != before.Remaining || after.Reserved != before.Reserved {
		t.Errorf("ad capacity changed (remaining %s->%s, reserved %s->%s); must roll back",
			before.Remaining, after.Remaining, before.Reserved, after.Reserved)
	}
	// No NGN reserved.
	pmSvc := NewPMService(db)
	_, reserved := ngnBalances(t, pmSvc, pmID)
	if reserved != "0" && reserved != "0.00" {
		t.Errorf("reserved_ngn = %q, want 0 (open rolled back)", reserved)
	}
}

// TestOpenTradeWithDeposit_PMBuyAd_ReservesNGN verifies a successful PM sell open
// reserves the fiat payout amount from the PM's NGN.
func TestOpenTradeWithDeposit_PMBuyAd_ReservesNGN(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t)
	createPaymentMerchantsTable(t, db)
	pmID := markAdAsPMWithBalance(t, db, adID, "1000000.00") // ample NGN

	svc.SetPMService(NewPMService(db))

	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")

	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{
			TokenAmount:         "50000000",
			PaymentMethodID:     userPm,
			PayoutAccountNumber: "0123456789",
			PayoutBank:          "058",
			PayoutAccountName:   "JOHN DOE",
		})
	if err != nil {
		t.Fatalf("OpenTrade(PM buy-ad): %v", err)
	}

	pmSvc := NewPMService(db)
	_, reserved := ngnBalances(t, pmSvc, pmID)
	// FiatAmount = 50 USDT × 1300 = 65000.
	if reserved != trade.FiatAmount {
		t.Errorf("reserved_ngn = %q, want %q (trade fiat)", reserved, trade.FiatAmount)
	}
}

// ─── Release on the OTHER terminal paths (reconcile / dispute-resolve) ─────────

// seedPMSellTradeWithReservationAndCapacity is seedPMSellTradeWithReservation
// plus stamping the ad's reserved capacity to cover the trade's TokenAmount, so
// the terminal `reserved -= amount` capacity restore stays non-negative.
func seedPMSellTradeWithReservationAndCapacity(t *testing.T, db *gorm.DB, pm *models.PaymentMerchant, fiat string) *models.P2PTrade {
	t.Helper()
	trade := seedPMSellTradeWithReservation(t, db, pm, fiat)
	if err := db.Model(&models.P2PAd{}).Where("id = ?", trade.AdID).
		Updates(map[string]any{"remaining": "0", "reserved": trade.TokenAmount}).Error; err != nil {
		t.Fatalf("set ad capacity: %v", err)
	}
	return trade
}

// assertReleased asserts a release (not a consume) settled the reservation:
// reserved back to 0, available UNCHANGED at fiat.
func assertReleased(t *testing.T, svc *PMService, pmID uuid.UUID, fiat string) {
	t.Helper()
	avail, reserved := ngnBalances(t, svc, pmID)
	if avail != fiat && avail != trimZeros(fiat) {
		t.Errorf("available_ngn = %q, want %q (released, not consumed)", avail, fiat)
	}
	if reserved != "0" && reserved != "0.00" {
		t.Errorf("reserved_ngn = %q, want 0 (released)", reserved)
	}
}

func trimZeros(s string) string {
	// "1500.00" -> "1500" for the SQLite NUMERIC-as-text comparison tolerance.
	if i := len(s) - 3; i > 0 && s[i] == '.' && s[i+1] == '0' && s[i+2] == '0' {
		return s[:i]
	}
	return s
}

// TestReconcileToCancelled_PMSell_ReleasesNGN: a PM sell trade reconciled to
// cancelled releases its held NGN reservation (reserved→0, available unchanged).
func TestReconcileToCancelled_PMSell_ReleasesNGN(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))

	pm := seedActivePM(t, db, "1234567890", 0)
	trade := seedPMSellTradeWithReservationAndCapacity(t, db, pm, "1500.00")

	if _, err := svc.reconcileToCancelled(context.Background(), trade, models.P2PTradeFundingDepositedByTaker); err != nil {
		t.Fatalf("reconcileToCancelled: %v", err)
	}
	assertReleased(t, NewPMService(db), pm.ID, "1500.00")
}

// TestReconcileToCancelled_NonPM_NoLedgerChange: a non-PM trade through the same
// path is a clean no-op on the ledger and must not panic.
func TestReconcileToCancelled_NonPM_NoLedgerChange(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))

	// A non-PM PaymentMerchant row exists with a balance, but the trade's ad has
	// no pm_id — the release must not touch it.
	pm := seedActivePM(t, db, "1234567890", 0)
	if err := db.Model(&models.PaymentMerchant{}).Where("id = ?", pm.ID).
		Updates(map[string]any{"available_ngn": "777.00", "reserved_ngn": "0.00"}).Error; err != nil {
		t.Fatalf("set ledger: %v", err)
	}
	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00") // ad has no pm_id
	if err := db.Model(&models.P2PAd{}).Where("id = ?", trade.AdID).
		Updates(map[string]any{"remaining": "0", "reserved": trade.TokenAmount}).Error; err != nil {
		// ad row may not exist for a bare seed; create a minimal one
	}
	// Ensure an ad row exists so the pm_id lookup runs (returns nil pm_id).
	db.Create(&models.P2PAd{ID: trade.AdID, SellerID: uuid.New(), Side: models.P2PAdSideSell,
		Token: "USDT", ChainID: 146, Amount: "1000000", Remaining: "0", Reserved: trade.TokenAmount,
		FiatCurrency: "NGN", Rate: "1300", RateType: models.P2PAdRateTypeFixed, Status: models.P2PAdStatusActive})

	if _, err := svc.reconcileToCancelled(context.Background(), trade, models.P2PTradeFundingDepositedByTaker); err != nil {
		t.Fatalf("reconcileToCancelled (non-PM): %v", err)
	}
	avail, reserved := ngnBalances(t, NewPMService(db), pm.ID)
	if (avail != "777" && avail != "777.00") || (reserved != "0" && reserved != "0.00") {
		t.Errorf("non-PM path changed ledger: available=%q reserved=%q (want 777.00 / 0)", avail, reserved)
	}
}

// TestReconcileOrphanOpen_PMSell_ReleasesNGN: an orphaned (open never landed)
// PM sell trade reclaimed by the reconciler releases its NGN reservation.
func TestReconcileOrphanOpen_PMSell_ReleasesNGN(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))

	pm := seedActivePM(t, db, "1234567890", 0)
	trade := seedPMSellTradeWithReservationAndCapacity(t, db, pm, "1500.00")
	// Orphan-reclaim requires an empty open_tx_hash on a pending_payment trade.
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Updates(map[string]any{"open_tx_hash": "", "status": models.P2PTradeStatusPendingPayment}).Error; err != nil {
		t.Fatalf("set orphan state: %v", err)
	}

	if _, err := svc.reconcileOrphanOpen(context.Background(), trade); err != nil {
		t.Fatalf("reconcileOrphanOpen: %v", err)
	}
	assertReleased(t, NewPMService(db), pm.ID, "1500.00")
}

// TestReconcileToResolved_PMSell_ReleasesNGN: a PM sell trade reconciled to
// resolved (admin refunded to seller) releases its NGN reservation. RELEASE on
// resolve in both branches — consume is exclusively a confirmed-payout event.
func TestReconcileToResolved_PMSell_ReleasesNGN(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))

	pm := seedActivePM(t, db, "1234567890", 0)
	trade := seedPMSellTradeWithReservationAndCapacity(t, db, pm, "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("status", models.P2PTradeStatusDisputed).Error; err != nil {
		t.Fatalf("set disputed: %v", err)
	}
	// reconcileToResolved reads the dispute row's RefundedToBuyer to pick the
	// branch; seed one with refund-to-seller (false).
	refundFalse := false
	if err := db.Create(&models.P2PDispute{
		ID: uuid.New(), TradeID: trade.ID, RaisedBy: trade.SellerID,
		RaisedRole: models.P2PDisputeRoleSeller, Reason: "test",
		Status: models.P2PDisputeStatusOpen, RefundedToBuyer: &refundFalse,
	}).Error; err != nil {
		t.Fatalf("seed dispute: %v", err)
	}

	if _, err := svc.reconcileToResolved(context.Background(), trade, models.P2PTradeFundingDepositedByTaker, big.NewInt(0)); err != nil {
		t.Fatalf("reconcileToResolved: %v", err)
	}
	assertReleased(t, NewPMService(db), pm.ID, "1500.00")
}

// TestConfirmAdminResolveDispute_PMSell_ReleasesNGN_ToBuyer: admin resolves a PM
// sell dispute in the buyer's favour (release-to-buyer). The reservation must
// still be RELEASED (consume is reserved for confirmed Nomba payout success).
func TestConfirmAdminResolveDispute_PMSell_ReleasesNGN_ToBuyer(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))

	pm := seedActivePM(t, db, "1234567890", 0)
	trade := seedPMSellTradeWithReservationAndCapacity(t, db, pm, "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("status", models.P2PTradeStatusDisputed).Error; err != nil {
		t.Fatalf("set disputed: %v", err)
	}
	db.Create(&models.P2PDispute{
		ID: uuid.New(), TradeID: trade.ID, RaisedBy: trade.SellerID,
		RaisedRole: models.P2PDisputeRoleSeller, Reason: "test",
		Status: models.P2PDisputeStatusOpen,
	})

	tokenAmt, _ := new(big.Int).SetString(trade.TokenAmount, 10)
	fee := big.NewInt(500)
	moved := new(big.Int).Sub(tokenAmt, fee)
	esc.resolvedEvent = &P2PTradeResolvedEvent{
		TradeID: HashID(trade.ID), ReleasedToBuyer: true, AmountMoved: moved, FeeToSystem: fee,
	}

	if _, err := svc.ConfirmAdminResolveDispute(context.Background(), trade.ID, uuid.New(), "0xres"); err != nil {
		t.Fatalf("ConfirmAdminResolveDispute(to buyer): %v", err)
	}
	assertReleased(t, NewPMService(db), pm.ID, "1500.00")
}

// TestConfirmAdminResolveDispute_PMSell_ReleasesNGN_ToSeller: admin resolves in
// the seller's favour (refund to seller). Reservation released.
func TestConfirmAdminResolveDispute_PMSell_ReleasesNGN_ToSeller(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)
	svc.SetPMService(NewPMService(db))

	pm := seedActivePM(t, db, "1234567890", 0)
	trade := seedPMSellTradeWithReservationAndCapacity(t, db, pm, "1500.00")
	if err := db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).
		Update("status", models.P2PTradeStatusDisputed).Error; err != nil {
		t.Fatalf("set disputed: %v", err)
	}
	db.Create(&models.P2PDispute{
		ID: uuid.New(), TradeID: trade.ID, RaisedBy: trade.SellerID,
		RaisedRole: models.P2PDisputeRoleSeller, Reason: "test",
		Status: models.P2PDisputeStatusOpen,
	})

	esc.resolvedEvent = &P2PTradeResolvedEvent{
		TradeID: HashID(trade.ID), ReleasedToBuyer: false,
		AmountMoved: big.NewInt(0), FeeToSystem: big.NewInt(0),
	}

	if _, err := svc.ConfirmAdminResolveDispute(context.Background(), trade.ID, uuid.New(), "0xres"); err != nil {
		t.Fatalf("ConfirmAdminResolveDispute(to seller): %v", err)
	}
	assertReleased(t, NewPMService(db), pm.ID, "1500.00")
}

// TestHandlePayoutStatus_Success_Retry_NotWedged: the consume-then-failed-release
// wedge. First success dispatch consumes the reservation; if the release failed
// transiently and the SAME success event is re-dispatched, the already-consumed
// reservation must NOT block AutoRelease (no wedge). available_ngn decremented
// exactly once.
func TestHandlePayoutStatus_Success_Retry_NotWedged(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePM(t, db, "1234567890", 0)
	trade := seedPMSellTradeWithReservation(t, db, pm, "1500.00")
	setReleasedEvent(esc, trade)

	gw := &fakeNombaGateway{}
	auto, signer := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))

	// Simulate the first success dispatch having already CONSUMED the reservation
	// (available 1500→0, reserved 1500→0) but the release having failed transiently
	// (trade still pending_payment). This is the exact state a retry sees.
	if err := auto.pm.ConsumeNGN(context.Background(), pm.ID, trade.FiatAmount); err != nil {
		t.Fatalf("pre-consume: %v", err)
	}

	// Re-dispatch the SAME success event.
	if err := auto.HandlePayoutStatus(context.Background(), PayoutStatus{
		EventID: "evt_ok", MerchantTxRef: trade.ID.String(), Status: "success",
	}); err != nil {
		t.Fatalf("HandlePayoutStatus retry: %v", err)
	}

	// AutoRelease must have been reached on the retry (not wedged on consume).
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1 (AutoRelease reached on retry)", len(signer.calls))
	}
	// available decremented exactly once (consume on pre-step only; retry must NOT
	// touch available again).
	avail, reserved := ngnBalances(t, auto.pm, pm.ID)
	if avail != "0" && avail != "0.00" {
		t.Errorf("available_ngn = %q, want 0 (consumed exactly once, not twice)", avail)
	}
	if reserved != "0" && reserved != "0.00" {
		t.Errorf("reserved_ngn = %q, want 0", reserved)
	}
}
