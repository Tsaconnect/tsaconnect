package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// The one emailed-code mechanism: web sign-in on a new browser and every guarded
// merchant action send a 6-digit code to the account's email and wait for it.
// Stored only as a hash bound to the row it unlocks, so a code for one thing can
// never unlock another; the attempt is spent before the code is compared.
const (
	EmailCodeTTL      = 15 * time.Minute
	EmailCodeAttempts = 5
)

// CodeMailer sends a code straight to an address. Never through the
// notification bus: the code must reach a user who muted notifications, and must
// never be stored in their inbox rows.
type CodeMailer interface {
	Send(toEmail, toName, subject, title, message, ctaText, ctaURL string) error
}

// NewEmailCode is six digits from crypto/rand.
func NewEmailCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// EmailCodeHash binds code to subject (the row it unlocks).
func EmailCodeHash(subject uuid.UUID, code string) string {
	sum := sha256.Sum256([]byte(subject.String() + ":" + code))
	return hex.EncodeToString(sum[:])
}

// EmailCodeMatches compares in constant time. code must already be trimmed.
func EmailCodeMatches(subject uuid.UUID, code, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(EmailCodeHash(subject, code)), []byte(hash)) == 1
}

// EmailCodeOutcome is what redeeming a typed code did to the row it unlocks.
type EmailCodeOutcome int

const (
	// EmailCodeClaimed: the code was right and this caller deleted the row, so
	// only this caller may go on.
	EmailCodeClaimed EmailCodeOutcome = iota + 1
	// EmailCodeWrong: the code was wrong; the row stays unless that was the
	// last attempt it had.
	EmailCodeWrong
	// EmailCodeExhausted: no attempt was left (or the row is gone); it is dropped.
	EmailCodeExhausted
	// EmailCodeGone: the code was right but another request claimed the row first.
	EmailCodeGone
)

// RedeemEmailCode is the one redeem sequence for an emailed code stored on row
// id of model's table (model is a pointer to the GORM model, e.g.
// &models.PayOwnerAction{}): spend an attempt in one guarded write, so parallel
// guesses cannot share one; compare; drop the row on the last wrong code; and
// claim a right code by deleting the row, so of two requests racing with it
// only one is Claimed. attemptsSoFar is the row's attempts as read by the
// caller, before this call spent one. Callers check expiry before calling.
func RedeemEmailCode(ctx context.Context, db *gorm.DB, model any, id uuid.UUID, attemptsSoFar int, hash, typed string) (EmailCodeOutcome, error) {
	spend := db.WithContext(ctx).Model(model).
		Where("id = ? AND attempts < ?", id, EmailCodeAttempts).
		Update("attempts", gorm.Expr("attempts + 1"))
	if spend.Error != nil {
		return 0, spend.Error
	}
	if spend.RowsAffected != 1 {
		dropEmailCodeRow(ctx, db, model, id)
		return EmailCodeExhausted, nil
	}
	if !EmailCodeMatches(id, strings.TrimSpace(typed), hash) {
		if attemptsSoFar+1 >= EmailCodeAttempts {
			dropEmailCodeRow(ctx, db, model, id)
		}
		return EmailCodeWrong, nil
	}
	claim := db.WithContext(ctx).Where("id = ?", id).Delete(model)
	if claim.Error != nil {
		return 0, claim.Error
	}
	if claim.RowsAffected != 1 {
		return EmailCodeGone, nil
	}
	return EmailCodeClaimed, nil
}

// dropEmailCodeRow ends a row whose code can no longer be redeemed. Best effort:
// a row left behind still expires, and has no attempts left to spend.
func dropEmailCodeRow(ctx context.Context, db *gorm.DB, model any, id uuid.UUID) {
	if err := db.WithContext(ctx).Where("id = ?", id).Delete(model).Error; err != nil {
		log.Printf("[email-code] could not drop code row %s: %v", id, err)
	}
}

// MaskEmail shows where a code went without printing the address in full.
func MaskEmail(email string) string {
	local, domain, ok := strings.Cut(strings.TrimSpace(email), "@")
	if !ok || local == "" || domain == "" {
		return "your email"
	}
	first, _ := utf8.DecodeRuneInString(local)
	return string(first) + "***@" + domain
}

// UserContact is the address and name an emailed code goes to.
func UserContact(ctx context.Context, db *gorm.DB, userID uuid.UUID) (email, name string, err error) {
	var u struct {
		Email sql.NullString
		Name  sql.NullString
	}
	err = db.WithContext(ctx).Table("users").Select("email", "name").Where("id = ?", userID).Limit(1).Scan(&u).Error
	return strings.TrimSpace(u.Email.String), u.Name.String, err
}
