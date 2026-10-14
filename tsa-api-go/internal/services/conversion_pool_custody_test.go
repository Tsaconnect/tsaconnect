package services

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// seedPoolCustody turns the rig's order into a converted pool order's record
// (holder tsa_pool, its handoff hash the first release) and writes one
// released slice per hash. The rig is SQLite, where P2PTrade's Postgres
// defaults do not migrate, so the slices live in a minimal p2p_trades table
// holding only the columns custody reads.
func seedPoolCustody(t *testing.T, r *conversionRig, hashes ...string) uuid.UUID {
	t.Helper()
	if err := r.db.Exec(`CREATE TABLE p2p_trades (id TEXT PRIMARY KEY, it_intent_id TEXT, status TEXT NOT NULL,
		release_tx_hash TEXT, release_reverted_at DATETIME, cancel_reason TEXT, dropped_at DATETIME, created_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	intent := uuid.New()
	r.db.Model(r.order).Updates(map[string]any{"holder": models.HolderPool, "reference": ITConversionRef(intent), "tx_hash": hashes[0]})
	for _, h := range hashes {
		seedCustodySlice(t, r, intent, models.P2PTradeStatusReleased, h, "", false)
	}
	return intent
}

func seedCustodySlice(t *testing.T, r *conversionRig, intent uuid.UUID, status, hash, cancelReason string, dropped bool) {
	t.Helper()
	var droppedAt *time.Time
	if dropped {
		at := r.clock
		droppedAt = &at
	}
	if err := r.db.Exec(`INSERT INTO p2p_trades (id, it_intent_id, status, release_tx_hash, cancel_reason, dropped_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, uuid.NewString(), intent.String(), status, hash, cancelReason, droppedAt, time.Now()).Error; err != nil {
		t.Fatal(err)
	}
}

// S22: custody is proven from every slice release, not from one hash.
func TestOrchestrator_PoolCustodySumsSliceReleases(t *testing.T) {
	r := newConversionRig(t)
	seedPoolCustody(t, r, "0xrelA", "0xrelB")
	r.transfers.amountByTx = map[string]*big.Int{"0xrelA": big.NewInt(60_000000), "0xrelB": big.NewInt(40_000000)}
	mustAdvance(t, r)
	if got := r.reload(t); got.Holder != models.HolderTreasury || got.TokenAmount != "100" {
		t.Fatalf("holder=%s amount=%s, want treasury custody of the full 100 (60 + 40)", got.Holder, got.TokenAmount)
	}
}

// A release that proves less than the record is a short delivery: the record
// is restated to what landed, never bridged on money that is not there.
func TestOrchestrator_PoolCustodyShortReleaseRefusesCustody(t *testing.T) {
	r := newConversionRig(t)
	seedPoolCustody(t, r, "0xrelA", "0xrelB")
	r.transfers.amountByTx = map[string]*big.Int{"0xrelA": big.NewInt(60_000000), "0xrelB": big.NewInt(30_000000)}
	mustAdvance(t, r)
	if got := r.reload(t); got.TokenAmount != "90" {
		t.Fatalf("amount = %s, want restated to the 90 (60 + 30) that landed, below the 100 promised", got.TokenAmount)
	}
}

// One release can never back two records (per-hash claim rule).
func TestOrchestrator_PoolCustodyPerHashClaim(t *testing.T) {
	r := newConversionRig(t)
	seedPoolCustody(t, r, "0xrelA", "0xrelB")
	r.transfers.amountByTx = map[string]*big.Int{"0xrelA": big.NewInt(60_000000), "0xrelB": big.NewInt(40_000000)}
	other := *r.order
	other.ID, other.Reference, other.TxHash, other.Holder = uuid.New(), "conv-"+uuid.NewString(), "0xrelB", models.HolderTreasury
	if err := r.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	_ = r.svc.Advance(context.Background(), r.reload(t))
	got := r.reload(t)
	if got.Holder == models.HolderTreasury || got.Status != models.PaycrestStatusRecoveryPending ||
		!strings.Contains(got.FailureReason, "0xrelB") {
		t.Fatalf("record = %s/%s %q, want parked: release 0xrelB already backs another record", got.Holder, got.Status, got.FailureReason)
	}
}

// A slice that went back to the LP (dropped, or F22 "returned"), was
// cancelled, or carries no real hash is never summed (plan C2 P1).
func TestOrchestrator_PoolCustodyIgnoresSlicesNotAtTheTreasury(t *testing.T) {
	r := newConversionRig(t)
	intent := seedPoolCustody(t, r, "0xrelA")
	seedCustodySlice(t, r, intent, models.P2PTradeStatusCancelled, "0xcancelled", "", false)
	seedCustodySlice(t, r, intent, models.P2PTradeStatusResolved, "0xdropped", "", true)
	seedCustodySlice(t, r, intent, models.P2PTradeStatusReleased, "0xreturned", "pool_f22:returned", true)
	seedCustodySlice(t, r, intent, models.P2PTradeStatusReleased, "reconciled", "", false)
	r.transfers.amountByTx = map[string]*big.Int{"0xrelA": big.NewInt(100_000000), "0xcancelled": big.NewInt(999_000000),
		"0xdropped": big.NewInt(999_000000), "0xreturned": big.NewInt(999_000000), "reconciled": big.NewInt(999_000000)}
	mustAdvance(t, r)
	if got := r.reload(t); got.TokenAmount != "100" {
		t.Fatalf("amount = %s, want 100 — only a slice at the treasury is custody", got.TokenAmount)
	}
}

// A second pool record naming the same release is a reuse too, whatever its
// holder: a release backs exactly one record.
func TestOrchestrator_PoolCustodyReuseByAnotherPoolRecord(t *testing.T) {
	r := newConversionRig(t)
	seedPoolCustody(t, r, "0xrelA", "0xrelB")
	r.transfers.amountByTx = map[string]*big.Int{"0xrelA": big.NewInt(60_000000), "0xrelB": big.NewInt(40_000000)}
	other := *r.order
	other.ID, other.Reference, other.TxHash, other.Holder = uuid.New(), ITConversionRef(uuid.New()), "0xrelB", models.HolderPool
	if err := r.db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	_ = r.svc.Advance(context.Background(), r.reload(t))
	got := r.reload(t)
	if got.Holder == models.HolderTreasury || got.Status != models.PaycrestStatusRecoveryPending ||
		!strings.Contains(got.FailureReason, "0xrelB") {
		t.Fatalf("record = %s/%s %q, want parked: release 0xrelB is named by another pool record", got.Holder, got.Status, got.FailureReason)
	}
}

// Two slice rows carrying one release transaction count it once.
func TestOrchestrator_PoolCustodyCountsAReleaseOnce(t *testing.T) {
	r := newConversionRig(t)
	intent := seedPoolCustody(t, r, "0xrelA")
	seedCustodySlice(t, r, intent, models.P2PTradeStatusResolved, "0xrelA", "", false)
	r.transfers.amountByTx = map[string]*big.Int{"0xrelA": big.NewInt(60_000000)}
	mustAdvance(t, r)
	if got := r.reload(t); got.TokenAmount != "60" {
		t.Fatalf("amount = %s, want 60 — one release transaction is counted once", got.TokenAmount)
	}
	if r.transfers.calls != 1 {
		t.Fatalf("VerifyTransfer calls = %d, want 1 per distinct release", r.transfers.calls)
	}
}

// Review I-1: a slice the order dropped and its LP then self-released is the
// LP's own USDT at the treasury — never her custody. Here it would turn a
// short delivery (60 of 100) into a full one.
func TestOrchestrator_PoolCustodyIgnoresADroppedSelfRelease(t *testing.T) {
	r := newConversionRig(t)
	intent := seedPoolCustody(t, r, "0xrelA")
	seedCustodySlice(t, r, intent, models.P2PTradeStatusReleased, "0xselfdropped", "", true)
	r.transfers.amountByTx = map[string]*big.Int{"0xrelA": big.NewInt(60_000000), "0xselfdropped": big.NewInt(40_000000)}
	mustAdvance(t, r)
	if got := r.reload(t); got.TokenAmount != "60" {
		t.Fatalf("amount = %s, want 60 — the dropped self-release is not hers", got.TokenAmount)
	}
}
