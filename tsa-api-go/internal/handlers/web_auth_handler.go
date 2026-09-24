package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// The TSA Connect web app's sign-in: the same account as the app, but the
// session lives in HttpOnly cookies, and a browser the account has not trusted
// yet must enter a code emailed to the account. Every route here sits behind
// middleware.WebAuthGuard (CSRF header + the app's own origin).

const (
	webAuthPath      = "/api/auth/web"
	webAccessPath    = "/api"
	webAccessMaxAge  = time.Hour
	webRefreshMaxAge = refreshTokenTTL
	webTrustedMaxAge = 90 * 24 * time.Hour
)

const webCodeUnavailable = "We couldn't send your sign-in code right now. Try again in a few minutes."

// WebLogin handles POST /api/auth/web/login {email, password}.
func (h *Handlers) WebLogin(c *gin.Context) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Email) == "" || req.Password == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Enter your email and password")
		return
	}
	user, ok := h.authenticateCredentials(c, strings.TrimSpace(req.Email), req.Password)
	if !ok {
		return
	}
	if h.trustedBrowser(c, user.ID) {
		h.startWebSession(c, user)
		return
	}
	// The password was right: the lockout counter starts over even though the
	// session still waits on the code.
	clearLoginAttempts(user)
	h.sendSigninCode(c, user)
}

// trustedBrowser reports whether the request carries a live trusted-browser
// cookie for this user, and refreshes its last-seen time. A cookie that no
// longer trusts any account (expired, forgotten, or never real) is cleared; one
// another account on this browser still trusts is left for that account.
func (h *Handlers) trustedBrowser(c *gin.Context, userID uuid.UUID) bool {
	raw, ok := middleware.SingleWebCookie(c, middleware.WebTrustedCookie)
	if !ok {
		return false
	}
	now := time.Now()
	res := config.DB.Model(&models.WebTrustedBrowser{}).
		Where("user_id = ? AND token_hash = ? AND expires_at > ?", userID, hashToken(raw), now).
		Update("last_seen_at", now)
	if res.Error != nil {
		return false
	}
	if res.RowsAffected == 1 {
		return true
	}
	var live int64
	if err := config.DB.Model(&models.WebTrustedBrowser{}).
		Where("token_hash = ? AND expires_at > ?", hashToken(raw), now).Count(&live).Error; err == nil && live == 0 {
		middleware.ClearWebCookie(c, middleware.WebTrustedCookie, webAuthPath)
	}
	return false
}

// sendSigninCode starts a challenge and emails its code. No mailer, or a send
// that fails, is a clear 503 — never a session without the code.
func (h *Handlers) sendSigninCode(c *gin.Context, user *models.User) {
	if h.WebCodeMailer == nil || strings.TrimSpace(user.Email) == "" {
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, webCodeUnavailable, "code_unavailable")
		return
	}
	code, err := services.NewEmailCode()
	if err != nil {
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
		return
	}
	ch := models.WebSigninChallenge{ID: uuid.New(), UserID: user.ID, ExpiresAt: time.Now().Add(services.EmailCodeTTL)}
	ch.CodeHash = services.EmailCodeHash(ch.ID, code)
	if err := replaceChallenge(&ch); err != nil {
		if services.IsUniqueViolation(err) {
			utils.ErrorResponseWithCode(c, http.StatusConflict, "Another sign-in to this account is in progress. Try again.", "signin_busy")
			return
		}
		log.Printf("WebLogin: store challenge for %s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
		return
	}
	message := fmt.Sprintf("Your TSA Connect sign-in code is %s. It expires in %d minutes.\n\n"+
		"Someone is signing in to your account on the web. If it isn't you, don't share this code — change your password.",
		code, int(services.EmailCodeTTL/time.Minute))
	if err := h.WebCodeMailer.Send(user.Email, user.Name, "Your TSA Connect sign-in code", "Your sign-in code", message, "", ""); err != nil {
		log.Printf("WebLogin: send code to user %s: %v", user.ID, err)
		config.DB.Delete(&ch)
		utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, webCodeUnavailable, "code_unavailable")
		return
	}
	middleware.SetWebCookie(c, middleware.WebChallengeCookie, ch.ID.String(), webAuthPath, services.EmailCodeTTL)
	utils.SuccessResponse(c, http.StatusOK, "Check your email", gin.H{"challenge": gin.H{"sentTo": services.MaskEmail(user.Email)}})
}

