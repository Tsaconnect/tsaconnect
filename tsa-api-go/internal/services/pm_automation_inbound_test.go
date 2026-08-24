package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// TestHandleInbound_UnknownAccountRef_MustNotFallThroughToTSA is the Prove-It
// test for FIX 1: when p.AccountRef is non-empty but resolves to no PM, the
// inbound MUST NOT fall through to the walletId path and credit the TSA PM.
// Before the fix resolveInboundPM falls through on a miss → walletId resolves
// the TSA parent PM → wrong NGN credit + possible release.
func TestHandleInbound_UnknownAccountRef_MustNotFallThroughToTSA(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	// TSA PM with a known walletId — this is what would be mis-credited if the
	// fallthrough bug is present.
	tsaPM := seedActivePM(t, db, "tsa-inbound-acct", 0) // NombaWalletID = "wallet-tsa-inbound-acct"
	ad := seedPMSellAd(t, db, tsaPM.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "5000.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	// Inbound: accountRef present but unknown, walletId matches the TSA PM.
	// Under the bug: accountRef miss → falls through to walletId → TSA PM resolved
	// → CreditNGN fires + trade released.
	// Under the fix: accountRef present → GetByAccountRef miss → no-op, full stop.
	err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		AccountRef:        "corp-ref-nobody-owns",
		WalletID:          tsaPM.NombaWalletID, // matches TSA — must NOT be used
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "5000.00",
		EventID:           "evt_unknown_ref_fallthrough",
	})
	if err != nil {
		t.Fatalf("HandleInbound: want nil (no-op), got %v", err)
	}

	// Signer must not be called — no release.
	if len(signer.calls) != 0 {
		t.Errorf("signer called %d times, want 0 (unknown accountRef must not resolve TSA and release)", len(signer.calls))
	}

	// Trade must remain pending_payment — not released.
	var persisted models.P2PTrade
	db.First(&persisted, "id = ?", trade.ID)
	if persisted.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("trade status = %q, want pending_payment (unknown accountRef must not release)", persisted.Status)
	}

	// TSA PM available_ngn must NOT have been credited.
	var avail string
	if err := db.Raw("SELECT available_ngn FROM payment_merchants WHERE id = ?", tsaPM.ID).Scan(&avail).Error; err != nil {
		t.Fatalf("read available_ngn: %v", err)
	}
	if avail != "0" {
		t.Errorf("TSA available_ngn = %q, want '0' — unknown accountRef must not credit TSA ledger", avail)
	}
}

// TestHandleInbound_EmptyAccountRef_IsIgnored pins the VA-only inbound contract:
// accountRef is the ONLY match key. An inbound with an empty AccountRef is a
// no-op even when its WalletID matches a known PM.
//
// This test previously asserted the opposite (a walletId fallback), which was
// deliberately removed in 17f761f "remove legacy TSA seed PM + walletId inbound
// fallback (accountRef-only)". The test was not updated with the code, so it
// failed on main while describing a contract that no longer exists. Releasing
// crypto against a walletId-only inbound is exactly the mis-attribution that
// commit set out to close — so the assertion is inverted, not the code.
func TestHandleInbound_EmptyAccountRef_IsIgnored(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, esc, _ := newService(t, db)

	tsaPM := seedActivePM(t, db, "tsa-acct-empty-ref", 0)
	ad := seedPMSellAd(t, db, tsaPM.ID, uuid.New())
	buyerID := uuid.New()
	trade := seedBuyTradeOnAd(t, db, ad, buyerID, "2000.00")
	setReleasedEvent(esc, trade)

	auto, signer := newPMAutomationService(t, db, esc, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	// Empty AccountRef → no corporate PM match → no-op, full stop.
	err := auto.HandleInbound(context.Background(), InboundPayment{
		Status:            "settled",
		AccountRef:        "",
		WalletID:          tsaPM.NombaWalletID, // known, but must NOT resolve
		SenderAccountName: "JOHN DOE",
		AmountNGN:         "2000.00",
		EventID:           "evt_empty_ref_ignored",
	})
	if err != nil {
		t.Fatalf("HandleInbound (empty accountRef): want nil (no-op), got %v", err)
	}
	if len(signer.calls) != 0 {
		t.Fatalf("signer called %d times, want 0 — an empty accountRef must never release", len(signer.calls))
	}
	var untouched models.P2PTrade
	db.First(&untouched, "id = ?", trade.ID)
	if untouched.Status != models.P2PTradeStatusPendingPayment {
		t.Errorf("trade status = %q, want pending_payment (empty accountRef must no-op)", untouched.Status)
	}
}

// TestHandleInbound_BadAmountNGN_SkipsCredit verifies that an unparseable
// AmountNGN (e.g. "abc" or "1.5.0") causes the CreditNGN step to be logged
// and skipped rather than returning an error from HandleInbound.
// The trade must not advance (no release), and the available_ngn balance must
// remain unchanged.
func TestHandleInbound_BadAmountNGN_SkipsCredit(t *testing.T) {
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	svc, _, _ := newService(t, db)

	pm := seedActivePM(t, db, "bad-amount-acct", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	buyerID := uuid.New()
	seedBuyTradeOnAd(t, db, ad, buyerID, "1500.00")

	auto, signer := newPMAutomationService(t, db, nil, svc, nameMap(map[uuid.UUID]string{
		buyerID: "JOHN DOE",
	}))

	for _, badAmount := range []string{"abc", "1.5.0", "--100"} {
		t.Run(badAmount, func(t *testing.T) {
			err := auto.HandleInbound(context.Background(), InboundPayment{
				Status:            "settled",
				AccountRef:        pm.NombaAccountRef,
				WalletID:          pm.NombaWalletID,
				SenderAccountName: "JOHN DOE",
				AmountNGN:         badAmount,
				EventID:           "evt_bad_" + badAmount,
			})
			if err != nil {
				t.Fatalf("HandleInbound with bad AmountNGN %q: want nil, got %v", badAmount, err)
			}
			if len(signer.calls) != 0 {
				t.Errorf("release signer must not be called for bad amount %q, got %d calls", badAmount, len(signer.calls))
			}

			// available_ngn must be unchanged (still "0").
			var avail string
			if err := db.Raw("SELECT available_ngn FROM payment_merchants WHERE id = ?", pm.ID).Scan(&avail).Error; err != nil {
				t.Fatalf("read available_ngn: %v", err)
			}
			if avail != "0" {
				t.Errorf("available_ngn = %q after bad amount %q, want '0'", avail, badAmount)
			}
		})
	}
}
