package services

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var payTestNow = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func newPayTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newCurrencyTestDB(t)
	createTestUsersTable(t, db)
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{}, &models.PayAuditEvent{}); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedPayMerchant is an active, fiat-settling merchant with the defaults an
// approval gives, ready to take payments.
func seedPayMerchant(t *testing.T, db *gorm.DB) *models.PayMerchant {
	t.Helper()
	m := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: uuid.New(), BusinessName: "Sky Air",
		WebsiteDomain: "skyair.ng", Status: models.PayMerchantActive, SettlementMode: models.PaySettlementFiat,
		BankCode: "058", AccountNumber: "0123456789", AccountName: "SKY AIR LTD",
		CustomerFeeSharePercent: 100, MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000,
		WebhookSecretVersion: 1, ReturnDomains: "skyair.ng,www.skyair.ng"}
	if err := db.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	return m
}
