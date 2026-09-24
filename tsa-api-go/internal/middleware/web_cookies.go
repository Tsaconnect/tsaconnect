package middleware

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

// The web app's session lives in cookies only (no web storage): the access
// token, the refresh token, the trusted-browser token and a pending sign-in
// challenge. All HttpOnly, Secure, SameSite=Strict and host-only; the web app is
// served on the same origin as the API.
const (
	WebAccessCookie    = "tsa_at"
	WebRefreshCookie   = "tsa_rt"
	WebTrustedCookie   = "tsa_tb"
	WebChallengeCookie = "tsa_ch"
	// WebCSRFHeader must accompany every cookie-authenticated write: another
	// site cannot set it, so it cannot make the browser act for the user.
	WebCSRFHeader = "X-TSA-Web"
)

// SetWebCookie sets one session cookie.
func SetWebCookie(c *gin.Context, name, value, path string, maxAge time.Duration) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: int(maxAge / time.Second),
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// ClearWebCookie removes one session cookie.
func ClearWebCookie(c *gin.Context, name, path string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: "", Path: path, MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// webSessionKey marks a request Auth authenticated as a web session: with the
// web cookie, or with a token carrying WebTokenClaim.
const webSessionKey = "webSession"

// IsWebSession reports whether the request was authenticated as a web session
// (the cookie, or a web-minted token sent as a bearer). A web session never carries staff powers (spec §4): every
// admin role check made inside a handler must also refuse it.
func IsWebSession(c *gin.Context) bool {
	v, ok := c.Get(webSessionKey)
	web, _ := v.(bool)
	return ok && web
}

// SingleWebCookie returns the one non-empty cookie called name. More than one
// means a sibling or apex site planted another with a Domain attribute; that
// request is treated as carrying none.
func SingleWebCookie(c *gin.Context, name string) (string, bool) {
	value, n := "", 0
	for _, ck := range c.Request.Cookies() {
		if ck.Name == name {
			value = ck.Value
			n++
		}
	}
	if n != 1 || value == "" {
		return "", false
	}
	return value, true
}

// WebRequestFromApp reports whether a cookie-carrying request came from the web
// app's own pages. Sibling sites (admin, landing) are same-SITE, so the cookie
// reaches them; this is what keeps them from using it.
func WebRequestFromApp(c *gin.Context, appOrigin string) bool {
	if site := c.GetHeader("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	if origin := c.GetHeader("Origin"); origin != "" && origin != appOrigin {
		return false
	}
	return true
}

// WebAuthGuard fronts every /api/auth/web route: each one reads or sets the
// session cookies, so each needs the CSRF header and the app's own origin.
func WebAuthGuard(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetHeader(WebCSRFHeader) != "1" {
			abortCSRF(c)
			return
		}
		if !WebRequestFromApp(c, cfg.WebAppOrigin) {
			abortOrigin(c)
			return
		}
		c.Next()
	}
}

func abortCSRF(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "Missing request header", "code": "csrf"})
}

func abortOrigin(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"success": false, "message": "Request refused", "code": "origin"})
}
