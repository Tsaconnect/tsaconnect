package middleware

import (
	"context"
	"errors"
	"net/http"
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
// A malformed/missing/unknown key answers 401 invalid_key; a key belonging
// to a suspended merchant answers 403 merchant_suspended; a live key while
// liveEnabled is false answers 403 live_disabled (a test key is always
// allowed regardless of liveEnabled).
func PayMerchantAuth(authenticate func(ctx context.Context, raw string) (*models.PayMerchant, string, error), liveEnabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		raw = strings.TrimSpace(raw)
		if !ok || raw == "" {
			utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "invalid API key", "invalid_key")
			c.Abort()
			return
		}

		m, mode, err := authenticate(c.Request.Context(), raw)
		if err != nil {
			if errors.Is(err, services.ErrPayMerchantSuspended) {
				utils.ErrorResponseWithCode(c, http.StatusForbidden, "this merchant account is suspended", "merchant_suspended")
			} else {
				utils.ErrorResponseWithCode(c, http.StatusUnauthorized, "invalid API key", "invalid_key")
			}
			c.Abort()
			return
		}

		if mode == models.PayModeLive && !liveEnabled {
			utils.ErrorResponseWithCode(c, http.StatusForbidden, "live payments are not enabled", "live_disabled")
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
