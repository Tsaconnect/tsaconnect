package services

import (
	"context"
	"errors"
	"testing"
	"time"

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
// order's own wallet, never anything a caller could supply. Asserted against
// what TransferToken actually handed the signer to sign (r.src.native's
// recorded PrepareERC20Transfer call) — the struct RefundFromTreasury builds
// and returns is not proof of what left the treasury; the prepared calldata
// is.
func TestRefundFromTreasury_AlwaysPaysTheOrdersOwnWallet(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))
	legRow, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if legRow.ToAddr != r.order.WalletAddress {
		t.Fatalf("refund leg records %s, want the order's wallet %s", legRow.ToAddr, r.order.WalletAddress)
	}
	_, _, to, amount := r.src.native.transferCall()
	if to != r.order.WalletAddress {
		t.Fatalf("TransferToken actually sent to %s, want the order's own wallet %s", to, r.order.WalletAddress)
	}
	if amount == nil || amount.String() != "100000000" {
		t.Fatalf("TransferToken amount = %v, want 100000000", amount)
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
	_, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID)
	if !errors.Is(err, ErrOwedUndetermined) {
		t.Fatalf("want ErrOwedUndetermined while the top-up leg is still in flight, got %v", err)
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

// ── Custody gate (C1) ────────────────────────────────────────────────────────
//
// This tool only ever answers for a converted BUY whose funds the treasury
// actually took custody of — mirroring Advance's own step-1 gate exactly.
// Without this, OwedToUser's origin branch (no legs, or none confirmed) would
// happily hand back order.TokenAmount for money that was never the
// treasury's to give: a direct order's payout, a sell's incoming deposit, or
// a converted buy the user has not even paid for yet.

// A direct order is paid by Paycrest straight to the user; the treasury was
// never involved.
func TestRefundFromTreasury_RefusesADirectOrder(t *testing.T) {
	r := newConversionRig(t)
	if err := r.db.Model(r.order).Update("route_kind", models.RouteKindDirect).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); !errors.Is(err, ErrRefundNotApplicable) {
		t.Fatalf("want ErrRefundNotApplicable, got %v", err)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; a direct order's money was never the treasury's to refund", r.broadcasts())
	}
}

// A sell order's TokenAmount is what the user SENDS, not something the
// treasury ever held.
func TestRefundFromTreasury_RefusesASellOrder(t *testing.T) {
	r := newConversionRig(t)
	if err := r.db.Model(r.order).Update("direction", models.PaycrestDirectionSell).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); !errors.Is(err, ErrRefundNotApplicable) {
		t.Fatalf("want ErrRefundNotApplicable, got %v", err)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; a sell order's TokenAmount is the user's money, not the treasury's", r.broadcasts())
	}
}

// A converted buy still awaiting the user's bank payment: Paycrest has
// delivered nothing, Holder is still Paycrest's, and there are zero legs.
func TestRefundFromTreasury_RefusesAConvertedBuyStillAwaitingPayment(t *testing.T) {
	r := newConversionRig(t)
	if err := r.db.Model(r.order).Updates(map[string]any{
		"holder": models.HolderPaycrest, "status": models.PaycrestStatusAwaitingPayment,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); !errors.Is(err, ErrRefundNotApplicable) {
		t.Fatalf("want ErrRefundNotApplicable, got %v", err)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; custody was never taken, there is nothing here to refund", r.broadcasts())
	}
}

// ── Stranded bridge (I3) ─────────────────────────────────────────────────────

// A bridge leg that broadcast a transaction but was left neither confirmed
// nor pending (LiFi reported it dead, or the row was simply left dangling)
// is stranded, not refundable: that money may still land. Mirrors Advance's
// own step 4 (broadcastBridgeLeg) — refunding the origin deposit on top of a
// bridge that could still deliver would pay the user twice.
func TestRefundFromTreasury_RefusesAStrandedBridge(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, &models.RampLeg{
		Kind: models.RampLegKindBridge, Attempt: 1, Status: models.RampLegStatusFailed,
		ChainKey: "base", Token: "USDC", FromAddr: sponsorTestSigner, ToAddr: sponsorTestUser,
		Amount: "100000000", TxHash: "0xbroadcastbutneverconfirmed",
	})
	_, _, amount, err := r.svc.OwedToUser(context.Background(), r.order)
	if err != nil || amount != "" {
		t.Fatalf("owed = %q, err = %v; a stranded bridge must be undetermined, not a refundable number", amount, err)
	}
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); !errors.Is(err, ErrOwedUndetermined) {
		t.Fatalf("want ErrOwedUndetermined, got %v", err)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; must not refund the origin deposit while a bridge might still land", r.broadcasts())
	}
}

