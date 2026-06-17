package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrPMProvisioningUnavailable is returned when the provisioner is nil
// (Nomba credentials not configured at startup).
var ErrPMProvisioningUnavailable = errors.New("PM provisioning unavailable: Nomba not configured")

// NombaProvisioner is the minimal interface PMOnboardingService needs so tests
// can inject a fake without depending on *NombaClient directly.
// *NombaClient satisfies this interface — the three methods were added in Task 2.
type NombaProvisioner interface {
	CreateSubAccount(ctx context.Context, name string) (subAccountID string, err error)
	CreateVirtualAccountForSubAccount(ctx context.Context, subAccountID, accountName string) (vaNumber, walletID string, err error)
	SetSubAccountSettlement(ctx context.Context, subAccountID, bankCode, accountNumber string) error
}

// PMOnboardingService provisions a Nomba sub-account + virtual account for a new
// corporate Payment-Merchant and activates the payment_merchants row.
//
// The provisioning is resume-able: each Nomba ID is persisted immediately after
// its API call so that a retry can re-load the row and skip already-completed
// steps (idempotency anchor = the stored nomba_sub_account_id).
type PMOnboardingService struct {
	db          *gorm.DB
	provisioner NombaProvisioner
}

// NewPMOnboardingService returns a PMOnboardingService.
// provisioner may be nil when Nomba creds are absent; in that case
// ProvisionAndActivate returns ErrPMProvisioningUnavailable.
func NewPMOnboardingService(db *gorm.DB, provisioner NombaProvisioner) *PMOnboardingService {
	return &PMOnboardingService{db: db, provisioner: provisioner}
}

// ProvisionAndActivate runs the four-step Nomba provisioning state machine for
// the corporate PM identified by req.UserID:
//
//  1. Upsert a payment_merchants row (keyed on user_id, provisioning_status='pending').
//  2. If NombaSubAccountID == "" → CreateSubAccount → persist immediately.
//  3. If InboundAccountNumber == "" → CreateVirtualAccountForSubAccount → persist.
//  4. SetSubAccountSettlement.
//  5. Flip provisioning_status='active', status='active'.
//
// On any Nomba error: persist whatever succeeded, leave the row non-active,
// return the error.  The caller can retry safely — already-completed steps are
// skipped on re-entry.
func (s *PMOnboardingService) ProvisionAndActivate(ctx context.Context, req *models.MerchantRequest) (*models.PaymentMerchant, error) {
	if s.provisioner == nil {
		return nil, ErrPMProvisioningUnavailable
	}

	// ── Step 1: Upsert the payment_merchants row ──────────────────────────────
	pm, err := s.upsertPendingRow(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("pm_onboarding: upsert: %w", err)
	}

	// ── Step 2: Sub-account (skip if already persisted) ───────────────────────
	if pm.NombaSubAccountID == "" {
		subAcctID, err := s.provisioner.CreateSubAccount(ctx, req.BusinessName)
		if err != nil {
			// nothing to persist — failed before any ID returned
			return pm, fmt.Errorf("pm_onboarding: create sub-account: %w", err)
		}
		pm.NombaSubAccountID = subAcctID
		if err := s.persistField(ctx, pm, "nomba_sub_account_id", subAcctID); err != nil {
			return pm, fmt.Errorf("pm_onboarding: persist sub-account id: %w", err)
		}
	}

	// ── Step 3: Virtual account (skip if already persisted) ───────────────────
	if pm.InboundAccountNumber == "" {
		vaNumber, walletID, err := s.provisioner.CreateVirtualAccountForSubAccount(ctx, pm.NombaSubAccountID, req.BusinessName)
		if err != nil {
			return pm, fmt.Errorf("pm_onboarding: create virtual account: %w", err)
		}
		pm.InboundAccountNumber = vaNumber
		pm.NombaWalletID = walletID
		if err := s.persistFields(ctx, pm, map[string]interface{}{
			"inbound_account_number": vaNumber,
			"nomba_wallet_id":        walletID,
		}); err != nil {
			return pm, fmt.Errorf("pm_onboarding: persist virtual account: %w", err)
		}
	}

	// ── Step 4: Settlement bank ───────────────────────────────────────────────
	// TODO(nomba-facts): SetSubAccountSettlement is re-invoked on every retry; safe only if Nomba's settlement endpoint is idempotent — add a settlement_configured flag once confirmed.
	if err := s.provisioner.SetSubAccountSettlement(ctx, pm.NombaSubAccountID, req.SettlementBankCode, req.SettlementAccountNumber); err != nil {
		return pm, fmt.Errorf("pm_onboarding: set settlement: %w", err)
	}

	// ── Step 4b: Mark as provisioned (all Nomba steps complete, pre-activation) ──
	if err := s.persistField(ctx, pm, "provisioning_status", models.PMProvisioningProvisioned); err != nil {
		return pm, fmt.Errorf("pm_onboarding: mark provisioned: %w", err)
	}
	pm.ProvisioningStatus = models.PMProvisioningProvisioned

	// ── Step 5: Activate ──────────────────────────────────────────────────────
	activationFields := map[string]interface{}{
		"provisioning_status":      models.PMProvisioningActive,
		"status":                   models.PaymentMerchantStatusActive,
		"settlement_bank_code":     req.SettlementBankCode,
		"settlement_account_number": req.SettlementAccountNumber,
		"operator_wallet_address":  req.MakerWalletAddress,
		"per_trade_cap_usd":        req.RequestedPerTradeCapUSD,
		"daily_cap_usd":            req.RequestedDailyCapUSD,
	}
	if err := s.persistFields(ctx, pm, activationFields); err != nil {
		return pm, fmt.Errorf("pm_onboarding: activate: %w", err)
	}
	pm.ProvisioningStatus = models.PMProvisioningActive
	pm.Status = models.PaymentMerchantStatusActive
	pm.SettlementBankCode = req.SettlementBankCode
	pm.SettlementAccountNumber = req.SettlementAccountNumber
	pm.OperatorWalletAddress = req.MakerWalletAddress
	pm.PerTradeCapUSD = req.RequestedPerTradeCapUSD
	pm.DailyCapUSD = req.RequestedDailyCapUSD

	return pm, nil
}