// replaceChallenge makes ch the user's one challenge, dropping any earlier one
// (expired or not). Two logins racing for the same user can both clear and then
// collide on the one-per-user index; the loser tries once more, taking over.
func replaceChallenge(ch *models.WebSigninChallenge) error {
	replace := func() error {
		return config.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("user_id = ?", ch.UserID).Delete(&models.WebSigninChallenge{}).Error; err != nil {
				return err
			}
			return tx.Create(ch).Error
		})
	}
	err := replace()
	if services.IsUniqueViolation(err) {
		err = replace()
	}
	return err
}

// WebVerify handles POST /api/auth/web/verify {code, rememberBrowser}.
func (h *Handlers) WebVerify(c *gin.Context) {
	var req struct {
		Code            string `json:"code"`
		RememberBrowser bool   `json:"rememberBrowser"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		utils.ErrorResponse(c, http.StatusBadRequest, "Enter the code we emailed you")
		return
	}
	expired := func() {
		middleware.ClearWebCookie(c, middleware.WebChallengeCookie, webAuthPath)
		utils.ErrorResponseWithCode(c, http.StatusGone, "This code has expired. Sign in again to get a new one.", "challenge_expired")
	}
	rawID, ok := middleware.SingleWebCookie(c, middleware.WebChallengeCookie)
	if !ok {
		expired()
		return
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		expired()
		return
	}
	var ch models.WebSigninChallenge
	if err := config.DB.First(&ch, "id = ?", id).Error; err != nil || !time.Now().Before(ch.ExpiresAt) {
		config.DB.Where("id = ?", id).Delete(&models.WebSigninChallenge{})
		expired()
		return
	}
	// Of two verifies racing with the right code, only the Claimed one starts a
	// session.
	outcome, err := services.RedeemEmailCode(c.Request.Context(), config.DB, &models.WebSigninChallenge{}, ch.ID, ch.Attempts, ch.CodeHash, req.Code)
	if err != nil {
		log.Printf("WebVerify: redeem %s: %v", ch.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Request failed")
		return
	}
	switch outcome {
	case services.EmailCodeClaimed:
	case services.EmailCodeWrong:
		utils.ErrorResponseWithCode(c, http.StatusUnprocessableEntity, "That code isn't right", "code_invalid")
		return
	default: // exhausted, or another verify claimed it first
		expired()
		return
	}
	middleware.ClearWebCookie(c, middleware.WebChallengeCookie, webAuthPath)

	var user models.User
	if err := config.DB.First(&user, "id = ?", ch.UserID).Error; err != nil || user.AccountStatus != models.AccountStatusActive {
		utils.ErrorResponseWithCode(c, http.StatusForbidden, "This account can't sign in right now. Contact support.", "account_not_active")
		return
	}
	if req.RememberBrowser {
		h.trustBrowser(c, user.ID)
	}
	h.startWebSession(c, &user)
}

// trustBrowser remembers this browser: a random token in the cookie, only its
// hash stored. Best-effort — failing to remember still signs the user in.
func (h *Handlers) trustBrowser(c *gin.Context, userID uuid.UUID) {
	raw, hashed, err := generateRefreshToken()
	if err != nil {
		log.Printf("WebVerify: trusted-browser token: %v", err)
		return
	}
	tb := models.WebTrustedBrowser{UserID: userID, TokenHash: hashed, ExpiresAt: time.Now().Add(webTrustedMaxAge), LastSeenAt: time.Now()}
	if err := config.DB.Create(&tb).Error; err != nil {
		log.Printf("WebVerify: store trusted browser for %s: %v", userID, err)
		return
	}
	middleware.SetWebCookie(c, middleware.WebTrustedCookie, raw, webAuthPath, webTrustedMaxAge)
	pruneTrustedBrowsers(userID)
}

// maxTrustedBrowsers caps how many browsers one account remembers.
const maxTrustedBrowsers = 10

// pruneTrustedBrowsers keeps the user's newest live trusted browsers, up to
// the cap, and drops the rest along with every expired one.
func pruneTrustedBrowsers(userID uuid.UUID) {
	keep := config.DB.Model(&models.WebTrustedBrowser{}).Select("id").
		Where("user_id = ? AND expires_at > ?", userID, time.Now()).
		Order("created_at DESC").Limit(maxTrustedBrowsers)
	if err := config.DB.Where("user_id = ? AND id NOT IN (?)", userID, keep).Delete(&models.WebTrustedBrowser{}).Error; err != nil {
		log.Printf("WebVerify: prune trusted browsers for %s: %v", userID, err)
	}
}

// startWebSession records the login and sets the session cookies.
func (h *Handlers) startWebSession(c *gin.Context, user *models.User) {
	h.recordLogin(c, user)
	access, refresh, err := h.issueSession(user, webSession)
	if err != nil {
		log.Printf("WebLogin: issue session for %s: %v", user.ID, err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to start your session")
		return
	}
	setWebSessionCookies(c, access, refresh)
	utils.SuccessResponse(c, http.StatusOK, "Signed in", gin.H{"user": webUserView(user)})
}

func setWebSessionCookies(c *gin.Context, access, refresh string) {
	middleware.SetWebCookie(c, middleware.WebAccessCookie, access, webAccessPath, webAccessMaxAge)
	middleware.SetWebCookie(c, middleware.WebRefreshCookie, refresh, webAuthPath, webRefreshMaxAge)
}

// WebRefresh handles POST /api/auth/web/refresh. A failure leaves the cookies
// alone: a request that lost a refresh race to another tab must not sign the
// browser out of the session the winner just stored.
func (h *Handlers) WebRefresh(c *gin.Context) {
	// A duplicated refresh cookie (one planted by a sibling site) reads as none.
	raw, _ := middleware.SingleWebCookie(c, middleware.WebRefreshCookie)
	user, access, refresh, err := h.rotateRefresh(raw, webSession)
	switch {
	case errors.Is(err, errRefreshInvalid), errors.Is(err, errRefreshNoUser), errors.Is(err, errRefreshInactive):
		utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "Please sign in again", "session_expired")
		return
	case err != nil:
		log.Printf("WebRefresh: %v", err)
		utils.ErrorResponse(c, http.StatusInternalServerError, "Failed to refresh your session")
		return
	}
	setWebSessionCookies(c, access, refresh)
	utils.SuccessResponse(c, http.StatusOK, "Refreshed", gin.H{"user": webUserView(user)})
}

// WebLogout handles POST /api/auth/web/logout. The trusted-browser cookie
// stays: signing out is not forgetting the browser. A sign-in still waiting on
// its code is abandoned.
func (h *Handlers) WebLogout(c *gin.Context) {
	// Every refresh cookie sent is revoked: with a planted duplicate, that
	// still ends the real one.
	for _, ck := range c.Request.Cookies() {
		if ck.Name != middleware.WebRefreshCookie || ck.Value == "" {
			continue
		}
		if err := config.DB.Model(&models.RefreshToken{}).Where("token_hash = ?", hashToken(ck.Value)).Update("revoked", true).Error; err != nil {
			log.Printf("WebLogout: revoke refresh token: %v", err)
		}
	}
	middleware.ClearWebCookie(c, middleware.WebAccessCookie, webAccessPath)
	middleware.ClearWebCookie(c, middleware.WebRefreshCookie, webAuthPath)
	middleware.ClearWebCookie(c, middleware.WebChallengeCookie, webAuthPath)
	utils.SuccessResponse(c, http.StatusOK, "Signed out", nil)
}

// WebMe handles GET /api/auth/web/me.
func (h *Handlers) WebMe(c *gin.Context) {
	user := getUserFromContext(c)
	if user == nil {
		utils.ErrorResponse(c, http.StatusUnauthorized, "Please authenticate")
		return
	}
	utils.SuccessResponse(c, http.StatusOK, "Me", gin.H{"user": webUserView(user)})
}

// forgetTrustedBrowsers deletes every remembered browser for the user, so the
// next sign-in from any browser needs an emailed code again. It does not end
// sessions already running; revokeRefreshTokens does that.
func forgetTrustedBrowsers(tx *gorm.DB, userID uuid.UUID) error {
	return tx.Where("user_id = ?", userID).Delete(&models.WebTrustedBrowser{}).Error
}

// revokeRefreshTokens ends every session the user has, app and web: none can be
// refreshed again, so each dies when its short-lived access token expires.
func revokeRefreshTokens(tx *gorm.DB, userID uuid.UUID) error {
	return tx.Model(&models.RefreshToken{}).Where("user_id = ? AND revoked = false", userID).
		Update("revoked", true).Error
}

func webUserView(u *models.User) gin.H {
	return gin.H{"id": u.ID.String(), "name": u.Name, "email": u.Email, "verificationStatus": u.VerificationStatus}
}
