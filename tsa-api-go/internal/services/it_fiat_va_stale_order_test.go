package services

import (
	"context"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// An MCGP buy order sits in pending_payment forever. OpenBuyOrder supersedes a
// previous one, but only when the user opens ANOTHER MCGP buy — opening a
// stablecoin purchase goes through a different function and leaves the MCGP row
// untouched. Nothing else expires it.
//
// That is how a user ends up with an order they abandoned days ago silently
// swallowing an unrelated later payment: the settle path takes the newest
// pending MCGP order with no regard for its age. The stablecoin leg has carried
// a 24h claim window since it shipped, for exactly this reason; the MCGP leg
// never got one.
func TestAutoCreditFromVA_StaleOpenOrderDoesNotClaimALaterPayment(t *testing.T) {
	s, user, va := newVATestService(t)
	ctx := context.Background()

	abandoned, err := s.OpenBuyOrder(ctx, user.ID, user.WalletAddress, "100000")
	if err != nil {
		t.Fatalf("open buy order: %v", err)
	}
	mintedRef := abandoned.PaymentRef
	if err := s.db.Model(&models.ITIntent{}).Where("id = ?", abandoned.ID).
		Update("created_at", time.Now().Add(-buyOrderClaimWindow-time.Hour)).Error; err != nil {
		t.Fatalf("age the order: %v", err)
	}

	_ = s.AutoCreditFromVA(ctx, va.FwCustomerID, va.Reference, testChargeID, "5000")

	var stale models.ITIntent
	if err := s.db.First(&stale, "id = ?", abandoned.ID).Error; err != nil {
		t.Fatalf("reload abandoned order: %v", err)
	}
	if stale.Status != models.ITStatusPendingPayment {
		t.Errorf("abandoned order status = %q, want it left at pending_payment", stale.Status)
	}
	if stale.PaymentRef != mintedRef {
		t.Errorf("abandoned order took the charge: payment_ref = %q, want its minted ref %q",
			stale.PaymentRef, mintedRef)
	}

	rows := buyIntents(t, s, user.ID)
	if len(rows) != 2 {
		t.Fatalf("want the abandoned order plus a fresh one, got %d intents", len(rows))
	}
	fresh := rows[len(rows)-1]
	if fresh.ID == abandoned.ID {
		t.Fatalf("no new intent was created for the charge")
	}
	if fresh.PaymentRef != testChargeID {
		t.Errorf("fresh intent payment_ref = %q, want the charge %q", fresh.PaymentRef, testChargeID)
	}
	if fresh.NgnKobo != "500000" {
		t.Errorf("ngnKobo = %q, want the transferred 500000", fresh.NgnKobo)
	}
}
