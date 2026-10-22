package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrSellerPayoutInstitution = errors.New("that bank or mobile money provider is not supported for payouts")
	ErrSellerPayoutAccount     = errors.New("the account could not be verified")
	ErrSellerPayoutUnavailable = errors.New("payout destinations are unavailable right now")
	// Only an account whose holder name the provider confirmed can be paid:
	// a typed name is the seller's word, and TSA would be the sender.
	ErrSellerPayoutUnverified = errors.New("that account's name can't be verified, so it can't receive payouts yet")
	ErrSellerPayoutKYC        = errors.New("verify your identity before adding a payout account")
	// A payout goes only to an account in the seller's own name, or in the name
	// of their verified business: anything else makes TSA the sender to a third party.
	ErrSellerPayoutNameMismatch = errors.New("the account name must match your verified name or your verified business name")
	ErrSellerPayoutCode         = errors.New("we couldn't send the confirmation code")
	ErrSellerPayoutBadCode      = errors.New("that code is incorrect")
	ErrSellerPayoutNoPending    = errors.New("there is no change waiting for that code")
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
	// mailer nil: a destination can't be confirmed, so none can be set.
	mailer CodeMailer
	// identity is the seller's KYC status and verified legal name.
	identity func(ctx context.Context, userID uuid.UUID) (verified bool, legalName string, err error)
	// businessNames are the seller's verified business names (active TSA Pay merchants).
	businessNames func(ctx context.Context, userID uuid.UUID) ([]string, error)
	now           func() time.Time
}

func NewSellerPayoutService(db *gorm.DB, dir sellerPayoutInstitutions, banks BankDirectory, mailer CodeMailer) *SellerPayoutService {
	return &SellerPayoutService{db: db, dir: dir, banks: banks, mailer: mailer, now: time.Now,
		identity: func(ctx context.Context, id uuid.UUID) (bool, string, error) { return KYCIdentity(ctx, db, id) },
		businessNames: func(ctx context.Context, id uuid.UUID) ([]string, error) {
			var names []string
			err := db.WithContext(ctx).Table("pay_merchants").
				Where("user_id = ? AND status = ?", id, models.PayMerchantActive).
				Pluck("business_name", &names).Error
			return names, err
		}}
}

// SellerPayoutPending says where the confirmation code went.
type SellerPayoutPending struct {
	ID        uuid.UUID `json:"id"`
	ExpiresAt time.Time `json:"expiresAt"`
	SentTo    string    `json:"sentTo"`
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

// Request validates the destination and emails the seller a code; it is saved
// only by Confirm. Nothing changes until then.
func (s *SellerPayoutService) Request(ctx context.Context, userID uuid.UUID, in SetSellerPayoutInput) (*SellerPayoutPending, error) {
	if s.mailer == nil {
		return nil, ErrSellerPayoutCode
	}
	verified, legalName, err := s.identity(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSellerPayoutUnavailable, err)
	}
	if !verified {
		return nil, ErrSellerPayoutKYC
	}
	row, err := s.validate(ctx, userID, in)
	if err != nil {
		return nil, err
	}
	if err := s.requireOwnName(ctx, userID, legalName, row.AccountName); err != nil {
		return nil, err
	}
	email, name, err := UserContact(ctx, s.db, userID)
	if err != nil {
		return nil, err
	}
	if email == "" {
		return nil, ErrSellerPayoutCode
	}
	body, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	code, err := NewEmailCode()
	if err != nil {
		return nil, err
	}
	change := models.SellerPayoutChange{ID: uuid.New(), UserID: userID, Payload: string(body), ExpiresAt: s.now().Add(EmailCodeTTL)}
	change.CodeHash = EmailCodeHash(change.ID, code)
	// One waiting change per seller; a concurrent request replaces it atomically.
	err = s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"id", "payload", "code_hash", "attempts", "expires_at", "created_at"}),
	}).Create(&change).Error
	if err != nil {
		return nil, err
	}
	message := fmt.Sprintf("Your code to pay marketplace sales into %s (%s) is %s. It expires in %d minutes.\n\n"+
		"If you didn't ask for this, don't share the code: change your password and contact support.",
		row.InstitutionName, row.AccountName, code, int(EmailCodeTTL/time.Minute))
	if err := s.mailer.Send(email, name, "Your payout confirmation code", "Confirm this change", message, "", ""); err != nil {
		log.Printf("[seller-payout] code for user %s could not be sent: %v", userID, err)
		s.db.WithContext(ctx).Where("id = ?", change.ID).Delete(&models.SellerPayoutChange{})
		return nil, ErrSellerPayoutCode
	}
	return &SellerPayoutPending{ID: change.ID, ExpiresAt: change.ExpiresAt, SentTo: MaskEmail(email)}, nil
}

