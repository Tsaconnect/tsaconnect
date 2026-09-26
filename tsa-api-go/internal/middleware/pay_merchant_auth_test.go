package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestPayMerchantAuth(t *testing.T) {
	auth := func(_ context.Context, raw string) (*models.PayMerchant, string, error) {
		switch raw {
		case "tsapk_live_good":
			return &models.PayMerchant{LiveEnabled: true}, models.PayModeLive, nil
		case "tsapk_live_notswitched":
			return &models.PayMerchant{}, models.PayModeLive, nil
		case "tsapk_test_good":
			return &models.PayMerchant{}, models.PayModeTest, nil
		case "tsapk_live_suspended":
			return nil, "", services.ErrPayMerchantSuspended
		}
		return nil, "", services.ErrPayKeyInvalid
	}
	for _, tc := range []struct {
		header, code string
		live         bool
		want         int
	}{
		{"", "invalid_key", false, 401},
		{"Bearer tsapk_live_bad", "invalid_key", true, 401},
		{"tsapk_live_good", "invalid_key", true, 401}, // no Bearer
		{"Bearer tsapk_live_suspended", "merchant_suspended", true, 403},
		{"Bearer tsapk_live_good", "live_disabled", false, 403},
		{"Bearer tsapk_live_good", "", true, 200},
		// Per-merchant switch: off refuses a live key even while live is on globally;
		// global off still answers live_disabled first; a test key needs neither.
		{"Bearer tsapk_live_notswitched", "live_not_enabled", true, 403},
		{"Bearer tsapk_live_notswitched", "live_disabled", false, 403},
		{"Bearer tsapk_test_good", "", false, 200},
	} {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/x", PayMerchantAuth(auth, tc.live), func(c *gin.Context) {
			if _, ok := c.Get("payMerchant"); !ok {
				t.Fatal("payMerchant not set")
			}
			c.Status(http.StatusOK)
		})
		req := httptest.NewRequest("GET", "/x", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want || (tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`)) {
			t.Fatalf("%q live=%v: %d %s", tc.header, tc.live, w.Code, w.Body.String())
		}
	}
}

func TestPayMerchantAuth_BearerSchemeIsCaseInsensitive(t *testing.T) {
	auth := func(_ context.Context, raw string) (*models.PayMerchant, string, error) {
		if raw == "tsapk_test_good" {
			return &models.PayMerchant{}, models.PayModeTest, nil
		}
		return nil, "", services.ErrPayKeyInvalid
	}
	for header, want := range map[string]int{
		"bearer tsapk_test_good": 200, "BEARER tsapk_test_good": 200, "Bearer  tsapk_test_good": 200,
		"Bearer": 401, "Bearer ": 401, "Basic tsapk_test_good": 401, "Bearertsapk_test_good": 401,
	} {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/x", PayMerchantAuth(auth, false), func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Authorization", header)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("%q: %d, want %d", header, w.Code, want)
		}
	}
}

func TestPayMerchantAuth_AnUnavailableAuthStoreIs503NotABadKey(t *testing.T) {
	auth := func(context.Context, string) (*models.PayMerchant, string, error) {
		return nil, "", services.ErrPayAuthUnavailable
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/x", PayMerchantAuth(auth, true), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer tsapk_live_x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"auth_unavailable"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestPayMerchantAuth_ALiveKeyNeedsTheMerchantSwitchedLive(t *testing.T) {
	merchant := &models.PayMerchant{}
	auth := func(_ context.Context, raw string) (*models.PayMerchant, string, error) {
		if raw == "tsapk_test_x" {
			return merchant, models.PayModeTest, nil
		}
		return merchant, models.PayModeLive, nil
	}
	call := func(key string) *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/x", PayMerchantAuth(auth, true), func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := call("tsapk_live_x")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"live_not_enabled"`) ||
		!strings.Contains(w.Body.String(), "Live payments aren't switched on for this merchant yet — finish testing and contact TSA Connect") {
		t.Fatalf("live key, merchant not switched live: %d %s", w.Code, w.Body.String())
	}
	if w := call("tsapk_test_x"); w.Code != http.StatusOK {
		t.Fatalf("test key, merchant not switched live: %d %s", w.Code, w.Body.String())
	}
	merchant.LiveEnabled = true
	if w := call("tsapk_live_x"); w.Code != http.StatusOK {
		t.Fatalf("live key, merchant switched live: %d %s", w.Code, w.Body.String())
	}
}
