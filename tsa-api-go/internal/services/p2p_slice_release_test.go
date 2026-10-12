package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// seedReleasableSlice is a paid pool slice on the mock escrow's chain (146),
// sized so the mock's default TradeReleased event (99 + 1) balances it.
func seedReleasableSlice(t *testing.T, svc *P2PTradeService) *models.P2PTrade {
	t.Helper()
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	intent := uuid.New()
	tr := &models.P2PTrade{
		ID: uuid.New(), AdID: uuid.New(), SellerID: uuid.New(), BuyerID: uuid.New(),
		Token: "USDT", ChainID: 146, Funding: models.P2PTradeFundingPooled,
		TokenAmount: "100", FiatAmount: "1354.74", FiatCurrency: "NGN", Rate: "1354.74",
		PaymentMethodID: uuid.Nil, Status: models.P2PTradeStatusPaid, OpenTxHash: "0xopen",
		ITIntentID: &intent, Generation: 1, OpenedAt: &now,
		ExpiresAt: now.Add(60 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
	tr.OnChainTradeID = HashIDHex(tr.ID)
	if err := svc.db.Create(tr).Error; err != nil {
		t.Fatalf("seed slice: %v", err)
	}
	return tr
}

func releaseKey() *fakeReleaseSigner {
	return &fakeReleaseSigner{addr: "0x00000000000000000000000000000000000000a1", hash: "0x" + strings.Repeat("ab", 32)}
}

// Review C-1: a broadcast release whose receipt is not seen within the verify
// wait stays `releasing` (at the treasury) with its hash — never rolled back.
func TestAutoRelease_SliceStaysReleasingAfterAnUnseenReceipt(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sl := seedReleasableSlice(t, svc)
	esc.verifyReleasedErr = errors.New("timed out waiting for receipt")
	key := releaseKey()
	if _, err := svc.AutoRelease(context.Background(), sl.ID, key); err == nil {
		t.Fatal("want the verify error")
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleasing || got.ReleaseTxHash != key.hash {
		t.Fatalf("slice = %s hash=%q, want releasing with the sent hash", got.Status, got.ReleaseTxHash)
	}
}

// Re-review I-C: an uncertain broadcast returns no hash from Send but carries
// it in the error; the slice records it and stays releasing.
func TestAutoRelease_SliceUncertainBroadcastRecordsItsHash(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sl := seedReleasableSlice(t, svc)
	key := releaseKey()
	key.hash = ""
	key.err = broadcastUncertainError{txHash: "0x" + strings.Repeat("cd", 32), err: errors.New("502 bad gateway")}
	if _, err := svc.AutoRelease(context.Background(), sl.ID, key); err == nil {
		t.Fatal("want the uncertain error")
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleasing || got.ReleaseTxHash != "0x"+strings.Repeat("cd", 32) {
		t.Fatalf("slice = %s hash=%q, want releasing with the uncertain broadcast's hash", got.Status, got.ReleaseTxHash)
	}
}

// Re-review I-C: a `releasing` slice with NO hash (a crash after broadcast) is
// never "gone": the stale rollback leaves it, whatever the release key shows.
func TestReconcile_SliceReleasingWithoutAHashIsNeverRolledBack(t *testing.T) {
	r, sl, _ := paidOne(t)
	r.db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Updates(map[string]any{
		"status": models.P2PTradeStatusReleasing, "release_tx_hash": "", "updated_at": time.Now().Add(-11 * time.Minute)})
	if gone, err := r.it.SliceHashReverted(context.Background(), &sl); err != nil || gone {
		t.Fatalf("SliceHashReverted(\"\") = %v %v, want false", gone, err)
	}
	if _, err := r.trades.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	r.db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleasing {
		t.Fatalf("slice = %s, want still releasing (uncertain)", got.Status)
	}
}

// A provably unsent release (ErrTxNotBroadcast) is still rolled back: nothing can land.
func TestAutoRelease_SliceNotBroadcastRollsBack(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sl := seedReleasableSlice(t, svc)
	key := releaseKey()
	key.err = notBroadcast(errors.New("insufficient funds"))
	_, _ = svc.AutoRelease(context.Background(), sl.ID, key)
	var got models.P2PTrade
	db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusPaid || got.ReleaseTxHash != "" {
		t.Fatalf("slice = %s hash=%q, want rolled back to paid with no hash", got.Status, got.ReleaseTxHash)
	}
}

// Ordinary trades keep today's rollback and write no hash on a failed verify.
func TestAutoRelease_OrdinaryTradeUnchangedOnVerifyFailure(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt"); err != nil {
		t.Fatal(err)
	}
	esc.verifyReleasedErr = errors.New("receipt not found")
	_, _ = svc.AutoRelease(context.Background(), trade.ID, releaseKey())
	var got models.P2PTrade
	db.First(&got, "id = ?", trade.ID)
	if got.Status != models.P2PTradeStatusPaid || got.ReleaseTxHash != "" {
		t.Fatalf("ordinary trade = %s hash=%q, want today's rollback", got.Status, got.ReleaseTxHash)
	}
}

// §19: an LP sees pool events (Tasks 5/8), never a P2P trade push.
func TestConfirmRelease_SliceSendsNoP2PPush(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, bus := newService(t, db)
	sl := seedReleasableSlice(t, svc)
	if _, err := svc.AutoRelease(context.Background(), sl.ID, releaseKey()); err != nil {
		t.Fatal(err)
	}
	for _, ev := range bus.published {
		if ev.Type == events.P2PTradeReleased {
			t.Fatalf("slice release pushed %+v", ev)
		}
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleased {
		t.Fatalf("slice = %s, want released", got.Status)
	}
}

func TestConfirmRelease_OrdinaryTradeStillPushes(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, bus := newService(t, db)
	trade := seedOpenedPMTrade(t, svc)
	if _, err := svc.SystemMarkPaid(context.Background(), trade.ID, "nomba:evt"); err != nil {
		t.Fatal(err)
	}
	esc.releasedEvent = &P2PTradeReleasedEvent{TradeID: HashID(trade.ID), AmountToBuyer: big.NewInt(999_500), FeeToSystem: big.NewInt(500)}
	if _, err := svc.AutoRelease(context.Background(), trade.ID, releaseKey()); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range bus.published {
		if ev.Type == events.P2PTradeReleased {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("pushes = %d, want seller + buyer as today", n)
	}
}

// The reconciler moves a landed slice to released but keeps its real hash.
func TestReconcileToReleased_SliceKeepsItsReleaseHash(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sl := seedReleasableSlice(t, svc)
	db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("release_tx_hash", "0xreal")
	esc.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusReleased, Amount: big.NewInt(100)}
	if _, err := svc.ReconcileTrade(context.Background(), sl.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleased || got.ReleaseTxHash != "0xreal" {
		t.Fatalf("slice = %s hash=%q, want released keeping 0xreal", got.Status, got.ReleaseTxHash)
	}
}

// The race the full PG gate exposed: the reconciler read the slice before
// AutoRelease recorded its hash, then wrote "reconciled" over the real one.
// A recorded hash survives the reconciler's stale read.
func TestReconcileToReleased_SliceStaleReadKeepsTheRecordedHash(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sl := seedReleasableSlice(t, svc) // read with no hash
	db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Update("release_tx_hash", "0xreal")
	if _, err := svc.reconcileToReleased(context.Background(), sl, 0, nil); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", sl.ID)
	if got.Status != models.P2PTradeStatusReleased || got.ReleaseTxHash != "0xreal" {
		t.Fatalf("slice = %s hash=%q, want released keeping 0xreal", got.Status, got.ReleaseTxHash)
	}
}

// The other order of the same race: the reconciler's "reconciled" marker
// lands before AutoRelease records its hash. The marker is not a hash, so the
// real one still replaces it — the proof never depends on the log scan alone.
func TestRecordSliceReleaseHash_ReplacesTheReconciledMarker(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	sl := seedReleasableSlice(t, svc)
	db.Model(&models.P2PTrade{}).Where("id = ?", sl.ID).Updates(map[string]any{"status": models.P2PTradeStatusReleased, "release_tx_hash": "reconciled"})
	svc.recordSliceReleaseHash(context.Background(), sl.ID, "0xreal")
	var got models.P2PTrade
	db.First(&got, "id = ?", sl.ID)
	if got.ReleaseTxHash != "0xreal" {
		t.Fatalf("hash = %q, want 0xreal over the marker", got.ReleaseTxHash)
	}
	svc.recordSliceReleaseHash(context.Background(), sl.ID, "0xother")
	db.First(&got, "id = ?", sl.ID)
	if got.ReleaseTxHash != "0xreal" {
		t.Fatalf("hash = %q, want a recorded hash never overwritten", got.ReleaseTxHash)
	}
}

// A release is sent at most once per slice: a second AutoRelease on a slice
// left `releasing` with its hash (a verify that never saw the receipt) never
// reaches Send — and neither does one on a paid slice still carrying a hash.
func TestAutoRelease_SliceWithARecordedHashNeverSendsAgain(t *testing.T) {
	db := setupTradeDB(t)
	svc, esc, _ := newService(t, db)
	sl := seedReleasableSlice(t, svc)
	esc.verifyReleasedErr = errors.New("timed out waiting for receipt")
	sender := &chainCountingSender{fakeReleaseSigner: *releaseKey()}
	if _, err := svc.AutoRelease(context.Background(), sl.ID, sender); err == nil {
		t.Fatal("want the verify error")
	}
	if len(sender.sendChains) != 1 {
		t.Fatalf("first release sends = %d, want 1", len(sender.sendChains))
	}
	esc.verifyReleasedErr = nil
	if _, err := svc.AutoRelease(context.Background(), sl.ID, sender); !errors.Is(err, ErrSliceReleaseInFlight) {
		t.Fatalf("second release err = %v, want ErrSliceReleaseInFlight", err)
	}
	if len(sender.sendChains) != 1 || len(sender.calls) != 1 {
		t.Fatalf("sends = %d broadcasts = %d after the second call, want no second Send", len(sender.sendChains), len(sender.calls))
	}

	paid := seedReleasableSlice(t, svc)
	db.Model(&models.P2PTrade{}).Where("id = ?", paid.ID).Update("release_tx_hash", "0x"+strings.Repeat("ef", 32))
	if _, err := svc.AutoRelease(context.Background(), paid.ID, sender); !errors.Is(err, ErrSliceReleaseInFlight) {
		t.Fatalf("paid slice with a hash: err = %v, want ErrSliceReleaseInFlight", err)
	}
	if len(sender.sendChains) != 1 {
		t.Fatalf("sends = %d, want no Send for a slice still carrying a hash", len(sender.sendChains))
	}
}
