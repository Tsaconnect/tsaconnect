package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrPMProvisioningUnavailable is returned when the provisioner is nil (no fiat
// provider — Flutterwave or Nomba — configured at startup).
var ErrPMProvisioningUnavailable = errors.New("PM provisioning unavailable: no fiat provider configured")

// NombaProvisioner is the minimal interface PMOnboardingService needs so tests
// can inject a fake without depending on *NombaClient directly. Under the
// VA-only corporate model a PM is a single virtual account under TSA's parent
// business — provisioning needs only CreateVirtualAccount.
//
// idType ("BVN"|"NIN") + idNumber are the account holder's identity, required by
// Flutterwave live mode for NGN static accounts. They are supplied by the
// merchant at activation time and forwarded straight to the provider — never
// stored in our DB. *NombaClient and *FlutterwaveVAProvisioner satisfy this.
type NombaProvisioner interface {
	CreateVirtualAccount(ctx context.Context, accountRef, accountName, idType, idNumber string) (bankAccountNumber string, err error)
}

// ErrPMKYCRequired is returned when activation is attempted without a valid
// BVN/NIN. The handler validates shape up front; this is the service-side guard.
var ErrPMKYCRequired = errors.New("a BVN or NIN is required to activate the payment merchant")

// ErrSecondRoleNotEnabled: the user already has a row of the other kind and
// the one-row-per-user index (dropped by the 2026-10-13 one-off) still stands.
var ErrSecondRoleNotEnabled = errors.New("payment merchant: a second role needs the 2026-10-13 one-off index migration")

// ErrLiquidityUnavailable: no pool chain is configured (or the pool is closed
// for activation), so an LP cannot be approved or activated.
var ErrLiquidityUnavailable = errors.New("liquidity: providing liquidity is not available right now")

// AccountNameResolver re-resolves a settlement account's name (S34).
// BankDirectory satisfies it.
type AccountNameResolver interface {
	Resolve(ctx context.Context, bankCode, accountNumber string) (NombaLookupResult, error)
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

	// virtualAccountsRetired is set when P2P buyers are routed to the company
	// account. Activation then mints no virtual account, because nothing would
	// ever send a buyer to it, and so needs no BVN/NIN.
	virtualAccountsRetired bool

	// TSA Liquidity (spec §8). poolChainID is set at boot only when the
	// registry has a pool chain; zero leaves LP approval and activation off.
	poolChainID int64
	accounts    AccountNameResolver
	poolGate    PoolOpener

	// companyPMID is IT_PM_ID: its user buys every pool slice, so it is never
	// approved or activated as an LP (review M5).
	companyPMID string
}

// NewPMOnboardingService returns a PMOnboardingService.
// provisioner may be nil when Nomba creds are absent; in that case
// ProvisionAndActivate returns ErrPMProvisioningUnavailable.
func NewPMOnboardingService(db *gorm.DB, provisioner NombaProvisioner) *PMOnboardingService {
	return &PMOnboardingService{db: db, provisioner: provisioner}
}

// SetVirtualAccountsRetired switches activation to the central-inbound model:
// no virtual account, no identity. Set once at boot, from the same decision
// that routes P2P buyers to the company account, so the two cannot disagree.
func (s *PMOnboardingService) SetVirtualAccountsRetired(retired bool) {
	s.virtualAccountsRetired = retired
}

// SetLiquidity enables LP onboarding on poolChainID. Called once at boot.
func (s *PMOnboardingService) SetLiquidity(poolChainID int64, accounts AccountNameResolver, gate PoolOpener) {
	s.poolChainID, s.accounts, s.poolGate = poolChainID, accounts, gate
}

// LiquidityChainID is the pool chain new LPs are approved onto, if any.
func (s *PMOnboardingService) LiquidityChainID() (int64, bool) {
	if s == nil || s.poolChainID == 0 {
		return 0, false
	}
	return s.poolChainID, true
}

// NeedsIdentity reports whether activation asks the merchant for a BVN/NIN.
func (s *PMOnboardingService) NeedsIdentity() bool { return !s.virtualAccountsRetired }

// pmAccountRef derives a deterministic, unique virtual-account reference for a
// PM from its user id. Deterministic so a retry produces the SAME ref (Nomba
// rejects a duplicate accountRef), and unique per corporate user.
func pmAccountRef(userID uuid.UUID) string { return "pm-" + userID.String() }

// ApproveFromRequest upserts the corporate PM row for req.UserID into the
// 'approved' state — the merchant is cleared to operate but the NGN virtual
// account is NOT created yet. VA creation is deferred to ActivateWithKYC, which
// the merchant triggers by supplying their BVN/NIN (Flutterwave live mode
// requires an account-holder identity we deliberately do not store).
//
//  1. Upsert the payment_merchants row of the request's kind (keyed on user_id + kind).
//  2. Copy the (already validated at submit) settlement bank / maker wallet /
//     caps and set status='approved', provisioning_status='pending'.
//
// Idempotent: re-running on an already-approved/active row copies the same
// fields without downgrading an active PM's status.
func (s *PMOnboardingService) ApproveFromRequest(ctx context.Context, req *models.MerchantRequest) (*models.PaymentMerchant, error) {
	var pm *models.PaymentMerchant
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		pm, err = s.ApproveFromRequestTx(ctx, tx, req)
		return err
	})
	if err != nil {
		return nil, err
	}
	return pm, nil
}

