package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

// PayMerchantTransport refuses a merchant API request that arrived the wrong
// way, before any key is looked at: through the web app's Netlify proxy (the
// API lives at its documented host only; answered 404 as if absent), or as
// plain http according to Cloudflare's CF-Visitor (403 https_required — the
// key has already crossed the network in the clear). The Cloudflare rules in
// deploy/README.md refuse plain http at the edge; this is the origin's own
// check for when a rule is skipped, and so the answer never goes back in the
// clear either. CF-Visitor is Cloudflare's to set; a request that bypasses
// Cloudflare can only forge it to refuse itself.
func PayMerchantTransport() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetBool(NetlifyProxiedKey) {
			utils.ErrorResponse(c, http.StatusNotFound, "Not found")
			c.Abort()
			return
		}
		var visitor struct {
			Scheme string `json:"scheme"`
		}
		if v := c.GetHeader("CF-Visitor"); v != "" && json.Unmarshal([]byte(v), &visitor) == nil && visitor.Scheme == "http" {
			utils.ErrorResponseWithCode(c, http.StatusForbidden,
				"TSA Pay only accepts https. A key sent over http has leaked — create a new one in your TSA Pay dashboard", "https_required")
			c.Abort()
			return
		}
		c.Next()
	}
}

// payRefusalLogEvery bounds the "live key refused" log line per merchant: a
// stolen key hammered from many addresses must not flood the log, and one
// line a minute per merchant is enough to be seen.
const payRefusalLogEvery = time.Minute

// PayMerchantAuth authenticates a TSA Pay merchant API request by its
// "Authorization: Bearer <key>" header. On success it sets "payMerchant"
// (*models.PayMerchant) and "payMode" (string) on the context for the
// handler to read. authenticate is *services.PayMerchantService.Authenticate
// — passed as a func so this middleware can be tested without a database.
//
// The Bearer scheme is matched case-insensitively. A malformed/missing/unknown key answers 401 invalid_key; a key belonging
// to a suspended merchant answers 403 merchant_suspended; a live key while
// liveEnabled is false answers 403 live_disabled, and a live key of a merchant
// a super admin has switched off (PayMerchant.LiveEnabled) answers 403
// live_not_enabled. A test key is always allowed regardless of either switch.
// A live key used from an address its merchant has not listed (see
// PayMerchant.KeyAllowedFrom) answers 403 ip_not_allowed, naming the address
// we saw so the merchant can list the right one — unless that address is a
// shared egress (services.PaySharedEgress), which means our real-client chain
// is broken: then the hint is left out and the refusal is logged as such. It
// is checked before the live switches, so a stolen key used from elsewhere
// does not learn whether live payments are on (it still learns that the key
// is valid, and whether the merchant is suspended).
// Each such refusal is also handed to onIPRefused (nil = none), which tells the
// owner: it is the clearest sign a live key has leaked.
// A failed key lookup (not a bad key) answers 503 auth_unavailable.
func PayMerchantAuth(authenticate func(ctx context.Context, raw string) (*models.PayMerchant, string, error), liveEnabled bool,
	onIPRefused func(m *models.PayMerchant, ip string)) gin.HandlerFunc {
	var lastRefusalLogged sync.Map // merchant ID → *atomic.Int64, unix nanos
	return func(c *gin.Context) {
		scheme, raw, ok := strings.Cut(c.GetHeader("Authorization"), " ")
		raw = strings.TrimSpace(raw)
		if !ok || !strings.EqualFold(scheme, "Bearer") || raw == "" {
			utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "invalid API key", "invalid_key")
			c.Abort()
			return
		}

		m, mode, err := authenticate(c.Request.Context(), raw)
		if err != nil {
			if errors.Is(err, services.ErrPayMerchantSuspended) {
				utils.ErrorResponseWithCode(c, http.StatusForbidden, "this merchant account is suspended", "merchant_suspended")
			} else if errors.Is(err, services.ErrPayAuthUnavailable) {
				log.Printf("[tsa-pay] merchant auth unavailable: %s %s", c.Request.Method, c.FullPath())
				utils.ErrorResponseWithCode(c, http.StatusServiceUnavailable, "authentication is temporarily unavailable", "auth_unavailable")
			} else {
				utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "invalid API key", "invalid_key")
			}
			c.Abort()
			return
		}

		seen := c.ClientIP()
		ip, _ := netip.ParseAddr(seen) // unparseable → the zero Addr, which no allowlist matches
		if !m.KeyAllowedFrom(mode, ip) {
			shared := services.PaySharedEgress(ip)
			last, _ := lastRefusalLogged.LoadOrStore(m.ID, new(atomic.Int64))
			if logDue(last.(*atomic.Int64), time.Now(), payRefusalLogEvery) {
				log.Printf("[tsa-pay] live key refused: merchant=%s ip=%s shared_egress=%t %s %s (logged at most once a minute per merchant)",
					m.ID, seen, shared, c.Request.Method, c.FullPath())
			}
			msg := "this live API key isn't allowed from " + seen + " — add it to your allowed IPs in your TSA Pay dashboard"
			if shared {
				// Listing a proxy's address would let anyone behind it in.
				msg = "this live API key isn't allowed from this address — contact TSA Connect"
			}
			if onIPRefused != nil {
				onIPRefused(m, seen)
			}
			utils.ErrorResponseWithCode(c, http.StatusForbidden, msg, "ip_not_allowed")
			c.Abort()
			return
		}

		if mode == models.PayModeLive && !liveEnabled {
			utils.ErrorResponseWithCode(c, http.StatusForbidden, "live payments are not enabled", "live_disabled")
			c.Abort()
			return
		}
		// Global is on here, so LiveAllowed is only the merchant's own switch.
		if mode == models.PayModeLive && !m.LiveAllowed(liveEnabled) {
			utils.ErrorResponseWithCode(c, http.StatusForbidden, "Live payments are switched off for this merchant — contact TSA Connect", "live_not_enabled")
			c.Abort()
			return
		}

		c.Set("payMerchant", m)
		c.Set("payMode", mode)
		c.Next()
	}
}

// PayMerchantRateKey keys a per-merchant rate limiter off the authenticated
// merchant set by PayMerchantAuth. Returns "" (falls back to per-IP) when
// no merchant is on the context, so it composes safely with any route that
// hasn't run PayMerchantAuth first.
func PayMerchantRateKey(c *gin.Context) string {
	v, ok := c.Get("payMerchant")
	if !ok {
		return ""
	}
	m, ok := v.(*models.PayMerchant)
	if !ok || m == nil {
		return ""
	}
	return m.ID.String()
}
