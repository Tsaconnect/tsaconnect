package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ErrPMNotFound is returned when a PaymentMerchant lookup finds no row.
var ErrPMNotFound = errors.New("payment merchant not found")

// ErrInvalidPMTransition is returned when a lifecycle action (reject / pause /
// resume) is attempted from a status that does not allow it.
var ErrInvalidPMTransition = errors.New("invalid payment merchant status transition")

// ErrReviewNoteRequired is returned when a reject is attempted without a note.
var ErrReviewNoteRequired = errors.New("a note is required to reject a payment merchant")

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

// GetByUserID looks up the most recent PaymentMerchant for a user regardless of
// status (pending/approved/active/paused/rejected). Used by the status read so
// the app can distinguish "approved, needs activation" from "active".
// Returns ErrPMNotFound when the user has no PM row.
func (s *PMService) GetByUserID(ctx context.Context, userID uuid.UUID) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
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

// WithinDailyCap reports whether releasing prospectiveUSD for pm would keep the
// PM's total released/committed USD for "today" (>= the current UTC midnight)
// within pm.DailyCapUSD. A cap of 0 is unlimited (always true).
//
// "Today" is computed from time.Now().UTC() truncated to midnight (the service
// holds no injected clock). The consumed total sums tradeUSDValue over this PM's
// trades created since midnight that have committed/consumed capacity — statuses
// IN ('paid','releasing','released') on this PM's ads. Rows with an unparseable
// token_amount (tradeUSDValue ok=false) are skipped, mirroring the per-trade
// guard. Returns (sum+prospectiveUSD) <= cap. A query error is surfaced so the
// caller can fail safe (never auto-release on a cap it can't evaluate).
func (s *PMService) WithinDailyCap(ctx context.Context, pm *models.PaymentMerchant, prospectiveUSD float64) (bool, error) {
	if pm.DailyCapUSD == 0 {
		return true, nil // unlimited
	}
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var trades []models.P2PTrade
	if err := s.db.WithContext(ctx).
		Table("p2p_trades").
		Select("p2p_trades.*").
		Joins("JOIN p2p_ads ON p2p_ads.id = p2p_trades.ad_id").
		Where("p2p_ads.pm_id = ?", pm.ID).
		Where("p2p_trades.status IN ?", []string{
			models.P2PTradeStatusPaid,
			models.P2PTradeStatusReleasing,
			models.P2PTradeStatusReleased,
		}).
		Where("p2p_trades.created_at >= ?", midnight).
		Scan(&trades).Error; err != nil {
		return false, err
	}

	var sum float64
	for i := range trades {
		usd, ok := tradeUSDValue(&trades[i])
		if !ok {
			// Unparseable token_amount — can't value this row; skip it (the same
			// row is independently blocked from auto-release by the per-trade guard).
			log.Printf("[pm] daily-cap: skipping trade=%s with unparseable token_amount %q", trades[i].ID, trades[i].TokenAmount)
			continue
		}
		sum += usd
	}
	return sum+prospectiveUSD <= pm.DailyCapUSD, nil
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

// RejectPM declines a PENDING payment merchant. A non-empty note is required
// (recorded for audit). The row is kept (status='rejected') rather than deleted
// so the decision is auditable. Returns ErrInvalidPMTransition unless the PM is
// currently pending, and ErrReviewNoteRequired when note is blank.
func (s *PMService) RejectPM(ctx context.Context, pmID, adminID uuid.UUID, note string) (*models.PaymentMerchant, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, ErrReviewNoteRequired
	}
	return s.transition(ctx, pmID, adminID, note,
		[]string{models.PaymentMerchantStatusPending},
		models.PaymentMerchantStatusRejected)
}

// PausePM flips an ACTIVE payment merchant to 'paused' (kill switch — no new
// trades). Returns ErrInvalidPMTransition unless the PM is currently active.
func (s *PMService) PausePM(ctx context.Context, pmID, adminID uuid.UUID) (*models.PaymentMerchant, error) {
	return s.transition(ctx, pmID, adminID, "",
		[]string{models.PaymentMerchantStatusActive},
		models.PaymentMerchantStatusPaused)
}

// ResumePM flips a PAUSED payment merchant back to 'active'. Returns
// ErrInvalidPMTransition unless the PM is currently paused.
func (s *PMService) ResumePM(ctx context.Context, pmID, adminID uuid.UUID) (*models.PaymentMerchant, error) {
	return s.transition(ctx, pmID, adminID, "",
		[]string{models.PaymentMerchantStatusPaused},
		models.PaymentMerchantStatusActive)
}

// transition loads the PM, verifies its current status is in `from`, and flips
// it to `to` while recording the reviewer (and optional note). The status guard
// and write run in one transaction so two concurrent admins can't both win.
func (s *PMService) transition(ctx context.Context, pmID, adminID uuid.UUID, note string, from []string, to string) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&pm, "id = ?", pmID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPMNotFound
			}
			return err
		}
		if !contains(from, pm.Status) {
			return ErrInvalidPMTransition
		}
		now := time.Now()
		fields := map[string]interface{}{
			"status":      to,
			"reviewed_by": adminID,
			"reviewed_at": now,
		}
		if note != "" {
			fields["review_note"] = note
		}
		if err := tx.Model(&pm).Updates(fields).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// contains reports whether s is present in xs.
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
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