// ApproveFromRequestTx is ApproveFromRequest inside the caller's transaction,
// so the merchant-request handler settles the row and its kind in the same
// transaction as the request's status and the user's role (review I2). Each
// approval writes only its own kind's row (owner, 2026-10-13: one user may
// hold both roles).
//
// The (user_id, kind) unique index with ON CONFLICT DO NOTHING in
// upsertPendingRow is what guarantees one row per role, with or without the
// user-row lock taken first (review M1). The lock is defence in depth: it
// runs two approvals for one user one after the other, so neither reads the
// user's rows while the other is half-written. Nothing depends on it today,
// and the concurrency tests pass without it; keep it anyway.
func (s *PMOnboardingService) ApproveFromRequestTx(ctx context.Context, tx *gorm.DB, req *models.MerchantRequest) (*models.PaymentMerchant, error) {
	kind, ok := models.PaymentMerchantKindFor(req.BusinessType)
	if !ok {
		return nil, fmt.Errorf("pm_onboarding: %s does not make a payment merchant", req.BusinessType)
	}
	var poolChainID *int64
	if kind == models.PaymentMerchantKindLP {
		id, ok := s.LiquidityChainID()
		if !ok {
			return nil, ErrLiquidityUnavailable
		}
		poolChainID = &id
	}
	tx = tx.WithContext(ctx)
	// NO KEY UPDATE serializes two approvals for the user (defence in depth,
	// see above) without blocking rows that merely reference it (KEY SHARE).
	// Find: a missing user has no role.
	var locked []models.User
	if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).Select("id").Where("id = ?", req.UserID).Find(&locked).Error; err != nil {
		return nil, fmt.Errorf("pm_onboarding: lock user: %w", err)
	}
	if kind == models.PaymentMerchantKindLP {
		if err := s.refuseCompanyUserAsLP(ctx, tx, req.UserID); err != nil {
			return nil, err
		}
	}
	pm, err := s.upsertPendingRow(ctx, tx, req, kind, poolChainID)
	if err != nil {
		return nil, fmt.Errorf("pm_onboarding: upsert: %w", err)
	}
	if kind == models.PaymentMerchantKindLP && (pm.Status == models.PaymentMerchantStatusActive || pm.Status == models.PaymentMerchantStatusPaused) {
		// A live LP's settlement name is the bank's, verified at activation
		// (S34); re-copying the request would replace it with the typed one.
		return nil, ErrInvalidPMTransition
	}

	perTrade, daily := req.RequestedPerTradeCapUSD, req.RequestedDailyCapUSD
	if kind == models.PaymentMerchantKindLP {
		perTrade, daily = 0, 0 // LP limits are on stake (spec §8.3), never per-trade caps
	}
	fields := map[string]interface{}{
		"settlement_bank_code":      req.SettlementBankCode,
		"settlement_account_number": req.SettlementAccountNumber,
		"settlement_account_name":   req.SettlementAccountName,
		"operator_wallet_address":   req.MakerWalletAddress,
		"per_trade_cap_usd":         perTrade,
		"daily_cap_usd":             daily,
	}
	if poolChainID != nil {
		fields["pool_chain_id"] = *poolChainID
	}
	// Only advance pending → approved; never downgrade an already-active PM.
	if pm.Status == models.PaymentMerchantStatusPending {
		fields["status"] = models.PaymentMerchantStatusApproved
	}
	if err := tx.Model(pm).Updates(fields).Error; err != nil {
		return pm, fmt.Errorf("pm_onboarding: approve: %w", err)
	}
	if pm.Status == models.PaymentMerchantStatusPending {
		pm.Status = models.PaymentMerchantStatusApproved
	}
	pm.SettlementBankCode = req.SettlementBankCode
	pm.SettlementAccountNumber = req.SettlementAccountNumber
	pm.SettlementAccountName = req.SettlementAccountName
	pm.OperatorWalletAddress = req.MakerWalletAddress
	pm.PerTradeCapUSD = perTrade
	pm.DailyCapUSD = daily
	if poolChainID != nil {
		pm.PoolChainID = poolChainID
	}

	return pm, nil
}

