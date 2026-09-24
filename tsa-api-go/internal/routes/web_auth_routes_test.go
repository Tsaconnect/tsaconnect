package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/handlers"
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

// Login and verify each have their own per-IP budget: spending login's does not
// block entering the code, and verify allows more tries than login.
func TestWebAuthRoutes_LoginAndVerifyHaveSeparateLimits(t *testing.T) {
	r := webAuthRouter(&config.Config{JWTSecret: "s", WebAppOrigin: "https://app.test"})
	post := func(path string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-TSA-Web", "1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	for i := 1; i <= 30; i++ {
		if code := post("/api/auth/web/login"); code == http.StatusTooManyRequests {
			t.Fatalf("login %d limited", i)
		}
	}
	if code := post("/api/auth/web/login"); code != http.StatusTooManyRequests {
		t.Fatalf("login 31: %d", code)
	}
	for i := 1; i <= 60; i++ {
		if code := post("/api/auth/web/verify"); code == http.StatusTooManyRequests {
			t.Fatalf("verify %d limited after login's budget was spent", i)
		}
	}
	if code := post("/api/auth/web/verify"); code != http.StatusTooManyRequests {
		t.Fatalf("verify 61: %d", code)
	}
}
