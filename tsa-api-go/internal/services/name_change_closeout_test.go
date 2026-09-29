package services

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Tests for the last verification findings: refunds whose send was cut off
// before a hash was known, a squatted hash the chain cannot judge yet, dropped
// payments landing as dust, refutations racing on one change, and late
// payments confirmed while a change paid another way is open.

// paidStable is a rig whose user has paid a $1 stablecoin fee.
func paidStable(t *testing.T) *nameChangeRig {
	t.Helper()
	r := newNameChangeRig(t, NameChangeConfig{})
	r.openStable(t)
	r.rail.payment = r.provenPayment(1_000_000)
	if _, err := r.svc.ConfirmPayment(context.Background(), r.user, nameChangeTx); err != nil {
		t.Fatal(err)
	}
	return r
}

// A refund claim records when it was made and the chain's head just before;
// a head that cannot be read sends nothing and backs off like any send that
// never left.
func TestNameChange_RefundClaimRecordsWhenAndFromWhichBlock(t *testing.T) {
	ctx := context.Background()
	r := paidStable(t)
	r.rail.headErr = errors.New("rpc down")
	view, err := r.svc.Cancel(ctx, r.user)
	if err != nil || view.FeeStatus != models.NameChangeFeeRefundFailed || r.rail.refunds != 0 {
		t.Fatalf("view %+v err %v refunds %d", view, err, r.rail.refunds)
	}
	if ch := r.change(t, view.ID); ch.RefundAttempts != 1 || ch.RefundRetryAt == nil || ch.RefundClaimedAt != nil {
		t.Fatalf("an unreadable head is a send that never left: %+v", ch)
	}
	r.rail.headErr, r.rail.head = nil, 4242
	r.rail.refundHash, r.rail.refundAttempted = "0xsent", true
	r.now = r.now.Add(time.Hour)
	if _, err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	ch := r.change(t, view.ID)
	if ch.FeeStatus != models.NameChangeFeeRefunded || ch.RefundClaimedBlock == nil || *ch.RefundClaimedBlock != 4242 ||
		ch.RefundClaimedAt == nil || !ch.RefundClaimedAt.Equal(r.now) {
		t.Fatalf("change %+v", ch)
	}
}

// M1: "never left" for a refund whose send was cut off before its hash was
// known is taken only when the token's logs show no transfer of the refund
// from the treasury to the paying wallet since the claim.
func TestNameChange_NeverLeftWithoutAHashIsCheckedAgainstTheLogs(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()
	r := paidStable(t)
	r.rail.head = 5000
	r.rail.refundErr, r.rail.refundAttempted, r.rail.refundHash = errors.New("connection reset"), true, ""
	view, err := r.svc.Cancel(ctx, r.user)
	if err != nil || view.FeeStatus != models.NameChangeFeeRefundUncertain {
		t.Fatalf("view %+v err %v", view, err)
	}
	if ch := r.change(t, view.ID); ch.FeeTxHash != "" || ch.RefundClaimedBlock == nil || *ch.RefundClaimedBlock != 5000 {
		t.Fatalf("change %+v", ch)
	}
	r.rail.payment = nil // no stray "any hash is mined" answers

	r.rail.transfers = []fakeTransferLog{{block: 5003, from: nameChangeTreasury, to: r.wallet, amount: 1_000_000}}
	if _, err := r.svc.AdminRecordRefundOutcome(ctx, admin, view.ID, NameChangeRefundOutcome{Sent: false}); !errors.Is(err, ErrNameChangeRefundMayHaveLeft) {
		t.Fatalf("a matching transfer since the claim: err = %v", err)
	}
	if r.rail.scannedFrom == nil || *r.rail.scannedFrom != 5000 {
		t.Fatalf("scanned from %v, want the claim block", r.rail.scannedFrom)
	}
	r.rail.scanErr = errors.New("getLogs refused")
	if _, err := r.svc.AdminRecordRefundOutcome(ctx, admin, view.ID, NameChangeRefundOutcome{Sent: false}); !errors.Is(err, ErrNameChangeChainUnreadable) {
		t.Fatalf("a scan that cannot finish: err = %v", err)
	}
	r.rail.scanErr = nil
	r.rail.transfers = []fakeTransferLog{
		{block: 4999, from: nameChangeTreasury, to: r.wallet, amount: 1_000_000}, // before the claim
		{block: 5003, from: nameChangeTreasury, to: r.wallet, amount: 999_999},   // another amount
		{block: 5004, from: nameChangeTreasury, to: nameChangeTreasury, amount: 1_000_000},
	}
	av, err := r.svc.AdminRecordRefundOutcome(ctx, admin, view.ID, NameChangeRefundOutcome{Sent: false, Note: "nothing on basescan"})
	if err != nil || av.FeeStatus != models.NameChangeFeeRefundFailed {
		t.Fatalf("view %+v err %v", av, err)
	}
}

