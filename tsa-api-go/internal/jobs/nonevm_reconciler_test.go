package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNonEVMReconcilerMeta(t *testing.T) {
	r := NewNonEVMReconciler(nil, nil)
	if r.Name() == "" {
		t.Fatal("Name empty")
	}
	if r.Interval() <= 0 {
		t.Fatalf("bad interval %v", r.Interval())
	}
	if r.Interval() != 45*time.Second {
		t.Fatalf("want 45s got %v", r.Interval())
	}
}

// stubStatusClient is a minimal chains.ChainClient that only answers GetTxStatus
// (the sole method the reconciler calls). The embedded interface leaves the rest
// nil — fine because they're never invoked here.
type stubStatusClient struct {
	chains.ChainClient
	confirmed bool
	success   bool
}

func (s stubStatusClient) Family() string { return "tron" }
func (s stubStatusClient) GetTxStatus(ctx context.Context, hash string) (chains.TxStatus, error) {
	return chains.TxStatus{Found: true, Confirmed: s.confirmed, Success: s.success, Confirmations: 1}, nil
}

func memDBWithActivity(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&models.WalletActivity{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestNonEVMReconcilerConfirmsPending proves the send->activity->reconcile loop:
// a pending row is finalized to "confirmed" once GetTxStatus reports success.
func TestNonEVMReconcilerConfirmsPending(t *testing.T) {
	db := memDBWithActivity(t)
	row := models.WalletActivity{
		ChainKey: "tron", Address: "Tfrom", Hash: "abc123",
		Direction: "out", AmountRaw: "1000000", Symbol: "TRX", Status: "pending",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	reg := chains.NewRegistry()
	reg.Register("tron", stubStatusClient{confirmed: true, success: true})

	if err := NewNonEVMReconciler(db, reg).Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var got models.WalletActivity
	if err := db.First(&got, "hash = ?", "abc123").Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != "confirmed" {
		t.Fatalf("want status confirmed, got %q", got.Status)
	}
}

// TestNonEVMReconcilerMarksFailed proves a mined-but-reverted tx becomes "failed".
func TestNonEVMReconcilerMarksFailed(t *testing.T) {
	db := memDBWithActivity(t)
	row := models.WalletActivity{
		ChainKey: "tron", Address: "Tfrom", Hash: "revert1",
		Direction: "out", AmountRaw: "5", Symbol: "USDT", Status: "pending",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	reg := chains.NewRegistry()
	reg.Register("tron", stubStatusClient{confirmed: true, success: false})

	if err := NewNonEVMReconciler(db, reg).Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var got models.WalletActivity
	if err := db.First(&got, "hash = ?", "revert1").Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != "failed" {
		t.Fatalf("want status failed, got %q", got.Status)
	}
}