// ── Concurrency with the state machine's own refund (I5) ────────────────────

// A refund leg already pending — whatever put it there, the scheduler's own
// automatic refund() or a concurrent admin call — must block a second one
// outright, rather than RefundFromTreasury computing its own non-colliding
// NextAttempt and broadcasting alongside it. The unique (order_id, kind,
// attempt) index only rejects two callers landing on the SAME attempt
// number; it does nothing to stop two DIFFERENT numbers both going out.
func TestRefundFromTreasury_RefusesWhenARefundIsAlreadyPending(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t,
		leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""),
		&models.RampLeg{
			Kind: models.RampLegKindRefund, Attempt: 1, Status: models.RampLegStatusPending,
			ChainKey: "base", Token: "USDC", FromAddr: sponsorTestSigner, ToAddr: sponsorTestUser,
			Amount: "100000000", TxHash: "0xalreadysentbysomeoneelse",
		},
	)
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); !errors.Is(err, ErrRefundInFlight) {
		t.Fatalf("want ErrRefundInFlight, got %v", err)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; a second refund attempt must never be created alongside a pending one", r.broadcasts())
	}
	if legs := r.legsOfKind(t, models.RampLegKindRefund); len(legs) != 1 {
		t.Fatalf("refund legs = %d, want exactly the 1 already pending", len(legs))
	}
}

// ── Caps and the treasury's own guard ────────────────────────────────────────

// A refund returns money the treasury ALREADY holds — it creates no new
// exposure — so the value caps that gate a NEW bridge/deliver must never
// block it. Proven with a cap so tiny a fresh conversion of this size would
// be refused outright.
func TestRefundFromTreasury_CapsDoNotBlockARefund(t *testing.T) {
	r := newConversionRig(t)
	r.treasury.caps = TreasuryCaps{MaxInFlightUSDPerOrder: 1, MaxInFlightUSDPerChain: 1}
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); err != nil {
		t.Fatalf("a refund must not be capped: %v", err)
	}
	if r.broadcasts() != 1 {
		t.Fatalf("broadcasts = %d, want 1", r.broadcasts())
	}
}

// The treasury's own kill switch must still fail the refund closed, exactly
// like every other treasury-guarded transfer — and the leg it briefly
// reserved must be freed (failed), not left pending, so a retry once the
// treasury is unpaused is not blocked by a transfer that never actually sent.
func TestRefundFromTreasury_KilledTreasuryFailsClosed(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))
	r.treasury.SetKillSwitch(true)
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); !errors.Is(err, ErrTreasuryKilled) {
		t.Fatalf("want ErrTreasuryKilled, got %v", err)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.broadcasts())
	}
	refundLegs := r.legsOfKind(t, models.RampLegKindRefund)
	if len(refundLegs) != 1 || refundLegs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("refund legs = %+v, want exactly one, FAILED (freed, not stuck pending)", refundLegs)
	}
}

// ── RefundPreview (I4) ───────────────────────────────────────────────────────

