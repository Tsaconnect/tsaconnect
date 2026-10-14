package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func sliceLeftoversDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE TABLE p2p_trades (id TEXT PRIMARY KEY, it_intent_id TEXT, status TEXT NOT NULL)`,
		`CREATE TABLE it_intents (id TEXT PRIMARY KEY, provider TEXT NOT NULL DEFAULT '', status TEXT NOT NULL)`,
		`CREATE TABLE pool_orders (intent_id TEXT NOT NULL, generation INTEGER NOT NULL, fill_units TEXT)`,
		`CREATE TABLE p2p_settlement_forwards (id TEXT PRIMARY KEY, provider_ref TEXT NOT NULL, status TEXT NOT NULL)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestCheckSliceLeftovers(t *testing.T) {
	db := sliceLeftoversDB(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	id := func() string { return uuid.NewString() }

	// Rows that must NOT refuse boot.
	intent := id()
	exec(`INSERT INTO p2p_trades VALUES (?, NULL, 'pending_payment')`, id()) // ordinary trade
	exec(`INSERT INTO p2p_trades VALUES (?, ?, 'released')`, id(), intent)   // terminal slice
	exec(`INSERT INTO p2p_trades VALUES (?, ?, 'cancelled')`, id(), intent)  // terminal slice
	vault := id()                                                            // a live vault order
	exec(`INSERT INTO it_intents VALUES (?, 'tsa_pool', 'payment_received')`, vault)
	exec(`INSERT INTO pool_orders VALUES (?, 1, '1000000')`, vault)
	exec(`INSERT INTO it_intents VALUES (?, 'tsa_pool', 'settled')`, id())              // terminal slice-era
	exec(`INSERT INTO it_intents VALUES (?, 'treasury', 'delivering')`, id())           // another provider
	exec(`INSERT INTO p2p_settlement_forwards VALUES (?, 'pool:x', 'settled')`, id())   // landed LP forward
	exec(`INSERT INTO p2p_settlement_forwards VALUES (?, 'NIBSS123', 'pending')`, id()) // merchant forward
	if err := checkSliceLeftovers(db); err != nil {
		t.Fatalf("clean books refused: %v", err)
	}

	// Each kind of leftover refuses on its own count.
	exec(`INSERT INTO p2p_trades VALUES (?, ?, 'paid')`, id(), intent)
	sliceEra := id()
	exec(`INSERT INTO it_intents VALUES (?, ?, 'opening')`, sliceEra, models.ITProviderPool)
	exec(`INSERT INTO pool_orders VALUES (?, 1, NULL)`, sliceEra) // slice-era snapshot: no fill_units
	exec(`INSERT INTO it_intents VALUES (?, 'tsa_pool', 'buy_awaiting_rate')`, id())
	exec(`INSERT INTO p2p_settlement_forwards VALUES (?, 'pool:y', 'submitted')`, id())
	exec(`INSERT INTO p2p_settlement_forwards VALUES (?, 'pool-f22:z', 'pending')`, id())

	got, err := services.CountSliceLeftovers(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if want := (services.SliceLeftovers{Slices: 1, Intents: 2, Forwards: 2}); got != want {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
	err = checkSliceLeftovers(db)
	if err == nil || !strings.Contains(err.Error(), "liquidity_slice_leftovers_refused") ||
		!strings.Contains(err.Error(), "slices=1 intents=2 lp_forwards=2") {
		t.Fatalf("refusal = %v", err)
	}
}

// A table it cannot read is an ALERT, never a crash-loop.
func TestCheckSliceLeftovers_ReadErrorDoesNotRefuse(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkSliceLeftovers(db); err != nil {
		t.Fatalf("missing tables refused boot: %v", err)
	}
}
