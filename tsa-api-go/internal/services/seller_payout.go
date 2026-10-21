package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrSellerPayoutInstitution = errors.New("that bank or mobile money provider is not supported for payouts")
	ErrSellerPayoutAccount     = errors.New("the account could not be verified")
	ErrSellerPayoutUnavailable = errors.New("payout destinations are unavailable right now")
)

// sellerPayoutInstitutions is the slice of PaycrestDirectory this needs.
type sellerPayoutInstitutions interface {
	Institutions(ctx context.Context, fiat string) ([]PaycrestPayoutOption, error)
}

// SellerPayoutService owns a marketplace seller's payout destination: a bank
// or mobile money account the seller is paid fiat into, or none (wallet).
type SellerPayoutService struct {
	db    *gorm.DB
	dir   sellerPayoutInstitutions
	banks BankDirectory // NGN name enquiry; may be nil
}

func NewSellerPayoutService(db *gorm.DB, dir sellerPayoutInstitutions, banks BankDirectory) *SellerPayoutService {
	return &SellerPayoutService{db: db, dir: dir, banks: banks}
}

// SellerPayout is a seller's resolved destination. Method is nil for a wallet.
type SellerPayout struct {
	Kind   string
	Method *models.SellerPayoutMethod
}

// Resolve returns where userID is paid. No row means the wallet.
func (s *SellerPayoutService) Resolve(ctx context.Context, userID uuid.UUID) (SellerPayout, error) {
	var m models.SellerPayoutMethod
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return SellerPayout{Kind: models.SellerPayoutWallet}, nil
	}
	if err != nil {
		return SellerPayout{}, err
	}
	return SellerPayout{Kind: m.Kind, Method: &m}, nil
}

// SetSellerPayoutInput is what the seller submits. The kind comes from the
// institution, never from the caller.
type SetSellerPayoutInput struct {
	Currency        string
	InstitutionCode string
	AccountNumber   string
	// AccountName is only used where no name enquiry exists (non-NGN).
	AccountName string
}

// Set validates and stores the destination, replacing any earlier one.
func (s *SellerPayoutService) Set(ctx context.Context, userID uuid.UUID, in SetSellerPayoutInput) (*models.SellerPayoutMethod, error) {
	if s.dir == nil {
		return nil, ErrSellerPayoutUnavailable
	}
	fiat := strings.ToUpper(strings.TrimSpace(in.Currency))
	if fiat == "" {
		fiat = "NGN"
	}
	code := strings.TrimSpace(in.InstitutionCode)
	account := strings.TrimSpace(in.AccountNumber)

	opts, err := s.dir.Institutions(ctx, fiat)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSellerPayoutUnavailable, err)
	}
	var inst *PaycrestPayoutOption
	for i := range opts {
		if opts[i].Code == code {
			inst = &opts[i]
			break
		}
	}
	if inst == nil {
		return nil, ErrSellerPayoutInstitution
	}
	if err := validatePayoutIdentifier(inst.Type, fiat, account); err != nil {
		return nil, err
	}

	name, verified := strings.TrimSpace(in.AccountName), false
	if fiat == "NGN" && inst.Type == PaycrestInstitutionBank {
		// The name comes from the bank. A seller cannot be paid into an account
		// we cannot name, and never under a name they typed.
		if s.banks == nil {
			return nil, ErrSellerPayoutUnavailable
		}
		res, err := s.banks.Resolve(ctx, code, account)
		if err != nil {
			if errors.Is(err, ErrNombaAccountNotFound) {
				return nil, ErrSellerPayoutAccount
			}
			return nil, fmt.Errorf("%w: %v", ErrSellerPayoutUnavailable, err)
		}
		name, verified = strings.TrimSpace(res.AccountName), true
	}
	if name == "" {
		return nil, ErrSellerPayoutAccount
	}

	row := models.SellerPayoutMethod{
		ID: uuid.New(), UserID: userID, Kind: inst.Type, Currency: fiat,
		InstitutionCode: inst.Code, InstitutionName: inst.Name,
		AccountNumber: account, AccountName: name, Verified: verified,
	}
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"kind", "currency", "institution_code", "institution_name",
			"account_number", "account_name", "verified", "updated_at",
		}),
	}).Create(&row).Error
	if err != nil {
		return nil, err
	}
	var saved models.SellerPayoutMethod
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&saved).Error; err != nil {
		return nil, err
	}
	return &saved, nil
}

// Clear returns the seller to wallet payouts.
func (s *SellerPayoutService) Clear(ctx context.Context, userID uuid.UUID) error {
	return s.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&models.SellerPayoutMethod{}).Error
}