// RefundPreview is a DIFFERENT figure from RampLegsInfo.OwedToUser (the
// destination-denominated promise): on an order whose bridge never
// confirmed, the preview names the ORIGIN token/chain/decimals a refund
// would actually move — not the destination one OwedToUser tracks. A console
// reading the wrong one, or guessing at decimals, would show a number the
// refund button does not actually send.
func TestLegsAndOwed_RefundPreviewCarriesTheActualRefundFigure(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))

	info, err := r.svc.LegsAndOwed(context.Background(), r.order)
	if err != nil {
		t.Fatal(err)
	}
	if info.OwedToUser != "99500000" {
		t.Fatalf("OwedToUser (promise) = %q, want the destination-denominated 99500000", info.OwedToUser)
	}
	if info.RefundPreview == nil {
		t.Fatal("no refund preview attached")
	}
	if info.RefundPreview.Token != "USDC" || info.RefundPreview.Chain != "base" || info.RefundPreview.Amount != "100000000" {
		t.Fatalf("refund preview = %+v, want the ORIGIN 100000000 USDC on base", info.RefundPreview)
	}
	if info.RefundPreview.Decimals != 6 {
		t.Fatalf("decimals = %d, want 6", info.RefundPreview.Decimals)
	}
}

// An order this tool cannot refund (never custodied) still returns its legs
// view — the preview is simply absent, not an error that hides the ledger.
func TestLegsAndOwed_NoRefundPreviewForANonCustodiedOrder(t *testing.T) {
	r := newConversionRig(t)
	if err := r.db.Model(r.order).Update("route_kind", models.RouteKindDirect).Error; err != nil {
		t.Fatal(err)
	}
	info, err := r.svc.LegsAndOwed(context.Background(), r.order)
	if err != nil {
		t.Fatalf("LegsAndOwed must not fail over a direct order: %v", err)
	}
	if info.RefundPreview != nil {
		t.Fatalf("refund preview = %+v, want nil for a direct order", info.RefundPreview)
	}
}

// ── Fix round 2 ──────────────────────────────────────────────────────────────

// Custody is proven the instant VerifyTransfer succeeds — takeCustody must
// record holder=treasury BEFORE resolving the destination, not after. An
// order whose requested token/chain cannot be routed (o.destination fails)
// still ends up parked in recovery_pending, but it must be parked as MONEY
// THE TREASURY HOLDS, not as a shape the refund tool refuses outright. This
// is exactly the stranded-funds case the admin refund tool exists for.
func TestOrchestrator_RecordsCustodyEvenWhenTheDestinationCannotBeResolved(t *testing.T) {
	r := newConversionRig(t)
	if err := r.db.Model(r.order).Updates(map[string]any{
		"holder": models.HolderPaycrest, "requested_token": "NOPE",
	}).Error; err != nil {
		t.Fatal(err)
	}

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %s, want treasury — VerifyTransfer already proved the money landed", got.Holder)
	}
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending", got.Status)
	}

	// The whole point of the fix: this order must now be refundable, not
	// refused as a shape the treasury never custodied.
	token, chain, amount, err := r.svc.OwedToUser(context.Background(), got)
	if err != nil {
		t.Fatalf("OwedToUser = %v; a verifiably-custodied order must not be ErrRefundNotApplicable", err)
	}
	if amount != "100000000" || token != "USDC" || chain != "base" {
		t.Fatalf("owed = %s %s %s, want the origin deposit the treasury actually holds", amount, token, chain)
	}
	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); err != nil {
		t.Fatalf("RefundFromTreasury = %v, want the stranded custody to be refundable", err)
	}
}

// A refund attempt must never un-park an order a human deliberately parked.
// The pre-broadcast "refunding" status write would otherwise clear the only
// status NeedsOperator() keys on and hand the order back to Tick's automatic
// state machine — which could resume converting an order an operator
// stopped on purpose. An attempt that does not actually land must leave the
// order exactly where the operator put it.
func TestRefundFromTreasury_LeavesAParkedOrderParkedWhenTheAttemptFails(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))
	if err := r.db.Model(r.order).Update("status", models.PaycrestStatusRecoveryPending).Error; err != nil {
		t.Fatal(err)
	}
	r.src.native.transferErr = errors.New("rpc rejected the transfer")

	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); err == nil {
		t.Fatal("expected the transfer build to fail")
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want the order to stay parked (recovery_pending) after an attempt that did not land", got.Status)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.broadcasts())
	}
}

