package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// markAdAsPM stamps pm_id onto an existing buy-ad row so the open path treats
// it as a Payment-Merchant buy-ad (PM automation pays the user). Mirrors how a
// PM-published buy-ad carries a non-nil pm_id.
func markAdAsPM(t *testing.T, svc *P2PTradeService, adID uuid.UUID) {
	t.Helper()
	pmID := uuid.New()
	if err := svc.db.Model(&models.P2PAd{}).
		Where("id = ?", adID).
		Update("pm_id", pmID).Error; err != nil {
		t.Fatalf("stamp pm_id: %v", err)
	}
}

// TestOpenTrade_PMSellAd_NoPaymentMethod_OK: a buyer opening a trade against a
// PM SELL ad with NO payment method is allowed — the buyer pays the PM's Nomba
// VA (pmPayTo), so the sell-ad open path must skip the payment-method allowlist
// check when the ad is a PM ad.
func TestOpenTrade_PMSellAd_NoPaymentMethod_OK(t *testing.T) {
	svc, _, adID, _, db := setupSignedAd(t)
	markAdAsPM(t, svc, adID) // stamp pm_id → treat as a PM ad

	buyerID := seedVerifiedUser(t, db)
	trade, raw, err := svc.OpenTrade(context.Background(), adID, buyerID, "0xbeef",
		OpenTradeParams{FiatAmount: "1000"}) // no PaymentMethodID
	if err != nil {
		t.Fatalf("OpenTrade(PM sell-ad, no payment method): %v", err)
	}
	if trade.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("trade status = %q, want pending_payment", trade.Status)
	}
	if len(raw) == 0 {
		t.Fatal("expected unsigned tx")
	}
}

// TestOpenTrade_NonPMSellAd_NoPaymentMethod_Rejected: the waiver is PM-only — a
// normal sell-ad still requires the buyer to reference a method in the ad's
// allowlist.
func TestOpenTrade_NonPMSellAd_NoPaymentMethod_Rejected(t *testing.T) {
	svc, _, adID, _, db := setupSignedAd(t) // pm_id stays NULL

	buyerID := seedVerifiedUser(t, db)
	_, _, err := svc.OpenTrade(context.Background(), adID, buyerID, "0xbeef",
		OpenTradeParams{FiatAmount: "1000"}) // no PaymentMethodID
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams for non-PM sell-ad with no method, got %v", err)
	}
}

// TestOpenTradeWithDeposit_PMBuyAd_StoresPayout verifies that opening a sell
// trade against a PM buy-ad persists the payout destination onto the trade so
// PMAutomationService.InitiateSellPayout can later pay the user.
func TestOpenTradeWithDeposit_PMBuyAd_StoresPayout(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t)
	markAdAsPM(t, svc, adID)

	userID := uuid.New() // the user selling crypto
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

	var persisted models.P2PTrade
	if err := db.First(&persisted, "id = ?", trade.ID).Error; err != nil {
		t.Fatalf("load trade: %v", err)
	}
	if persisted.PayoutAccountNumber != "0123456789" {
		t.Errorf("PayoutAccountNumber = %q, want 0123456789", persisted.PayoutAccountNumber)
	}
	if persisted.PayoutBank != "058" {
		t.Errorf("PayoutBank = %q, want 058", persisted.PayoutBank)
	}
	if persisted.PayoutAccountName != "JOHN DOE" {
		t.Errorf("PayoutAccountName = %q, want JOHN DOE", persisted.PayoutAccountName)
	}
}

// TestOpenTradeWithDeposit_PMBuyAd_MissingPayout_Errors verifies that opening
// a PM sell trade without a payout account/bank is rejected before any state
// mutation — error wraps ErrInvalidParams and capacity is not debited.
func TestOpenTradeWithDeposit_PMBuyAd_MissingPayout_Errors(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t)
	markAdAsPM(t, svc, adID)

	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")

	var before models.P2PAd
	db.First(&before, "id = ?", adID)

	_, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{
			TokenAmount:     "50000000",
			PaymentMethodID: userPm,
			// PayoutAccountNumber + PayoutBank intentionally empty.
			PayoutAccountName: "JOHN DOE",
		})
	if !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("expected ErrInvalidParams, got %v", err)
	}

	// No trade row was created.
	var count int64
	db.Model(&models.P2PTrade{}).Where("ad_id = ?", adID).Count(&count)
	if count != 0 {
		t.Errorf("expected 0 trades created, got %d", count)
	}

	// Capacity not debited.
	var after models.P2PAd
	db.First(&after, "id = ?", adID)
	if after.Remaining != before.Remaining {
		t.Errorf("remaining changed %s -> %s; capacity must not be debited on validation failure",
			before.Remaining, after.Remaining)
	}
	if after.Reserved != before.Reserved {
		t.Errorf("reserved changed %s -> %s; must not be touched on validation failure",
			before.Reserved, after.Reserved)
	}
}

// TestOpenTradeWithDeposit_NonPMBuyAd_PayoutOptional verifies that the payout
// fields are optional on a normal (non-PM, pm_id NULL) buy-ad — empty payout
// fields must not error, preserving existing behavior.
func TestOpenTradeWithDeposit_NonPMBuyAd_PayoutOptional(t *testing.T) {
	svc, _, _, _, adID, _, db := buyAdFixture(t) // pm_id stays NULL

	userID := uuid.New()
	userAddr := "0x000000000000000000000000000000000000beef"
	userPm := seedPaymentMethod(t, db, userID, "NGN")

	trade, _, err := svc.OpenTrade(context.Background(), adID, userID, userAddr,
		OpenTradeParams{TokenAmount: "50000000", PaymentMethodID: userPm})
	if err != nil {
		t.Fatalf("OpenTrade(non-PM buy-ad, empty payout): %v", err)
	}
	if trade.PayoutAccountNumber != "" || trade.PayoutBank != "" {
		t.Errorf("non-PM trade should not carry payout fields: %+v", trade)
	}
}
