package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

type fakeFeeReader struct {
	kobo  *big.Int
	err   error
	calls int
}

func (f *fakeFeeReader) TransferFeeKobo(context.Context, string) (*big.Int, error) {
	f.calls++
	return f.kobo, f.err
}

type fakeLookup struct {
	tr    FWTransfer
	found bool
	err   error
	calls int
}

func (f *fakeLookup) FindForwardTransfer(context.Context, string, time.Time) (FWTransfer, bool, error) {
	f.calls++
	return f.tr, f.found, f.err
}

func (r *fwdRig) startTimedOut(t *testing.T, ref string) *models.P2PSettlementForward {
	t.Helper()
	r.gateway.err = errors.New("context deadline exceeded")
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit(ref, "1500.00")); err != nil {
		t.Fatal(err)
	}
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Update("created_at", time.Now().Add(-10*time.Minute))
	return r.forward(t)
}

// Lesson C-1: money that may leave is a durable marker first.
func TestForwarder_StartedMarkerCommittedBeforeGateway(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	var seen *time.Time
	r.gateway.onCall = func() {
		var f models.P2PSettlementForward
		r.db.First(&f, "trade_id = ?", r.trade.ID)
		seen = f.StartedAt
	}
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999100000001", "1500.00")); err != nil {
		t.Fatal(err)
	}
	if seen == nil {
		t.Fatal("the gateway was called before started_at was committed")
	}
}

func TestForwarder_TimeoutKeepsPendingNeverFailed(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	f := r.startTimedOut(t, "999100000002")
	if f.Status != models.P2PForwardPending || f.StartedAt == nil || f.NeedsAttention() || !ngnEqual(r.availableNGN(t), "1500.00") {
		t.Fatalf("forward = %s started=%v, want pending, started, credited", f.Status, f.StartedAt)
	}
	// Merchant impact (review residual 2): the operator queue shows the stored
	// reason, so the warning lives there, not only in the log line.
	if !strings.Contains(f.FailureReason, "may have paid — never pay by hand") {
		t.Fatalf("stored reason = %q, want the never-pay-by-hand warning", f.FailureReason)
	}
}

// Review C-2: a started row survives an outage that also breaks the balance
// read — seven re-drives, never "NOT paid".
func TestForwarder_StartedRowNeverFailsOnBalanceOrCeiling(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.startTimedOut(t, "999100000003")
	r.bal.err = errors.New("provider 503")
	for i := 0; i < 7; i++ {
		_, _ = r.fwd.RedrivePending(context.Background())
	}
	if f := r.forward(t); f.Status == models.P2PForwardFailed {
		t.Fatalf("a started forward was failed: %q", f.FailureReason)
	}
}

func TestForwarder_StartedRowNeverFailsWithoutAGateway(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.startTimedOut(t, "999100000004")
	r.fwd.gateway = nil
	_, _ = r.fwd.RedrivePending(context.Background())
	if f := r.forward(t); f.Status == models.P2PForwardFailed {
		t.Fatal("no gateway is never proof that nothing was paid")
	}
}

// I-1: the disburse webhook for a timed-out first call lands while the row is
// still pending: it settles and debits, with the payout id recorded.
func TestForwarder_WebhookBeforeRedriveSettles(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	f := r.startTimedOut(t, "999100000005")
	if err := r.fwd.ConfirmPayout(context.Background(), f.Reference, "trf_late", true, ""); err != nil {
		t.Fatal(err)
	}
	got := r.forward(t)
	if got.Status != models.P2PForwardSettled || got.PayoutID != "trf_late" || !ngnEqual(r.availableNGN(t), "0") {
		t.Fatalf("forward = %s id=%s available=%s, want settled and debited", got.Status, got.PayoutID, r.availableNGN(t))
	}
}

// I-1: a re-drive asks the provider by reference before it re-POSTs.
func TestForwarder_RedriveLooksUpTheReferenceFirst(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.startTimedOut(t, "999100000006")
	lk := &fakeLookup{found: true, tr: FWTransfer{ID: "trf_found", Status: "PENDING"}}
	r.fwd.SetTransferLookup(lk)
	calls := len(r.gateway.calls)
	r.gateway.err = nil
	_, _ = r.fwd.RedrivePending(context.Background())
	got := r.forward(t)
	if lk.calls != 1 || len(r.gateway.calls) != calls || got.Status != models.P2PForwardSubmitted || got.PayoutID != "trf_found" {
		t.Fatalf("lookups=%d posts=%d status=%s id=%s, want found by reference, no re-POST", lk.calls, len(r.gateway.calls)-calls, got.Status, got.PayoutID)
	}
}

