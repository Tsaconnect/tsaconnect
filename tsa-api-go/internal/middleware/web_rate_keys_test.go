package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func rateKeyCtx(req *http.Request) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

func TestWebLoginRateKey_IsPerAccountAndLeavesTheBodyForTheHandler(t *testing.T) {
	login := func(body string) (string, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/web/login", strings.NewReader(body))
		c := rateKeyCtx(req)
		k := WebLoginRateKey(c)
		rest, _ := io.ReadAll(c.Request.Body)
		return k, string(rest)
	}
	k1, body := login(`{"email":" Ada@SkyAir.ng ","password":"x"}`)
	if body != `{"email":" Ada@SkyAir.ng ","password":"x"}` {
		t.Fatalf("the handler lost the body: %q", body)
	}
	k2, _ := login(`{"email":"ada@skyair.ng","password":"y"}`)
	k3, _ := login(`{"email":"bola@skyair.ng","password":"y"}`)
	if k1 == "" || k1 != k2 || k1 == k3 {
		t.Fatalf("keys: same account %q/%q, other account %q", k1, k2, k3)
	}
	if k, _ := login(`not json`); k != "" {
		t.Fatalf("unparseable body keyed %q, want the IP fallback", k)
	}
}

func TestSessionCookieRateKeys_HashTheCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/web/refresh", nil)
	req.AddCookie(&http.Cookie{Name: WebRefreshCookie, Value: "raw-refresh-token"})
	k := WebRefreshRateKey(rateKeyCtx(req))
	if k == "" || strings.Contains(k, "raw-refresh-token") {
		t.Fatalf("refresh key %q must exist and not hold the raw token", k)
	}
	if k := WebChallengeRateKey(rateKeyCtx(httptest.NewRequest(http.MethodPost, "/", nil))); k != "" {
		t.Fatalf("no challenge cookie keyed %q", k)
	}
}
