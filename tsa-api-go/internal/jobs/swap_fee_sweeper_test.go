package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// Raw DDL rather than AutoMigrate: the model carries Postgres-only defaults
// (gen_random_uuid()) that SQLite cannot parse — the same reason the wallet and
// fee-ledger suites hand-write their schema.
func memDBWithPendingFees(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(`CREATE TABLE pending_swap_fees (
		id TEXT PRIMARY KEY, user_id TEXT NOT NULL, tx_hash TEXT NOT NULL UNIQUE,
		feature TEXT NOT NULL, token_symbol TEXT NOT NULL, amount_human REAL NOT NULL,
		chain_id INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
		attempts INTEGER NOT NULL DEFAULT 0, last_err TEXT,
		created_at DATETIME, updated_at DATETIME, settled_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// captureFeeService records what the sweeper would accrue without needing the
// real engine + its ledger tables.
type captureFeeService struct{ events []services.FeeEvent }

func (c *captureFeeService) EnqueueFeeSettled(ev services.FeeEvent) { c.events = append(c.events, ev) }

func pendingRow(t *testing.T, db *gorm.DB, txHash, symbol string, amount float64, age time.Duration) models.PendingSwapFee {
	t.Helper()
	row := models.PendingSwapFee{
		ID:          uuid.New(), // no DB-side default under SQLite
		UserID:      uuid.New(),
		TxHash:      txHash,
		Feature:     models.FeatureAllswap,
		TokenSymbol: symbol,
		AmountHuman: amount,
		ChainID:     42161,
		Status:      models.PendingSwapFeePending,
		CreatedAt:   time.Now().Add(-age),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	// GORM stamps CreatedAt on insert; force the age we asked for.
	db.Model(&row).UpdateColumn("created_at", time.Now().Add(-age))
	return row
}

func sweeperFor(db *gorm.DB, ps *services.PriceService) *SwapFeeSweeper {
	return &SwapFeeSweeper{
		DB: db, PriceService: ps, Grace: 10 * time.Minute, FeeBps: 300,
	}
}

// A swap whose client never called settle must still be charged — that window
// is exactly what this job exists to close.
func TestSwapFeeSweeper_ChargesAbandonedSwaps(t *testing.T) {
	db := memDBWithPendingFees(t)
	ps := services.NewPriceService()
	ps.SeedPriceForTest("USDC", services.PriceData{USD: 1.0})
	row := pendingRow(t, db, "0xabandoned", "USDC", 1000, time.Hour)

	s := sweeperFor(db, ps)
	fees := &captureFeeService{}
	s.enqueue = fees.EnqueueFeeSettled

	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fees.events) != 1 {
		t.Fatalf("want 1 accrual, got %d", len(fees.events))
	}
	ev := fees.events[0]
	// 1,000 USDC at $1 → 3%, the same rate the settle path books. A swept swap
	// is not charged extra for its client having gone away: the accrual mirrors
	// a fixed cut LiFi already took either way.
	if ev.FeeUSD < 29.999 || ev.FeeUSD > 30.001 {
		t.Errorf("feeUSD = %v, want 30", ev.FeeUSD)
	}
	if ev.Feature != models.FeatureAllswap || ev.Token != "USDC" {
		t.Errorf("booked as %+v", ev)
	}
	// The same key the settle endpoint derives, so a late client settle for this
	// swap collapses into one fee event instead of charging twice.
	want := uuid.NewSHA1(uuid.NameSpaceURL, []byte("allswap:0xabandoned"))
	if ev.SourceID != want {
		t.Errorf("source id = %s, want the settle-compatible %s", ev.SourceID, want)
	}

	var after models.PendingSwapFee
	db.First(&after, "tx_hash = ?", row.TxHash)
	if after.Status != models.PendingSwapFeeSettled || after.SettledAt == nil {
		t.Errorf("row not marked settled: %+v", after)
	}
}

// Inside the grace window the client still owns the settle. Sweeping early
// would charge the conservative rate on a swap that is about to report its
// exact one.
func TestSwapFeeSweeper_LeavesRecentSwapsToTheClient(t *testing.T) {
	db := memDBWithPendingFees(t)
	ps := services.NewPriceService()
	ps.SeedPriceForTest("USDC", services.PriceData{USD: 1.0})
	pendingRow(t, db, "0xfresh", "USDC", 1000, time.Minute)

	s := sweeperFor(db, ps)
	fees := &captureFeeService{}
	s.enqueue = fees.EnqueueFeeSettled

	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fees.events) != 0 {
		t.Fatalf("swept inside the grace window: %+v", fees.events)
	}
}

// A settled row is done. Re-sweeping it would double-charge if the engine's
// guard ever changed.
func TestSwapFeeSweeper_IgnoresSettledRows(t *testing.T) {
	db := memDBWithPendingFees(t)
	ps := services.NewPriceService()
	ps.SeedPriceForTest("USDC", services.PriceData{USD: 1.0})
	row := pendingRow(t, db, "0xdone", "USDC", 1000, time.Hour)
	db.Model(&row).Update("status", models.PendingSwapFeeSettled)

	s := sweeperFor(db, ps)
	fees := &captureFeeService{}
	s.enqueue = fees.EnqueueFeeSettled

	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(fees.events) != 0 {
		t.Fatalf("re-swept a settled row: %+v", fees.events)
	}
}

// Without a price there is no honest basis, and the sweeper has no quote to
// fall back on. It must give up loudly rather than invent a number.
func TestSwapFeeSweeper_AbandonsUnpriceableTokens(t *testing.T) {
	db := memDBWithPendingFees(t)
	ps := services.NewPriceService() // nothing seeded
	row := pendingRow(t, db, "0xnoprice", "WOOF", 2_000_000, time.Hour)

	s := sweeperFor(db, ps)
	s.MaxAttempts = 2
	fees := &captureFeeService{}
	s.enqueue = fees.EnqueueFeeSettled

	for i := 0; i < 2; i++ {
		if err := s.Run(context.Background()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if len(fees.events) != 0 {
		t.Fatalf("charged an unpriceable swap: %+v", fees.events)
	}
	var after models.PendingSwapFee
	db.First(&after, "tx_hash = ?", row.TxHash)
	if after.Status != models.PendingSwapFeeAbandoned {
		t.Errorf("status = %q after %d attempts, want abandoned", after.Status, after.Attempts)
	}
	if after.LastErr == "" {
		t.Error("abandoned row should say why")
	}
}

func TestSwapFeeSweeper_Defaults(t *testing.T) {
	s := &SwapFeeSweeper{}
	if s.Name() != "swap-fee-sweeper" {
		t.Errorf("name = %q", s.Name())
	}
	if s.Interval() != 5*time.Minute || s.grace() != 10*time.Minute {
		t.Errorf("defaults: interval=%v grace=%v", s.Interval(), s.grace())
	}
	// A sweeper with no fee engine must no-op rather than nil-panic on a tick.
	if err := s.Run(context.Background()); err != nil {
		t.Errorf("run with no deps: %v", err)
	}
}
