package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// seedActivePMWithDailyCap inserts an active PM with both a per-trade cap and a
// daily cap (USD). 0 = unlimited for either.
func seedActivePMWithDailyCap(t *testing.T, db *gorm.DB, inboundAcct string, perTradeCapUSD, dailyCapUSD float64) *models.PaymentMerchant {
	t.Helper()
	pm := &models.PaymentMerchant{
		ID:                   uuid.New(),
		UserID:               uuid.New(),
		Name:                 "TSA",
		Status:               models.PaymentMerchantStatusActive,
		InboundAccountNumber: inboundAcct,
		InboundBank:          "Nomba",
		NombaWalletID:        "wallet-" + inboundAcct,
		PerTradeCapUSD:       perTradeCapUSD,
		DailyCapUSD:          dailyCapUSD,
	}
	if err := db.Create(pm).Error; err != nil {
		t.Fatalf("seed PM with daily cap: %v", err)
	}
	return pm
}

// markTradeStatusToday forces a trade's status (bypassing transition rules) AND
// stamps created_at to "now" so it falls within today's UTC window — simulating
// already-consumed daily capacity. (The shared seeder hardcodes a fixed past
// created_at; the daily-cap window is relative to the current UTC midnight.)
func markTradeStatusToday(t *testing.T, db *gorm.DB, tradeID uuid.UUID, status string) {
	t.Helper()
	if err := db.Exec("UPDATE p2p_trades SET status = ?, created_at = ? WHERE id = ?",
		status, time.Now().UTC(), tradeID).Error; err != nil {
		t.Fatalf("mark status %s: %v", status, err)
	}
}

// TestWithinDailyCap is a unit test for the sum + unlimited logic.
func TestWithinDailyCap(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)
	auto, _ := newPMAutomationService(t, db, nil, svc, nameMap(nil))

	ctx := context.Background()

	t.Run("zero daily cap is unlimited", func(t *testing.T) {
		pm := seedActivePMWithDailyCap(t, db, "acct-unl", 0, 0)
		ok, err := auto.withinDailyCap(ctx, pm, 1_000_000)
		if err != nil {
			t.Fatalf("withinDailyCap: %v", err)
		}
		if !ok {
			t.Fatal("zero daily cap must be unlimited (ok=true)")
		}
	})

	t.Run("no prior trades: prospective alone vs cap", func(t *testing.T) {
		pm := seedActivePMWithDailyCap(t, db, "acct-none", 0, 1000)
		ok, err := auto.withinDailyCap(ctx, pm, 999)
		if err != nil || !ok {
			t.Fatalf("999 <= 1000 should be within (ok=%v err=%v)", ok, err)
		}
		ok, err = auto.withinDailyCap(ctx, pm, 1001)
		if err != nil {
			t.Fatalf("withinDailyCap: %v", err)
		}
		if ok {
			t.Fatal("1001 > 1000 should be over cap (ok=false)")
		}
	})

	t.Run("sums released/paid/releasing trades today", func(t *testing.T) {
		pm := seedActivePMWithDailyCap(t, db, "acct-sum", 0, 2.0) // $2 cap; token_amount 1000000 (6dp USDT) = $1
		ad := seedPMSellAd(t, db, pm.ID, uuid.New())
		// One already-released trade today consuming $1.
		tr1 := seedBuyTradeOnAd(t, db, ad, uuid.New(), "1300.00")
		markTradeStatusToday(t, db, tr1.ID, models.P2PTradeStatusReleased)
		// A pending_payment trade must NOT count toward consumed capacity.
		tr2 := seedBuyTradeOnAd(t, db, ad, uuid.New(), "1300.00")
		_ = tr2

		// $1 consumed + $1 prospective = $2 <= $2 → within.
		ok, err := auto.withinDailyCap(ctx, pm, 1.0)
		if err != nil || !ok {
			t.Fatalf("$1 + $1 <= $2 should be within (ok=%v err=%v)", ok, err)
		}
		// $1 consumed + $1.5 prospective = $2.5 > $2 → over.
		ok, err = auto.withinDailyCap(ctx, pm, 1.5)
		if err != nil {
			t.Fatalf("withinDailyCap: %v", err)
		}
		if ok {
			t.Fatal("$1 consumed + $1.5 > $2 should be over cap")
		}
	})
}

// TestHandleInbound_OverDailyCap_NoRelease seeds a PM whose daily cap is nearly
// exhausted by an already-released trade today; a new settled inbound that would
// exceed the daily cap must NOT release (signer untouched, trade stays pending).
func TestHandleInbound_OverDailyCap_NoRelease(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	// Each trade is token_amount 1000000 with 6dp USDT → $1. Daily cap $1.50 so
	// one released trade ($1) + a new one ($1) = $2 > $1.50 → over cap.
	pm := seedActivePMWithDailyCap(t, db, "1234567890", 0, 1.5)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())

	releasedBuyer := uuid.New()
	priorReleased := seedBuyTradeOnAd(t, db, ad, releasedBuyer, "1300.00")
	markTradeStatusToday(t, db, priorReleased.ID, models.P2PTradeStatusReleased)

	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_over_daily",
	}); err != nil {
		t.Fatalf("HandleInbound: %v (over daily cap should no-op, not error)", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("status = %q, want pending_payment (over daily cap must not release)", persisted.Status)
	}
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0 (over daily cap)", len(signer.calls))
	}
}

// TestHandleInbound_WithinDailyCap_Releases verifies a settled inbound under the
// daily cap still releases.
func TestHandleInbound_WithinDailyCap_Releases(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	// Daily cap $5; one prior released $1 trade; new $1 trade → $2 <= $5 → release.
	pm := seedActivePMWithDailyCap(t, db, "1234567890", 0, 5)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())

	priorReleased := seedBuyTradeOnAd(t, db, ad, uuid.New(), "1300.00")
	markTradeStatusToday(t, db, priorReleased.ID, models.P2PTradeStatusReleased)

	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	if err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		WalletID:          pm.NombaWalletID,
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "1500.00",
		EventID:           "evt_within_daily",
	}); err != nil {
		t.Fatalf("HandleInbound: %v", err)
	}

	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusReleased {
		t.Errorf("status = %q, want released (within daily cap)", persisted.Status)
	}
	if len(signer.calls) != 1 {
		t.Errorf("signer called %d times, want 1", len(signer.calls))
	}
}

// TestManualRelease_OverDailyCap_Errors verifies the operator path also enforces
// the daily cap: an operator release that would exceed it returns a clear error
// (does not silently release).
func TestManualRelease_OverDailyCap_Errors(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	pm := seedActivePMWithDailyCap(t, db, "1234567890", 0, 1.5)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())

	priorReleased := seedBuyTradeOnAd(t, db, ad, uuid.New(), "1300.00")
	markTradeStatusToday(t, db, priorReleased.ID, models.P2PTradeStatusReleased)

	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{buyerID: "ALICE"}))

	_, err := auto.ManualRelease(context.Background(), trade.ID, uuid.New())
	if err == nil {
		t.Fatal("expected error for over-daily-cap manual release, got nil")
	}
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0 (must not release over daily cap)", len(signer.calls))
	}
	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status == models.P2PTradeStatusReleased {
		t.Error("trade released despite exceeding daily cap")
	}
}
