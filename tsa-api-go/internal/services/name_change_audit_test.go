package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Tests for the final review and security-audit fixes: payments that mine but
// never match, hashes squatted by another account, payments we stopped waiting
// for, and the admin's refund and clearing decisions held to the chain.

func txHash(b string) string { return "0x" + strings.Repeat(b, 32) }

// A mined transaction that moved none of the fee from the proven wallet waits
// while a reorg could still change it, and once it is as deep as a payment
// must be it is refuted: the change is free to take the real payment, the
// hash is remembered, and it no longer holds the one-payment-per-transaction
// slot against anyone.
func TestNameChange_MinedPaymentMatchingNothingIsRefutedOnceDeep(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{MinConfirmations: 3})
	out := r.openStable(t)
	h := txHash("b1")
	p := r.provenPayment(1_000_000)
	p.Confirmations = 1
	// Paid by someone else's wallet, signed by the user: nothing matches the
	// proven wallet's transfer to the treasury.
	r.rail.byHash = map[string]fakeChainTx{h: {pay: p, from: "0x2222222222222222222222222222222222222222"}}
	if _, err := r.svc.ConfirmPayment(ctx, r.user, h); !errors.Is(err, ErrNameChangePaymentOnChain) {
		t.Fatalf("shallow, nothing matched: err = %v", err)
	}
	if ch := r.change(t, out.Change.ID); ch.PayRef == nil || *ch.PayRef != h {
		t.Fatalf("kept while shallow: %+v", ch)
	}
	p.Confirmations = 3
	if _, err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	ch := r.change(t, out.Change.ID)
	if ch.PayRef != nil || ch.Status != models.NameChangeStatusPaying || len(ch.RefutedPayRefs) != 1 || ch.RefutedPayRefs[0] != h {
		t.Fatalf("deep, nothing matched: %+v", ch)
	}
	if _, err := r.svc.ConfirmPayment(ctx, r.user, h); !errors.Is(err, ErrNameChangePaymentMismatch) {
		t.Fatalf("named again: err = %v", err)
	}
	// Nobody holds the slot: another account naming it is answered on its
	// merits, not "already paid for another change".
	otherKey, _ := crypto.GenerateKey()
	otherWallet := crypto.PubkeyToAddress(otherKey.PublicKey).Hex()
	other := r.seedUserWallet(t, models.VerificationStatusVerified, "EMEKA NWOSU", otherWallet)
	if _, err := r.svc.Create(ctx, other, NameChangeRequest{PayMethod: models.NameChangePayStablecoin, Chain: "base", Token: "USDC", ProvenWallet: otherWallet}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.ConfirmPayment(ctx, other, h); !errors.Is(err, ErrNameChangePaymentMismatch) || strings.Contains(err.Error(), "another change") {
		t.Fatalf("the freed hash: err = %v", err)
	}
	// And the user's real payment still pays.
	r.rail.byHash[txHash("b2")] = fakeChainTx{pay: r.provenPayment(1_000_000)}
	if view, err := r.svc.ConfirmPayment(ctx, r.user, txHash("b2")); err != nil || view.Status != models.NameChangeStatusPaid {
		t.Fatalf("view %+v err %v", view, err)
	}
}

// squatRig is a victim with a stablecoin change open, and an attacker with
// their own wallet and change who names the victim's transaction first.
type squatRig struct {
	*nameChangeRig
	victimChange, attackerChange uuid.UUID
	attacker                     uuid.UUID
	attackerWallet               string
	h                            string
}

func newSquatRig(t *testing.T) *squatRig {
	t.Helper()
	r := newNameChangeRig(t, NameChangeConfig{MinConfirmations: 3})
	key, _ := crypto.GenerateKey()
	sq := &squatRig{nameChangeRig: r, attackerWallet: crypto.PubkeyToAddress(key.PublicKey).Hex(), h: txHash("c1")}
	sq.attacker = r.seedUserWallet(t, models.VerificationStatusVerified, "EMEKA NWOSU", sq.attackerWallet)
	a, err := r.svc.Create(context.Background(), sq.attacker, NameChangeRequest{PayMethod: models.NameChangePayStablecoin,
		Chain: "base", Token: "USDC", ProvenWallet: sq.attackerWallet})
	if err != nil {
		t.Fatal(err)
	}
	sq.attackerChange = a.Change.ID
	sq.victimChange = r.openStable(t).Change.ID
	return sq
}

