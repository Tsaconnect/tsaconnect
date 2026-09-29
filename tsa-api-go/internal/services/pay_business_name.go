package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"golang.org/x/text/unicode/norm"
	"gorm.io/gorm"
)

// A TSA Pay owner asking to rename their business. The business name is what
// customers are shown at checkout, on links and receipts, and what a payout
// account may be named — so a rename takes effect only when an admin approves
// it. Until then the owner sees their request as pending and everything else
// keeps the current name. The request itself is a guarded owner action
// (password, then an emailed code), so a stolen session cannot even file one.

var (
	// ErrPayBusinessNameChangeNotFound: no rename by that id.
	ErrPayBusinessNameChangeNotFound = errors.New("business name change not found")
	// ErrPayBusinessNameChangeDecided: already approved, rejected or superseded.
	ErrPayBusinessNameChangeDecided = errors.New("this business name change has already been decided")
	// ErrPayBusinessNameSelfReview: an admin may not decide their own merchant's rename.
	ErrPayBusinessNameSelfReview = errors.New("you can't review a rename of your own business")
)

// Business names an owner may choose: long enough to mean something, short
// enough for a receipt line.
const (
	payBusinessNameMinRunes = 2
	payBusinessNameMaxRunes = 100
)

// DefaultReservedBusinessNames are words no owner's business name may contain:
// the platform's own names and the words an impersonator would lead with.
var DefaultReservedBusinessNames = []string{"TSA Connect", "TSAConnect", "TSA Pay", "TSAPay", "support", "refund"}

// SetReservedBusinessNames replaces the words an owner's business name may not
// contain (compared case-insensitively, ignoring spacing).
func (s *PayMerchantService) SetReservedBusinessNames(words []string) {
	s.reserved = nil
	for _, w := range words {
		if w = strings.TrimSpace(w); w != "" {
			s.reserved = append(s.reserved, w)
		}
	}
}

type payBusinessNamePayload struct {
	Name string `json:"name"`
}

// NormalizeBusinessName is the one rule every business name is held to —
// applied when a TSA Pay application is submitted, approved and provisioned,
// when an owner asks for a rename, and when a super admin renames a merchant.
// It collapses the name's whitespace and refuses what a customer could be
// misled by: hidden or direction-flipping characters (the same rule
// payment-link text follows), a length outside the bounds, and the reserved
// words, however they are spelled (foldBusinessName).
func (s *PayMerchantService) NormalizeBusinessName(name string) (string, error) {
	return s.normalizeBusinessName(name)
}

func (s *PayMerchantService) normalizeBusinessName(name string) (string, error) {
	if payHasHiddenChars(name) {
		return "", fmt.Errorf("%w: a business name can't contain hidden or control characters", ErrPayMerchantInvalid)
	}
	name = strings.Join(strings.Fields(name), " ")
	if n := utf8.RuneCountInString(name); n < payBusinessNameMinRunes || n > payBusinessNameMaxRunes {
		return "", fmt.Errorf("%w: a business name must be %d to %d characters", ErrPayMerchantInvalid, payBusinessNameMinRunes, payBusinessNameMaxRunes)
	}
	reserved := s.reserved
	if reserved == nil {
		reserved = DefaultReservedBusinessNames
	}
	folded := foldBusinessName(name)
	for _, w := range reserved {
		if f := foldBusinessName(w); f != "" && strings.Contains(folded, f) {
			return "", fmt.Errorf("%w: a business name can't contain %q", ErrPayMerchantInvalid, w)
		}
	}
	return name, nil
}

// businessNameLookalikes maps characters that pass for Latin letters — the
// common Cyrillic and Greek homoglyphs, and the digits and symbols used as
// letters — to the letter they pass for. Applied to both sides of the
// reserved-word check, so "i", "l", "1" and "|" all read as one letter.
var businessNameLookalikes = map[rune]rune{
	// Cyrillic
	'а': 'a', 'в': 'b', 'е': 'e', 'ё': 'e', 'к': 'k', 'м': 'm', 'н': 'h', 'о': 'o', 'р': 'p', 'с': 'c',
	'т': 't', 'у': 'y', 'х': 'x', 'ѕ': 's', 'і': 'l', 'ї': 'l', 'ј': 'j', 'ԁ': 'd', 'ԛ': 'q', 'ԝ': 'w',
	'ӏ': 'l', 'ү': 'y', 'һ': 'h', 'п': 'n',
	// Greek
	'α': 'a', 'β': 'b', 'γ': 'y', 'ε': 'e', 'η': 'n', 'ι': 'l', 'κ': 'k', 'ν': 'v', 'ο': 'o', 'ρ': 'p',
	'τ': 't', 'υ': 'u', 'χ': 'x', 'ω': 'w', 'ς': 's', 'σ': 'o',
	// Digits, symbols and letters that read as another
	'0': 'o', '1': 'l', 'i': 'l', '|': 'l', '!': 'l', '3': 'e', '5': 's', '$': 's', '@': 'a', '4': 'a', '7': 't',
}

