package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrPMProvisioningUnavailable is returned when the provisioner is nil
// (Nomba credentials not configured at startup).
var ErrPMProvisioningUnavailable = errors.New("PM provisioning unavailable: Nomba not configured")

// NombaProvisioner is the minimal interface PMOnboardingService needs so tests
// can inject a fake without depending on *NombaClient directly. Under the
// VA-only corporate model a PM is a single Nomba virtual account under TSA's
// parent business — provisioning needs only CreateVirtualAccount.
// *NombaClient satisfies this interface.
type NombaProvisioner interface {
	CreateVirtualAccount(ctx context.Context, accountRef, accountName string) (bankAccountNumber string, err error)
}

// PMOnboardingService provisions a Nomba virtual account (VA) for a new
// corporate Payment-Merchant and activates the payment_merchants row.
//
// VA-only model: each corporate PM is a virtual account under TSA's single
// parent Nomba business (NOT a heavyweight sub-account). Funds paid into the
// PM's VA land in the parent balance, attributed by the VA's deterministic
// accountRef. There is no per-PM sub-account, settlement, or balance call.
//
// The provisioning is resume-able: the accountRef + VA number are persisted
// immediately after the API call so a retry can re-load the row and skip the
// already-completed VA create (idempotency anchor = the stored nomba_account_ref).
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

// pmAccountRef derives a deterministic, unique virtual-account reference for a
// PM from its user id. Deterministic so a retry produces the SAME ref (Nomba
// rejects a duplicate accountRef), and unique per corporate user.
func pmAccountRef(userID uuid.UUID) string { return "pm-" + userID.String() }

// ProvisionAndActivate runs the VA-only Nomba provisioning for the corporate PM
// identified by req.UserID:
//
//  1. Upsert a payment_merchants row (keyed on user_id, provisioning_status='pending').
//  2. If NombaAccountRef == "" → CreateVirtualAccount(accountRef, businessName) →
//     persist nomba_account_ref + inbound_account_number (the VA bankAccountNumber).
//  3. Flip provisioning_status='active', status='active' and copy the
//     (already validated at submit) settlement bank / maker wallet / caps.
//
// On any Nomba error: persist whatever succeeded, leave the row non-active,
// return the error. The caller can retry safely — a row whose nomba_account_ref
// is already set skips the VA create on re-entry.
func (s *PMOnboardingService) ProvisionAndActivate(ctx context.Context, req *models.MerchantRequest) (*models.PaymentMerchant, error) {
	if s.provisioner == nil {
		return nil, ErrPMProvisioningUnavailable
	}

	// ── Step 1: Upsert the payment_merchants row ──────────────────────────────
	pm, err := s.upsertPendingRow(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("pm_onboarding: upsert: %w", err)
	}

	// ── Step 2: Virtual account (skip if already provisioned) ─────────────────
	if pm.NombaAccountRef == "" {
		accountRef := pmAccountRef(req.UserID)
		vaNumber, err := s.provisioner.CreateVirtualAccount(ctx, accountRef, req.BusinessName)
		if err != nil {
			// nothing to persist — failed before any VA number returned.
			return pm, fmt.Errorf("pm_onboarding: create virtual account: %w", err)
		}
		pm.NombaAccountRef = accountRef
		pm.InboundAccountNumber = vaNumber
		if err := s.persistFields(ctx, pm, map[string]interface{}{
			"nomba_account_ref":      accountRef,
			"inbound_account_number": vaNumber,
		}); err != nil {
			return pm, fmt.Errorf("pm_onboarding: persist virtual account: %w", err)
		}
	}

	// ── Step 3: Activate (VA created + settlement/maker/caps copied) ──────────
	activationFields := map[string]interface{}{
		"provisioning_status":       models.PMProvisioningActive,
		"status":                    models.PaymentMerchantStatusActive,
		"settlement_bank_code":      req.SettlementBankCode,
		"settlement_account_number": req.SettlementAccountNumber,
		"settlement_account_name":   req.SettlementAccountName,
		"operator_wallet_address":   req.MakerWalletAddress,
		"per_trade_cap_usd":         req.RequestedPerTradeCapUSD,
		"daily_cap_usd":             req.RequestedDailyCapUSD,
	}
	if err := s.persistFields(ctx, pm, activationFields); err != nil {
		return pm, fmt.Errorf("pm_onboarding: activate: %w", err)
	}
	pm.ProvisioningStatus = models.PMProvisioningActive
	pm.Status = models.PaymentMerchantStatusActive
	pm.SettlementBankCode = req.SettlementBankCode
	pm.SettlementAccountNumber = req.SettlementAccountNumber
	pm.SettlementAccountName = req.SettlementAccountName
	pm.OperatorWalletAddress = req.MakerWalletAddress
	pm.PerTradeCapUSD = req.RequestedPerTradeCapUSD
	pm.DailyCapUSD = req.RequestedDailyCapUSD

	return pm, nil
}

