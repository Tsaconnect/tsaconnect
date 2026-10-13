package services

import (
	"context"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// oneRoleIndexName is the pre-2026-10-13 one-row-per-user index. While it
// stands, a user can hold only one role; the one-off drops it.
const oneRoleIndexName = "uq_payment_merchants_user"

// RefuseSecondRoleWhileDisabled is ErrSecondRoleNotEnabled when the user
// already holds the other role and the one-row-per-user index still stands, so
// an application that no approval could complete is refused at submit instead
// of sitting pending. Once the index is gone it never comes back, so that
// answer is remembered.
func (s *PMOnboardingService) RefuseSecondRoleWhileDisabled(ctx context.Context, userID uuid.UUID, kind string) error {
	if s.secondRoleEnabled.Load() {
		return nil
	}
	if !s.db.WithContext(ctx).Migrator().HasIndex(&models.PaymentMerchant{}, oneRoleIndexName) {
		s.secondRoleEnabled.Store(true)
		return nil
	}
	other := models.PaymentMerchantKindLP
	if kind == models.PaymentMerchantKindLP {
		other = models.PaymentMerchantKindMerchant
	}
	var n int64
	if err := wherePMKind(s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).Where("user_id = ?", userID), other).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrSecondRoleNotEnabled
	}
	return nil
}
