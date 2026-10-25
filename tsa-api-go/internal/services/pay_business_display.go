package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// A merchant's business name is what customers see at checkout and what payout
// accounts are matched against. Until the business itself is verified (KYB), the
// owner's legal name is part of it — "Ojay store - Ademu Emmanuel" — so a name
// says who stands behind it, and two stores called "Ojay store" are told apart.
// Names are unique by what a reader sees (case, spacing, punctuation, accents
// and lookalike letters ignored).

// BusinessNameTakenCode is the machine code every route answers a taken
// business name with.
const BusinessNameTakenCode = "business_name_taken"

// ErrPayBusinessNameTaken: another merchant already shows this name.
var ErrPayBusinessNameTaken = errors.New("that business name is already taken")

// payBusinessNameJoiner sits between the owner's name and their legal name.
const payBusinessNameJoiner = " - "

// payLegalNameMaxRunes keeps a composed name inside its column whatever the ID said.
const payLegalNameMaxRunes = 100

// titleCaseName turns the name as printed on an ID ("ADEMU EMMANUEL") into the
// way it is read ("Ademu Emmanuel"): each word, and each part after a hyphen or
// apostrophe, starts with a capital.
func titleCaseName(s string) string {
	var b strings.Builder
	start := true
	for _, r := range strings.Join(strings.Fields(s), " ") {
		switch {
		case start && unicode.IsLetter(r):
			b.WriteRune(unicode.ToUpper(r))
			start = false
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
			start = r == ' ' || r == '-' || r == '\'' || r == '’'
		}
	}
	return b.String()
}

// Didit's scan of a Nigerian NIN slip reads the words printed as field labels as
// the person's name ("Surname N Surname N"), so a legal name made of them is a
// scan error, not a person. "Surname" is only ever a label. The other words
// ("Given", "First", "Last", …) are also real names, so they count only when
// nothing but labels and initials is left.
var (
	surnameLabel = regexp.MustCompile(`^surname[ns]?$`)
	idFieldLabel = regexp.MustCompile(`^(first|firstnames?|given|givennames?|middle|middlenames?|last|lastnames?|names?|none|null|unknown)$`)
)

// looksLikeFieldLabels reports a legal name that is a scan of an ID's field labels.
func looksLikeFieldLabels(legalName string) bool {
	words := strings.Fields(strings.ToLower(legalName))
	if len(words) == 0 {
		return false
	}
	onlyPlaceholders := true
	for _, word := range words {
		word = strings.Trim(word, ".,:;-")
		switch {
		case surnameLabel.MatchString(word):
			return true
		case idFieldLabel.MatchString(word) || utf8.RuneCountInString(word) <= 1: // a label, or an initial
		default:
			onlyPlaceholders = false
		}
	}
	return onlyPlaceholders
}

// legalNameSuffix is the owner's legal name as it is added to a business name,
// or "" when they have none on record, or one that is only a scan of an ID's
// field labels.
func legalNameSuffix(legalName string) string {
	if looksLikeFieldLabels(legalName) {
		return ""
	}
	name := titleCaseName(legalName)
	if utf8.RuneCountInString(name) > payLegalNameMaxRunes {
		name = string([]rune(name)[:payLegalNameMaxRunes])
	}
	return strings.TrimSpace(name)
}

// stripLegalSuffix removes the owner's legal name from the end of a name they
// typed, so pasting the full name back in ("Ojay store - Ademu Emmanuel") does
// not add it twice.
func stripLegalSuffix(name, legalName string) string {
	suffix := legalNameSuffix(legalName)
	if suffix == "" {
		return name
	}
	tail := payBusinessNameJoiner + suffix
	if len(name) > len(tail) && strings.EqualFold(name[len(name)-len(tail):], tail) {
		return strings.TrimSpace(name[:len(name)-len(tail)])
	}
	return name
}

// composeBusinessName is the name a business is shown under: its own name, plus
// the owner's legal name until the business is verified.
func composeBusinessName(base, legalName string, businessVerified bool) string {
	suffix := legalNameSuffix(legalName)
	if businessVerified || suffix == "" {
		return base
	}
	return base + payBusinessNameJoiner + suffix
}

// businessNameKey is the uniqueness key of a shown name.
func businessNameKey(name string) string { return foldBusinessName(name) }

// ownerLegalName is the legal name on the owner's verified identity, "" when
// they are not verified or none was captured.
func ownerLegalName(ctx context.Context, db *gorm.DB, ownerID uuid.UUID) (string, error) {
	verified, legal, err := KYCIdentity(ctx, db, ownerID)
	if err != nil || !verified {
		return "", err
	}
	return legal, nil
}

// ensureBusinessNameFree refuses a name another merchant already shows.
func ensureBusinessNameFree(ctx context.Context, db *gorm.DB, key string, except uuid.UUID) error {
	if key == "" {
		return nil
	}
	var n int64
	if err := db.WithContext(ctx).Model(&models.PayMerchant{}).
		Where("business_name_key = ? AND id <> ?", key, except).Count(&n).Error; err != nil {
		return err
	}
	if n > 0 {
		return ErrPayBusinessNameTaken
	}
	return nil
}

