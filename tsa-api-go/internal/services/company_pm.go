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
	return pm, nil
}

// SetCompanyPMID names IT_PM_ID, whose user may never become an LP. Called
// once at boot.
func (s *PMOnboardingService) SetCompanyPMID(id string) { s.companyPMID = strings.TrimSpace(id) }

// refuseCompanyUserAsLP fails closed: an unreadable IT_PM_ID row is an error,
// never "not the company". The row's own status does not matter: a paused
// company PM's user is still the slice buyer once it resumes.
func (s *PMOnboardingService) refuseCompanyUserAsLP(ctx context.Context, db *gorm.DB, userID uuid.UUID) error {
	if s.companyPMID == "" {
		return nil
	}
	pm, err := companyPMRow(ctx, db, s.companyPMID)
	if errors.Is(err, ErrCompanyPMUnusable) {
		return nil // no company PM row, so no company user
	}
	if err != nil {
		return fmt.Errorf("pm_onboarding: company PM lookup: %w", err)
	}
	if pm.UserID == userID {
		return ErrCompanyUserCannotBeLP
	}
	return nil
}

// RefuseCompanyUserAsLP is refuseCompanyUserAsLP for an LP application.
func (s *PMOnboardingService) RefuseCompanyUserAsLP(ctx context.Context, userID uuid.UUID) error {
	return s.refuseCompanyUserAsLP(ctx, s.db, userID)
}
