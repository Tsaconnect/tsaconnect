package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func netlifySign(t *testing.T, secret, iss string, exp time.Time) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": iss, "exp": exp.Unix(), "deploy_context": "production", "site_url": "https://tsa-web.netlify.app",
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// clientIPThrough serves one request behind the loopback trusted proxy (as
// production: nginx on 127.0.0.1) and returns what c.ClientIP() saw. Each of
// added is one more copy of a header, on top of headers.
func clientIPThrough(t *testing.T, secret string, headers map[string]string, added ...[2]string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"127.0.0.1", "::1"}); err != nil {
		t.Fatal(err)
	}
	r.Use(NetlifyClientIP(secret))
	var seen string
	r.GET("/api/x", func(c *gin.Context) { seen = c.ClientIP() })
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("X-Real-IP", "104.23.1.9") // what nginx says: Netlify's egress
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, h := range added {
		req.Header.Add(h[0], h[1])
	}
	r.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func TestNetlifyClientIP_TrustsTheVisitorOnlyWithAValidSignature(t *testing.T) {
	const secret = "netlify-proxy-secret"
	valid := netlifySign(t, secret, "netlify", time.Now().Add(time.Minute))
	if ip := clientIPThrough(t, secret, map[string]string{NetlifySignHeader: valid, NetlifyClientIPHeader: "41.58.3.7"}); ip != "41.58.3.7" {
		t.Fatalf("signed request: client %q, want the visitor", ip)
	}
	for name, h := range map[string]map[string]string{
		"no signature":      {NetlifyClientIPHeader: "41.58.3.7"},
		"wrong secret":      {NetlifySignHeader: netlifySign(t, "guess", "netlify", time.Now().Add(time.Minute)), NetlifyClientIPHeader: "41.58.3.7"},
		"expired":           {NetlifySignHeader: netlifySign(t, secret, "netlify", time.Now().Add(-time.Minute)), NetlifyClientIPHeader: "41.58.3.7"},
		"another issuer":    {NetlifySignHeader: netlifySign(t, secret, "someone", time.Now().Add(time.Minute)), NetlifyClientIPHeader: "41.58.3.7"},
		"not an address":    {NetlifySignHeader: valid, NetlifyClientIPHeader: "41.58.3.7, 1.2.3.4"},
		"garbage signature": {NetlifySignHeader: "not.a.jws", NetlifyClientIPHeader: "41.58.3.7"},
	} {
		if ip := clientIPThrough(t, secret, h); ip != "104.23.1.9" {
			t.Errorf("%s: client %q, want nginx's address unchanged", name, ip)
		}
	}
	if ip := clientIPThrough(t, "", map[string]string{NetlifySignHeader: valid, NetlifyClientIPHeader: "41.58.3.7"}); ip != "104.23.1.9" {
		t.Errorf("secret unset: client %q, want the proxy's address", ip)
	}
}

// The signature does not cover the address header, so a second copy of it — the
// first could be Netlify's, the second anyone's — is trusted in neither order.
func TestNetlifyClientIP_TwoVisitorAddressesAreNeitherTrusted(t *testing.T) {
	const secret = "netlify-proxy-secret"
	valid := netlifySign(t, secret, "netlify", time.Now().Add(time.Minute))
	for _, order := range [][2]string{{"41.58.3.7", "1.2.3.4"}, {"1.2.3.4", "41.58.3.7"}} {
		ip := clientIPThrough(t, secret, map[string]string{NetlifySignHeader: valid, NetlifyClientIPHeader: order[0]},
			[2]string{NetlifyClientIPHeader, order[1]})
		if ip != "104.23.1.9" {
			t.Errorf("addresses %v: client %q, want nginx's address unchanged", order, ip)
		}
	}
}

// A visitor address that is not trusted is logged — a secret that stopped
// matching Netlify's would otherwise go unseen — but at most once a minute, and
// never for a request that names no visitor or is correctly signed.
func TestNetlifyClientIP_AnUntrustedVisitorIsLoggedAtMostOnceAMinute(t *testing.T) {
	const secret = "netlify-proxy-secret"
	gin.SetMode(gin.TestMode)
	logs := captureLog(t)
	start := time.Now()
	now := start
	r := gin.New()
	r.Use(netlifyClientIP(secret, func() time.Time { return now }))
	r.GET("/api/x", func(c *gin.Context) {})
	serve := func(headers map[string]string) {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	lines := func() int { return strings.Count(logs.String(), "netlify_client_ip_untrusted=1") }
	wrong := netlifySign(t, "rotated-on-netlify", "netlify", start.Add(time.Hour))

	// Names no visitor; correctly signed.
	serve(map[string]string{})
	serve(map[string]string{NetlifySignHeader: netlifySign(t, secret, "netlify", start.Add(time.Hour)), NetlifyClientIPHeader: "41.58.3.7"})
	if lines() != 0 {
		t.Fatalf("log %q: nothing untrusted was sent", logs.String())
	}
	serve(map[string]string{NetlifySignHeader: wrong, NetlifyClientIPHeader: "41.58.3.7"})
	if lines() != 1 || !strings.Contains(logs.String(), `reason="signature invalid`) {
		t.Fatalf("log %q: want one line saying the signature is invalid", logs.String())
	}
	now = start.Add(59 * time.Second)
	serve(map[string]string{NetlifyClientIPHeader: "41.58.3.7"})
	if lines() != 1 {
		t.Fatalf("log %q: a second line within the minute", logs.String())
	}
	now = start.Add(61 * time.Second)
	serve(map[string]string{NetlifyClientIPHeader: "41.58.3.7"})
	if lines() != 2 || !strings.Contains(logs.String(), `reason="signature missing"`) {
		t.Fatalf("log %q: want a second line once the minute has passed", logs.String())
	}
	if strings.Contains(logs.String(), "41.58.3.7") {
		t.Fatalf("log %q: the visitor's address is not the log's business", logs.String())
	}
}
