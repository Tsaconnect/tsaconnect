package config

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// One order per OrderEscrow key: a second order mirroring the same key is refused, while orders
// that never touched OrderEscrow (empty key) are not affected.
func TestOrderEscrowKeyIndex_OnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "order_escrow_key_idx_", &models.Order{})
	if err := MigrateOrderEscrowKeyIndex(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateOrderEscrowKeyIndex(db); err != nil {
		t.Fatalf("a second run must be a no-op: %v", err)
	}
	row := func(key string) error {
		return db.Create(&models.Order{ID: uuid.New(), EscrowOrderKey: key}).Error
	}
	key := "0x01"
	for _, k := range []string{"", "", key} {
		if err := row(k); err != nil {
			t.Fatalf("insert %q: %v", k, err)
		}
	}
	if err := row(key); err == nil {
		t.Fatal("a second order with the same escrow key must be refused")
	}
}
