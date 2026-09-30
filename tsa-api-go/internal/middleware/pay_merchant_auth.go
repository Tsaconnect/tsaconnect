package middleware

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/utils"
)

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
// we saw so the merchant can list the right one; it is checked before the live
// switches so a stolen key learns nothing more about the merchant.
// A failed key lookup (not a bad key) answers 503 auth_unavailable.
func PayMerchantAuth(authenticate func(ctx context.Context, raw string) (*models.PayMerchant, string, error), liveEnabled bool) gin.HandlerFunc {
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

		ip, _ := netip.ParseAddr(c.ClientIP()) // unparseable → the zero Addr, which no allowlist matches
		if !m.KeyAllowedFrom(mode, ip) {
			seen := c.ClientIP()
			log.Printf("[tsa-pay] live key refused: merchant=%s ip=%s %s %s", m.ID, seen, c.Request.Method, c.FullPath())
			utils.ErrorResponseWithCode(c, http.StatusForbidden,
				"this live API key isn't allowed from "+seen+" — add it to your allowed IPs in your TSA Pay dashboard", "ip_not_allowed")
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
