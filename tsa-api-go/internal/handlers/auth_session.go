package handlers

import (
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// The session logic the app's login/refresh and the web's cookie sign-in share:
// one credential check, one login record, one session issue and one refresh
// rotation, so the two front doors cannot drift apart.

const refreshTokenTTL = 30 * 24 * time.Hour

// sessionKind is which front door a session belongs to. A web session's access
// token carries middleware.WebTokenClaim, and its refresh token rotates only on
// the web, so a web session never becomes an app session.
type sessionKind bool

const (
	appSession sessionKind = false
	webSession sessionKind = true
)

var (
	errRefreshInvalid  = errors.New("invalid or expired refresh token")
	errRefreshNoUser   = errors.New("refresh token user not found")
	errRefreshInactive = errors.New("account is not active")
)

// authenticateCredentials finds the user by email or username, checks the
// password against the login lockout, and refuses accounts that must not get a
// session. false = the refusal is already written.
func (h *Handlers) authenticateCredentials(c *gin.Context, identifier, password string) (*models.User, bool) {
	// Look up by email if the identifier looks like an email, otherwise by username.
	// Falls through to the opposite lookup so both formats work even if misclassified.
	var user models.User
	var err error
	lowered := strings.ToLower(identifier)
	if isValidEmail(identifier) {
		err = config.DB.Where("email = ?", lowered).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = config.DB.Where("LOWER(username) = ?", lowered).First(&user).Error
		}
	} else {
		err = config.DB.Where("LOWER(username) = ?", lowered).First(&user).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			err = config.DB.Where("email = ?", lowered).First(&user).Error
		}
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = bcrypt.CompareHashAndPassword([]byte(dummyBcryptHash), []byte(password))
			utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "Invalid credentials", "invalid_credentials")
			return nil, false
		}
		log.Printf("Login DB error: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Internal server error")
		return nil, false
	}

	// A locked account is refused before the password is checked; a wrong
	// password counts toward the lockout.
	switch err := checkPasswordWithLockout(config.DB, h.EventBus, &user, password); {
	case errors.Is(err, errAccountLocked):
		utils.ErrorResponseWithCode(c, http.StatusLocked, "Account is temporarily locked. Try again later.", "account_locked")
		return nil, false
	case err != nil:
		utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "Invalid credentials", "invalid_credentials")
		return nil, false
	}

	// Check account status — deleted or suspended accounts must not receive session tokens
	if user.AccountStatus == models.AccountStatusDeleted {
		// Check if there is a pending deletion request within the grace window
		var delReq models.AccountDeletionRequest
		if config.DB != nil {
			if err := config.DB.Where("user_id = ? AND status = ? AND purge_after > ?",
				user.ID, models.AccountDeletionStatusPending, time.Now()).
				Order("created_at DESC").First(&delReq).Error; err == nil {
				daysLeft := int(math.Ceil(time.Until(delReq.PurgeAfter).Hours() / 24))
				if daysLeft < 1 {
					daysLeft = 1
				}
				c.JSON(http.StatusForbidden, gin.H{
					"success": false,
					"message": fmt.Sprintf("Your account is scheduled for deletion on %s (%d days left). You can restore your account to continue.",
						delReq.PurgeAfter.Format("2 January 2006"), daysLeft),
					"code": "ACCOUNT_DELETED_GRACE_PERIOD",
					"data": gin.H{
						"canRestore":    true,
						"purgeAfter":    delReq.PurgeAfter,
						"graceDaysLeft": daysLeft,
					},
				})
				return nil, false
			}
		}

		utils.ErrorResponseWithCode(c, http.StatusForbidden,
			"This account has been deleted. Please contact support if you wish to restore it.", "ACCOUNT_DELETED")
		return nil, false
	}
	if user.AccountStatus != models.AccountStatusActive {
		utils.ErrorResponseWithCode(c, http.StatusForbidden,
			fmt.Sprintf("Account is %s. Please contact support.", user.AccountStatus), "ACCOUNT_NOT_ACTIVE")
		return nil, false
	}
	return &user, true
}

// clearLoginAttempts resets the lockout counters after a right password whose
// session still waits on something else (the web's emailed code).
func clearLoginAttempts(user *models.User) {
	config.DB.Model(user).Updates(map[string]interface{}{
		"login_attempts": 0,
		"lock_until":     nil,
		"updated_at":     time.Now(),
	})
}

// recordLogin clears the lockout counters, stamps the login and tells the user
// about it.
func (h *Handlers) recordLogin(c *gin.Context, user *models.User) {
	now := time.Now()
	config.DB.Model(user).Updates(map[string]interface{}{
		"login_attempts": 0,
		"last_login":     now,
		"updated_at":     now,
		"lock_until":     nil,
	})

	if h.EventBus != nil {
		h.EventBus.Publish(events.Event{
			Type:    events.SecurityLoginNewDevice,
			UserID:  user.ID,
			Title:   "New Login",
			Message: "A new login was detected on your account",
			Data: map[string]interface{}{
				"ip": c.ClientIP(),
			},
		})
	}
}

// issueSession mints an access token and a new refresh token for user.
func (h *Handlers) issueSession(user *models.User, kind sessionKind) (access, refresh string, err error) {
	return h.issueSessionIn(config.DB, user, kind)
}

// issueSessionIn is issueSession inside db, so rotation can store the new
// refresh token in the same transaction that revokes the old one.
func (h *Handlers) issueSessionIn(db *gorm.DB, user *models.User, kind sessionKind) (access, refresh string, err error) {
	access, err = generateAccessToken(user.ID, user.Email, h.Config.JWTSecret, kind)
	if err != nil {
		return "", "", fmt.Errorf("generate access token: %w", err)
	}
	refresh, hashedRefresh, err := generateRefreshToken()
	if err != nil {
		return "", "", fmt.Errorf("generate refresh token: %w", err)
	}
	rt := models.RefreshToken{
		UserID:    user.ID,
		TokenHash: hashedRefresh,
		ExpiresAt: time.Now().Add(refreshTokenTTL),
		Web:       kind == webSession,
	}
	if err := db.Create(&rt).Error; err != nil {
		return "", "", fmt.Errorf("store refresh token: %w", err)
	}
	return access, refresh, nil
}

// rotateRefresh trades a live refresh token of kind for a new session of the
// same kind, revoking the old token. A token of the other kind is invalid here.
// The revoke is conditional, so two requests racing with the same token cannot
// both rotate it: the loser gets errRefreshInvalid.
func (h *Handlers) rotateRefresh(raw string, kind sessionKind) (user *models.User, access, refresh string, err error) {
	var stored models.RefreshToken
	if err := config.DB.Where("token_hash = ? AND revoked = false AND expires_at > ? AND web = ?", hashToken(raw), time.Now(), kind == webSession).First(&stored).Error; err != nil {
		return nil, "", "", errRefreshInvalid
	}

	var u models.User
	if err := config.DB.First(&u, "id = ?", stored.UserID).Error; err != nil {
		return nil, "", "", errRefreshNoUser
	}
	if u.AccountStatus != models.AccountStatusActive {
		return nil, "", "", errRefreshInactive
	}

	err = config.DB.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&models.RefreshToken{}).Where("id = ? AND revoked = ?", stored.ID, false).Update("revoked", true)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return errRefreshInvalid
		}
		var ierr error
		access, refresh, ierr = h.issueSessionIn(tx, &u, kind)
		return ierr
	})
	if err != nil {
		return nil, "", "", err
	}
	return &u, access, refresh, nil
}
