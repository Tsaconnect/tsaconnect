package services

import (
	"math/big"
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
	if err := r.db.AutoMigrate(&models.PoolOrder{}); err != nil { // a vault order's fill is looked up first
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

// Shared pool: a vault order has no slices — its one proven fill is the leg.
func TestOrchestrator_PoolCustodyFromTheVaultFill(t *testing.T) {
	r := newConversionRig(t)
	intent := seedPoolCustody(t, r, "0xunused")
	r.db.Exec("DELETE FROM p2p_trades")
	fill, at := "100000000000000000000", time.Now()
	if err := r.db.Create(&models.PoolOrder{IntentID: intent, Generation: 1, ChainID: 97, Token: "USDT", TokenDecimals: 18,
		MidMinor: "0", LPRateMinor: "0", DisplayRateMinor: "0", OpenedUnits: "0", SurplusUnits: "0",
		FillUnits: &fill, FillTxHash: "0xfill", FilledAt: &at}).Error; err != nil {
		t.Fatal(err)
	}
	r.db.Model(r.order).Update("tx_hash", "0xfill")
	r.transfers.amountByTx = map[string]*big.Int{"0xfill": big.NewInt(100_000000)}
	mustAdvance(t, r)
	if got := r.reload(t); got.Holder != models.HolderTreasury || got.TokenAmount != "100" {
		t.Fatalf("holder=%s amount=%s, want treasury custody of the fill's 100", got.Holder, got.TokenAmount)
	}
}

// An unproven fill is never custody, whatever the treasury holds.
func TestOrchestrator_PoolCustodyWaitsForAProvenFill(t *testing.T) {
	r := newConversionRig(t)
	intent := seedPoolCustody(t, r, "0xunused")
	r.db.Exec("DELETE FROM p2p_trades")
	fill := "100000000000000000000"
	r.db.Create(&models.PoolOrder{IntentID: intent, Generation: 1, ChainID: 97, Token: "USDT", TokenDecimals: 18,
		MidMinor: "0", LPRateMinor: "0", DisplayRateMinor: "0", OpenedUnits: "0", SurplusUnits: "0",
		FillUnits: &fill, FillTxHash: "0xfill"})
	r.transfers.amountByTx = map[string]*big.Int{"0xfill": big.NewInt(100_000000)}
	mustAdvance(t, r)
	if got := r.reload(t); got.Holder != models.HolderPool {
		t.Fatalf("holder=%s, want no custody before the fill is proven", got.Holder)
	}
}
