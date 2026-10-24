package services

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// MerchantBankAccount is the user's payout account when it is a verified naira
// bank account, else nil. It is the merchant's one payout destination: TSA Pay
// and P2P start from it instead of asking for a bank again, and it already
// passed the name rule (verified name or business name) when it was saved.
func MerchantBankAccount(ctx context.Context, db *gorm.DB, userID uuid.UUID) (*models.SellerPayoutMethod, error) {
	var m models.SellerPayoutMethod
	err := db.WithContext(ctx).
		Where("user_id = ? AND kind = ? AND currency = ? AND verified = ?",
			userID, PaycrestInstitutionBank, "NGN", true).
		First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// syncPMSettlement points the user's payment-merchant rows at a saved payout
// account, so changing it in one place changes it for P2P too. Only a verified
// naira bank account can be a P2P settlement; anything else leaves the
// payment merchant as it is. It runs in the caller's transaction.
func syncPMSettlement(tx *gorm.DB, m *models.SellerPayoutMethod) error {
	if m.Kind != PaycrestInstitutionBank || m.Currency != "NGN" || !m.Verified {
		return nil
	}
	return wherePMKind(tx.Model(&models.PaymentMerchant{}).Where("user_id = ?", m.UserID), models.PaymentMerchantKindMerchant).
		Updates(map[string]interface{}{
			"settlement_bank_code":      m.InstitutionCode,
			"settlement_account_number": m.AccountNumber,
			"settlement_account_name":   m.AccountName,
		}).Error
}