// Not found on a complete scan → re-ask on the SAME reference.
func TestForwarder_RedriveResendsTheSameReference(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.startTimedOut(t, "999100000007")
	r.fwd.SetTransferLookup(&fakeLookup{found: false})
	r.gateway.err = nil
	_, _ = r.fwd.RedrivePending(context.Background())
	if len(r.gateway.calls) != 2 || r.gateway.calls[0].Reference != r.gateway.calls[1].Reference || r.forward(t).Status != models.P2PForwardSubmitted {
		t.Fatalf("calls=%d status=%s, want one re-ask on the same reference", len(r.gateway.calls), r.forward(t).Status)
	}
}

// Re-review I-D: lookup errors never count toward the ceiling; the row stays
// re-checkable, and the first successful lookup settles it.
func TestForwarder_LookupErrorsNeverReachTheCeiling(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.startTimedOut(t, "999100000013")
	lk := &fakeLookup{err: errors.New("flutterwave list-transfers: more than 200 pages")}
	r.fwd.SetTransferLookup(lk)
	for i := 0; i < 7; i++ {
		r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Update("updated_at", time.Now().Add(-2*time.Hour))
		_, _ = r.fwd.RedrivePending(context.Background())
	}
	lk.err, lk.found, lk.tr = nil, true, FWTransfer{ID: "trf_late", Status: "SUCCEEDED"}
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Update("updated_at", time.Now().Add(-2*time.Hour))
	_, _ = r.fwd.RedrivePending(context.Background())
	if got := r.forward(t); got.Status != models.P2PForwardSettled || lk.calls != 8 {
		t.Fatalf("status=%s lookups=%d, want settled on the 8th (SUCCEEDED counts as success, m-1)", got.Status, lk.calls)
	}
}

// Re-review I-D: past the re-POST ceiling a started row is still looked up,
// hourly, never re-POSTed and never failed.
func TestForwarder_PastTheCeilingIsRecheckedSlowly(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.startTimedOut(t, "999100000014")
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Updates(map[string]any{"attempts": 9, "updated_at": time.Now().Add(-10 * time.Minute)})
	lk := &fakeLookup{found: false}
	r.fwd.SetTransferLookup(lk)
	posts := len(r.gateway.calls)
	_, _ = r.fwd.RedrivePending(context.Background())
	if lk.calls != 0 {
		t.Fatal("a row past the ceiling is re-checked hourly, not every 5 minutes")
	}
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Update("updated_at", time.Now().Add(-2*time.Hour))
	_, _ = r.fwd.RedrivePending(context.Background())
	if f := r.forward(t); lk.calls != 1 || len(r.gateway.calls) != posts || f.Status != models.P2PForwardPending {
		t.Fatalf("lookups=%d posts=%d status=%s, want one lookup, no re-POST, still pending", lk.calls, len(r.gateway.calls)-posts, f.Status)
	}
}

// Re-review I-D: the operator's recheck never fails a forward as not paid.
func TestForwarder_RecheckNeverFails(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	f := r.startTimedOut(t, "999100000015")
	r.fwd.SetTransferLookup(&fakeLookup{err: errors.New("provider 503")})
	_ = r.fwd.Recheck(context.Background(), f.ID)
	if got := r.forward(t); got.Status != models.P2PForwardPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
}

// The provider saying FAILED is the one other way a started row may fail.
func TestForwarder_ProviderConfirmedFailureFailsAStartedRow(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.startTimedOut(t, "999100000008")
	r.fwd.SetTransferLookup(&fakeLookup{found: true, tr: FWTransfer{ID: "trf_x", Status: "FAILED"}})
	_, _ = r.fwd.RedrivePending(context.Background())
	if f := r.forward(t); f.Status != models.P2PForwardFailed {
		t.Fatalf("status = %s, want failed on the provider's own FAILED", f.Status)
	}
}

func TestForwarder_RefusalOnRetryIsNotFinal(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.startTimedOut(t, "999100000009")
	r.gateway.err = &ProviderRejectedError{Err: errors.New("reference already used")}
	_, _ = r.fwd.RedrivePending(context.Background())
	if f := r.forward(t); f.Status != models.P2PForwardPending {
		t.Fatalf("status = %s, want pending (outcome unknown)", f.Status)
	}
}

func TestForwarder_FirstAttemptRefusalFails(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	r.gateway.err = &ProviderRejectedError{Err: errors.New("invalid account")}
	_ = r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999100000010", "1500.00"))
	if f := r.forward(t); f.Status != models.P2PForwardFailed {
		t.Fatalf("status = %s, want failed (a first-attempt refusal moved no money)", f.Status)
	}
}