// ApprovePM approves an existing PENDING payment_merchants row by id (admin-driven
// from the Payment Merchants page): pending → approved. It does NOT create the NGN
// virtual account — that is deferred to ActivateWithKYC, which the merchant triggers
// by supplying their BVN/NIN. Only the admin review audit is recorded here.
//
// Errors:
//   - ErrPMNotFound: no PM row with that id.
//   - ErrInvalidPMTransition: PM is not currently pending.
func (s *PMOnboardingService) ApprovePM(ctx context.Context, pmID, adminID uuid.UUID) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	if err := s.db.WithContext(ctx).First(&pm, "id = ?", pmID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPMNotFound
		}
		return nil, err
	}
	if pm.IsLP() {
		// An LP is admitted only from its application (ApproveFromRequest via the
		// merchant-request approval), which checks the pool, KYC and wallet.
		return nil, ErrInvalidPMTransition
	}
	if pm.Status != models.PaymentMerchantStatusPending {
		return nil, ErrInvalidPMTransition
	}

	now := time.Now()
	if err := s.persistFields(ctx, &pm, map[string]interface{}{
		"status":      models.PaymentMerchantStatusApproved,
		"reviewed_by": adminID,
		"reviewed_at": now,
	}); err != nil {
		return &pm, fmt.Errorf("pm_onboarding: approve: %w", err)
	}
	pm.Status = models.PaymentMerchantStatusApproved
	pm.ReviewedBy = adminID
	pm.ReviewedAt = &now

	return &pm, nil
}

