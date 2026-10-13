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

// wherePMKind narrows q to one kind. Anything but lp reads as merchant, the
// column default — the one kind rule every per-role read shares.
func wherePMKind(q *gorm.DB, kind string) *gorm.DB {
	if kind == models.PaymentMerchantKindLP {
		return q.Where("kind = ?", models.PaymentMerchantKindLP)
	}
	return q.Where("kind <> ?", models.PaymentMerchantKindLP)
}

// GetActiveByUserIDKind is the user's ACTIVE row of one kind.
func (s *PMService) GetActiveByUserIDKind(ctx context.Context, userID uuid.UUID, kind string) (*models.PaymentMerchant, error) {
	q := wherePMKind(s.db.WithContext(ctx).Where("user_id = ? AND status = ?", userID, models.PaymentMerchantStatusActive), kind)
	return firstPM(q)
}

// GetByUserIDKind is the user's row of one kind, any status. A user holds at
// most one row per kind (uq_payment_merchants_user_kind); a merchant read
// never returns the LP row, nor an LP read the merchant row.
func (s *PMService) GetByUserIDKind(ctx context.Context, userID uuid.UUID, kind string) (*models.PaymentMerchant, error) {
	return firstPM(wherePMKind(s.db.WithContext(ctx).Where("user_id = ?", userID), kind).Order("created_at DESC"))
}

func firstPM(q *gorm.DB) (*models.PaymentMerchant, error) {
	var pm models.PaymentMerchant
	err := q.First(&pm).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrPMNotFound
	}
	if err != nil {
		return nil, err
	}
	return &pm, nil
}

// LPIDs are every LP row's id, any status: the ads they own never list (S29).
func (s *PMService) LPIDs(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.db.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("kind = ?", models.PaymentMerchantKindLP).Pluck("id", &ids).Error
	return ids, err
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

// AutoSettleRefusal is why a PM buy trade may not settle without a person:
// Code is a models.InboundHold* value (safe for logs and email, names nobody)
// and Reason is console prose for an operator.
type AutoSettleRefusal struct {
	Code   string
	Reason string
}

// CheckAutoSettle applies the merchant's own limits to a buy trade about to be
// settled automatically: the merchant must be active, the trade must be
// valuable, and it must fit the per-trade and daily caps. A trade that cannot
// be valued is refused — "within the cap" is something to show, not assume.
//
// The one check for both inbound paths. The VA webhook routes a refusal to the
// PM manual queue; central inbound holds the credit. A non-nil error means the
// daily cap could not be evaluated, which callers must treat as "do not
// settle" and may retry.
func (s *PMService) CheckAutoSettle(ctx context.Context, pm *models.PaymentMerchant, trade *models.P2PTrade) (*AutoSettleRefusal, error) {
	if !pm.IsActive() {
		return &AutoSettleRefusal{
			Code: models.InboundHoldMerchantNotActive,
			Reason: fmt.Sprintf("P2P trade %s: merchant %q is %s, so the payment was not settled automatically. Resume the merchant and assign this credit to the trade, or refund the sender",
				trade.ID, pm.Name, pm.Status),
		}, nil
	}
	usd, ok := tradeUSDValue(trade)
	if !ok {
		return &AutoSettleRefusal{
			Code: models.InboundHoldOutsideMerchantCap,
			Reason: fmt.Sprintf("P2P trade %s has an unreadable token amount %q, so it cannot be checked against merchant %q's caps. Check the trade, then assign this credit to it or refund the sender",
				trade.ID, trade.TokenAmount, pm.Name),
		}, nil
	}
	if !WithinPerTradeCap(pm, usd) {
		return &AutoSettleRefusal{
			Code: models.InboundHoldOutsideMerchantCap,
			Reason: fmt.Sprintf("P2P trade %s is $%.2f, over merchant %q's per-trade cap of $%.2f. Assign this credit to the trade to settle it anyway, or refund the sender",
				trade.ID, usd, pm.Name, pm.PerTradeCapUSD),
		}, nil
	}
	within, err := s.WithinDailyCap(ctx, pm, usd)
	if err != nil {
		return nil, fmt.Errorf("daily-cap check for trade %s: %w", trade.ID, err)
	}
	if !within {
		return &AutoSettleRefusal{
			Code: models.InboundHoldOutsideMerchantCap,
			Reason: fmt.Sprintf("P2P trade %s ($%.2f) would take merchant %q over their daily cap of $%.2f. Assign this credit to the trade to settle it anyway, or refund the sender",
				trade.ID, usd, pm.Name, pm.DailyCapUSD),
		}, nil
	}
	return nil, nil
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
		Select("p2p_trades.id, p2p_trades.token, p2p_trades.chain_id, p2p_trades.token_amount").
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

// AutoPauseLP pauses an active LP for a system reason (spec F21): it is never
// allocated again until an admin resumes it; in-flight slices finish. A row
// that is not active is left as it is (nil, nil).
func (s *PMService) AutoPauseLP(ctx context.Context, pmID uuid.UUID, reason string) (*models.PaymentMerchant, error) {
	pm, err := s.transition(ctx, pmID, uuid.Nil, reason, []string{models.PaymentMerchantStatusActive}, models.PaymentMerchantStatusPaused)
	if errors.Is(err, ErrInvalidPMTransition) {
		return nil, nil
	}
	return pm, err
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
	return s.ListByKind(ctx, "")
}

// ListByKind is ListAll narrowed to one kind ("" = every row).
func (s *PMService) ListByKind(ctx context.Context, kind string) ([]models.PaymentMerchant, error) {
	q := s.db.WithContext(ctx).Order("created_at DESC")
	if kind != "" {
		q = wherePMKind(q, kind)
	}
	pms := []models.PaymentMerchant{}
	if err := q.Find(&pms).Error; err != nil {
		return nil, err
	}
	return pms, nil
}
