package handlers

import (
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
	"gorm.io/gorm"
)

// WalletRequiredCode is the stable machine code returned when a request cannot
// be processed because no wallet address is known for the caller. Clients
// should key on this rather than matching the human-readable message string.
const WalletRequiredCode = "WALLET_REQUIRED"

// errWalletConflict is a sentinel returned by claimWalletTx when the address
// is already held by another active account.
var errWalletConflict = errors.New("wallet_conflict")

// getUserFromContext extracts the authenticated user from the Gin context.
func getUserFromContext(c *gin.Context) *models.User {
	user, exists := c.Get("user")
	if !exists {
		return nil
	}
	switch v := user.(type) {
	case *models.User:
		return v
	case models.User:
		return &v
	default:
		return nil
	}
}

// callerWallet resolves the wallet address an entitlement endpoint acts on.
//
// `claimed` is the wallet the calling device holds keys for. When it matches
// the account's stored wallet (in any letter case) that wallet is used as-is.
// When it differs, the request would re-point the account at another wallet —
// and the operator spends Spendable MCGP from whatever wallet the account
// points at, with no signature from the user. So a change goes through only
// with proof the caller owns the new wallet: an earlier proof by this account
// (switching back to a wallet it already proved), or a signed challenge in the
// X-Wallet-Proof-Nonce / X-Wallet-Proof-Signature headers. Otherwise 403
// WALLET_PROOF_REQUIRED; the app links wallets through POST /wallet/register.
//
// If `claimed` is omitted, the user's registered wallet_address is used. If
// both are empty, the request is refused with HTTP 400 and machine code
// WALLET_REQUIRED.
func callerWallet(c *gin.Context, user *models.User, claimed string) (string, bool) {
	claimed = strings.TrimSpace(claimed)
	if claimed == "" {
		if user.WalletAddress == "" {
			utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Set up a wallet first", WalletRequiredCode)
			return "", false
		}
		return user.WalletAddress, true
	}
	if !isValidEthAddress(claimed) {
		utils.ErrorResponse(c, http.StatusBadRequest, "Invalid wallet address")
		return "", false
	}
	if strings.EqualFold(claimed, user.WalletAddress) {
		return user.WalletAddress, true
	}
	if config.DB == nil {
		// Nothing to verify a proof against: never re-point the account blind.
		utils.ErrorResponseWithCode(c, http.StatusForbidden, walletProofRequiredMessage, WalletProofRequiredCode)
		return "", false
	}
	if err := claimWalletTx(config.DB, user, claimed, walletProofFromHeaders(c)); err != nil {
		respondWalletLinkError(c, user, err)
		return "", false
	}
	return user.WalletAddress, true
}

// requireOwnWallet is callerWallet for read-only endpoints: it never links. A
// claimed address is accepted only when it IS the stored wallet (any letter
// case); anything else is 403, so a GET cannot re-point an account.
func requireOwnWallet(c *gin.Context, user *models.User, claimed string) (string, bool) {
	claimed = strings.TrimSpace(claimed)
	if user.WalletAddress == "" {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Set up a wallet first", WalletRequiredCode)
		return "", false
	}
	if claimed != "" && !strings.EqualFold(claimed, user.WalletAddress) {
		utils.ErrorResponseWithCode(c, http.StatusForbidden,
			"This wallet isn't the one linked to your account. Switch wallets in the app first.", WalletProofRequiredCode)
		return "", false
	}
	return user.WalletAddress, true
}

