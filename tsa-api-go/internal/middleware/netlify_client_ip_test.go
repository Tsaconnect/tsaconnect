package middleware

import (
	"net/http"
	"net/http/httptest"
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
// production: nginx on 127.0.0.1) and returns what c.ClientIP() saw.
func clientIPThrough(t *testing.T, secret string, headers map[string]string) string {
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
