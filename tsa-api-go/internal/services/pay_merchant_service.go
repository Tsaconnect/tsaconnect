package services

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// payChainRegistry resolves a chain ID to its name for crypto settlement.
// Kept narrow (rather than depending on the full BlockchainService) so this
// service can be constructed and tested without a live chain registry.
type payChainRegistry interface {
	ChainNameForID(id int64) (string, bool)
}

// ErrPayMerchantNotFound is returned when no PayMerchant matches a lookup.
var ErrPayMerchantNotFound = errors.New("merchant not found")

// Approval defaults for a payment_acceptance merchant request. An admin can
// change these later (Task 5's merchant-settings path).
const (
	payMerchantDefaultFeeSharePercent = 100
	payMerchantDefaultMinAmountKobo   = 100_000
	payMerchantDefaultMaxAmountKobo   = 500_000_000
)

// PayMerchantService provisions and manages TSA Pay merchants.
type PayMerchantService struct {
	db     *gorm.DB
	banks  BankDirectory    // nil → settlement can't be verified, so it stays unset
	chains payChainRegistry // nil → crypto settlement refused
	master []byte
	now    func() time.Time
}

// NewPayMerchantService constructs a PayMerchantService. banks and chains may
// be nil — TSA Pay stays dormant-friendly: a nil bank directory just leaves
// settlement unconfigured until an admin sets it, rather than failing boot.
func NewPayMerchantService(db *gorm.DB, banks BankDirectory, chains payChainRegistry, master []byte, now func() time.Time) *PayMerchantService {
	return &PayMerchantService{db: db, banks: banks, chains: chains, master: master, now: now}
}

// CreateFromRequest provisions a PayMerchant from an approved
// payment_acceptance MerchantRequest. It is idempotent: calling it again for
// a request that already provisioned a merchant (e.g. a retried approval)
// returns the existing row instead of erroring or inserting a duplicate.
//
// Settlement is only set when the applicant supplied a complete bank/account
// and the account resolves via the name-enquiry: the account name stored is
// always the resolver's, never the applicant-typed one. An unverifiable or
// incomplete settlement destination is left unset for an admin to configure.
func (s *PayMerchantService) CreateFromRequest(ctx context.Context, req *models.MerchantRequest) (*models.PayMerchant, error) {
	var existing models.PayMerchant
	err := s.db.WithContext(ctx).Where("merchant_request_id = ?", req.ID).First(&existing).Error
	if err == nil {
		return &existing, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// The submit handler validates and trims the website, but CreateFromRequest
	// is also the resume path for requests approved before that validation
	// existed (or edited directly), so it trims defensively too — url.Parse
	// errors (returns a nil *url.URL) on a whitespace-padded string, and a
	// malformed Website must return an error, never a nil-pointer dereference
	// on u.Hostname().
	u, err := url.Parse(strings.TrimSpace(req.Website))
	if err != nil {
		return nil, fmt.Errorf("pay merchant: invalid website %q: %w", req.Website, err)
	}
	domain := strings.ToLower(u.Hostname())

	m := &models.PayMerchant{
		MerchantRequestID:       req.ID,
		UserID:                  req.UserID,
		BusinessName:            req.BusinessName,
		WebsiteDomain:           domain,
		ReturnDomains:           domain,
		Status:                  models.PayMerchantActive,
		CustomerFeeSharePercent: payMerchantDefaultFeeSharePercent,
		MinAmountKobo:           payMerchantDefaultMinAmountKobo,
		MaxAmountKobo:           payMerchantDefaultMaxAmountKobo,
		WebhookSecretVersion:    1,
	}

	if req.SettlementBankCode != "" && req.SettlementAccountNumber != "" && req.SettlementAccountName != "" && s.banks != nil {
		if res, resolveErr := s.banks.Resolve(ctx, req.SettlementBankCode, req.SettlementAccountNumber); resolveErr == nil {
			m.SettlementMode = models.PaySettlementFiat
			m.BankCode = req.SettlementBankCode
			m.AccountNumber = req.SettlementAccountNumber
			m.AccountName = res.AccountName
		}
		// A failed resolve (including ErrNombaAccountNotFound) leaves settlement
		// unset rather than trusting the applicant-typed account name.
	}

	if err := s.db.WithContext(ctx).Create(m).Error; err != nil {
		if isUniqueViolation(err) {
			var again models.PayMerchant
			if reErr := s.db.WithContext(ctx).Where("merchant_request_id = ?", req.ID).First(&again).Error; reErr == nil {
				return &again, nil
			}
		}
		return nil, err
	}

	return m, nil
}
