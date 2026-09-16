package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// leg builds a RampLeg for the refund tests below. ChainKey/Token/FromAddr
// default to the rig's ORIGIN lane (base/USDC/treasury) and ToAddr to the
// rig's own user wallet, because that is what startBridge and refund()
// actually stamp on a bridge/refund leg — see conversion_orchestrator.go.
// seedLegs (below) fixes the timestamp; nothing here needs the rig itself.
func leg(kind string, attempt int, status, amount, amountReceived string) *models.RampLeg {
	return &models.RampLeg{
		Kind: kind, Attempt: attempt, Status: status,
		ChainKey: "base", Token: "USDC",
		FromAddr: sponsorTestSigner, ToAddr: sponsorTestUser,
		Amount: amount, AmountReceived: amountReceived,
	}
}

// seedLegs is seedLeg (singular, already on conversionRig) fanned out over
// several rows, stamping each on the order under test.
func (r *conversionRig) seedLegs(t *testing.T, legs ...*models.RampLeg) {
	t.Helper()
	for _, l := range legs {
		l.OrderID = r.order.ID
		r.seedLeg(t, l)
	}
}

// ── OwedToUser / RefundFromTreasury ─────────────────────────────────────────

// A bridge attempt that failed before anything broadcast never moved the
// treasury's ORIGIN holdings, so what is owed is exactly what Paycrest
// delivered — in the token and on the chain the treasury actually holds it,
// not the destination the failed bridge was aiming for.
func TestRefundFromTreasury_PaysOnlyWhatTheLegsOwe(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t,
		leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""),
	)
	token, chain, amount, err := r.svc.OwedToUser(context.Background(), r.order)
	if err != nil || amount != "100000000" || token != "USDC" || chain != "base" {
		t.Fatalf("owed = %s %s %s, err = %v", amount, token, chain, err)
	}
}

// A confirmed bridge that met the promise closes the question: refunding the
// origin deposit on top of a landed conversion would pay the user twice.
func TestRefundFromTreasury_RefusesWhenNothingIsOwed(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusConfirmed, "100000000", "99800000"))
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); !errors.Is(err, ErrNothingOwed) {
		t.Fatalf("want ErrNothingOwed, got %v", err)
	}
}

// The single most important rule in this file: a live bridge makes the
// liability UNDETERMINED, not zero and not the full promise — the user may
// already hold the money. RefundFromTreasury must refuse with a DIFFERENT
// error than ErrNothingOwed, so an operator cannot mistake "we don't know
// yet" for "there is nothing to pay".
func TestRefundFromTreasury_RefusesWhenUndetermined(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, &models.RampLeg{
		Kind: models.RampLegKindBridge, Attempt: 1, Status: models.RampLegStatusPending,
		ChainKey: "base", Token: "USDC", FromAddr: sponsorTestSigner, ToAddr: sponsorTestUser,
		Amount: "100000000", TxHash: "0xstillpending",
	})
	token, chain, amount, err := r.svc.OwedToUser(context.Background(), r.order)
	if err != nil || amount != "" {
		t.Fatalf("owed = %s %s %s, err = %v; a live bridge must be undetermined, not a number", amount, token, chain, err)
	}
	_, err = r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID)
	if !errors.Is(err, ErrOwedUndetermined) {
		t.Fatalf("want ErrOwedUndetermined, got %v", err)
	}
	if errors.Is(err, ErrNothingOwed) {
		t.Fatal("undetermined and nothing-owed must be DIFFERENT errors")
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; nothing may be paid against an undetermined figure", r.broadcasts())
	}
}

// The refund handler takes only an order id: the destination is always the
// order's own wallet, never anything a caller could supply.
func TestRefundFromTreasury_AlwaysPaysTheOrdersOwnWallet(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))
	legRow, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if legRow.ToAddr != r.order.WalletAddress {
		t.Fatalf("refund went to %s, want the order's wallet %s", legRow.ToAddr, r.order.WalletAddress)
	}
}

// A second admin refund on an order already paid out must not send again —
// it returns the same confirmed leg.
func TestRefundFromTreasury_IsIdempotent(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))
	first, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID)
	if err != nil || second.ID != first.ID || r.broadcasts() != 1 {
		t.Fatalf("a second refund must not send again: %v %v broadcasts=%d", second, err, r.broadcasts())
	}
}

// A refund must never race the state machine: a pending deliver (top-up) leg
// means the orchestrator itself could still finish paying the user.
func TestRefundFromTreasury_RefusesWhileATopUpIsInFlight(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t,
		leg(models.RampLegKindBridge, 1, models.RampLegStatusConfirmed, "100000000", "90000000"),
		&models.RampLeg{
			Kind: models.RampLegKindDeliver, Attempt: 1, Status: models.RampLegStatusPending,
			ChainKey: "polygon", Token: "USDT", FromAddr: sponsorTestSigner, ToAddr: sponsorTestUser,
			Amount: "9500000", TxHash: "0xtopup",
		},
	)
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); err == nil {
		t.Fatal("want a refusal while the top-up leg is still in flight")
	}
	if errors.Is(context.Canceled, ErrNothingOwed) { // sanity: keep the sentinels distinct
		t.Fatal("sentinels must stay distinct")
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; must not race the orchestrator's own top-up", r.broadcasts())
	}
}

// An order already settled or refunded owes nothing, whatever the legs say —
// the status is the fast, authoritative answer for a finished order.
func TestRefundFromTreasury_RefusesOnASettledOrder(t *testing.T) {
	r := newConversionRig(t)
	r.order.Status = models.PaycrestStatusSettled
	if err := r.db.Model(r.order).Update("status", models.PaycrestStatusSettled).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); !errors.Is(err, ErrNothingOwed) {
		t.Fatalf("want ErrNothingOwed on a settled order, got %v", err)
	}
}
