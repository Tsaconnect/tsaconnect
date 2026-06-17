package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

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