// ProvisionAndActivatePM approves an existing PENDING payment_merchants row by
// id (admin-driven from the Payment Merchants page). Unlike ProvisionAndActivate
// — which is keyed on a MerchantRequest and runs as part of merchant approval —
// this works directly off the PM row whose settlement bank / maker wallet / caps
// are already populated. It re-runs Nomba provisioning if it never completed and
// flips the row to active. Idempotent on the VA create (anchored on the stored
// nomba_account_ref), so a retry of a previously-failed approval is safe.
//
// Errors:
//   - ErrPMProvisioningUnavailable: Nomba not configured (provisioner nil).
//   - ErrPMNotFound: no PM row with that id.
//   - ErrInvalidPMTransition: PM is not currently pending.
//   - wrapped Nomba error: VA creation failed (row left non-active, retry-safe).
func (s *PMOnboardingService) ProvisionAndActivatePM(ctx context.Context, pmID, adminID uuid.UUID) (*models.PaymentMerchant, error) {
	if s.provisioner == nil {
		return nil, ErrPMProvisioningUnavailable
	}

	var pm models.PaymentMerchant
	if err := s.db.WithContext(ctx).First(&pm, "id = ?", pmID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPMNotFound
		}
		return nil, err
	}
	if pm.Status != models.PaymentMerchantStatusPending {
		return nil, ErrInvalidPMTransition
	}

	// ── Virtual account (skip if a prior attempt already created it) ──────────
	if pm.NombaAccountRef == "" {
		accountRef := pmAccountRef(pm.UserID)
		vaNumber, err := s.provisioner.CreateVirtualAccount(ctx, accountRef, pm.Name)
		if err != nil {
			return &pm, fmt.Errorf("pm_onboarding: create virtual account: %w", err)
		}
		pm.NombaAccountRef = accountRef
		pm.InboundAccountNumber = vaNumber
		if err := s.persistFields(ctx, &pm, map[string]interface{}{
			"nomba_account_ref":      accountRef,
			"inbound_account_number": vaNumber,
			"provisioning_status":    models.PMProvisioningProvisioned,
		}); err != nil {
			return &pm, fmt.Errorf("pm_onboarding: persist virtual account: %w", err)
		}
	}

	// ── Activate ──────────────────────────────────────────────────────────────
	now := time.Now()
	if err := s.persistFields(ctx, &pm, map[string]interface{}{
		"provisioning_status": models.PMProvisioningActive,
		"status":              models.PaymentMerchantStatusActive,
		"reviewed_by":         adminID,
		"reviewed_at":         now,
	}); err != nil {
		return &pm, fmt.Errorf("pm_onboarding: activate: %w", err)
	}
	pm.ProvisioningStatus = models.PMProvisioningActive
	pm.Status = models.PaymentMerchantStatusActive
	pm.ReviewedBy = adminID
	pm.ReviewedAt = &now

	return &pm, nil
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
		SettlementAccountName:   req.SettlementAccountName,
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

// persistFields updates multiple columns on the payment_merchants row.
func (s *PMOnboardingService) persistFields(ctx context.Context, pm *models.PaymentMerchant, fields map[string]interface{}) error {
	return s.db.WithContext(ctx).
		Model(pm).
		Updates(fields).Error
}