// Anyone could name a victim's pending transaction for their own change first;
// the victim's confirm then tests that claim instead of being refused, and the
// transaction goes to the change of the wallet that sent it.
func TestNameChange_ASquattedTransactionGoesToItsPayer(t *testing.T) {
	ctx := context.Background()
	t.Run("named from the mempool", func(t *testing.T) {
		sq := newSquatRig(t)
		sq.rail.pendingFrom = map[string]string{sq.h: sq.wallet} // the victim signed it; not mined
		if _, err := sq.svc.ConfirmPayment(ctx, sq.attacker, sq.h); !errors.Is(err, ErrNameChangePaymentOnChain) {
			t.Fatalf("the squat is stored like any pending hash: err = %v", err)
		}
		if _, err := sq.svc.ConfirmPayment(ctx, sq.user, sq.h); !errors.Is(err, ErrNameChangePaymentOnChain) {
			t.Fatalf("the victim's confirm: err = %v", err)
		}
		if v := sq.change(t, sq.victimChange); v.PayRef == nil || *v.PayRef != sq.h {
			t.Fatalf("the victim's change must hold its own payment: %+v", v)
		}
		a := sq.change(t, sq.attackerChange)
		if a.PayRef != nil || len(a.RefutedPayRefs) != 1 {
			t.Fatalf("the squatter's claim must be released and remembered: %+v", a)
		}
		if _, err := sq.svc.ConfirmPayment(ctx, sq.attacker, sq.h); !errors.Is(err, ErrNameChangePaymentMismatch) {
			t.Fatalf("the squatter naming it again: err = %v", err)
		}
		// It mines: the victim's fee is proven.
		sq.rail.pendingFrom = nil
		sq.rail.byHash = map[string]fakeChainTx{sq.h: {pay: sq.provenPayment(1_000_000)}}
		if view, err := sq.svc.ConfirmPayment(ctx, sq.user, sq.h); err != nil || view.Status != models.NameChangeStatusPaid {
			t.Fatalf("view %+v err %v", view, err)
		}
	})
	t.Run("held by another user's unmatched change", func(t *testing.T) {
		sq := newSquatRig(t)
		sq.rail.pendingFrom = map[string]string{sq.h: sq.wallet}
		if _, err := sq.svc.ConfirmPayment(ctx, sq.attacker, sq.h); !errors.Is(err, ErrNameChangePaymentOnChain) {
			t.Fatal(err)
		}
		// Mined and deep before any sweep looked: from the victim's wallet, so
		// nothing in it matches the squatter's.
		sq.rail.pendingFrom = nil
		sq.rail.byHash = map[string]fakeChainTx{sq.h: {pay: sq.provenPayment(1_000_000)}}
		if view, err := sq.svc.ConfirmPayment(ctx, sq.user, sq.h); err != nil || view.Status != models.NameChangeStatusPaid {
			t.Fatalf("view %+v err %v", view, err)
		}
		if a := sq.change(t, sq.attackerChange); a.PayRef != nil {
			t.Fatalf("squatter %+v", a)
		}
	})
	t.Run("a claim that may still be good stands", func(t *testing.T) {
		sq := newSquatRig(t)
		sq.rail.pendingFrom = map[string]string{sq.h: sq.attackerWallet} // the holder's own pending payment
		if _, err := sq.svc.ConfirmPayment(ctx, sq.attacker, sq.h); !errors.Is(err, ErrNameChangePaymentOnChain) {
			t.Fatal(err)
		}
		if _, err := sq.svc.ConfirmPayment(ctx, sq.user, sq.h); !errors.Is(err, ErrNameChangePaymentMismatch) ||
			!strings.Contains(err.Error(), "another change") {
			t.Fatalf("err = %v", err)
		}
		if a := sq.change(t, sq.attackerChange); a.PayRef == nil || *a.PayRef != sq.h {
			t.Fatalf("the holder keeps it: %+v", a)
		}
	})
}