// ActivateWithKYC creates the PM's NGN static virtual account using the merchant's
// own BVN/NIN and flips the row to active. This is the merchant-triggered step that
// completes onboarding:
//
//  1. Load the PM by id and verify it belongs to userID (else ErrPMNotFound — an
//     ownership mismatch must not reveal that the row exists).
//  2. Require status 'approved' (idempotent success if already 'active').
//  3. If no VA yet → CreateVirtualAccount(accountRef, name, idType, idNumber) →
//     persist nomba_account_ref + inbound_account_number.
//  4. Flip status='active', provisioning_status='active'.
//
// idNumber (the BVN/NIN) is forwarded to the provider and NEVER stored or logged.
// Retry-safe: a row whose nomba_account_ref is already set skips the VA create.
//
// With virtual accounts retired (central inbound) step 3 is skipped entirely:
// there is no account to create, so neither a provider nor an id is required —
// instead the merchant's own Didit verification must have passed.
func (s *PMOnboardingService) ActivateWithKYC(ctx context.Context, pmID, userID uuid.UUID, idType, idNumber string) (*models.PaymentMerchant, error) {
	if !s.virtualAccountsRetired {
		if s.provisioner == nil {
			return nil, ErrPMProvisioningUnavailable
		}
		if strings.TrimSpace(idNumber) == "" {
			return nil, ErrPMKYCRequired
		}
	}

	var pm models.PaymentMerchant
	if err := s.db.WithContext(ctx).First(&pm, "id = ?", pmID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPMNotFound
		}
		return nil, err
	}
	if pm.UserID != userID {
		// Do not leak existence of another user's PM.
		return nil, ErrPMNotFound
	}
	if pm.IsLP() {
		// LPs activate through their own path, which adds the S34 name match.
		return nil, ErrInvalidPMTransition
	}
	if pm.Status == models.PaymentMerchantStatusActive {
		return &pm, nil // already activated — idempotent success
	}
	if pm.Status != models.PaymentMerchantStatusApproved {
		return nil, ErrInvalidPMTransition
	}

	// Without a virtual account there is no BVN/NIN either, and the merchant is
	// about to have third-party money forwarded to them. Their own Didit
	// verification is the identity check that replaces it — the same one a
	// TSA Pay applicant must pass.
	if s.virtualAccountsRetired {
		verified, err := IsKYCVerified(ctx, s.db, userID)
		if err != nil {
			return nil, fmt.Errorf("pm_onboarding: check verification: %w", err)
		}
		if !verified {
			return nil, ErrKYCVerificationRequired
		}
	}

	// ── Virtual account (skip if a prior attempt already created it) ──────────
	if pm.NombaAccountRef == "" && !s.virtualAccountsRetired {
		accountRef := pmAccountRef(pm.UserID)
		vaNumber, err := s.provisioner.CreateVirtualAccount(ctx, accountRef, pm.Name, idType, idNumber)
		if err != nil {
			// Wrap without the id number; the provider error carries no PII.
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
	if err := s.persistFields(ctx, &pm, map[string]interface{}{
		"provisioning_status": models.PMProvisioningActive,
		"status":              models.PaymentMerchantStatusActive,
	}); err != nil {
		return &pm, fmt.Errorf("pm_onboarding: activate: %w", err)
	}
	pm.ProvisioningStatus = models.PMProvisioningActive
	pm.Status = models.PaymentMerchantStatusActive

	return &pm, nil
}

// upsertPendingRow finds or creates a payment_merchants row of kind for
// req.UserID. If a row already exists (from a prior failed attempt), it is
// reloaded so the caller can skip already-completed provisioning steps. Both
// reads are kind-scoped: the other role's row is never returned.
//
// A rejected LP re-applying as an LP is reset to pending in one guarded
// statement so the approval can advance it (plan B P18); merchant rows are
// reused exactly as they are.
func (s *PMOnboardingService) upsertPendingRow(ctx context.Context, db *gorm.DB, req *models.MerchantRequest, kind string, poolChainID *int64) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	db = db.WithContext(ctx)

	// Try to find an existing row for this user (from a prior attempt).
	err := wherePMKind(db.Where("user_id = ?", req.UserID), kind).First(&pm).Error
	if err == nil {
		if kind == models.PaymentMerchantKindLP && pm.Status == models.PaymentMerchantStatusRejected {
			res := db.Model(&models.PaymentMerchant{}).
				Where("id = ? AND status = ?", pm.ID, models.PaymentMerchantStatusRejected).
				Updates(map[string]interface{}{"status": models.PaymentMerchantStatusPending, "review_note": ""})
			if res.Error != nil {
				return nil, res.Error
			}
			if res.RowsAffected == 1 {
				pm.Status, pm.ReviewNote = models.PaymentMerchantStatusPending, ""
			}
		}
		return &pm, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	perTrade, daily := req.RequestedPerTradeCapUSD, req.RequestedDailyCapUSD
	if kind == models.PaymentMerchantKindLP {
		perTrade, daily = 0, 0
	}

	// No existing row — create one in pending state.
	pm = models.PaymentMerchant{
		ID:                      uuid.New(),
		UserID:                  req.UserID,
		Name:                    req.BusinessName,
		Status:                  models.PaymentMerchantStatusPending,
		Kind:                    kind,
		PoolChainID:             poolChainID,
		ProvisioningStatus:      models.PMProvisioningPending,
		SettlementBankCode:      req.SettlementBankCode,
		SettlementAccountNumber: req.SettlementAccountNumber,
		SettlementAccountName:   req.SettlementAccountName,
		OperatorWalletAddress:   req.MakerWalletAddress,
		PerTradeCapUSD:          perTrade,
		DailyCapUSD:             daily,
	}

	// Use OnConflict DoNothing + Clauses so that a concurrent duplicate insert
	// (unlikely in practice) doesn't fail — we reload below.
	result := db.
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&pm)
	if result.Error != nil {
		return nil, result.Error
	}

	// If another concurrent insert won the race, reload — into a fresh struct:
	// pm's own (never written) id would otherwise join the WHERE and miss. An
	// empty kind-scoped reload means the old one-row-per-user index refused
	// the row: the user holds the other role and the one-off has not run.
	if result.RowsAffected == 0 {
		var winner models.PaymentMerchant
		err := wherePMKind(db.Where("user_id = ?", req.UserID), kind).First(&winner).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSecondRoleNotEnabled
		}
		if err != nil {
			return nil, err
		}
		return &winner, nil
	}

	return &pm, nil
}

