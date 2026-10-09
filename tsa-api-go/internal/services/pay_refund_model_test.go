package services

import (
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

func newRefundRow(paymentID uuid.UUID, status string) *models.PayRefund {
	return &models.PayRefund{PaymentID: paymentID, MerchantID: uuid.New(), Mode: models.PayModeLive,
		AmountMicroUSD: 1_000_000, CapMicroUSD: 5_000_000, ToAddress: "0x000000000000000000000000000000000000dEaD",
		Status: status, RequestedVia: models.PayRefundViaWeb}
}

// assertOneOpenRefundIndex: a payment holds at most one requested, submitted
// or needs_review refund at a time; closed ones sit beside it freely.
func assertOneOpenRefundIndex(t *testing.T, db *gorm.DB) {
	t.Helper()
	paymentID := uuid.New()
	first := newRefundRow(paymentID, models.PayRefundRequested)
	if err := db.Create(first).Error; err != nil {
		t.Fatalf("first open refund: %v", err)
	}
	for _, status := range []string{models.PayRefundRequested, models.PayRefundSubmitted, models.PayRefundNeedsReview} {
		if err := db.Create(newRefundRow(paymentID, status)).Error; err == nil {
			t.Fatalf("a second open refund (%s) was accepted", status)
		}
	}
	for _, status := range []string{models.PayRefundConfirmed, models.PayRefundFailed, models.PayRefundCancelled} {
		if err := db.Create(newRefundRow(paymentID, status)).Error; err != nil {
			t.Fatalf("a closed refund (%s) beside the open one: %v", status, err)
		}
	}
	if err := db.Model(first).Update("status", models.PayRefundConfirmed).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(newRefundRow(paymentID, models.PayRefundRequested)).Error; err != nil {
		t.Fatalf("a new refund once the first closed: %v", err)
	}
	if err := db.Create(newRefundRow(uuid.New(), models.PayRefundRequested)).Error; err != nil {
		t.Fatalf("another payment's open refund: %v", err)
	}
}

func TestPayRefund_OneOpenRefundPerPaymentIsEnforcedByTheIndex(t *testing.T) {
	assertOneOpenRefundIndex(t, newPayTestDB(t))
}

// The same partial index, as PostgreSQL builds it from the tag (skips without DATABASE_URL).
func TestPayRefund_OneOpenIndexOnPostgres(t *testing.T) {
	assertOneOpenRefundIndex(t, testutil.TempPostgres(t, "pay_refund_idx_", &models.PayRefund{}))
}

func TestPayRefund_ATransactionHashIsUsedOnce(t *testing.T) {
	db := newPayTestDB(t)
	h1, h2 := "0xabc", "0xabc"
	a := newRefundRow(uuid.New(), models.PayRefundSubmitted)
	a.TxHash = &h1
	b := newRefundRow(uuid.New(), models.PayRefundSubmitted)
	b.TxHash = &h2
	if err := db.Create(a).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(b).Error; err == nil {
		t.Fatal("a second refund took the same transaction hash")
	}
	// Refunds not yet submitted have no hash, any number of them.
	for i := 0; i < 2; i++ {
		if err := db.Create(newRefundRow(uuid.New(), models.PayRefundRequested)).Error; err != nil {
			t.Fatalf("hashless refund %d: %v", i, err)
		}
	}
	if !models.IsPayRefundStatus("needs_review") || models.IsPayRefundStatus("refunded") {
		t.Fatal("IsPayRefundStatus")
	}
}