// claimWalletTx links `claimed` to the caller inside a single DB transaction:
//  1. Ownership: the account proved this wallet before, or `proof` is a valid
//     signed challenge for it (spent here, so a failed link leaves it unused).
//  2. No OTHER active account that PROVED the address holds it in any letter
//     case (409). Holders that never proved it — links made before proofs
//     existed, squats included — are unlinked, with an AUDIT log line.
//  3. Clear the address from any deleted accounts that still hold it.
//  4. Store it on the caller's row in canonical (checksum) form.
//
// One transaction, so two concurrent claims cannot both pass the check and
// both write. Returns errWalletProofRequired / errWalletProofInvalid,
// errWalletConflict, or a plain error on unexpected DB failure.
func claimWalletTx(db *gorm.DB, user *models.User, claimed string, proof walletProof) error {
	canonical := canonicalWalletAddress(claimed)
	lower := strings.ToLower(canonical)
	var evicted []string
	err := db.Transaction(func(tx *gorm.DB) error {
		evicted = nil // a retried closure starts clean
		proven, err := walletProven(tx, user.ID, canonical)
		if err != nil {
			return err
		}
		if !proven {
			if !proof.present() {
				return errWalletProofRequired
			}
			if err := consumeWalletProof(tx, user.ID, canonical, proof, time.Now()); err != nil {
				return err
			}
		}

		var holders []models.User
		if err := tx.Select("id").Where(
			"LOWER(wallet_address) = ? AND account_status != ? AND id != ?",
			lower, models.AccountStatusDeleted, user.ID,
		).Find(&holders).Error; err != nil {
			return err
		}
		if len(holders) > 0 {
			// A holder that proved the wallet keeps it: one account per wallet.
			// A holder that never did is a link made before proofs existed —
			// possibly a squat on this very wallet — and must not lock out the
			// account that just proved it holds the key.
			for _, h := range holders {
				proven, err := walletProven(tx, h.ID, canonical)
				if err != nil {
					return err
				}
				if proven {
					return errWalletConflict
				}
				evicted = append(evicted, h.ID.String())
			}
			if err := tx.Model(&models.User{}).Where("id IN ?", evicted).
				Update("wallet_address", "").Error; err != nil {
				return err
			}
		}

		if err := tx.Model(&models.User{}).
			Where("LOWER(wallet_address) = ? AND account_status = ?", lower, models.AccountStatusDeleted).
			Update("wallet_address", "").Error; err != nil {
			// Non-fatal: the conflict check above already confirmed no active
			// conflict; the unique index is the backstop.
			log.Printf("[claimWalletTx] stale-cleanup failed for user %s: %v", user.ID, err)
		}

		if canonical != user.WalletAddress {
			if err := tx.Model(&models.User{}).
				Where("id = ?", user.ID).
				Update("wallet_address", canonical).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) || isDuplicateKeyErr(err) {
			// A concurrent claim committed first; the unique index caught it.
			return errWalletConflict
		}
		return err
	}
	if len(evicted) > 0 {
		log.Printf("[wallet-proof] AUDIT wallet_link_evicted=1 owner_user=%s evicted_users=%s reason=unproven_link",
			user.ID, strings.Join(evicted, ","))
	}
	if user.WalletAddress != canonical {
		log.Printf("User %s linked wallet %s (was %q)", user.ID, canonical, user.WalletAddress)
	}
	user.WalletAddress = canonical
	return nil
}

var (
	// errAccountLocked: the account is locked out after repeated wrong passwords.
	errAccountLocked = errors.New("account is temporarily locked")
	// errPasswordIncorrect: the password did not match.
	errPasswordIncorrect = errors.New("incorrect password")
)

// checkPasswordWithLockout is the one password check that counts toward the
// login lockout: a locked account is refused before bcrypt runs, and a wrong
// password is recorded — locking the account (and saying so, when bus is set)
// on the attempt that reaches the limit. Every place a password guards
// something (login, reactivation, a merchant changing where they are paid) goes
// through here, so a stolen session cannot guess a password somewhere the
// lockout does not see.
func checkPasswordWithLockout(db *gorm.DB, bus *events.Bus, user *models.User, password string) error {
	if user.IsLocked() {
		return errAccountLocked
	}
	if err := user.ComparePassword(password); err != nil {
		attempts, lockUntil := user.IncrementLoginAttempts()
		updates := map[string]interface{}{
			"login_attempts": attempts,
			"updated_at":     time.Now(),
		}
		if lockUntil != nil {
			updates["lock_until"] = lockUntil
			if bus != nil {
				bus.Publish(events.Event{
					Type:    events.SecurityAccountLocked,
					UserID:  user.ID,
					Title:   "Account Locked",
					Message: "Your account has been temporarily locked due to multiple failed login attempts",
					Data: map[string]interface{}{
						"attempts": attempts,
					},
				})
			}
		}
		db.Model(user).Updates(updates)
		return errPasswordIncorrect
	}
	return nil
}

// splitCommaList splits a comma-separated query value, trimming each part and
// dropping empty ones. A blank input yields nil.
func splitCommaList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