// foldBusinessName reduces a name to the letters a reader sees, for the
// reserved-word check: compatibility-decomposed (NFKD: full-width and styled
// letters become plain ones, accents come off), lower-cased, lookalikes mapped
// to the Latin letter they imitate, and everything that is not a letter or
// digit dropped — so "T.S.A-Pay", "Ｔｓａ Ｐａｙ", "Tѕа Pay" (Cyrillic) and
// "TSA P@y" all read as "tsapay".
func foldBusinessName(s string) string {
	var b strings.Builder
	for _, r := range norm.NFKD.String(strings.ToLower(s)) {
		r = unicode.ToLower(r)
		if m, ok := businessNameLookalikes[r]; ok {
			r = m
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RequestBusinessNameChange asks to rename the owner's business, as a guarded
// owner action: the request is filed only once the owner enters the code
// emailed to them. Asking for the name the business already has files nothing
// and withdraws any request still waiting.
func (s *PayMerchantService) RequestBusinessNameChange(ctx context.Context, ownerID uuid.UUID, name string, expectedVersion *int) (*PaySettlementResult, error) {
	m, err := s.activeOwnedMerchant(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	if expectedVersion != nil && *expectedVersion != m.Version {
		return nil, ErrPayMerchantStale
	}
	name, err = s.normalizeBusinessName(name)
	if err != nil {
		return nil, err
	}
	if name == m.BusinessName {
		if err := supersedePendingBusinessNames(s.db.WithContext(ctx), m.ID, "withdrawn: the owner asked for the current name back"); err != nil {
			return nil, err
		}
		return &PaySettlementResult{Merchant: m}, nil
	}
	pending, err := s.requestOwnerAction(ctx, ownerID, m, models.PayOwnerActionBusinessName, payBusinessNamePayload{Name: name},
		fmt.Sprintf("ask for your business to be renamed %q", name))
	if err != nil {
		return nil, err
	}
	return &PaySettlementResult{Merchant: m, Pending: pending}, nil
}

// confirmBusinessNameByOwner files a confirmed rename request, with the owner's
// verified legal name as it stands now, superseding any request still waiting.
// The merchant itself is untouched.
func (s *PayMerchantService) confirmBusinessNameByOwner(ctx context.Context, ownerID uuid.UUID, m *models.PayMerchant, a *models.PayOwnerAction) (*models.PayMerchant, error) {
	var p payBusinessNamePayload
	if err := json.Unmarshal([]byte(a.Payload), &p); err != nil {
		return nil, err
	}
	name, err := s.normalizeBusinessName(p.Name)
	if err != nil {
		return nil, err
	}
	verified, legal, err := verifiedIdentity(ctx, s.db, ownerID)
	if err != nil {
		return nil, err
	}
	if !verified {
		legal = ""
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := supersedePendingBusinessNames(tx, m.ID, "superseded by a later rename request"); err != nil {
			return err
		}
		return tx.Create(&models.PayMerchantNameChange{MerchantID: m.ID, OldName: m.BusinessName, NewName: name,
			OwnerVerifiedName: legal, Status: models.PayNameChangePending, RequestedBy: ownerID}).Error
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// supersedePendingBusinessNames closes every rename still waiting for review.
func supersedePendingBusinessNames(tx *gorm.DB, merchantID uuid.UUID, note string) error {
	return tx.Model(&models.PayMerchantNameChange{}).
		Where("merchant_id = ? AND status = ?", merchantID, models.PayNameChangePending).
		Updates(map[string]interface{}{"status": models.PayNameChangeRejected, "note": note}).Error
}

// PendingBusinessName is a merchant's rename waiting for review.
type PendingBusinessName struct {
	Name  string
	Since time.Time
}

// PendingBusinessNames maps each merchant with a rename waiting for review to it.
func (s *PayMerchantService) PendingBusinessNames(ctx context.Context, merchantIDs []uuid.UUID) (map[uuid.UUID]PendingBusinessName, error) {
	out := map[uuid.UUID]PendingBusinessName{}
	if len(merchantIDs) == 0 {
		return out, nil
	}
	var rows []models.PayMerchantNameChange
	if err := s.db.WithContext(ctx).Where("merchant_id IN ? AND status = ?", merchantIDs, models.PayNameChangePending).
		Order("created_at asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.MerchantID] = PendingBusinessName{Name: r.NewName, Since: r.CreatedAt} // newest wins; only one should be pending
	}
	return out, nil
}

// PendingBusinessName is the rename of one merchant waiting for review, if any.
func (s *PayMerchantService) PendingBusinessName(ctx context.Context, merchantID uuid.UUID) (*PendingBusinessName, error) {
	names, err := s.PendingBusinessNames(ctx, []uuid.UUID{merchantID})
	if err != nil {
		return nil, err
	}
	if p, ok := names[merchantID]; ok {
		return &p, nil
	}
	return nil, nil
}

// ListBusinessNameChanges is the admin review queue, newest first, optionally
// narrowed to one status.
func (s *PayMerchantService) ListBusinessNameChanges(ctx context.Context, status string, page, limit int) ([]models.PayMerchantNameChange, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	q := s.db.WithContext(ctx).Model(&models.PayMerchantNameChange{})
	if status = strings.TrimSpace(status); status != "" {
		q = q.Where("status = ?", status)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows := []models.PayMerchantNameChange{}
	if err := q.Order("created_at desc").Offset((page - 1) * limit).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ReviewBusinessNameChange approves or rejects a pending rename. Approval
// renames the merchant — through the same compare-and-set and audit row as any
// merchant edit, in the same transaction as the decision — and only while the
// business still has the name the request was made from.
func (s *PayMerchantService) ReviewBusinessNameChange(ctx context.Context, adminID, changeID uuid.UUID, approve bool, note string) (*models.PayMerchantNameChange, *models.PayMerchant, error) {
	var change models.PayMerchantNameChange
	if err := s.db.WithContext(ctx).First(&change, "id = ?", changeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrPayBusinessNameChangeNotFound
		}
		return nil, nil, err
	}
	m, err := s.Get(ctx, change.MerchantID)
	if err != nil {
		return nil, nil, err
	}
	if m.UserID == adminID {
		return nil, nil, ErrPayBusinessNameSelfReview
	}
	now := s.now()
	status := models.PayNameChangeRejected
	if approve {
		status = models.PayNameChangeApproved
	}
	note = truncateRunes(strings.TrimSpace(note), 512)
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.PayMerchantNameChange{}).Where("id = ? AND status = ?", change.ID, models.PayNameChangePending).
			Updates(map[string]interface{}{"status": status, "reviewed_by": adminID, "reviewed_at": &now, "note": note})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrPayBusinessNameChangeDecided
		}
		if approve {
			if m.BusinessName != change.OldName {
				return ErrPayBusinessNameChangeDecided
			}
			won, err := s.casUpdate(tx, m.ID, m.Version, map[string]interface{}{"business_name": change.NewName})
			if err != nil {
				return err
			}
			if !won {
				return ErrPayMerchantStale
			}
			after := *m
			after.BusinessName = change.NewName
			if err := s.writeAudit(tx, m.ID, adminID, models.PayAuditActionMerchantUpdate, payAuditChanges(m, &after)); err != nil {
				return err
			}
		}
		return s.writeAudit(tx, change.MerchantID, adminID, models.PayAuditActionBusinessNameReview, map[string]any{
			"changeId": change.ID, "decision": status, "oldName": change.OldName, "newName": change.NewName, "note": note})
	})
	if err != nil {
		return nil, nil, err
	}
	if err := s.db.WithContext(ctx).First(&change, "id = ?", change.ID).Error; err != nil {
		return nil, nil, err
	}
	m, err = s.Get(ctx, change.MerchantID)
	if err != nil {
		return nil, nil, err
	}
	return &change, m, nil
}
