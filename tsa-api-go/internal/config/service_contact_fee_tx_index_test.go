package config

import (
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// One fee transfer pays for one contact: the same hash (any case) is refused a second time,
// while rows still waiting for their fee tx (empty hash) are not affected.
func TestServiceContactFeeTxIndex_OnPostgres(t *testing.T) {
	db := testutil.TempPostgres(t, "sc_fee_tx_idx_", &models.ServiceContactPayment{})
	if err := MigrateServiceContactFeeTxIndex(db); err != nil {
		t.Fatal(err)
	}
	if err := MigrateServiceContactFeeTxIndex(db); err != nil {
		t.Fatalf("a second run must be a no-op: %v", err)
	}
	row := func(hash string) error {
		return db.Create(&models.ServiceContactPayment{ID: uuid.New(), CallerID: uuid.New(), ServiceProviderID: uuid.New(),
			ServiceID: uuid.New(), PayFeeTxHash: hash}).Error
	}
	for _, h := range []string{"", "", "0xABC"} {
		if err := row(h); err != nil {
			t.Fatalf("insert %q: %v", h, err)
		}
	}
	if err := row("0xabc"); err == nil {
		t.Fatal("the same fee tx (different case) must be refused for a second contact")
	}
}
