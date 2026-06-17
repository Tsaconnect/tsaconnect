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
		Status:    "pending",
		WalletID:  pm.NombaWalletID,
		AmountNGN: "999.00",
		EventID:   "evt_pending",
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