// upsertPendingRow finds or creates a payment_merchants row for req.UserID.
// If a row already exists (from a prior failed attempt), it is reloaded so the
// caller can skip already-completed provisioning steps.
func (s *PMOnboardingService) upsertPendingRow(ctx context.Context, req *models.MerchantRequest) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant

	// Try to find an existing row for this user (from a prior attempt).
	err := s.db.WithContext(ctx).Where("user_id = ?", req.UserID).First(&pm).Error
	if err == nil {
		// Row already exists — re-use it (resume from wherever it got to).
		return &pm, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// No existing row — create one in pending state.
	pm = models.PaymentMerchant{
		ID:                      uuid.New(),
		UserID:                  req.UserID,
		Name:                    req.BusinessName,
		Status:                  models.PaymentMerchantStatusPending,
		ProvisioningStatus:      models.PMProvisioningPending,
		SettlementBankCode:      req.SettlementBankCode,
		SettlementAccountNumber: req.SettlementAccountNumber,
		OperatorWalletAddress:   req.MakerWalletAddress,
		PerTradeCapUSD:          req.RequestedPerTradeCapUSD,
		DailyCapUSD:             req.RequestedDailyCapUSD,
	}

	// Use OnConflict DoNothing + Clauses so that a concurrent duplicate insert
	// (unlikely in practice) doesn't fail — we reload below.
	result := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&pm)
	if result.Error != nil {
		return nil, result.Error
	}

	// If another concurrent insert won the race, reload.
	if result.RowsAffected == 0 {
		if err := s.db.WithContext(ctx).Where("user_id = ?", req.UserID).First(&pm).Error; err != nil {
			return nil, err
		}
	}

	return &pm, nil
}

// persistField updates a single column on the payment_merchants row identified
// by pm.ID.
func (s *PMOnboardingService) persistField(ctx context.Context, pm *models.PaymentMerchant, column string, value interface{}) error {
	return s.db.WithContext(ctx).
		Model(pm).
		UpdateColumn(column, value).Error
}

// persistFields updates multiple columns on the payment_merchants row.
func (s *PMOnboardingService) persistFields(ctx context.Context, pm *models.PaymentMerchant, fields map[string]interface{}) error {
	return s.db.WithContext(ctx).
		Model(pm).
		Updates(fields).Error
}

