package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// Rate-limit keys for the web app. The web app reaches the API through a proxy
// (Netlify, then Cloudflare), so the API sees a handful of proxy addresses, not
// each visitor's. A limit keyed on the client IP alone would make every web user
// share one budget. These keys count per user, account or session instead; each
// returns "" when it has nothing to go on, and RateLimiterBy then falls back to
// the client IP, so a missing or forged value never escapes the limit.

// WebSessionRateKey keys the API-wide limit: a request carrying a valid web
// access cookie counts against its user. A forged or expired cookie keys by IP.
func WebSessionRateKey(cfg *config.Config) func(*gin.Context) string {
	return func(c *gin.Context) string {
		if !cfg.WebAuthEnabled() {
			return ""
		}
		raw, ok := SingleWebCookie(c, WebAccessCookie)
		if !ok {
			return ""
		}
		claims, userID, ok := parseAccessToken(cfg.JWTSecret, raw)
		if !ok || !isWebToken(claims) {
			return ""
		}
		return "web-user:" + userID.String()
	}
}

// webLoginBodyLimit bounds how much of a sign-in body is read to find the email.
const webLoginBodyLimit = 4 << 10

// WebLoginRateKey counts web sign-in attempts per address and account, so people
// behind one proxy address do not share a budget. The body is put back for the
// handler.
func WebLoginRateKey(c *gin.Context) string {
	head, err := io.ReadAll(io.LimitReader(c.Request.Body, webLoginBodyLimit))
	c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(head), c.Request.Body))
	if err != nil {
		return ""
	}
	var in struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(head, &in) != nil {
		return ""
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" {
		return ""
	}
	return "web-login:" + c.ClientIP() + "|" + email
}

// WebChallengeRateKey counts code entries per pending sign-in.
func WebChallengeRateKey(c *gin.Context) string {
	return cookieRateKey(c, "web-challenge:", WebChallengeCookie)
}

// WebRefreshRateKey counts refreshes per session.
func WebRefreshRateKey(c *gin.Context) string {
	return cookieRateKey(c, "web-refresh:", WebRefreshCookie)
}

// cookieRateKey keys on a hash of the cookie, so raw session tokens never sit in
// the limiter's memory.
func cookieRateKey(c *gin.Context, prefix, cookie string) string {
	raw, ok := SingleWebCookie(c, cookie)
	if !ok {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return prefix + hex.EncodeToString(sum[:])
}