// A payment we stopped waiting for — cleared by an admin, or dropped as unknown
// to the chain — is still watched for the late-payment window, found by a
// confirm from any of the user's changes, and sent back whole if it lands:
// under the minimum refund or not, and without using the user's allowance.
func TestNameChange_ClearedPaymentIsWatchedAndReturnedWhole(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{MinRefundUSD: "0.50", MaxRefundedCancels: 1})
	first := r.namedPendingPayment(t)
	r.rail.txUnknown = true
	if _, err := r.svc.AdminClearUnprovenPayment(ctx, uuid.New(), first.Change.ID, "dropped from the mempool"); err != nil {
		t.Fatal(err)
	}
	cleared := r.change(t, first.Change.ID)
	if cleared.ClearedPayRef == nil || *cleared.ClearedPayRef != nameChangeTx || cleared.PayRef != nil ||
		strings.Contains(cleared.UserMessage, "confirm the payment again") {
		t.Fatalf("cleared %+v", cleared)
	}

	// The user starts again; the old hash confirmed from the new change is
	// answered by the change watching it, and not taken by the new one.
	r.now = r.now.Add(time.Hour)
	second := r.openStable(t)
	if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); !errors.Is(err, ErrNameChangePaymentMismatch) ||
		!strings.Contains(err.Error(), "automatically") {
		t.Fatalf("the watched hash: err = %v", err)
	}
	if ch := r.change(t, second.Change.ID); ch.PayRef != nil {
		t.Fatalf("the new change took a hash another change is watching: %+v", ch)
	}

	// It lands after all — $0.10, under the minimum refund — and the sweep
	// sends it back whole, on its own.
	p := r.provenPayment(100_000)
	p.BlockTime = r.now.Add(-30 * time.Minute)
	r.rail.byHash = map[string]fakeChainTx{nameChangeTx: {pay: p}}
	r.rail.refundHash, r.rail.refundAttempted = txHash("d1"), true
	if _, err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got := r.change(t, first.Change.ID)
	if got.FeeStatus != models.NameChangeFeeRefunded || got.FeeDecision != nameChangeFeeReturnOps || got.RefundAmount != "100000" ||
		got.ClearedPayRef != nil || got.PayRef == nil || r.rail.refunds != 1 || !strings.EqualFold(r.rail.refundedTo, r.wallet) {
		t.Fatalf("change %+v refunds=%d", got, r.rail.refunds)
	}
	if next, err := r.svc.refundLimit(ctx, r.user); err != nil || next != nil {
		t.Fatalf("a return of ours must not use the user's allowance: next=%v err=%v", next, err)
	}
	if _, err := r.svc.Sweep(ctx); err != nil || r.rail.refunds != 1 {
		t.Fatalf("returned twice: refunds=%d err=%v", r.rail.refunds, err)
	}
}

// Dropped by the sweep as unknown to the chain, a payment that lands is found
// by the sweep itself — no confirm needed — within the window, and not after.
func TestNameChange_DroppedPaymentIsReturnedOnlyWithinTheWindow(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		wait     time.Duration
		returned bool
	}{
		"lands within the window": {wait: time.Hour, returned: true},
		"lands after the window":  {wait: nameChangeLatePaymentWindow + time.Hour},
	} {
		t.Run(name, func(t *testing.T) {
			r := newNameChangeRig(t, NameChangeConfig{})
			out := r.namedPendingPayment(t)
			r.rail.txUnknown = true
			r.now = r.now.Add(7*24*time.Hour + nameChangeVanishedAfter + time.Minute)
			if _, err := r.svc.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if ch := r.change(t, out.Change.ID); ch.Status != models.NameChangeStatusCancelled || ch.ClearedPayRef == nil {
				t.Fatalf("dropped %+v", ch)
			}
			r.now = r.now.Add(tc.wait)
			p := r.provenPayment(1_000_000)
			p.BlockTime = r.now
			r.rail.byHash = map[string]fakeChainTx{nameChangeTx: {pay: p}}
			r.rail.refundHash, r.rail.refundAttempted = txHash("d2"), true
			if _, err := r.svc.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if got := r.rail.refunds == 1; got != tc.returned {
				t.Fatalf("returned=%v, want %v (%+v)", got, tc.returned, r.change(t, out.Change.ID))
			}
		})
	}
}

// uncertainRefund is a paid stablecoin change cancelled into a refund whose
// broadcast ended uncertain — sent, maybe, as maybeTx.
func uncertainRefund(t *testing.T) (*nameChangeRig, uuid.UUID, string) {
	t.Helper()
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{})
	r.openStable(t)
	r.rail.payment = r.provenPayment(1_000_000)
	if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); err != nil {
		t.Fatal(err)
	}
	maybeTx := txHash("e1")
	r.rail.refundErr, r.rail.refundAttempted, r.rail.refundHash = errors.New("rpc timeout"), true, maybeTx
	view, err := r.svc.Cancel(ctx, r.user)
	if err != nil || view.FeeStatus != models.NameChangeFeeRefundUncertain {
		t.Fatalf("view %+v err %v", view, err)
	}
	return r, view.ID, maybeTx
}

