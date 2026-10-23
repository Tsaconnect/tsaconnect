package services

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

type fakeSwitch struct {
	on  bool
	err error
}

func (f *fakeSwitch) Enabled(context.Context) (bool, error) { return f.on, f.err }

type recordingAlerts struct {
	mu   sync.Mutex
	seen []SellerPayoutAlert
}

func (r *recordingAlerts) NotifySellerPayoutReview(_ context.Context, a SellerPayoutAlert) {
	r.mu.Lock()
	r.seen = append(r.seen, a)
	r.mu.Unlock()
}

func newAdmin(t *testing.T, r *payoutRig) (*SellerPayoutAdmin, uuid.UUID) {
	t.Helper()
	if err := r.db.AutoMigrate(&models.P2PAuditLog{}); err != nil {
		// Postgres-only column types: fall back to raw DDL for the audit table.
		r.db.Exec(`CREATE TABLE IF NOT EXISTS p2p_audit_logs (id TEXT PRIMARY KEY, actor_id TEXT, trade_id TEXT, ad_id TEXT, action TEXT, metadata TEXT, created_at DATETIME)`)
	}
	return NewSellerPayoutAdmin(r.db, r.svc), uuid.New()
}

func (r *payoutRig) park(status string, safe bool) {
	r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Updates(map[string]any{
		"status": models.SellerPayoutNeedsReview, "parked_from": status, "retry_safe": safe, "failure_reason": "test",
	})
}

func TestSellerPayout_TheSwitchPausesEverySendButNotTheProofs(t *testing.T) {
	r := newPayoutRig(t)
	sw := &fakeSwitch{on: false}
	r.svc.WithSwitch(sw)
	r.mustStep() // the release is proven: bookkeeping continues
	r.wantStatus(models.SellerPayoutReceived)
	r.mustStep()
	r.mustStep()
	if r.broadcasts() != 0 || len(r.legs(models.RampLegKindBridge)) != 0 {
		t.Fatal("a paused machine must not bridge")
	}
	sw.on = true
	r.mustStep()
	r.wantStatus(models.SellerPayoutBridging)
	if r.broadcasts() == 0 {
		t.Fatal("switched back on, the bridge goes")
	}
}

func TestSellerPayout_AnUnreadableSwitchHoldsSends(t *testing.T) {
	r := newPayoutRig(t)
	r.svc.WithSwitch(&fakeSwitch{err: errors.New("db down")})
	r.mustStep()
	r.mustStep()
	if r.broadcasts() != 0 {
		t.Fatal("an unknown switch is not an open one")
	}
}

func TestSellerPayout_ParksRecordWhereAndWhetherARetryIsSafe(t *testing.T) {
	// Stopped before anything was sent: safe.
	r := newPayoutRig(t)
	alerts := &recordingAlerts{}
	r.svc.WithAlerts(alerts)
	r.mustStep()
	r.treasury.SetKillSwitch(true)
	r.mustStep()
	got := r.wantStatus(models.SellerPayoutNeedsReview)
	if got.ParkedFrom != models.SellerPayoutReceived || !got.RetrySafe {
		t.Fatalf("parked_from=%q retry_safe=%v; a stop before the bridge is safe to retry", got.ParkedFrom, got.RetrySafe)
	}
	if len(alerts.seen) != 1 || alerts.seen[0].PayoutID != r.payout.ID || !alerts.seen[0].RetrySafe {
		t.Fatalf("operators were told %+v", alerts.seen)
	}

	// A broadcast bridge that never confirmed: never safe.
	r2 := newPayoutRig(t)
	r2.mustStep()
	r2.mustStep()
	r2.status.set(LiFiStatusFailed, "")
	r2.mustStep()
	got = r2.wantStatus(models.SellerPayoutNeedsReview)
	if got.RetrySafe {
		t.Fatal("a bridge that broadcast is never safe to retry blindly")
	}
}

func TestSellerPayoutAdmin_RetryPutsItBackAndTheMachineCarriesOn(t *testing.T) {
	r := newPayoutRig(t)
	admin, who := newAdmin(t, r)
	ctx := context.Background()
	r.mustStep()
	r.treasury.SetKillSwitch(true)
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)

	if err := admin.Retry(ctx, r.payout.ID, who, "  ", false); !errors.Is(err, ErrPayoutNeedsNote) {
		t.Fatalf("no note: %v", err)
	}
	r.treasury.SetKillSwitch(false)
	if err := admin.Retry(ctx, r.payout.ID, who, "lane funded again", false); err != nil {
		t.Fatal(err)
	}
	got := r.wantStatus(models.SellerPayoutReceived)
	if got.FailureReason != "" || got.RetrySafe {
		t.Fatalf("a retried payout is clean: %+v", got)
	}
	r.mustStep()
	r.wantStatus(models.SellerPayoutBridging)
	if err := admin.Retry(ctx, r.payout.ID, who, "again", false); !errors.Is(err, ErrPayoutNotParked) {
		t.Fatalf("a payout that is moving: %v", err)
	}
}

