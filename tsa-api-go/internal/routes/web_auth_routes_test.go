package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
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