// A refund actually landing on a parked order still closes it out: the order
// is now resolved, so it correctly leaves the operator queue.
func TestRefundFromTreasury_ClosesOutAParkedOrderOnSuccess(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))
	if err := r.db.Model(r.order).Update("status", models.PaycrestStatusRecoveryPending).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); err != nil {
		t.Fatal(err)
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRefunded {
		t.Fatalf("status = %s, want refunded once the transfer actually lands", got.Status)
	}
}

// pendingLegOfKinds regression: attempts are numbered independently PER
// KIND, so a pending refund at a HIGHER attempt number must never hide a
// pending bridge at a lower one. Reverting to a kind-filtered-after-the-fact
// "latest pending leg across every kind" would make this fall through to the
// origin branch and return a real number — the exact bug this helper exists
// to prevent.
//
// The bridge leg deliberately carries NO tx hash: with one, broadcastBridgeLeg
// (a separate, later check) would independently also read this as
// undetermined and mask whether pendingLegOfKinds is actually doing its job.
// A pending, hashless bridge is caught ONLY by the pending-leg check.
func TestOwedToUser_APendingRefundAtAHigherAttemptDoesNotHideAPendingBridge(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t,
		&models.RampLeg{
			Kind: models.RampLegKindBridge, Attempt: 1, Status: models.RampLegStatusPending,
			ChainKey: "base", Token: "USDC", FromAddr: sponsorTestSigner, ToAddr: sponsorTestUser,
			Amount: "100000000", TxHash: "",
		},
		&models.RampLeg{
			Kind: models.RampLegKindRefund, Attempt: 5, Status: models.RampLegStatusPending,
			ChainKey: "base", Token: "USDC", FromAddr: sponsorTestSigner, ToAddr: sponsorTestUser,
			Amount: "100000000", TxHash: "0xsomeearlierattempt",
		},
	)
	_, _, amount, err := r.svc.OwedToUser(context.Background(), r.order)
	if err != nil || amount != "" {
		t.Fatalf("owed = %q, err = %v; a live bridge must be undetermined even behind a higher-attempt-numbered pending refund", amount, err)
	}
}

// ── Fix round 3 ──────────────────────────────────────────────────────────────

// The custody fix (round 2) introduced a non-atomic window: holder=treasury
// is persisted, then the decimals read, then promised_amount. A transient
// failure in between — most realistically the source-decimals RPC read —
// used to leave the order stuck FOREVER: Holder is already Treasury, so no
// LATER tick ever re-enters takeCustody (Advance's own step-1 gate), and a
// bare error repeated silently on every tick with no leg for the stall
// detector to ever notice (afterStep returns early when len(legs)==0). It
// must park instead, so a human sees it — and STAYS parked, since
// recovery_pending only ever clears by an operator's own action, never by
// the RPC happening to recover on a later tick.
func TestOrchestrator_ParksWhenTheSourceDecimalsReadFails(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.src.native.decimalsErr = errors.New("rpc timeout")

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %s, want treasury — VerifyTransfer already proved the money landed", got.Holder)
	}
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending — a transient decimals failure must park, not loop silently forever", got.Status)
	}
	if r.lifi.quoteCalls() != 0 {
		t.Fatalf("quotes = %d, want 0 — must not reach the bridge with no readable promise", r.lifi.quoteCalls())
	}

	// The RPC "recovering" on a later tick must not resurrect it into the
	// automated pipeline on its own — Advance's own NeedsOperator() gate now
	// owns this order until a human acts.
	r.src.native.decimalsErr = nil
	if err := r.svc.Advance(context.Background(), r.reload(t)); err != nil {
		t.Fatal(err)
	}
	still := r.reload(t)
	if still.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s after a later tick, want it to stay parked until an operator acts", still.Status)
	}
}

