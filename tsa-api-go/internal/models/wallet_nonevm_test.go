package models_test

import (
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openMem(t *testing.T) *gorm.DB {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&models.WalletAddress{}, &models.WalletActivity{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestWalletAddressRoundTrip(t *testing.T) {
	db := openMem(t)
	wa := models.WalletAddress{
		UserID:   uuid.New(),
		WalletID: "0xabc",
		ChainKey: "tron",
		Family:   "tron",
		Address:  "TXk...abc",
	}
	if err := db.Create(&wa).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got models.WalletAddress
	if err := db.Where("chain_key = ? AND wallet_id = ?", "tron", "0xabc").First(&got).Error; err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Address != "TXk...abc" {
		t.Fatalf("unexpected addr: %s", got.Address)
	}
}