// persistFields updates multiple columns on the payment_merchants row.
func (s *PMOnboardingService) persistFields(ctx context.Context, pm *models.PaymentMerchant, fields map[string]interface{}) error {
	return s.db.WithContext(ctx).
		Model(pm).
		Updates(fields).Error
}

// ErrSettlementNameMismatch: the bank's name for the settlement account is not
// the LP's verified legal name (S34).
var ErrSettlementNameMismatch = errors.New("liquidity: the settlement account name does not match your verified name")

// ErrSettlementAccountUnresolved: the bank directory could not resolve the
// settlement account right now.
var ErrSettlementAccountUnresolved = errors.New("liquidity: the settlement account could not be verified with the bank")

// ActivateLP takes an approved LP live (spec §8.2 step 2, plan B P4). No BVN/NIN
// and no virtual account in either inbound mode: LP naira is forwarded from the
// company account. It requires the pool open, a Didit-verified legal name, and
// the settlement account re-resolved with the bank, whose name must match the
// legal name (S34). The bank's name is what is stored.
//
// provisioning_status=active is load-bearing: it closes the merchant-request
// retry path (isPMRetry), which would otherwise re-run ApproveFromRequest and
// overwrite the verified settlement name with the applicant-typed one.
func (s *PMOnboardingService) ActivateLP(ctx context.Context, pmID, userID uuid.UUID) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	if err := s.db.WithContext(ctx).First(&pm, "id = ?", pmID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPMNotFound
		}
		return nil, err
	}
	if pm.UserID != userID {
		return nil, ErrPMNotFound
	}
	if !pm.IsLP() {
		return nil, ErrInvalidPMTransition
	}
	if pm.Status == models.PaymentMerchantStatusActive {
		return &pm, nil
	}
	if pm.Status != models.PaymentMerchantStatusApproved {
		return nil, ErrInvalidPMTransition
	}
	if err := s.RefuseCompanyUserAsLP(ctx, userID); err != nil {
		return nil, err
	}
	chainID, ok := s.LiquidityChainID()
	if !ok || pm.PoolChainID == nil || *pm.PoolChainID != chainID || s.poolGate == nil || !s.poolGate.Open() || s.accounts == nil {
		return nil, ErrLiquidityUnavailable
	}
	verified, legal, err := verifiedIdentity(ctx, s.db, userID)
	if err != nil {
		return nil, fmt.Errorf("pm_onboarding: check verification: %w", err)
	}
	if !verified {
		return nil, ErrKYCVerificationRequired
	}
	if legal == "" {
		return nil, ErrKYCLegalNameMissing
	}
	res, err := s.accounts.Resolve(ctx, pm.SettlementBankCode, pm.SettlementAccountNumber)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrSettlementAccountUnresolved, RedactAlertText(err.Error()))
	}
	bankName := strings.TrimSpace(res.AccountName)
	if bankName == "" {
		return nil, ErrSettlementAccountUnresolved
	}
	if !MatchAccountName(legal, bankName).IsMatch() {
		return nil, ErrSettlementNameMismatch
	}

	// Guarded on the row still being the approved one whose account was just
	// resolved: a concurrent pause, reject or re-approval must not be overwritten
	// by an activation, nor the bank's name stored against another account.
	upd := s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("id = ? AND user_id = ? AND kind = ? AND status = ? AND settlement_bank_code = ? AND settlement_account_number = ?",
			pm.ID, userID, models.PaymentMerchantKindLP, models.PaymentMerchantStatusApproved, pm.SettlementBankCode, pm.SettlementAccountNumber).
		Updates(map[string]interface{}{
			"settlement_account_name": bankName,
			"provisioning_status":     models.PMProvisioningActive,
			"status":                  models.PaymentMerchantStatusActive,
		})
	if upd.Error != nil {
		return nil, fmt.Errorf("pm_onboarding: activate LP: %w", upd.Error)
	}
	if upd.RowsAffected != 1 {
		var current models.PaymentMerchant
		if err := s.db.WithContext(ctx).First(&current, "id = ?", pm.ID).Error; err == nil &&
			current.Status == models.PaymentMerchantStatusActive && current.SettlementAccountName == bankName {
			return &current, nil // a concurrent activation of the same account won
		}
		return nil, ErrInvalidPMTransition
	}
	pm.SettlementAccountName = bankName
	pm.ProvisioningStatus = models.PMProvisioningActive
	pm.Status = models.PaymentMerchantStatusActive
	return &pm, nil
}
