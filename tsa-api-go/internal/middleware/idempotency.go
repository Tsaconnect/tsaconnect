package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// idempotencyKeyHeader is the canonical client header name. Stripe convention.
const idempotencyKeyHeader = "Idempotency-Key"
const maxIdempotencyKeyLen = 200

// idempotencyWriter captures the response body so the middleware can persist
// it after the handler completes. We wrap (not replace) gin's ResponseWriter
// so all downstream Header / Status / Flush behavior is preserved.
type idempotencyWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *idempotencyWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *idempotencyWriter) WriteString(s string) (int, error) {
	w.body.WriteString(s)
	return w.ResponseWriter.WriteString(s)
}

// Idempotency returns a Gin middleware that captures and replays write
// requests by client-supplied Idempotency-Key. Apply only to authenticated
// routes — the key is scoped per user.
//
// Behaviour:
//   - No Idempotency-Key header → pass-through (idempotency is opt-in per
//     request).
//   - Header present, key already seen for this user with matching
//     request-hash → replay cached status + body and abort (no handler
//     execution).
//   - Header present, key already seen for this user with DIFFERENT
//     request-hash → 409 Conflict; protects against the client reusing a key
//     for a logically-different action.
//   - Header present, key already seen for a DIFFERENT user → treat as a new
//     row attempt; if the eventual insert collides on the (Key) primary key
//     the conflict surfaces at INSERT time, which we map to 409.
//   - Header present, key not seen → execute handler, capture
//     (status, body), persist a RequestIdempotency row for future replay.
//
// Successful 2xx responses are cached. 4xx/5xx are NOT cached — the client
// can retry the same key after fixing the underlying problem. Rows are pruned
// after models.RequestIdempotencyRetention by jobs.RequestIdempotencyPruner.
func Idempotency(db *gorm.DB, opts ...IdempotencyOption) gin.HandlerFunc {
	var cfg idempotencyConfig
	for _, o := range opts {
		o(&cfg)
	}
	return func(c *gin.Context) {
		key := c.GetHeader(idempotencyKeyHeader)
		if key == "" {
			c.Next()
			return
		}
		if len(key) > maxIdempotencyKeyLen {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"success": false,
				"message": "Idempotency-Key too long",
			})
			return
		}

		user := userFromContext(c)
		if user == nil {
			// Auth middleware should have run first; if it hasn't, fail closed.
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "Authentication required for idempotent requests",
			})
			return
		}

		// Compute request fingerprint over (method + path and query + body) so
		// that reusing a key for a different request is rejected — the query
		// included, since ?mode=live and ?mode=test are different requests with
		// the same body.
		body, err := readAndRestoreBody(c)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"success": false,
				"message": "Could not read request body",
			})
			return
		}
		target := c.Request.URL.Path
		if q := c.Request.URL.RawQuery; q != "" {
			// Only when there is one, so a query-less request hashes as it
			// always has and a retry across a deploy still replays.
			target += "?" + q
		}
		requestHash := fingerprint(c.Request.Method, target, cfg.fingerprintBody(body))

		var existing models.RequestIdempotency
		err = db.Where("key = ?", key).First(&existing).Error
		if err == nil {
			// Row exists — replay or reject.
			if existing.UserID != user.ID {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{
					"success": false,
					"message": "Idempotency-Key reused by a different user",
				})
				return
			}
			if existing.RequestHash != requestHash {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{
					"success": false,
					"message": "Idempotency-Key reused with a different request payload",
				})
				return
			}
			// Cache hit: replay original response — restore captured headers so
			// 201-with-Location, ETag, Set-Cookie, X-RateLimit, etc. survive
			// the replay. Headers stored as map[string][]string JSONB.
			if len(existing.ResponseHeaders) > 0 {
				var headers map[string][]string
				if err := json.Unmarshal(existing.ResponseHeaders, &headers); err == nil {
					for k, vs := range headers {
						for _, v := range vs {
							c.Writer.Header().Add(k, v)
						}
					}
				} else {
					log.Printf("[idempotency] failed to decode cached headers for key=%s: %v", key, err)
				}
			}
			c.Header("Idempotent-Replayed", "true")
			c.Data(existing.ResponseStatus, "application/json", []byte(existing.ResponseBody))
			c.Abort()
			return
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"success": false,
				"message": "Idempotency lookup failed",
			})
			return
		}

		// First-time call: wrap the writer so we can persist the response.
		writer := &idempotencyWriter{
			ResponseWriter: c.Writer,
			body:           &bytes.Buffer{},
		}
		c.Writer = writer

		c.Next()

		// Only persist successful 2xx responses — a 4xx/5xx is the right
		// outcome to retry after the client adjusts and shouldn't be cached.
		status := writer.Status()
		if status < 200 || status >= 300 {
			return
		}

		// Validate the response is JSON before persisting. The
		// ResponseBody column is JSONB; non-JSON payloads (HTML error pages
		// from panic recovery, CSV streams, raw bytes) would fail the INSERT
		// silently and break idempotency for exactly the requests that need
		// it most. Skip caching with a warning instead.
		respBody := writer.body.Bytes()
		if !json.Valid(respBody) {
			log.Printf("[idempotency] key=%s response body is not valid JSON (len=%d); skipping cache",
				key, len(respBody))
			return
		}

		// Capture response-affecting headers for replay. We only care about
		// non-default headers — Content-Type is set by c.Data on replay.
		headersJSON, err := json.Marshal(writer.Header())
		if err != nil {
			log.Printf("[idempotency] key=%s failed to encode response headers: %v", key, err)
			headersJSON = []byte("{}")
		}

		row := models.RequestIdempotency{
			Key:             key,
			UserID:          user.ID,
			RequestHash:     requestHash,
			ResponseStatus:  status,
			ResponseBody:    respBody,
			ResponseHeaders: headersJSON,
		}
		// INSERT-or-ignore semantics: a race between two concurrent first-
		// time requests with the same key is rare in practice (clients
		// generate fresh keys per logical action) but possible. The loser's
		// INSERT collides on PK; we log it because a frequent loss rate
		// would indicate either buggy clients or a real concurrency bug
		// upstream. The winner's row becomes the canonical replay.
		if err := db.Create(&row).Error; err != nil {
			log.Printf("[idempotency] key=%s persist failed: %v (will not replay)", key, err)
		}
	}
}