// A hash left from an earlier send never stands for a later one: the claim
// clears it, so an uncertain retry with no hash of its own is checked against
// the logs, not against the old (unknown) transaction.
func TestNameChange_RefundClaimClearsAnEarlierSendsHash(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()
	r, id, maybeTx := uncertainRefund(t)
	r.rail.payment, r.rail.txUnknown = nil, true
	if _, err := r.svc.AdminRecordRefundOutcome(ctx, admin, id, NameChangeRefundOutcome{Sent: false}); err != nil {
		t.Fatal(err)
	}
	r.rail.head = 7000
	r.rail.refundErr, r.rail.refundAttempted, r.rail.refundHash = errors.New("connection reset"), true, ""
	if _, err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	ch := r.change(t, id)
	if ch.FeeStatus != models.NameChangeFeeRefundUncertain || ch.FeeTxHash == maybeTx || ch.FeeTxHash != "" {
		t.Fatalf("change %+v", ch)
	}
	r.rail.transfers = []fakeTransferLog{{block: 7001, from: nameChangeTreasury, to: r.wallet, amount: 1_000_000}}
	if _, err := r.svc.AdminRecordRefundOutcome(ctx, admin, id, NameChangeRefundOutcome{Sent: false}); !errors.Is(err, ErrNameChangeRefundMayHaveLeft) {
		t.Fatalf("the retry's transfer is on the chain: err = %v", err)
	}
}

// L2: "sent" is refused for a treasury transfer mined before the refund was
// claimed, and for a hash another change records, whatever its case.
func TestNameChange_SentRefundMustPostdateTheClaim(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()
	r, id, _ := uncertainRefund(t)
	sent := func(at time.Time) fakeChainTx {
		return fakeChainTx{pay: &StablePayment{Credited: big.NewInt(1_000_000), Sender: nameChangeTreasury, BlockTime: at, Confirmations: 2},
			from: nameChangeTreasury, to: r.wallet}
	}
	r.rail.byHash = map[string]fakeChainTx{
		txHash("a8"): sent(r.now.Add(-2 * time.Hour)), // an older transfer of the same amount
		txHash("a9"): sent(r.now.Add(time.Minute)),    // another change's, recorded in capitals
		txHash("aa"): sent(r.now.Add(time.Minute)),    // the refund
	}
	if err := r.db.Create(&models.UserNameChange{ID: uuid.New(), UserID: uuid.New(), Status: models.NameChangeStatusCancelled, FeeUSD: "1.00",
		PayMethod: models.NameChangePayStablecoin, FeeStatus: models.NameChangeFeeRefunded,
		FeeTxHash: "0x" + strings.ToUpper(strings.Repeat("a9", 32)), ExpiresAt: r.now}).Error; err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{txHash("a8"), txHash("a9")} {
		if _, err := r.svc.AdminRecordRefundOutcome(ctx, admin, id, NameChangeRefundOutcome{Sent: true, TxHash: bad}); !errors.Is(err, ErrNameChangeRefundUnproven) {
			t.Fatalf("sent as %s: err = %v", bad[:6], err)
		}
	}
	if av, err := r.svc.AdminRecordRefundOutcome(ctx, admin, id, NameChangeRefundOutcome{Sent: true, TxHash: txHash("aa")}); err != nil ||
		av.FeeStatus != models.NameChangeFeeRefunded {
		t.Fatalf("view %+v err %v", av, err)
	}
}