// RecomposeBusinessName brings a merchant's shown name back in line with its
// owner's legal name, after that name changed. It runs in the caller's
// transaction and never fails it: if the new name is taken, or anything else
// is off, the old name stays and an operator is told.
func RecomposeBusinessName(ctx context.Context, tx *gorm.DB, ownerID uuid.UUID, legalName string) {
	var m models.PayMerchant
	if err := tx.WithContext(ctx).Where("user_id = ?", ownerID).First(&m).Error; err != nil || m.BusinessBaseName == "" || m.BusinessVerified {
		return
	}
	next := composeBusinessName(m.BusinessBaseName, legalName, false)
	if next == m.BusinessName {
		return
	}
	if err := ensureBusinessNameFree(ctx, tx, businessNameKey(next), m.ID); err != nil {
		log.Printf("[tsa-pay] ALERT business_name_recompose_skipped=1 merchant=%s: %v", m.ID, err)
		return
	}
	res := tx.WithContext(ctx).Model(&models.PayMerchant{}).Where("id = ? AND version = ?", m.ID, m.Version).
		Updates(map[string]interface{}{"business_name": next, "business_name_key": businessNameKey(next), "version": gorm.Expr("version + 1")})
	if res.Error != nil || res.RowsAffected != 1 {
		log.Printf("[tsa-pay] ALERT business_name_recompose_skipped=1 merchant=%s: %v", m.ID, res.Error)
		return
	}
	if _, err := renameOwnersBusiness(tx.WithContext(ctx), ownerID, next); err != nil {
		log.Printf("[tsa-pay] ALERT business_name_recompose_cascade_failed=1 merchant=%s: %v", m.ID, err)
	}
	log.Printf("[tsa-pay] AUDIT business_name_recomposed=1 merchant=%s from=%q to=%q", m.ID, m.BusinessName, next)
}

// FillBusinessNameKeys gives every merchant the key and base name the
// uniqueness rule reads, from the name it already shows. It is safe to run on
// every boot: it touches only merchants without a key, and a merchant whose
// name another already holds is logged and left without one.
func FillBusinessNameKeys(ctx context.Context, db *gorm.DB) error {
	var rows []models.PayMerchant
	if err := db.WithContext(ctx).Where("business_name_key = '' OR business_name_key IS NULL").Find(&rows).Error; err != nil {
		return err
	}
	for _, m := range rows {
		base := m.BusinessBaseName
		if base == "" {
			base = m.BusinessName
		}
		key := businessNameKey(m.BusinessName)
		if err := ensureBusinessNameFree(ctx, db, key, m.ID); err != nil {
			log.Printf("[tsa-pay] ALERT business_name_duplicate=1 merchant=%s name=%q", m.ID, m.BusinessName)
			continue
		}
		if err := db.WithContext(ctx).Model(&models.PayMerchant{}).Where("id = ?", m.ID).
			Updates(map[string]interface{}{"business_base_name": base, "business_name_key": key}).Error; err != nil {
			return err
		}
	}
	return nil
}

// BusinessNameCompose is one merchant's rename when names are composed.
type BusinessNameCompose struct {
	MerchantID uuid.UUID
	From, To   string
	Skipped    string
}

// ComposeBusinessNames is the one-time step that gives every existing,
// unverified merchant the owner's legal name in their business name. A dry run
// changes nothing and reports what would happen. Merchants whose owner has no
// legal name, or whose composed name is taken, are reported and left as they are.
func ComposeBusinessNames(ctx context.Context, db *gorm.DB, dryRun bool) ([]BusinessNameCompose, error) {
	var merchants []models.PayMerchant
	if err := db.WithContext(ctx).Order("created_at asc").Find(&merchants).Error; err != nil {
		return nil, err
	}
	var out []BusinessNameCompose
	for _, m := range merchants {
		item := BusinessNameCompose{MerchantID: m.ID, From: m.BusinessName}
		base := m.BusinessBaseName
		if base == "" {
			base = m.BusinessName
		}
		// Stray spaces are tidied here, so one never doubles at the joiner.
		base = strings.Join(strings.Fields(base), " ")
		legal, err := ownerLegalName(ctx, db, m.UserID)
		if err != nil {
			return out, err
		}
		base = stripLegalSuffix(base, legal)
		item.To = composeBusinessName(base, legal, m.BusinessVerified)
		switch {
		case item.To == m.BusinessName && m.BusinessBaseName != "":
			item.Skipped = "already composed"
		case legalNameSuffix(legal) == "" && !m.BusinessVerified:
			item.Skipped = "owner has no usable verified legal name"
		default:
			if err := ensureBusinessNameFree(ctx, db, businessNameKey(item.To), m.ID); err != nil {
				item.Skipped = "the composed name is taken"
			}
		}
		out = append(out, item)
		if item.Skipped != "" || dryRun {
			continue
		}
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Model(&models.PayMerchant{}).Where("id = ? AND version = ?", m.ID, m.Version).Updates(map[string]interface{}{
				"business_name": item.To, "business_base_name": base, "business_name_key": businessNameKey(item.To),
				"version": gorm.Expr("version + 1")})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return fmt.Errorf("merchant %s changed while its name was composed", m.ID)
			}
			if _, err := renameOwnersBusiness(tx, m.UserID, item.To); err != nil {
				return err
			}
			return tx.Create(&models.PayAuditEvent{MerchantID: m.ID, ActorID: uuid.Nil, Action: models.PayAuditActionMerchantUpdate,
				Details: fmt.Sprintf(`{"businessName":{"before":%q,"after":%q}}`, item.From, item.To)}).Error
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}
