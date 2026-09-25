package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

func rateKeyCtx(req *http.Request) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = req
	return c
}

func webTokenFor(t *testing.T, secret string, user uuid.UUID, web bool) string {
	t.Helper()
	claims := jwt.MapClaims{"userId": user.String(), "exp": time.Now().Add(time.Hour).Unix()}
	if web {
		claims[WebTokenClaim] = true
	}
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// Behind the web app's proxy every visitor shares a few addresses: a signed-in
// web session is counted against its user, while a forged or app token cannot
// pick its own bucket.
func TestWebSessionRateKey_CountsSignedInWebUsersApart(t *testing.T) {
	cfg := &config.Config{JWTSecret: "s3cret", WebAppOrigin: "https://app.test"}
	key := WebSessionRateKey(cfg)
	withCookie := func(v string) *gin.Context {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.AddCookie(&http.Cookie{Name: WebAccessCookie, Value: v})
		return rateKeyCtx(req)
	}
	ada, bola := uuid.New(), uuid.New()
	a, b := key(withCookie(webTokenFor(t, "s3cret", ada, true))), key(withCookie(webTokenFor(t, "s3cret", bola, true)))
	if a == "" || b == "" || a == b {
		t.Fatalf("two web users must get their own keys: %q %q", a, b)
	}
	for name, v := range map[string]string{
		"forged":      webTokenFor(t, "wrong", ada, true),
		"app token":   webTokenFor(t, "s3cret", ada, false),
		"not a token": "garbage",
	} {
		if k := key(withCookie(v)); k != "" {
			t.Errorf("%s keyed %q, want the IP fallback", name, k)
		}
	}
	if k := WebSessionRateKey(&config.Config{JWTSecret: "s3cret"})(withCookie(webTokenFor(t, "s3cret", ada, true))); k != "" {
		t.Errorf("web app off: keyed %q", k)
	}
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