// An admin's refund outcome is held to the chain: never their own change;
// only "sent" is taken — "never left" stays flagged for support — and only
// for a mined treasury transfer of exactly the refund to the paying wallet
// that no other change counts.
func TestNameChange_RefundOutcomeIsCheckedAgainstTheChain(t *testing.T) {
	ctx := context.Background()
	admin := uuid.New()
	r, id, maybeTx := uncertainRefund(t)
	if _, err := r.svc.AdminRecordRefundOutcome(ctx, r.user, id, NameChangeRefundOutcome{Sent: true, TxHash: txHash("f4")}); !errors.Is(err, ErrNameChangeSelfReview) {
		t.Fatalf("own change: err = %v", err)
	}
	for name, o := range map[string]NameChangeRefundOutcome{
		"never left":         {Sent: false, Note: "not on basescan"},
		"never left, hashed": {Sent: false, TxHash: maybeTx},
		"sent, no hash":      {Sent: true, TxHash: "  "},
	} {
		if _, err := r.svc.AdminRecordRefundOutcome(ctx, admin, id, o); !errors.Is(err, ErrNameChangeRefundSentOnly) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if got := r.change(t, id); got.FeeStatus != models.NameChangeFeeRefundUncertain || r.rail.refunds != 1 {
		t.Fatalf("a refused outcome moved the fee or sent again: %+v refunds=%d", got, r.rail.refunds)
	}

	refund := func(credited int64, sender, from, to string) fakeChainTx {
		return fakeChainTx{pay: &StablePayment{Credited: big.NewInt(credited), Sender: sender, BlockTime: r.now, Confirmations: 2}, from: from, to: to}
	}
	other := "0x3333333333333333333333333333333333333333"
	r.rail.byHash = map[string]fakeChainTx{
		txHash("f1"): refund(900_000, nameChangeTreasury, nameChangeTreasury, r.wallet),                                                          // short
		txHash("f2"): refund(1_000_000, other, nameChangeTreasury, r.wallet),                                                                     // not signed by the treasury
		txHash("f3"): refund(1_000_000, nameChangeTreasury, nameChangeTreasury, other),                                                           // to someone else
		txHash("f4"): refund(1_000_000, nameChangeTreasury, nameChangeTreasury, r.wallet),                                                        // the refund
		txHash("f5"): refund(1_000_000, nameChangeTreasury, nameChangeTreasury, r.wallet),                                                        // another change's
		maybeTx:      refund(1_000_000, nameChangeTreasury, nameChangeTreasury, r.wallet),                                                        // what was broadcast
		txHash("f6"): {pay: &StablePayment{Credited: big.NewInt(1_000_000), Sender: nameChangeTreasury}, from: nameChangeTreasury, to: r.wallet}, // unmined
	}
	if err := r.db.Create(&models.UserNameChange{ID: uuid.New(), UserID: uuid.New(), Status: models.NameChangeStatusCancelled, FeeUSD: "1.00",
		PayMethod: models.NameChangePayStablecoin, FeeStatus: models.NameChangeFeeRefunded, FeeTxHash: txHash("f5"), ExpiresAt: r.now}).Error; err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{txHash("f1"), txHash("f2"), txHash("f3"), txHash("f5"), txHash("f6"), txHash("f7")} {
		if _, err := r.svc.AdminRecordRefundOutcome(ctx, admin, id, NameChangeRefundOutcome{Sent: true, TxHash: bad}); !errors.Is(err, ErrNameChangeRefundUnproven) {
			t.Errorf("sent as %s: err = %v", bad[:6], err)
		}
	}
	if got := r.change(t, id); got.FeeStatus != models.NameChangeFeeRefundUncertain {
		t.Fatalf("a refused outcome moved the fee: %+v", got)
	}
	av, err := r.svc.AdminRecordRefundOutcome(ctx, admin, id, NameChangeRefundOutcome{Sent: true, TxHash: txHash("f4")})
	if err != nil || av.FeeStatus != models.NameChangeFeeRefunded || av.FeeTxHash != txHash("f4") {
		t.Fatalf("view %+v err %v", av, err)
	}
}

// The admin view says when a refund's outcome waits for an admin, and the list
// finds the changes whose money needs a look.
func TestNameChange_AdminSeesStuckRefunds(t *testing.T) {
	ctx := context.Background()
	r, id, _ := uncertainRefund(t)
	views, total, err := r.svc.AdminList(ctx, NameChangeFilter{FeeStatuses: []string{models.NameChangeFeeRefundUncertain, models.NameChangeFeeRefundSending}})
	if err != nil || total != 1 || len(views) != 1 || views[0].ID != id || !views[0].RefundStuck {
		t.Fatalf("views %+v total %d err %v", views, total, err)
	}
	if _, total, _ := r.svc.AdminList(ctx, NameChangeFilter{FeeStatuses: []string{models.NameChangeFeeRefunded}}); total != 0 {
		t.Fatalf("filtered on another fee status: %d", total)
	}
	// Sending, and recent: it may still finish — not yet an admin's.
	r.db.Exec("UPDATE user_name_changes SET fee_status = ?, updated_at = ? WHERE id = ?", models.NameChangeFeeRefundSending, r.now, id)
	if v, _ := r.svc.adminView(ctx, r.change(t, id)); v.RefundStuck {
		t.Fatal("a refund still sending is not stuck")
	}
	if _, err := r.svc.AdminRecordRefundOutcome(ctx, uuid.New(), id, NameChangeRefundOutcome{Sent: true, TxHash: txHash("f4")}); !errors.Is(err, ErrNameChangeBadState) {
		t.Fatalf("an outcome for a refund still sending: err = %v", err)
	}
	r.db.Exec("UPDATE user_name_changes SET updated_at = ? WHERE id = ?", r.now.Add(-time.Hour), id)
	if v, _ := r.svc.adminView(ctx, r.change(t, id)); !v.RefundStuck {
		t.Fatal("a refund sending past the alert window is stuck")
	}
}

// An account that lost its verification while its change ran is not renamed
// by a pass: the change is held, and an admin cannot approve it either.
func TestNameChange_AnUnverifiedAccountIsHeldNotRenamed(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{})
	r.payMCGP(t)
	ch := r.session(t, "sess-1")
	r.db.Exec("UPDATE users SET verification_status = ? WHERE id = ?", models.VerificationStatusRejected, r.user)
	ch = r.decide(t, strongPass(ch, "ADA OBI"), ch.ID)
	if ch.Status != models.NameChangeStatusHeldForReview || !strings.Contains(ch.HoldReason, "no longer verified") {
		t.Fatalf("change %+v", ch)
	}
	if _, err := r.svc.AdminApprove(ctx, uuid.New(), ch.ID, ""); !errors.Is(err, ErrNameChangeAccountUnverified) {
		t.Fatalf("approve: err = %v", err)
	}
	if got := r.userRow(t); got.VerifiedLegalName != "ADA CHIOMA OBI" || r.change(t, ch.ID).Status != models.NameChangeStatusHeldForReview {
		t.Fatalf("user %+v", got)
	}
}