// M2: the victim confirming a squatted hash before our node has seen it — or
// while the chain cannot be read — is told to try again (payment_pending_chain),
// never payment_mismatch; once the node sees the victim signed it, the victim
// takes it.
func TestNameChange_ASquatTheChainCannotJudgeYetIsRetryable(t *testing.T) {
	ctx := context.Background()
	sq := newSquatRig(t)
	sq.rail.txUnknown = true // broadcast, not yet on our node
	if _, err := sq.svc.ConfirmPayment(ctx, sq.attacker, sq.h); !errors.Is(err, ErrNameChangePaymentOnChain) {
		t.Fatalf("the squat: err = %v", err)
	}
	if _, err := sq.svc.ConfirmPayment(ctx, sq.user, sq.h); !errors.Is(err, ErrNameChangePaymentOnChain) || errors.Is(err, ErrNameChangePaymentMismatch) {
		t.Fatalf("the victim, before the node knows the tx: err = %v", err)
	}
	sq.rail.txLookupErr = errors.New("rpc down")
	if _, err := sq.svc.ConfirmPayment(ctx, sq.user, sq.h); !errors.Is(err, ErrNameChangePaymentOnChain) {
		t.Fatalf("the victim, chain unreadable: err = %v", err)
	}
	if a := sq.change(t, sq.attackerChange); a.PayRef == nil || *a.PayRef != sq.h {
		t.Fatalf("an undecided claim is not released: %+v", a)
	}
	sq.rail.txLookupErr, sq.rail.pendingFrom = nil, map[string]string{sq.h: sq.wallet}
	if _, err := sq.svc.ConfirmPayment(ctx, sq.user, sq.h); !errors.Is(err, ErrNameChangePaymentOnChain) {
		t.Fatalf("the victim, once the node sees it: err = %v", err)
	}
	if v := sq.change(t, sq.victimChange); v.PayRef == nil || *v.PayRef != sq.h {
		t.Fatalf("the victim's change must hold its own payment: %+v", v)
	}
	if a := sq.change(t, sq.attackerChange); a.PayRef != nil {
		t.Fatalf("the squat must be released: %+v", a)
	}
}

// L1: a payment the sweep dropped as unknown to the chain that lands as dust is
// kept whole for support, not returned at the treasury's cost; at or above the
// minimum it goes back. (An admin's clear goes back whole whatever its size:
// TestNameChange_ClearedPaymentIsWatchedAndReturnedWhole.)
func TestNameChange_DroppedPaymentIsReturnedOnlyFromTheMinimum(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		credited int64
		returned bool
	}{
		"below the minimum": {credited: 100_000},
		"at the minimum":    {credited: 500_000, returned: true},
	} {
		t.Run(name, func(t *testing.T) {
			r := newNameChangeRig(t, NameChangeConfig{MinRefundUSD: "0.50"})
			out := r.namedPendingPayment(t)
			r.rail.txUnknown = true
			r.now = r.now.Add(7*24*time.Hour + nameChangeVanishedAfter + time.Minute)
			if _, err := r.svc.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if ch := r.change(t, out.Change.ID); ch.ClearedPayRef == nil || ch.PayRefClearedByAdmin {
				t.Fatalf("dropped %+v", ch)
			}
			r.now = r.now.Add(time.Hour)
			p := r.provenPayment(tc.credited)
			p.BlockTime = r.now
			r.rail.byHash = map[string]fakeChainTx{nameChangeTx: {pay: p}}
			r.rail.refundHash, r.rail.refundAttempted = txHash("d3"), true
			if _, err := r.svc.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			ch := r.change(t, out.Change.ID)
			if got := r.rail.refunds == 1; got != tc.returned {
				t.Fatalf("returned=%v, want %v (%+v)", got, tc.returned, ch)
			}
			if !tc.returned && (ch.FeeDecision != nameChangeFeeKeepAll || ch.FeeStatus != models.NameChangeFeeSettled ||
				!strings.Contains(ch.UserMessage, "support")) {
				t.Fatalf("dust must be kept whole, for support: %+v", ch)
			}
			if tc.returned && ch.FeeDecision != nameChangeFeeReturnOps {
				t.Fatalf("change %+v", ch)
			}
		})
	}
}

