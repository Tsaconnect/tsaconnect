package services

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// TestLedgerOp_JoinsTheCallersTransaction pins that a ledger op running inside
// a caller-supplied transaction JOINS it rather than opening a nested one.
//
// This matters because the two representations (legacy columns and pm_balances)
// are written together. If inTx failed to detect the outer transaction it would
// call db.Transaction, which GORM implements as a SAVEPOINT when already in one
// — and an inner rollback would then leave the outer transaction alive with only
// half the ledger written. The assertion is therefore not just "no error" but
// that an OUTER rollback undoes BOTH writes.
func TestLedgerOp_JoinsTheCallersTransaction(t *testing.T) {
	db := newSQLiteForPMConcurrent(t)
	svc := NewPMService(db)
	pmID := seedConcurrentPM(t, db, "1000.00")

	detected := false
	_ = db.Transaction(func(tx *gorm.DB) error {
		_, isTx := tx.Statement.ConnPool.(gorm.TxCommitter)
		detected = isTx
		// Reserve inside the caller's tx, then fail the outer tx.
		ok, err := svc.ReserveInTx(tx, pmID, "NGN", "100.00")
		if err != nil || !ok {
			t.Fatalf("reserve in outer tx: ok=%v err=%v", ok, err)
		}
		return gorm.ErrInvalidData // force outer rollback
	})

	if !detected {
		t.Error("inTx did NOT detect an existing transaction — it would open a nested one")
	}

	var row models.PMBalance
	_ = db.Where("pm_id = ? AND currency = ?", pmID, "NGN").First(&row).Error
	if !decimalEqual(row.Reserved, "0") {
		t.Errorf("outer rollback did not undo the reserve: reserved=%q, want 0", row.Reserved)
	}
	var pm models.PaymentMerchant
	_ = db.First(&pm, "id = ?", pmID).Error
	if !decimalEqual(pm.ReservedNGN, "0") {
		t.Errorf("outer rollback did not undo the legacy write: reserved_ngn=%q, want 0", pm.ReservedNGN)
	}
}
