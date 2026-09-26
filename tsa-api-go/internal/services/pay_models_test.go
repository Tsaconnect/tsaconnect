package services

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestPayModels_ReferenceIsUniquePerMerchantAndMode(t *testing.T) {
	db := newPayTestDB(t)
	m := seedPayMerchant(t, db)
	mk := func(mode string) *models.PayPayment {
		return &models.PayPayment{MerchantID: m.ID, Mode: mode, Reference: "ORD-1", AmountKobo: 100_000, Currency: "NGN",
			SettlementMode: models.PaySettlementFiat, Status: models.PayStatusPending, ExpiresAt: time.Now()}
	}
	if err := db.Create(mk(models.PayModeLive)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(mk(models.PayModeLive)).Error; err == nil {
		t.Fatal("a second live payment with the same reference was stored")
	}
	if err := db.Create(mk(models.PayModeTest)).Error; err != nil {
		t.Fatalf("the same reference in test mode must be allowed: %v", err)
	}
	other := seedPayMerchant(t, db)
	p := mk(models.PayModeLive)
	p.MerchantID = other.ID
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("another merchant may reuse a reference: %v", err)
	}
}

func TestPayModels_KeyHashesAndAttemptsAreUniqueButOptional(t *testing.T) {
	db := newPayTestDB(t)
	a, b := seedPayMerchant(t, db), seedPayMerchant(t, db) // nil hashes twice: fine
	if err := db.Model(a).Update("live_key_hash", "abc").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(b).Update("live_key_hash", "abc").Error; err == nil {
		t.Fatal("two merchants share a live key hash")
	}
	order := uuid.New()
	mk := func(ref string) *models.PayPayment {
		return &models.PayPayment{MerchantID: a.ID, Mode: models.PayModeLive, Reference: ref, AmountKobo: 1, Currency: "NGN",
			SettlementMode: models.PaySettlementFiat, Status: models.PayStatusProcessing, ExpiresAt: time.Now(), PaycrestOrderID: &order}
	}
	if err := db.Create(mk("A")).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(mk("B")).Error; err == nil {
		t.Fatal("one Paycrest order linked to two payments")
	}
}
