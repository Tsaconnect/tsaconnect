package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ErrPMNotFound is returned when a PaymentMerchant lookup finds no row.
var ErrPMNotFound = errors.New("payment merchant not found")

// PMService handles lookups, cap checks and seeding for PaymentMerchant rows.
type PMService struct{ db *gorm.DB }

// NewPMService returns a PMService backed by db.
func NewPMService(db *gorm.DB) *PMService { return &PMService{db: db} }

// GetByInboundAccount looks up the active PM by its inbound account number.
// Returns ErrPMNotFound when no row matches.
func (s *PMService) GetByInboundAccount(ctx context.Context, acct string) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).First(&pm, "inbound_account_number = ?", acct).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPMNotFound
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// GetByWalletID looks up the PM by its Nomba walletId (data.merchant.walletId
// on the inbound payment_success event). This is the real inbound key. An empty
// walletID is rejected with ErrPMNotFound up front: an empty value must NEVER
// match an arbitrary PM row (e.g. one whose wallet id is still unconfigured).
// Returns ErrPMNotFound when no row matches.
func (s *PMService) GetByWalletID(ctx context.Context, walletID string) (*models.PaymentMerchant, error) {
	if walletID == "" {
		return nil, ErrPMNotFound
	}
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).First(&pm, "nomba_wallet_id = ?", walletID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPMNotFound
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// GetByAccountRef looks up the PM by its Nomba virtual-account reference
// (NombaAccountRef). This is the VA-model inbound match key: a settled credit
// into a PM's VA is attributed back by accountRef. An empty ref is rejected with
// ErrPMNotFound up front — an empty accountRef must NEVER match an arbitrary PM
// row (mirroring the GetByWalletID empty guard). Returns ErrPMNotFound on miss.
func (s *PMService) GetByAccountRef(ctx context.Context, ref string) (*models.PaymentMerchant, error) {
	if ref == "" {
		return nil, ErrPMNotFound
	}
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).First(&pm, "nomba_account_ref = ?", ref).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPMNotFound
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// GetActiveByUserID looks up the ACTIVE PaymentMerchant whose UserID matches
// the given user. Non-active rows (pending, paused) are not returned.
// Returns ErrPMNotFound when no active PM row exists for the user.
func (s *PMService) GetActiveByUserID(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND status = ?", userID, models.PaymentMerchantStatusActive).
		First(&pm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPMNotFound
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// GetByID looks up a PM by its primary key UUID.
// Returns ErrPMNotFound when no row matches.
func (s *PMService) GetByID(ctx context.Context, id uuid.UUID) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).First(&pm, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPMNotFound
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// WithinPerTradeCap reports whether usd falls within the merchant's per-trade cap.
// A cap of 0 is treated as unlimited (always returns true).
func WithinPerTradeCap(pm *models.PaymentMerchant, usd float64) bool {
	return pm.PerTradeCapUSD == 0 || usd <= pm.PerTradeCapUSD
}

// SumActiveAvailableNGN returns the SUM of available_ngn over all ACTIVE PMs as
// a 2-dp NGN decimal string, plus the count of active PMs summed. Under the
// VA-only model the NGN reconciler compares this aggregate against the single
// parent Nomba balance (Σ ledgers vs parent), rather than per-PM sub-account
// balances. An empty/zero table yields ("0.00", 0). Summation is done in integer
// kobo to avoid float drift; an unparseable available_ngn row is skipped (and
// reflected in a lower count) rather than corrupting the sum.
func (s *PMService) SumActiveAvailableNGN(ctx context.Context) (sumNGN string, count int, err error) {
	var pms []models.PaymentMerchant
	if err := s.db.WithContext(ctx).
		Where("status = ?", models.PaymentMerchantStatusActive).
		Find(&pms).Error; err != nil {
		return "", 0, err
	}
	var totalKobo int64
	for i := range pms {
		k, ok := sumKoboParse(pms[i].AvailableNGN)
		if !ok {
			continue // skip unparseable; do not corrupt the aggregate
		}
		totalKobo += k
		count++
	}
	return koboToNGNString(totalKobo), count, nil
}

// sumKoboParse parses a 2-dp NGN decimal string into integer kobo (naira×100),
// truncating to 2dp. Returns ok=false on empty/non-numeric/negative input. A
// local copy of the matcher's ngnToKobo kept beside its sole caller.
func sumKoboParse(s string) (int64, bool) { return ngnToKobo(s) }

// koboToNGNString renders integer kobo back to a 2-dp NGN decimal string.
func koboToNGNString(kobo int64) string {
	neg := kobo < 0
	if neg {
		kobo = -kobo
	}
	whole := kobo / 100
	frac := kobo % 100
	out := fmt.Sprintf("%d.%02d", whole, frac)
	if neg {
		out = "-" + out
	}
	return out
}

// ListAll returns every PaymentMerchant row ordered by created_at DESC.
// Never returns nil; returns an empty slice when the table is empty.
func (s *PMService) ListAll(ctx context.Context) ([]models.PaymentMerchant, error) {
	var pms []models.PaymentMerchant
	if err := s.db.WithContext(ctx).Order("created_at DESC").Find(&pms).Error; err != nil {
		return nil, err
	}
	return pms, nil
}

// SeedTSA upserts the single TSA PaymentMerchant row identified by
// inboundAccount. If a row with that account number already exists its
// name, status, caps and operator wallet are refreshed; otherwise a new
// active row is inserted. The call is idempotent: two identical calls
// produce exactly one DB row with the same ID.
func (s *PMService) SeedTSA(
	ctx context.Context,
	userID uuid.UUID,
	name, inboundAccount, inboundBank, operatorWallet, nombaWalletID string,
	perTradeCapUSD, dailyCapUSD float64,
) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant

	// Try to find an existing row first.
	err := s.db.WithContext(ctx).
		Where("inbound_account_number = ?", inboundAccount).
		First(&pm).Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		// Insert a new row.
		pm = models.PaymentMerchant{
			UserID:                userID,
			Name:                  name,
			Status:                models.PaymentMerchantStatusActive,
			InboundAccountNumber:  inboundAccount,
			InboundBank:           inboundBank,
			OperatorWalletAddress: operatorWallet,
			NombaWalletID:         nombaWalletID,
			PerTradeCapUSD:        perTradeCapUSD,
			DailyCapUSD:           dailyCapUSD,
		}
		if createErr := s.db.WithContext(ctx).Create(&pm).Error; createErr != nil {
			return nil, createErr
		}
		return &pm, nil
	}
	if err != nil {
		return nil, err
	}

	// Row already exists — refresh mutable fields.
	updates := map[string]any{
		"name":                    name,
		"status":                  models.PaymentMerchantStatusActive,
		"inbound_bank":            inboundBank,
		"operator_wallet_address": operatorWallet,
		"nomba_wallet_id":         nombaWalletID,
		"per_trade_cap_usd":       perTradeCapUSD,
		"daily_cap_usd":           dailyCapUSD,
	}
	if updateErr := s.db.WithContext(ctx).Model(&pm).Updates(updates).Error; updateErr != nil {
		return nil, updateErr
	}
	return &pm, nil
}
