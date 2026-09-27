package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
)

// Rate-limit keys for the web app's sign-in routes: per account, pending sign-in
// or session, used under a per-address ceiling. Each returns "" when it has
// nothing to go on, and RateLimiterBy then falls back to the client IP, so a
// missing value never escapes the limit.

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
	return "web-login:" + ClientKey(c) + "|" + email
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