func TestSellerPayoutAdmin_AnUnsafeRetryNeedsForce(t *testing.T) {
	r := newPayoutRig(t)
	admin, who := newAdmin(t, r)
	r.park(models.SellerPayoutDepositing, false)
	if err := admin.Retry(context.Background(), r.payout.ID, who, "checked the chain", false); !errors.Is(err, ErrPayoutRetryUnsafe) {
		t.Fatalf("got %v", err)
	}
	if err := admin.Retry(context.Background(), r.payout.ID, who, "checked the chain: deposit never left", true); err != nil {
		t.Fatal(err)
	}
	r.wantStatus(models.SellerPayoutDepositing)
}

func TestSellerPayoutAdmin_MarkPaidNeedsAReferenceAndOnlyWorksWhenParked(t *testing.T) {
	r := newPayoutRig(t)
	admin, who := newAdmin(t, r)
	ctx := context.Background()
	if err := admin.MarkPaid(ctx, r.payout.ID, who, "paid by hand", "NIP-123", false); !errors.Is(err, ErrPayoutNotParked) {
		t.Fatalf("not parked: %v", err)
	}
	r.park(models.SellerPayoutDeposited, false)
	if err := admin.MarkPaid(ctx, r.payout.ID, who, "paid by hand", "", false); !errors.Is(err, ErrPayoutNeedsNote) {
		t.Fatalf("no reference: %v", err)
	}
	if err := admin.MarkPaid(ctx, r.payout.ID, who, "paid by hand", "NIP-123", false); err != nil {
		t.Fatal(err)
	}
	got := r.wantStatus(models.SellerPayoutPaid)
	if got.FailureReason != "paid by hand: NIP-123" {
		t.Fatalf("reason = %q", got.FailureReason)
	}
	var audits int64
	r.db.Table("p2p_audit_logs").Where("action = ?", "seller_payout.marked_paid").Count(&audits)
	if audits != 1 {
		t.Fatalf("audit rows = %d", audits)
	}
}

func TestSellerPayoutAdmin_CancelOnlyWhenNothingWasSent(t *testing.T) {
	r := newPayoutRig(t)
	admin, who := newAdmin(t, r)
	ctx := context.Background()
	r.park(models.SellerPayoutAwaitingRelease, true) // nothing proven, nothing held
	if err := admin.Cancel(ctx, r.payout.ID, who, "seller asked"); err != nil {
		t.Fatal(err)
	}
	r.wantStatus(models.SellerPayoutCancelled)

	// The treasury holds proven funds: closing the payout would hide them.
	r1 := newPayoutRig(t)
	admin1, _ := newAdmin(t, r1)
	r1.mustStep() // received: 100 USDC held
	r1.park(models.SellerPayoutReceived, true)
	if err := admin1.Cancel(ctx, r1.payout.ID, who, "seller asked"); !errors.Is(err, ErrPayoutHoldsFunds) {
		t.Fatalf("got %v; cancelling would hide funds the treasury holds", err)
	}
	r1.wantStatus(models.SellerPayoutNeedsReview)

	// After a bridge broadcast, cancelling would hide money in flight.
	r2 := newPayoutRig(t)
	admin2, _ := newAdmin(t, r2)
	r2.mustStep()
	r2.mustStep() // bridge broadcast
	r2.park(models.SellerPayoutBridging, false)
	if err := admin2.Cancel(ctx, r2.payout.ID, who, "give up"); !errors.Is(err, ErrPayoutHoldsFunds) && !errors.Is(err, ErrWalletPaymentNotSafe) {
		t.Fatalf("got %v", err)
	}
	r2.wantStatus(models.SellerPayoutNeedsReview)
}