// IdempotencyOption adjusts one route's Idempotency.
type IdempotencyOption func(*idempotencyConfig)

type idempotencyConfig struct {
	ignoreFields []string
}

// IdempotencyIgnoringFields leaves the named top-level JSON fields out of the
// request fingerprint. The fingerprint is stored for as long as the key is
// kept, and an unsalted hash of a body carrying a password is an offline
// guessing oracle for it — so a route whose body carries a secret leaves the
// secret out. A retry is still matched on everything else, and still replays
// only an answer the first request (which passed its own checks) earned.
func IdempotencyIgnoringFields(fields ...string) IdempotencyOption {
	return func(c *idempotencyConfig) { c.ignoreFields = append(c.ignoreFields, fields...) }
}

// fingerprintBody is the body as the fingerprint sees it: with the ignored
// fields removed (and the rest re-encoded, keys sorted) when it is a JSON
// object. A body that is not one is hashed as sent — the handler refuses it,
// and nothing is ever replayed for a refusal.
func (c idempotencyConfig) fingerprintBody(body []byte) []byte {
	if len(c.ignoreFields) == 0 {
		return body
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return body
	}
	// Case-insensitively, as encoding/json binds keys to the handler's fields:
	// "Password" reaches the handler as the password just as "password" does.
	for key := range fields {
		for _, f := range c.ignoreFields {
			if strings.EqualFold(key, f) {
				delete(fields, key)
				break
			}
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return body
	}
	return out
}

func fingerprint(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// readAndRestoreBody consumes c.Request.Body and reinstalls a fresh reader
// so downstream handlers can read it again.
func readAndRestoreBody(c *gin.Context) ([]byte, error) {
	if c.Request.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))
	return body, nil
}

// userFromContext duplicates handlers.getUserFromContext to avoid a circular
// import. Auth middleware sets c.Set("user", models.User).
func userFromContext(c *gin.Context) *models.User {
	val, ok := c.Get("user")
	if !ok {
		return nil
	}
	switch u := val.(type) {
	case *models.User:
		return u
	case models.User:
		return &u
	default:
		return nil
	}
}
