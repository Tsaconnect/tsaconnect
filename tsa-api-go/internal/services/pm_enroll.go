package services

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

var (
	// ErrNotAMerchant: only a user an admin already approved as a merchant may
	// enroll for P2P without a second application.
	ErrNotAMerchant = errors.New("payment merchant: caller is not an approved merchant")
	// ErrPMEnrollDecided: the user already has a merchant row that is pending,
	// rejected or paused, or a P2P application an admin has not approved, so an
	// admin's decision stands rather than being replaced by self-service.
	ErrPMEnrollDecided = errors.New("payment merchant: an admin decision already exists for this merchant")
	// ErrPayoutAccountRequired: the merchant has no verified naira bank payout
	// account yet. It is their one payout destination, so they set it there
	// first and P2P settles into it.
	ErrPayoutAccountRequired = errors.New("payment merchant: set a naira bank payout account first")
)

// PMEnrollInput is what an approved merchant supplies to take part in P2P.
type PMEnrollInput struct {
	UserID             uuid.UUID
	MakerWalletAddress string
}

// EnrollApprovedMerchant gives an approved merchant their payment-merchant row
// without a second application: the merchant approval was the review. It goes
// through ApproveFromRequestTx, so the row is the one an admin approval would
// write ('approved', uncapped), and the merchant then activates it the usual
// way. Idempotent for a row that is already approved or active.
func (s *PMOnboardingService) EnrollApprovedMerchant(ctx context.Context, in PMEnrollInput) (*models.PaymentMerchant, error) {
	var user models.User
	if err := s.db.WithContext(ctx).Select("id", "name", "role").First(&user, "id = ?", in.UserID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotAMerchant
		}
		return nil, err
	}
	if user.Role != models.RoleMerchant {
		return nil, ErrNotAMerchant
	}

	// An admin's decision on a P2P application stands: a pending or rejected
	// application (the newest one, so a later approval after a rejection counts)
	// is not something self-service can step around.
	var latest models.MerchantRequest
	switch err := s.db.WithContext(ctx).
		Where("user_id = ? AND business_type = ?", in.UserID, models.BusinessTypeP2PMerchant).
		Order("created_at DESC").First(&latest).Error; {
	case err == nil:
		if latest.Status != models.MerchantRequestStatusApproved {
			return nil, ErrPMEnrollDecided
		}
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}

	var existing models.PaymentMerchant
	err := wherePMKind(s.db.WithContext(ctx).Where("user_id = ?", in.UserID), models.PaymentMerchantKindMerchant).First(&existing).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err == nil {
		switch existing.Status {
		case models.PaymentMerchantStatusApproved, models.PaymentMerchantStatusActive:
			return &existing, nil
		default:
			return nil, ErrPMEnrollDecided
		}
	}
	if err := s.RefuseSecondRoleWhileDisabled(ctx, in.UserID, models.PaymentMerchantKindMerchant); err != nil {
		return nil, err
	}

	// Nobody reviews this enrollment, so P2P settles into the merchant's own
	// payout account, which already passed the name rule when it was saved.
	account, err := MerchantBankAccount(ctx, s.db, in.UserID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrPayoutAccountRequired
	}

	req := &models.MerchantRequest{
		UserID:                  in.UserID,
		BusinessType:            models.BusinessTypeP2PMerchant,
		BusinessName:            s.enrollName(ctx, &user),
		SettlementBankCode:      account.InstitutionCode,
		SettlementAccountNumber: account.AccountNumber,
		SettlementAccountName:   account.AccountName,
		MakerWalletAddress:      in.MakerWalletAddress,
	}
	var pm *models.PaymentMerchant
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		if pm, err = s.ApproveFromRequestTx(ctx, tx, req); err != nil {
			return err
		}
		return tx.Model(pm).Update("review_note", "self-enrolled: approved merchant").Error
	})
	if err != nil {
		return nil, err
	}
	return pm, nil
}

// enrollName is the name the merchant shows: the business name they were
// approved under (else their account name), with their legal name added as on
// every merchant record until the business is verified.
func (s *PMOnboardingService) enrollName(ctx context.Context, user *models.User) string {
	base := user.Name
	var req models.MerchantRequest
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND status = ? AND business_type <> ?", user.ID,
			models.MerchantRequestStatusApproved, models.BusinessTypePaymentAcceptance).
		Order("created_at DESC").First(&req).Error
	if err == nil && strings.TrimSpace(req.BusinessName) != "" {
		base = strings.TrimSpace(req.BusinessName)
	}
	legal, err := ownerLegalName(ctx, s.db, user.ID)
	if err != nil {
		return base
	}
	return composeBusinessName(stripLegalSuffix(base, legal), legal, false)
}