func TestSellerPayoutAdmin_PayWalletSendsOnceAndProvesIt(t *testing.T) {
	r := newPayoutRig(t)
	admin, who := newAdmin(t, r)
	ctx := context.Background()
	r.mustStep() // received, 100 USDC proven
	r.park(models.SellerPayoutReceived, true)

	if err := admin.PayWallet(ctx, r.payout.ID, who, "Paycrest has no venue for this size"); err != nil {
		t.Fatal(err)
	}
	r.wantStatus(models.SellerPayoutPayingWallet)
	token, from, to, amount := r.sonic.native.transferCall()
	if to != sponsorTestUser || amount.String() != "100000000" || token != sponsorTestUSDC || from != r.treasury.Address() {
		t.Fatalf("transfer %s %s -> %s %s; want 100 USDC from the treasury to the seller's wallet", token, from, to, amount)
	}
	sent := r.broadcasts()

	r.sonicTx.mu.Lock()
	r.sonicTx.err = errors.New("not mined yet")
	r.sonicTx.mu.Unlock()
	r.mustStep() // not proven yet
	r.mustStep()
	if r.broadcasts() != sent {
		t.Fatal("the wallet payment is sent once")
	}
	r.wantStatus(models.SellerPayoutPayingWallet)

	r.sonicTx.mu.Lock()
	r.sonicTx.err = nil
	r.sonicTx.amount = big.NewInt(100_000000)
	r.sonicTx.mu.Unlock()
	r.mustStep()
	r.wantStatus(models.SellerPayoutPaidWallet)
	if legs := r.legs(models.RampLegKindRefund); len(legs) != 1 || legs[0].Status != models.RampLegStatusConfirmed {
		t.Fatalf("refund legs = %+v", legs)
	}
	// Terminal: nothing more moves.
	r.mustStep()
	if r.broadcasts() != sent {
		t.Fatal("a paid payout moves nothing")
	}
}