// Every session a change ever bought is recognised — so first-time KYC never
// acts on a superseded re-verification — and nothing else is.
func TestNameChange_IsNameChangeSession(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{MaxAttempts: 3})
	r.payMCGP(t)
	r.decline(t, r.session(t, "sess-old"))
	r.session(t, "sess-new")
	for id, want := range map[string]bool{"sess-old": true, "sess-new": true, "sess-first-kyc": false, "sess-": false, "sess-%": false, "sess_old": false, "": false} {
		if got, err := r.svc.IsNameChangeSession(ctx, id); err != nil || got != want {
			t.Errorf("%q: %v err %v", id, got, err)
		}
	}
}

// An excess refund that failed, and is now under a raised minimum, is kept.
func TestNameChange_FailedExcessRefundUnderTheMinimumIsKept(t *testing.T) {
	ctx := context.Background()
	r := newNameChangeRig(t, NameChangeConfig{MinRefundUSD: "0.50"})
	r.openStable(t)
	r.rail.payment = r.provenPayment(1_700_000)
	if _, err := r.svc.ConfirmPayment(ctx, r.user, nameChangeTx); err != nil {
		t.Fatal(err)
	}
	r.rail.refundErr = ErrTreasuryKilled
	ch := r.session(t, "sess-1")
	if ch = r.decide(t, strongPass(ch, "ADA OBI"), ch.ID); ch.FeeStatus != models.NameChangeFeeRefundFailed {
		t.Fatalf("change %+v", ch)
	}
	r.svc.minRefundMicros = big.NewInt(1_000_000)
	r.now = r.now.Add(time.Hour)
	if _, err := r.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := r.change(t, ch.ID); got.FeeStatus != models.NameChangeFeeSettled || r.rail.refunds != 1 {
		t.Fatalf("change %+v refunds=%d", got, r.rail.refunds)
	}
}
