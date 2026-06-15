package models

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPaymentMerchant_PersistsAndDefaults(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&PaymentMerchant{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pm := PaymentMerchant{
		ID:                    uuid.New(),
		UserID:                uuid.New(),
		Name:                  "TSA Desk",
		NombaAccountID:        "acct_123",
		InboundAccountNumber:  "0123456789",
		InboundBank:           "Nomba",
		OperatorWalletAddress: "0x0000000000000000000000000000000000000001",
	}
	if err := db.Create(&pm).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var got PaymentMerchant
	if err := db.First(&got, "id = ?", pm.ID).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Status != PaymentMerchantStatusPending {
		t.Fatalf("default status = %q, want %q", got.Status, PaymentMerchantStatusPending)
	}
	if got.IsActive() {
		t.Fatal("pending merchant must not be active")
	}
	got.Status = PaymentMerchantStatusActive
	if !got.IsActive() {
		t.Fatal("active merchant must report IsActive()")
	}
}
