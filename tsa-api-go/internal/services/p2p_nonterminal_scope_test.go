package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Leftover slices are never reconciled, so they must not take the
// reconciler's page: with more stale slices than the limit, the ordinary
// trade (updated last) is still listed.
func TestListNonTerminalTrades_SkipsLeftoverSlices(t *testing.T) {
	db := setupTradeDB(t)
	svc := NewP2PTradeService(db, &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{}}, &mockRates{}, &mockBus{}, 146)
	old := time.Now().Add(-48 * time.Hour)
	insert := func(intent *uuid.UUID, updated time.Time) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if err := db.Exec(`INSERT INTO p2p_trades (id, ad_id, buyer_id, seller_id, status, expires_at, it_intent_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'paid', ?, ?, ?, ?)`,
			id, uuid.New(), uuid.New(), uuid.New(), updated, intent, updated, updated).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	intent := uuid.New()
	for i := 0; i < 5; i++ {
		insert(&intent, old)
	}
	ordinary := insert(nil, time.Now())

	got, err := svc.ListNonTerminalTrades(context.Background(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != ordinary {
		t.Fatalf("listed %d trade(s) %+v, want only the ordinary trade %s", len(got), got, ordinary)
	}
}
