package services

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// newSQLiteForPMNGN reuses the newSQLiteForPM helper (defined in
// pm_service_test.go) but the payment_merchants DDL there already includes the
// new NGN columns after this task's patch — same package, same test binary.

func TestReserveNGN_AtomicGate(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	pm := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Status:               models.PaymentMerchantStatusActive,
		AvailableNGN:         "1000.00",
		ReservedNGN:          "0.00",
		InboundAccountNumber: "1",
	}
	db.Create(pm)

	// First reserve: 600 of 1000 free → should succeed.
	ok, err := svc.ReserveNGN(context.Background(), pm.ID, "600.00")
	if err != nil || !ok {
		t.Fatalf("first reserve should succeed: ok=%v err=%v", ok, err)
	}

	// Second reserve: another 600 would require 1200 reserved against 1000 available → must fail.
	ok2, err2 := svc.ReserveNGN(context.Background(), pm.ID, "600.00")
	if err2 != nil {
		t.Fatalf("second reserve returned unexpected error: %v", err2)
	}
	if ok2 {
		t.Fatal("second reserve exceeding available must fail")
	}

	// Verify DB state: reserved == 600, available unchanged.
	var got models.PaymentMerchant
	db.First(&got, "id = ?", pm.ID)
	if got.ReservedNGN != "600.00" && got.ReservedNGN != "600" {
		t.Fatalf("reserved=%s, want 600.00", got.ReservedNGN)
	}
	if got.AvailableNGN != "1000.00" && got.AvailableNGN != "1000" {
		t.Fatalf("available=%s, want 1000.00", got.AvailableNGN)
	}
}

func TestConsumeReleaseCreditNGN(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	pm := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Status:               models.PaymentMerchantStatusActive,
		AvailableNGN:         "0.00",
		ReservedNGN:          "0.00",
		InboundAccountNumber: "2",
	}
	db.Create(pm)

	// Credit 500 → available becomes 500.
	if err := svc.CreditNGN(context.Background(), pm.ID, "500.00"); err != nil {
		t.Fatalf("CreditNGN: %v", err)
	}

	// Reserve 300 → reserved becomes 300, available stays 500.
	ok, err := svc.ReserveNGN(context.Background(), pm.ID, "300.00")
	if err != nil || !ok {
		t.Fatalf("ReserveNGN after credit: ok=%v err=%v", ok, err)
	}

	// Consume 300 → both available and reserved decrease by 300.
	if err := svc.ConsumeNGN(context.Background(), pm.ID, "300.00"); err != nil {
		t.Fatalf("ConsumeNGN: %v", err)
	}

	var afterConsume models.PaymentMerchant
	db.First(&afterConsume, "id = ?", pm.ID)
	// available: 500 - 300 = 200; reserved: 300 - 300 = 0
	if afterConsume.AvailableNGN != "200.00" && afterConsume.AvailableNGN != "200" {
		t.Fatalf("after consume: available=%s, want 200.00", afterConsume.AvailableNGN)
	}
	if afterConsume.ReservedNGN != "0.00" && afterConsume.ReservedNGN != "0" {
		t.Fatalf("after consume: reserved=%s, want 0.00", afterConsume.ReservedNGN)
	}

	// Now test ReleaseNGN (reserve → release drops reserved only, not available).
	// Credit another 400 and reserve 200.
	if err := svc.CreditNGN(context.Background(), pm.ID, "400.00"); err != nil {
		t.Fatalf("second CreditNGN: %v", err)
	}
	ok2, err2 := svc.ReserveNGN(context.Background(), pm.ID, "200.00")
	if err2 != nil || !ok2 {
		t.Fatalf("second ReserveNGN: ok=%v err=%v", ok2, err2)
	}

	// Release 200 → reserved drops by 200, available unchanged.
	if err := svc.ReleaseNGN(context.Background(), pm.ID, "200.00"); err != nil {
		t.Fatalf("ReleaseNGN: %v", err)
	}

	var afterRelease models.PaymentMerchant
	db.First(&afterRelease, "id = ?", pm.ID)
	// available: 200 + 400 = 600; reserved: 0 + 200 - 200 = 0
	if afterRelease.AvailableNGN != "600.00" && afterRelease.AvailableNGN != "600" {
		t.Fatalf("after release: available=%s, want 600.00", afterRelease.AvailableNGN)
	}
	if afterRelease.ReservedNGN != "0.00" && afterRelease.ReservedNGN != "0" {
		t.Fatalf("after release: reserved=%s, want 0.00", afterRelease.ReservedNGN)
	}
}

func TestWithdrawNGN_BlockedBelowReserved(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	pm := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Status:               models.PaymentMerchantStatusActive,
		AvailableNGN:         "1000.00",
		ReservedNGN:          "800.00",
		InboundAccountNumber: "3",
	}
	db.Create(pm)

	// Free balance = 1000 - 800 = 200. Withdraw 300 must fail.
	ok, err := svc.WithdrawNGN(context.Background(), pm.ID, "300.00")
	if err != nil {
		t.Fatalf("WithdrawNGN(300) returned unexpected error: %v", err)
	}
	if ok {
		t.Fatal("WithdrawNGN(300) must fail: 300 > free balance 200")
	}

	// Verify no change.
	var unchanged models.PaymentMerchant
	db.First(&unchanged, "id = ?", pm.ID)
	if unchanged.AvailableNGN != "1000.00" && unchanged.AvailableNGN != "1000" {
		t.Fatalf("available changed after failed withdraw: %s", unchanged.AvailableNGN)
	}

	// Withdraw 200 must succeed.
	ok2, err2 := svc.WithdrawNGN(context.Background(), pm.ID, "200.00")
	if err2 != nil || !ok2 {
		t.Fatalf("WithdrawNGN(200) should succeed: ok=%v err=%v", ok2, err2)
	}

	var afterWithdraw models.PaymentMerchant
	db.First(&afterWithdraw, "id = ?", pm.ID)
	// available: 1000 - 200 = 800; reserved stays 800.
	if afterWithdraw.AvailableNGN != "800.00" && afterWithdraw.AvailableNGN != "800" {
		t.Fatalf("after withdraw: available=%s, want 800.00", afterWithdraw.AvailableNGN)
	}
	if afterWithdraw.ReservedNGN != "800.00" && afterWithdraw.ReservedNGN != "800" {
		t.Fatalf("after withdraw: reserved=%s, want 800.00", afterWithdraw.ReservedNGN)
	}
}

func TestReserveNGN_InvalidAmount(t *testing.T) {
	db := newSQLiteForPM(t)
	svc := NewPMService(db)

	pm := &models.PaymentMerchant{
		UserID:               uuid.New(),
		Status:               models.PaymentMerchantStatusActive,
		AvailableNGN:         "1000.00",
		ReservedNGN:          "0.00",
		InboundAccountNumber: "4",
	}
	db.Create(pm)

	for _, bad := range []string{"", "-1.00", "abc", "1.001"} {
		_, err := svc.ReserveNGN(context.Background(), pm.ID, bad)
		if err == nil {
			t.Errorf("ReserveNGN(%q): expected error for invalid amount, got nil", bad)
		}
	}
}