// Defence in depth for startBridge itself: whatever might leave an
// in-custody order (Holder==Treasury) with no readable promise, looping
// forever on a bare error is worse than parking it — the same "invisible to
// the stall detector" reasoning as the decimals-read fix above.
func TestOrchestrator_ParksWhenTheOrderHasNoReadablePromise(t *testing.T) {
	r := newConversionRig(t)
	if err := r.db.Model(r.order).Update("promised_amount", "").Error; err != nil {
		t.Fatal(err)
	}
	// Holder is already Treasury (the rig's default) — this must reach
	// startBridge directly, not takeCustody.

	if err := r.svc.Advance(context.Background(), r.reload(t)); err != nil {
		t.Fatal(err)
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending", got.Status)
	}
	if r.lifi.quoteCalls() != 0 {
		t.Fatalf("quotes = %d, want 0", r.lifi.quoteCalls())
	}
}

// The pre-broadcast "refunding" status write is only proven by testing the
// SKIP branch (recovery_pending) so far — deleting the write entirely still
// leaves that test green. This pins the WRITE branch directly: a `converting`
// order must have its status flipped to `refunding` BEFORE the broadcast, so
// a crash between the write and the broadcast reads as "a refund was under
// way", not as an order still waiting to convert.
func TestRefundFromTreasury_SetsRefundingStatusBeforeTheBroadcast(t *testing.T) {
	r := newConversionRig(t)
	r.seedLegs(t, leg(models.RampLegKindBridge, 1, models.RampLegStatusFailed, "100000000", ""))
	// r.order.Status is "converting" (the rig's default) — NOT recovery_pending.

	// Fail the broadcast build itself, so the write under test — which must
	// happen BEFORE the broadcast — is observable without the order also
	// reaching its post-success "refunded" state.
	r.src.native.transferErr = errors.New("rpc rejected the transfer")

	if _, err := r.svc.RefundFromTreasury(context.Background(), uuid.New(), r.order.ID); err == nil {
		t.Fatal("expected the transfer build to fail")
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRefunding {
		t.Fatalf("status = %s, want refunding — the intent must be written before the broadcast, even one that then fails", got.Status)
	}
	if got.Fault != models.FaultPlatform {
		t.Fatalf("fault = %s, want platform", got.Fault)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.broadcasts())
	}
}

// ── Fix round 5 ──────────────────────────────────────────────────────────────
//
// "Park, don't loop forever" (round 3, and round 4's startBridge/refund
// extensions of it) is only real if reverting any ONE of the three park
// branches to a bare error is caught by SOME test. takeCustody's own branch
// was already pinned by TestOrchestrator_ParksWhenTheSourceDecimalsReadFails
// (round 3); these two cover the other two functions the same reasoning
// applies to.

// startBridge's own decimals-read failure must park too, for the identical
// reason takeCustody's does: Holder is already Treasury by the time
// Advance ever reaches startBridge, so a bare error here would repeat
// silently on every tick forever with no leg yet reserved for the stall
// detector to notice.
func TestOrchestrator_StartBridgeParksWhenTheSourceDecimalsReadFails(t *testing.T) {
	r := newConversionRig(t)
	// Holder is already Treasury (the rig's default) — Advance must reach
	// startBridge directly, not takeCustody.
	r.src.native.decimalsErr = errors.New("rpc timeout")

	if err := r.svc.Advance(context.Background(), r.order); err != nil {
		t.Fatal(err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending — a decimals RPC failure inside startBridge must park, not loop", got.Status)
	}
	if r.lifi.quoteCalls() != 0 || r.broadcasts() != 0 {
		t.Fatalf("quotes=%d broadcasts=%d, want 0 — nothing may be attempted before the amount is even readable",
			r.lifi.quoteCalls(), r.broadcasts())
	}
}

// refund()'s own decimals-read failure must park too — same reasoning, one
// function further down the automatic state machine.
func TestOrchestrator_RefundParksWhenTheSourceDecimalsReadFails(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.err = ErrLiFiNoRoute
	mustAdvance(t, r) // attempt 1 fails before ever broadcasting
	r.clock = r.clock.Add(31 * time.Minute)
	r.src.native.decimalsErr = errors.New("rpc timeout")

	if err := r.svc.Advance(context.Background(), r.reload(t)); err != nil {
		t.Fatal(err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending — a decimals RPC failure inside refund() must park, not loop", got.Status)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.broadcasts())
	}
}
