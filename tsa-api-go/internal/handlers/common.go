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
// In an active-wallet / decentralised model, `claimed` is the wallet address the
// calling device currently holds active keys for. If provided and valid:
//   - The conflict check and wallet-address update run inside a single DB
//     transaction, so two concurrent requests claiming the same address cannot
//     both pass the SELECT and both proceed to UPDATE (check-then-act race).
//   - Stale references on deleted accounts are cleared inside the same TX.
//   - It is returned as the active wallet for this request.
//
// If `claimed` is omitted, the user's previously registered wallet_address is
// used as a fallback. If both are empty, the request is refused with HTTP 400
// and machine code WALLET_REQUIRED.
func callerWallet(c *gin.Context, user *models.User, claimed string) (string, bool) {
	claimed = strings.TrimSpace(claimed)
	if claimed != "" {
		if !isValidEthAddress(claimed) {
			utils.ErrorResponse(c, http.StatusBadRequest, "Invalid wallet address")
			return "", false
		}

		if config.DB != nil {
			// claimWalletTx performs the conflict check, stale-cleanup, and
			// wallet-address sync in one ACID transaction, eliminating the
			// check-then-act race that would exist if these were separate queries.
			err := claimWalletTx(config.DB, user, claimed)
			if err == errWalletConflict {
				utils.ErrorResponse(c, http.StatusConflict, "Wallet address already in use by another account")
				return "", false
			}
			if err != nil {
				log.Printf("[callerWallet] unexpected DB error for user %s: %v", user.ID, err)
				utils.ErrorResponse(c, http.StatusInternalServerError, "Could not validate wallet address")
				return "", false
			}
		} else {
			// No DB wired (lightweight test stubs): accept address as-is.
			user.WalletAddress = claimed
		}
		return claimed, true
	}

	// Fallback: use the address the user previously registered.
	if user.WalletAddress == "" {
		utils.ErrorResponseWithCode(c, http.StatusBadRequest, "Set up a wallet first", WalletRequiredCode)
		return "", false
	}
	return user.WalletAddress, true
}

// claimWalletTx runs the wallet-claim logic inside a single DB transaction:
//  1. Check that no OTHER active account already holds `claimed`.
//  2. Clear the address from any deleted accounts that still hold it.
//  3. Sync the address onto the caller's users row (if different).
//
// Returns errWalletConflict when another active user owns the address, or a
// plain error on unexpected DB failure.
func claimWalletTx(db *gorm.DB, user *models.User, claimed string) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// 1. Conflict check — reject if another ACTIVE account owns this address.
		var otherUser models.User
		if err := tx.Where(
			"wallet_address = ? AND account_status != ? AND id != ?",
			claimed, models.AccountStatusDeleted, user.ID,
		).First(&otherUser).Error; err == nil {
			// Found a conflicting active user.
			return errWalletConflict
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			// Unexpected query error — surface it.
			return err
		}

		// 2. Release stale references on deleted accounts.
		if err := tx.Model(&models.User{}).
			Where("wallet_address = ? AND account_status = ?", claimed, models.AccountStatusDeleted).
			Update("wallet_address", "").Error; err != nil {
			log.Printf("[claimWalletTx] stale-cleanup failed for %s: %v", claimed, err)
			// Non-fatal: proceed anyway; the conflict check above already
			// confirmed no active conflict.
		}

		// 3. Sync the caller's registered wallet if it differs.
		if !strings.EqualFold(claimed, user.WalletAddress) {
			if err := tx.Model(&models.User{}).
				Where("id = ?", user.ID).
				Update("wallet_address", claimed).Error; err != nil {
				return err
			}
			user.WalletAddress = claimed
		}
		return nil
	})
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