// Confirm saves the destination waiting behind code, replacing any earlier one.
// The attempt is spent before the code is compared and the change is claimed
// (deleted) before it is applied, so it applies at most once.
func (s *SellerPayoutService) Confirm(ctx context.Context, userID, changeID uuid.UUID, code string) (*models.SellerPayoutMethod, error) {
	var ch models.SellerPayoutChange
	err := s.db.WithContext(ctx).Where("id = ? AND user_id = ?", changeID, userID).First(&ch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSellerPayoutNoPending
	}
	if err != nil {
		return nil, err
	}
	if !s.now().Before(ch.ExpiresAt) {
		s.db.WithContext(ctx).Where("id = ?", ch.ID).Delete(&models.SellerPayoutChange{})
		return nil, ErrSellerPayoutNoPending
	}
	outcome, err := RedeemEmailCode(ctx, s.db, &models.SellerPayoutChange{}, ch.ID, ch.Attempts, ch.CodeHash, code)
	if err != nil {
		return nil, err
	}
	switch outcome {
	case EmailCodeClaimed:
	case EmailCodeWrong:
		return nil, ErrSellerPayoutBadCode
	default:
		return nil, ErrSellerPayoutNoPending
	}
	// KYC can be revoked while a code is waiting.
	verified, legalName, err := s.identity(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSellerPayoutUnavailable, err)
	}
	if !verified {
		return nil, ErrSellerPayoutKYC
	}
	var row models.SellerPayoutMethod
	if err := json.Unmarshal([]byte(ch.Payload), &row); err != nil {
		return nil, err
	}
	row.ID, row.UserID = uuid.New(), userID
	// Names can change between request and confirm (a rename review).
	if err := s.requireOwnName(ctx, userID, legalName, row.AccountName); err != nil {
		return nil, err
	}
	return s.save(ctx, row)
}

// requireOwnName holds the account to the seller's verified legal name or one
// of their verified business names (same matcher as inbound attribution).
func (s *SellerPayoutService) requireOwnName(ctx context.Context, userID uuid.UUID, legalName, accountName string) error {
	if MatchAccountName(legalName, accountName).IsMatch() {
		return nil
	}
	names, err := s.businessNames(ctx, userID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSellerPayoutUnavailable, err)
	}
	for _, n := range names {
		if MatchAccountName(n, accountName).IsMatch() {
			return nil
		}
	}
	return ErrSellerPayoutNameMismatch
}

// validate checks the destination against the provider's directory and, for
// an NGN bank, the bank's own name for the account. It stores nothing.
func (s *SellerPayoutService) validate(ctx context.Context, userID uuid.UUID, in SetSellerPayoutInput) (*models.SellerPayoutMethod, error) {
	row, err := s.build(ctx, userID, in)
	if err != nil {
		return nil, err
	}
	if !row.Verified {
		return nil, ErrSellerPayoutUnverified
	}
	return row, nil
}

func (s *SellerPayoutService) build(ctx context.Context, userID uuid.UUID, in SetSellerPayoutInput) (*models.SellerPayoutMethod, error) {
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

	return &models.SellerPayoutMethod{
		ID: uuid.New(), UserID: userID, Kind: inst.Type, Currency: fiat,
		InstitutionCode: inst.Code, InstitutionName: inst.Name,
		AccountNumber: account, AccountName: name, Verified: verified,
	}, nil
}

func (s *SellerPayoutService) save(ctx context.Context, row models.SellerPayoutMethod) (*models.SellerPayoutMethod, error) {
	err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
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
	if err := s.db.WithContext(ctx).Where("user_id = ?", row.UserID).First(&saved).Error; err != nil {
		return nil, err
	}
	return &saved, nil
}

// Clear returns the seller to wallet payouts.
func (s *SellerPayoutService) Clear(ctx context.Context, userID uuid.UUID) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A code still waiting must not bring the old destination back.
		if err := tx.Where("user_id = ?", userID).Delete(&models.SellerPayoutChange{}).Error; err != nil {
			return err
		}
		return tx.Where("user_id = ?", userID).Delete(&models.SellerPayoutMethod{}).Error
	})
}