// A second caller that read the row before the marker never calls the gateway.
func TestForwarder_StartedByAnotherAttemptIsLeftAlone(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	f := r.startTimedOut(t, "999100000011")
	stale := *f
	stale.StartedAt = nil
	if first, err := r.fwd.markStarted(context.Background(), &stale); err != nil || first {
		t.Fatalf("first=%v err=%v: a row already started is never a first attempt", first, err)
	}
}

// S47 / P10: the actual fee, read once.
func TestForwarder_RecordFeeWritesOnce(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999100000012", "1500.00")); err != nil {
		t.Fatal(err)
	}
	fees := &fakeFeeReader{kobo: big.NewInt(1075)}
	r.fwd.SetFeeReader(fees)
	f := r.forward(t)
	if err := r.fwd.Confirm(context.Background(), f.Reference, true, ""); err != nil {
		t.Fatal(err)
	}
	_ = r.fwd.RecordFee(context.Background(), f.ID)
	got := r.forward(t)
	if got.FeeKobo == nil || *got.FeeKobo != "1075" || fees.calls != 1 {
		t.Fatalf("fee=%v calls=%d, want 1075 read once", got.FeeKobo, fees.calls)
	}
}

func TestTransferFee_DecodesShapes(t *testing.T) {
	for _, raw := range []string{`10.75`, `"10.75"`, `{"value":10.75,"currency":"NGN"}`, `{"amount":"10.75"}`, `[5,5.75]`} {
		got, err := decodeFWTransferFeeKobo(json.RawMessage(raw))
		if err != nil || got.Int64() != 1075 {
			t.Fatalf("%s → %v %v, want 1075 kobo", raw, got, err)
		}
	}
	for _, raw := range []string{``, `null`, `""`, `{"x":1}`, `"abc"`} {
		if _, err := decodeFWTransferFeeKobo(json.RawMessage(raw)); err == nil {
			t.Fatalf("%q must be an error, never a zero fee", raw)
		}
	}
}

func TestInsertForwardAndCredit_ReplayCreditsOnce(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	for i := 0; i < 2; i++ {
		if err := r.db.Transaction(func(tx *gorm.DB) error {
			_, _, err := r.src.insertForwardAndCredit(tx, r.trade, r.pm, "pool:replay")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !ngnEqual(r.availableNGN(t), "1500.00") {
		t.Fatalf("available = %s, want one credit", r.availableNGN(t))
	}
}

// Review m-11: the fee is decoded exactly (decimal text → kobo), never through
// float64 — half a kobo rounds up, and a large value keeps every digit.
func TestTransferFee_DecodesExactly(t *testing.T) {
	for raw, want := range map[string]string{
		`"10.075"`:            "1008",
		`10.075`:              "1008",
		`"90071992547409.93"`: "9007199254740993",
		`[0.1, 0.2]`:          "30",
		`{"value":"1e1"}`:     "1000",
	} {
		got, err := decodeFWTransferFeeKobo(json.RawMessage(raw))
		if err != nil || got.String() != want {
			t.Fatalf("%s → %v %v, want %s kobo", raw, got, err, want)
		}
	}
	if _, err := decodeFWTransferFeeKobo(json.RawMessage(`"-1.00"`)); err == nil {
		t.Fatal("a negative fee must be an error")
	}
}

// Merchant impact (review residual 3): the disburse webhook's Confirm makes
// no provider call for the fee — the sweep reads it on its own cadence, once.
func TestForwarder_FeeIsReadByTheSweepNotTheWebhook(t *testing.T) {
	r := newForwardRig(t, "1500.00")
	if err := r.src.Accept(context.Background(), r.trade.ID, p2pInboundCredit("999100000013", "1500.00")); err != nil {
		t.Fatal(err)
	}
	fees := &fakeFeeReader{kobo: big.NewInt(1075)}
	r.fwd.SetFeeReader(fees)
	f := r.forward(t)
	if err := r.fwd.Confirm(context.Background(), f.Reference, true, ""); err != nil {
		t.Fatal(err)
	}
	if fees.calls != 0 {
		t.Fatalf("fee reads on the webhook = %d, want 0", fees.calls)
	}
	for i := 0; i < 2; i++ {
		if _, err := r.fwd.RecordSettledFees(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.forward(t); got.FeeKobo == nil || *got.FeeKobo != "1075" || fees.calls != 1 {
		t.Fatalf("fee=%v calls=%d, want 1075 read once by the sweep", got.FeeKobo, fees.calls)
	}
}
