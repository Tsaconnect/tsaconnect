package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ErrCompanyPMUnusable: IT_PM_ID does not name an active company payment
// merchant (unset, not a UUID, missing, not active, or an LP row).
var ErrCompanyPMUnusable = errors.New("IT_PM_ID is not an active company payment merchant")

// ErrCompanyUserCannotBeLP: the company PM's user is every pool slice's buyer,
// so it can never also be an LP whose stock fills those slices (review M5).
var ErrCompanyUserCannotBeLP = errors.New("liquidity: the company account cannot provide liquidity")

// CompanyAccountNotEligibleCode is the API code for ErrCompanyUserCannotBeLP.
const CompanyAccountNotEligibleCode = "company_account_not_eligible"

// CompanyPMUserID is IT_PM_ID's user, whatever the row's status or kind.
func CompanyPMUserID(ctx context.Context, db *gorm.DB, id string) (uuid.UUID, error) {
	pm, err := companyPMRow(ctx, db, id)
	if err != nil {
		return uuid.Nil, err
	}
	return pm.UserID, nil
}

// companyPMRow is IT_PM_ID's row, any status and kind. Every refusal wraps
// ErrCompanyPMUnusable; a database failure is returned as itself.
func companyPMRow(ctx context.Context, db *gorm.DB, id string) (*models.PaymentMerchant, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: IT_PM_ID is not set", ErrCompanyPMUnusable)
	}
	pmID, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("%w: IT_PM_ID is not a UUID", ErrCompanyPMUnusable)
	}
	var pm models.PaymentMerchant
	err = db.WithContext(ctx).First(&pm, "id = ?", pmID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: IT_PM_ID %s names no payment merchant", ErrCompanyPMUnusable, pmID)
	}
	if err != nil {
		return nil, fmt.Errorf("IT_PM_ID %s: %w", pmID, err)
	}
	return &pm, nil
}

// LoadCompanyPM is IT_PM_ID's row, required active and never an LP (review
// M4). Every IT_PM_ID reader goes through it: the pool's gate 7, the slice
// buyer and IT's pay-into account.
func LoadCompanyPM(ctx context.Context, db *gorm.DB, id string) (*models.PaymentMerchant, error) {
	pm, err := companyPMRow(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if !pm.IsActive() {
		return nil, fmt.Errorf("%w: IT_PM_ID %s is %s, not active", ErrCompanyPMUnusable, pm.ID, pm.Status)
	}
	if pm.IsLP() {
		return nil, fmt.Errorf("%w: IT_PM_ID %s is a liquidity provider, not the company payment merchant", ErrCompanyPMUnusable, pm.ID)
	}
	// The company user buys every slice, so it may hold no LP row either: its
	// stock would fill its own slices.
	var lpRows int64
	if err := db.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("user_id = ? AND kind = ?", pm.UserID, models.PaymentMerchantKindLP).Count(&lpRows).Error; err != nil {
		return nil, fmt.Errorf("IT_PM_ID %s: LP check: %w", pm.ID, err)
	}
	if lpRows > 0 {
		return nil, fmt.Errorf("%w: IT_PM_ID %s's user is also a liquidity provider", ErrCompanyPMUnusable, pm.ID)
	}
	return pm, nil
}
