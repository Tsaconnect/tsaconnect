package services

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

const PayWebhookSignatureHeader = "X-TSA-Signature"

// GeneratePayKey generates a new API key for the given mode ("live" or "test").
// Returns the raw key, a 6-character prefix for display, the SHA-256 hash for storage, or an error.
func GeneratePayKey(mode string) (raw, prefix, hash string, err error) {
	if mode != "live" && mode != "test" {
		return "", "", "", fmt.Errorf("invalid mode: %s", mode)
	}

	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", "", "", err
	}

	encodedRandom := base64.RawURLEncoding.EncodeToString(randomBytes)
	raw = fmt.Sprintf("tsapk_%s_%s", mode, encodedRandom)

	prefixLen := len("tsapk_"+mode+"_") + 6
	prefix = raw[:prefixLen]

	hash = HashPayKey(raw)

	return raw, prefix, hash, nil
}

// HashPayKey returns the lowercase hex SHA-256 hash of a pay key.
func HashPayKey(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// PayKeyMode returns the mode ("live" or "test") if the raw key is valid, and false otherwise.
// A valid key starts with "tsapk_live_" or "tsapk_test_" and has a non-empty body.
func PayKeyMode(raw string) (string, bool) {
	if strings.HasPrefix(raw, "tsapk_live_") {
		body := raw[len("tsapk_live_"):]
		if body != "" {
			return "live", true
		}
	} else if strings.HasPrefix(raw, "tsapk_test_") {
		body := raw[len("tsapk_test_"):]
		if body != "" {
			return "test", true
		}
	}
	return "", false
}

// DerivePayWebhookSecret derives a per-merchant webhook signing secret from the master key.
// The secret is "whsec_" + hex(HMAC-SHA256(master, "<merchantID>:<mode>:<version>"))
func DerivePayWebhookSecret(master []byte, merchantID uuid.UUID, mode string, version int) string {
	msg := merchantID.String() + ":" + mode + ":" + strconv.Itoa(version)
	h := hmac.New(sha256.New, master)
	h.Write([]byte(msg))
	return "whsec_" + hex.EncodeToString(h.Sum(nil))
}

// SignPayWebhook signs a webhook event with the given secret and timestamp.
// Returns a signature string in the format: t=<timestamp>,v1=<hex HMAC-SHA256(secret, "<ts>.<body>")>
func SignPayWebhook(secret string, ts int64, body []byte) string {
	msg := strconv.FormatInt(ts, 10) + "." + string(body)
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	return fmt.Sprintf("t=%d,v1=%s", ts, hex.EncodeToString(h.Sum(nil)))
}