// L4: two refutations on one change, each from its own (stale) copy of the
// row, both keep their hash.
func TestNameChange_RacingRefutationsKeepEveryHash(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{})
	out := r.openStable(t)
	named, cleared := txHash("b3"), txHash("b4")
	r.db.Model(&models.UserNameChange{}).Where("id = ?", out.Change.ID).
		Updates(map[string]any{"pay_ref": named, "cleared_pay_ref": cleared, "pay_ref_cleared_at": r.now})
	stale := r.change(t, out.Change.ID)
	// The cleared hash mined deep, moving nothing from the proven wallet.
	r.rail.byHash = map[string]fakeChainTx{cleared: {pay: r.provenPayment(1_000_000), from: "0x2222222222222222222222222222222222222222"}}
	if out := r.svc.settleClearedPayment(ctx, stale); out != stableRefuted {
		t.Fatalf("outcome %v", out)
	}
	if !r.svc.clearPayRef(ctx, stale, named) {
		t.Fatal("the named hash was not cleared")
	}
	got := r.change(t, out.Change.ID)
	if len(got.RefutedPayRefs) != 2 || !slices.Contains(got.RefutedPayRefs, named) || !slices.Contains(got.RefutedPayRefs, cleared) {
		t.Fatalf("refuted %v, want both hashes", got.RefutedPayRefs)
	}
}

// I1: a late stablecoin payment is still taken by the change that closed
// waiting for it while a change paid another way is open; with no such change,
// the answer is an explicit "never" (payment_mismatch), not bad_state.
func TestNameChange_LatePaymentIsTakenBesideAnOpenMCGPChange(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{})
	closed := r.openStable(t)
	r.now = r.now.Add(8 * 24 * time.Hour)
	if _, err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	r.payMCGP(t)
	p := r.provenPayment(1_000_000)
	p.BlockTime = r.now
	r.rail.payment, r.rail.refundHash, r.rail.refundAttempted = p, "0xlate", true
	view, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx)
	if err != nil || view.ID != closed.Change.ID || view.FeeStatus != models.NameChangeFeeRefunded {
		t.Fatalf("view %+v err %v", view, err)
	}

	only := newNameChangeRig(t, NameChangeConfig{})
	only.payMCGP(t)
	if _, err := only.svc.ConfirmPayment(ctx, only.user, nameChangeTx); !errors.Is(err, ErrNameChangePaymentMismatch) {
		t.Fatalf("no stablecoin change it could pay: err = %v", err)
	}
}

// I1: a hash confirmed for a change already paid by another transaction is an
// explicit "never" too.
func TestNameChange_AHashForAChangePaidByAnotherIsAMismatch(t *testing.T) {
	ctx := context.Background()
	r := paidStable(t)
	r.rail.byHash = map[string]fakeChainTx{nameChangeTx: {pay: r.provenPayment(1_000_000)}}
	if _, err := r.svc.ConfirmPayment(ctx, r.user, txHash("b5")); !errors.Is(err, ErrNameChangePaymentMismatch) {
		t.Fatalf("err = %v", err)
	}
}
