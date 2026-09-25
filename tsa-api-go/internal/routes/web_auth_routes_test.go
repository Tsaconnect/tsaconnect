package routes

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
)

func webAuthRouter(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r, cfg, &handlers.Handlers{Config: cfg}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return r
}

func TestWebAuthRoutes_OffWithoutWebAppOrigin(t *testing.T) {
	r := webAuthRouter(&config.Config{JWTSecret: "s"})
	req := httptest.NewRequest(http.MethodPost, "/api/auth/web/login", strings.NewReader(`{}`))
	req.Header.Set("X-TSA-Web", "1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("web login with WEB_APP_ORIGIN unset: %d", w.Code)
	}
}

func TestWebAuthRoutes_OnWithWebAppOriginAndGuarded(t *testing.T) {
	r := webAuthRouter(&config.Config{JWTSecret: "s", WebAppOrigin: "https://app.test"})
	registered := map[string]bool{}
	for _, rt := range r.Routes() {
		registered[rt.Method+" "+rt.Path] = true
	}
	for _, want := range []string{
		"POST /api/auth/web/login",
		"POST /api/auth/web/verify",
		"POST /api/auth/web/refresh",
		"POST /api/auth/web/logout",
		"GET /api/auth/web/me",
	} {
		if !registered[want] {
			t.Errorf("route not registered: %s", want)
		}
	}
	for _, path := range []string{"/api/auth/web/login", "/api/auth/web/verify", "/api/auth/web/refresh", "/api/auth/web/logout"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`)))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"csrf"`) {
			t.Errorf("%s without the header: %d %s", path, w.Code, w.Body.String())
		}
	}
}

// The web app arrives through a proxy, so many people share one address. Sign-in
// is limited per account from that address (one person's failures don't block a
// colleague), and code entry per pending sign-in (spending login's budget does
// not block entering the code).
func TestWebAuthRoutes_SignInLimitsArePerAccountAndPerChallenge(t *testing.T) {
	r := webAuthRouter(&config.Config{JWTSecret: "s", WebAppOrigin: "https://app.test"})
	post := func(path, body string, cookie *http.Cookie) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-TSA-Web", "1")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	// No password: the handler refuses before any lookup, and the limiter
	// only needs the email.
	ada := `{"email":"ada@skyair.ng"}`
	for i := 1; i <= 10; i++ {
		if code := post("/api/auth/web/login", ada, nil); code == http.StatusTooManyRequests {
			t.Fatalf("login %d limited", i)
		}
	}
	if code := post("/api/auth/web/login", ada, nil); code != http.StatusTooManyRequests {
		t.Fatalf("ada's 11th sign-in: %d", code)
	}
	if code := post("/api/auth/web/login", `{"email":"bola@skyair.ng"}`, nil); code == http.StatusTooManyRequests {
		t.Fatal("a colleague behind the same address was blocked by ada's attempts")
	}
	challenge := &http.Cookie{Name: middleware.WebChallengeCookie, Value: uuid.NewString()}
	for i := 1; i <= 20; i++ {
		if code := post("/api/auth/web/verify", `{"code":""}`, challenge); code == http.StatusTooManyRequests {
			t.Fatalf("verify %d limited after login's budget was spent", i)
		}
	}
	if code := post("/api/auth/web/verify", `{"code":""}`, challenge); code != http.StatusTooManyRequests {
		t.Fatalf("verify 21 on one sign-in: %d", code)
	}
}

// Behind the web app's signed Netlify proxy the per-address ceiling counts each
// visitor, not Netlify: one visitor spending it (with any number of emails)
// leaves another visitor through the same proxy untouched. This is the ceiling
// that stops one person spraying many accounts.
func TestWebAuthRoutes_LoginCeilingIsPerNetlifyVisitor(t *testing.T) {
	const secret = "netlify-proxy-secret"
	r := webAuthRouter(&config.Config{JWTSecret: "s", WebAppOrigin: "https://app.test", NetlifyProxySecret: secret})
	sign, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "netlify", "exp": time.Now().Add(time.Minute).Unix()}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	login := func(visitor string, n int) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/web/login", strings.NewReader(fmt.Sprintf(`{"email":"user%d@skyair.ng"}`, n)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-TSA-Web", "1")
		req.Header.Set(middleware.NetlifySignHeader, sign)
		req.Header.Set(middleware.NetlifyClientIPHeader, visitor)
		req.RemoteAddr = "104.23.1.9:443" // Netlify's egress, the same for everyone
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	for i := 1; i <= 300; i++ {
		if code := login("41.58.3.7", i); code == http.StatusTooManyRequests {
			t.Fatalf("visitor A's sign-in %d limited", i)
		}
	}
	if code := login("41.58.3.7", 301); code != http.StatusTooManyRequests {
		t.Fatalf("visitor A past the ceiling: %d", code)
	}
	if code := login("102.89.40.2", 1); code == http.StatusTooManyRequests {
		t.Fatal("visitor B was blocked by visitor A through the same Netlify address")
	}
}
