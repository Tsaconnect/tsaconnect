package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// stubTron is a hand fake of chains.ChainClient for handler wiring tests.
type stubTron struct{ chains.ChainClient }

func (stubTron) Family() string                { return "tron" }
func (stubTron) ValidateAddress(a string) error { return nil }
func (stubTron) GetBalances(context.Context, string) ([]chains.Balance, error) {
	return []chains.Balance{{Symbol: "TRX", Raw: "5", Decimals: 6, IsNative: true}}, nil
}

func TestRegistryResolvesFamily(t *testing.T) {
	r := chains.NewRegistry()
	r.Register("tron", stubTron{})
	c, ok := r.ForKey("tron")
	if !ok {
		t.Fatal("tron not registered")
	}
	bals, _ := c.GetBalances(context.Background(), "TXYZ")
	if len(bals) != 1 || bals[0].Symbol != "TRX" {
		t.Fatalf("unexpected balances: %+v", bals)
	}
}

// TestRegisterNonEVM_OwnershipScoping proves that a caller cannot overwrite
// another user's registered non-EVM address by passing that user's walletId.
// User B owns wallet "0xB" with Tron address "ToldB". User A calls
// RegisterNonEVMAddress with walletId="0xB" and address="TevilA". The WHERE
// clause includes user_id=A which misses user B's row, so FirstOrCreate inserts
// a new row (which the unique index on (wallet_id, chain_key) rejects), and
// user B's original address must remain unchanged.
func TestRegisterNonEVM_OwnershipScoping(t *testing.T) {
	// Fresh SQLite in-memory DB — migrate only WalletAddress to avoid the
	// pre-existing fee_distributions SQLite failure unrelated to this task.
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&models.WalletAddress{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	config.DB = db

	uuidB := uuid.New()
	uuidA := uuid.New()

	// Seed user B's row.
	seedRow := models.WalletAddress{
		UserID:   uuidB,
		WalletID: "0xB",
		ChainKey: "tron",
		Family:   "tron",
		Address:  "ToldB",
	}
	if err := db.Create(&seedRow).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Wire a registry with a stub tron client.
	reg := chains.NewRegistry()
	reg.Register("tron", stubTron{})

	h := &Handlers{ChainRegistry: reg}

	// Call RegisterNonEVMAddress AS user A, targeting wallet "0xB".
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &models.User{ID: uuidA})

	body, _ := json.Marshal(map[string]string{
		"walletId": "0xB",
		"chainKey": "tron",
		"address":  "TevilA",
	})
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.RegisterNonEVMAddress(c)

	// The attempt must fail (unique index on wallet_id+chain_key rejects the
	// insert that FirstOrCreate tries when the user_id-scoped WHERE finds nothing).
	if w.Code == http.StatusOK {
		t.Errorf("expected non-200, got %d — ownership scoping failed", w.Code)
	}

	// The original row for user B must be unchanged.
	var got models.WalletAddress
	if err := db.Where("wallet_id = ? AND chain_key = ?", "0xB", "tron").First(&got).Error; err != nil {
		t.Fatalf("reload B row: %v", err)
	}
	if got.Address != "ToldB" {
		t.Errorf("user B's address was overwritten: got %q, want %q", got.Address, "ToldB")
	}
	if got.UserID != uuidB {
		t.Errorf("user B's user_id was changed: got %v, want %v", got.UserID, uuidB)
	}
}