func TestSellerPayoutAdmin_PayWalletRefusesOnceMoneyMayHaveMoved(t *testing.T) {
	admin := func(r *payoutRig) (*SellerPayoutAdmin, uuid.UUID) { return newAdmin(t, r) }
	ctx := context.Background()

	t.Run("a bridge that broadcast", func(t *testing.T) {
		r := newPayoutRig(t)
		a, who := admin(r)
		r.mustStep()
		r.mustStep()
		r.park(models.SellerPayoutBridging, false)
		sent := r.broadcasts()
		if err := a.PayWallet(ctx, r.payout.ID, who, "x"); !errors.Is(err, ErrWalletPaymentNotSafe) {
			t.Fatalf("got %v", err)
		}
		if r.broadcasts() != sent {
			t.Fatal("nothing may be sent")
		}
	})
	t.Run("a Paycrest order exists", func(t *testing.T) {
		r := newPayoutRig(t)
		a, who := admin(r)
		r.toBridged()
		r.mustStep() // ordered
		r.park(models.SellerPayoutOrdered, false)
		if err := a.PayWallet(ctx, r.payout.ID, who, "x"); err == nil {
			t.Fatal("an order that exists may be fundable: no wallet payment")
		}
	})
	t.Run("no proven amount", func(t *testing.T) {
		r := newPayoutRig(t)
		a, who := admin(r)
		r.park(models.SellerPayoutAwaitingRelease, true)
		if err := a.PayWallet(ctx, r.payout.ID, who, "x"); !errors.Is(err, ErrWalletPaymentUnavailable) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("not parked", func(t *testing.T) {
		r := newPayoutRig(t)
		a, who := admin(r)
		r.mustStep()
		if err := a.PayWallet(ctx, r.payout.ID, who, "x"); err == nil {
			t.Fatal("a payout that is moving is not paid by hand")
		}
	})
}

func TestSellerPayoutAdmin_DetailGathersTheEvidence(t *testing.T) {
	r := newPayoutRig(t)
	admin, _ := newAdmin(t, r)
	r.toBridged()
	d, err := admin.Detail(context.Background(), r.payout.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Payout.ID != r.payout.ID || len(d.Legs) != 1 || d.Order == nil || d.Order.ReleaseTxHash != "0xrelease" {
		t.Fatalf("detail = %+v", d)
	}
	if _, err := admin.Detail(context.Background(), uuid.New()); !errors.Is(err, ErrPayoutNotFound) {
		t.Fatalf("missing: %v", err)
	}
	rows, err := admin.List(context.Background(), nil, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("queue = %d, %v", len(rows), err)
	}
}

func TestSellerPayout_ExposureAndGasAreCounted(t *testing.T) {
	r := newPayoutRig(t)
	r.mustStep() // received: 100 USDC held on sonic
	ctx := context.Background()
	if usd, err := sellerPayoutExposureUSD(ctx, r.db, "sonic", uuid.New()); err != nil || usd != 100 {
		t.Fatalf("sonic exposure = %v, %v", usd, err)
	}
	if usd, _ := sellerPayoutExposureUSD(ctx, r.db, "sonic", r.payout.ID); usd != 0 {
		t.Fatalf("a payout is not its own exposure: %v", usd)
	}
	if usd, _ := sellerPayoutExposureUSD(ctx, r.db, "base", uuid.New()); usd != 0 {
		t.Fatalf("nothing is on base yet: %v", usd)
	}
	units, err := NewSellerPayoutCommitments(r.db, "sonic").Units(ctx)
	if err != nil || units != treasuryRouteReserveGas {
		t.Fatalf("sonic gas owed = %d, %v; want one route", units, err)
	}
	if units, _ := NewSellerPayoutCommitments(r.db, "base").Units(ctx); units != 0 {
		t.Fatalf("base gas owed = %d", units)
	}
	_ = time.Second
}

func TestSellerPayoutAdmin_MarkPaidRefusesWhileTheSaleIsStillAlive(t *testing.T) {
	r := newPayoutRig(t)
	admin, who := newAdmin(t, r)
	ctx := context.Background()
	r.toBridged()
	r.mustStep() // ordered: a live Paycrest sale
	r.park(models.SellerPayoutOrdered, false)
	if err := admin.MarkPaid(ctx, r.payout.ID, who, "paid by hand", "NIP-1", false); !errors.Is(err, ErrPayoutStillLive) {
		t.Fatalf("got %v; paying by hand while Paycrest may also pay is a double payment", err)
	}
	r.wantStatus(models.SellerPayoutNeedsReview)
	if err := admin.MarkPaid(ctx, r.payout.ID, who, "paid by hand; Paycrest order cancelled by me", "NIP-1", true); err != nil {
		t.Fatalf("forced: %v", err)
	}
	r.wantStatus(models.SellerPayoutPaid)
}

func TestSellerPayoutAdmin_ParkedPayoutsListFirst(t *testing.T) {
	r := newPayoutRig(t)
	admin, _ := newAdmin(t, r)
	// Many waiting payouts newer than the parked one.
	r.park(models.SellerPayoutReceived, true)
	for i := 0; i < 5; i++ {
		row := *r.payout
		row.ID, row.MarketplaceOrderID = uuid.New(), uuid.New()
		row.Status, row.CreatedAt = models.SellerPayoutAwaitingRelease, r.clock.Add(time.Duration(i+1)*time.Hour)
		if err := r.db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows, err := admin.List(context.Background(), nil, 2)
	if err != nil || len(rows) != 2 || rows[0].ID != r.payout.ID {
		t.Fatalf("rows = %+v, %v; the parked payout must come first even when older", rows, err)
	}
}

func TestSellerPayoutAdmin_RetryFromASaleStopRunsTheSaleAgain(t *testing.T) {
	ctx := context.Background()
	t.Run("no live order: back to where the sale is opened", func(t *testing.T) {
		r := newPayoutRig(t)
		admin, who := newAdmin(t, r)
		r.toBridged()
		r.park(models.SellerPayoutSelling, false)
		if err := admin.Retry(ctx, r.payout.ID, who, "no order at Paycrest", true); err != nil {
			t.Fatal(err)
		}
		r.wantStatus(models.SellerPayoutBridged)
		r.mustStep()
		r.wantStatus(models.SellerPayoutOrdered)
	})
	t.Run("a live order exists: adopted, not opened twice", func(t *testing.T) {
		r := newPayoutRig(t)
		admin, who := newAdmin(t, r)
		r.toBridged()
		r.mustStep() // ordered, one order
		order := r.reload().PaycrestOrderID
		r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Updates(map[string]any{"paycrest_order_id": nil})
		r.park(models.SellerPayoutSelling, false)
		if err := admin.Retry(ctx, r.payout.ID, who, "order exists", true); err != nil {
			t.Fatal(err)
		}
		r.wantStatus(models.SellerPayoutSelling)
		r.mustStep()
		got := r.wantStatus(models.SellerPayoutOrdered)
		if got.PaycrestOrderID == nil || *got.PaycrestOrderID != *order || r.seller.calls != 1 {
			t.Fatalf("payout = %+v, calls = %d", got, r.seller.calls)
		}
	})
}

func TestNotParkedIfRaced(t *testing.T) {
	if !errors.Is(notParkedIfRaced(errSellerPayoutTransition), ErrPayoutNotParked) {
		t.Fatal("a lost race must read as not parked (409)")
	}
	other := errors.New("db down")
	if notParkedIfRaced(other) != other {
		t.Fatal("other errors pass through")
	}
}
