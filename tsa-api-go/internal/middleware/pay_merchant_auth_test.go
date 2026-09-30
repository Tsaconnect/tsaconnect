package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
		r.GET("/x", PayMerchantAuth(auth, tc.live, nil), func(c *gin.Context) {
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
		r.GET("/x", PayMerchantAuth(auth, false, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
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
	r.GET("/x", PayMerchantAuth(auth, true, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
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
		r.GET("/x", PayMerchantAuth(auth, true, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w := call("tsapk_live_x")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"live_not_enabled"`) ||
		!strings.Contains(w.Body.String(), "Live payments are switched off for this merchant — contact TSA Connect") {
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

func TestPayMerchantAuth_ALiveKeyIsOnlyAcceptedFromItsAllowedIPs(t *testing.T) {
	merchant := &models.PayMerchant{LiveEnabled: true, AllowedIPs: "203.0.113.7,198.51.100.0/24"}
	auth := func(_ context.Context, raw string) (*models.PayMerchant, string, error) {
		if raw == "tsapk_test_x" {
			return merchant, models.PayModeTest, nil
		}
		return merchant, models.PayModeLive, nil
	}
	call := func(key, from string, liveEnabled bool) *httptest.ResponseRecorder {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/x", PayMerchantAuth(auth, liveEnabled, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
		req := httptest.NewRequest("GET", "/x", nil)
		req.RemoteAddr = from
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	for _, from := range []string{"203.0.113.7:4000", "198.51.100.42:4000"} {
		if w := call("tsapk_live_x", from, true); w.Code != http.StatusOK {
			t.Fatalf("live key from listed %s: %d %s", from, w.Code, w.Body.String())
		}
	}
	w := call("tsapk_live_x", "192.0.2.50:4000", true)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"ip_not_allowed"`) ||
		!strings.Contains(w.Body.String(), "isn't allowed from 192.0.2.50") {
		t.Fatalf("live key from an unlisted address: %d %s — want 403 ip_not_allowed naming the address", w.Code, w.Body.String())
	}
	// Refused before the live switches, so a stolen key cannot probe them.
	if w := call("tsapk_live_x", "192.0.2.50:4000", false); !strings.Contains(w.Body.String(), `"code":"ip_not_allowed"`) {
		t.Fatalf("with live off globally: %d %s", w.Code, w.Body.String())
	}
	if w := call("tsapk_test_x", "192.0.2.50:4000", true); w.Code != http.StatusOK {
		t.Fatalf("a test key is not restricted: %d %s", w.Code, w.Body.String())
	}
}

// payAuthBehindNginx serves one live-key request as production does: nginx on
// loopback in front (a trusted proxy), X-Forwarded-For as nginx builds it, and
// the transport guard before auth. It returns the response and each address
// the refusal hook was handed.
func payAuthBehindNginx(t *testing.T, m *models.PayMerchant, peer string, headers map[string]string) (*httptest.ResponseRecorder, []string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		t.Fatal(err)
	}
	var refused []string
	auth := func(context.Context, string) (*models.PayMerchant, string, error) { return m, models.PayModeLive, nil }
	r.GET("/x", PayMerchantTransport(),
		PayMerchantAuth(auth, true, func(_ *models.PayMerchant, ip string) { refused = append(refused, ip) }),
		func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest("GET", "/x", nil)
	req.RemoteAddr = peer
	req.Header.Set("Authorization", "Bearer tsapk_live_x")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w, refused
}

func TestPayMerchantAuth_TheAllowlistSeesTheClientNginxNamesNotOneTheCallerForged(t *testing.T) {
	m := &models.PayMerchant{LiveEnabled: true, AllowedIPs: "203.0.113.7,2001:db8:1::/48"}
	for name, tc := range map[string]struct {
		peer string
		xff  string
		want int
	}{
		// nginx appends the real client to whatever the caller sent.
		"listed client":                  {"127.0.0.1:5000", "203.0.113.7", 200},
		"forged listed hop, real client": {"127.0.0.1:5000", "203.0.113.7, 192.0.2.50", 403},
		"listed IPv6 client":             {"[::1]:5000", "2001:db8:1:ff::9", 200},
		"unlisted IPv6 client":           {"[::1]:5000", "2001:db8:2::9", 403},
		// Straight at the origin, not through nginx: the header is not trusted.
		"direct with a forged header": {"192.0.2.50:5000", "203.0.113.7", 403},
	} {
		w, refused := payAuthBehindNginx(t, m, tc.peer, map[string]string{"X-Forwarded-For": tc.xff})
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if (tc.want == 403) != (len(refused) == 1) {
			t.Fatalf("%s: the refusal hook saw %v", name, refused)
		}
	}
}

func TestPayMerchantAuth_AProxyAddressIsNeverOfferedAsOneToList(t *testing.T) {
	m := &models.PayMerchant{LiveEnabled: true, AllowedIPs: "203.0.113.7"}
	// What a stale nginx real-IP list would hand us: a Cloudflare edge address.
	w, refused := payAuthBehindNginx(t, m, "127.0.0.1:5000", map[string]string{"X-Forwarded-For": "104.16.5.5"})
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"ip_not_allowed"`) ||
		strings.Contains(w.Body.String(), "104.16.5.5") || strings.Contains(w.Body.String(), "add it") {
		t.Fatalf("%d %s: a shared address must not be named or offered", w.Code, w.Body.String())
	}
	if len(refused) != 1 {
		t.Fatalf("hook %v", refused)
	}
}

func TestPayMerchantTransport_RefusesTheWebOriginAndPlainHTTP(t *testing.T) {
	m := &models.PayMerchant{LiveEnabled: true}
	if w, _ := payAuthBehindNginx(t, m, "127.0.0.1:5000", map[string]string{"CF-Visitor": `{"scheme":"http"}`}); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), `"code":"https_required"`) {
		t.Fatalf("plain http: %d %s", w.Code, w.Body.String())
	}
	if w, _ := payAuthBehindNginx(t, m, "127.0.0.1:5000", map[string]string{"CF-Visitor": `{"scheme":"https"}`}); w.Code != http.StatusOK {
		t.Fatalf("https: %d %s", w.Code, w.Body.String())
	}

	// Through the web app's signed Netlify proxy: absent, before any key lookup.
	const secret = "netlify-proxy-secret"
	gin.SetMode(gin.TestMode)
	r := gin.New()
	looked := false
	auth := func(context.Context, string) (*models.PayMerchant, string, error) {
		looked = true
		return m, models.PayModeLive, nil
	}
	r.Use(NetlifyClientIP(secret))
	r.GET("/api/pay/v1/payments/:id", PayMerchantTransport(), PayMerchantAuth(auth, true, nil), func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest("GET", "/api/pay/v1/payments/1", nil)
	req.Header.Set("Authorization", "Bearer tsapk_live_x")
	req.Header.Set(NetlifySignHeader, netlifySign(t, secret, "netlify", time.Now().Add(time.Minute)))
	req.Header.Set(NetlifyClientIPHeader, "203.0.113.7")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || looked {
		t.Fatalf("netlify-proxied: %d %s looked=%v", w.Code, w.Body.String(), looked)
	}
}
