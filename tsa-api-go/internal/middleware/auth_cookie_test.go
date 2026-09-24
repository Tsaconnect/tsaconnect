package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const cookieRigOrigin = "https://app.test"

func cookieRigWith(t *testing.T, cfg *config.Config) (*gin.Engine, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, account_status TEXT DEFAULT 'active', role TEXT DEFAULT 'admin', deleted_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	db.Exec(`INSERT INTO users (id, email) VALUES (?, 'a@b.co')`, id.String())
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"userId": id.String(), "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(cfg.JWTSecret))
	gin.SetMode(gin.TestMode)
	r := gin.New()
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.GET("/api/x", Auth(cfg), ok)
	r.POST("/api/x", Auth(cfg), ok)
	r.GET("/api/admin/x", AdminAuth(cfg), ok)
	r.GET("/api/staff", Auth(cfg), RequireP2PAdmin(), ok)
	r.GET("/api/super", Auth(cfg), RequireSuperAdmin(), ok)
	r.GET("/api/web", Auth(cfg), func(c *gin.Context) {
		if !IsWebSession(c) {
			c.Status(http.StatusNoContent)
			return
		}
		c.Status(http.StatusOK)
	})
	return r, tok
}

func cookieRig(t *testing.T) (*gin.Engine, string) {
	return cookieRigWith(t, &config.Config{JWTSecret: "s3cret", WebAppOrigin: cookieRigOrigin})
}

type cookieReq struct {
	method, path, cookie, bearer, csrf, origin, fetchSite string
}

func (q cookieReq) send(r *gin.Engine) int {
	req := httptest.NewRequest(q.method, q.path, nil)
	if q.cookie != "" {
		req.AddCookie(&http.Cookie{Name: WebAccessCookie, Value: q.cookie})
	}
	if q.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+q.bearer)
	}
	if q.csrf != "" {
		req.Header.Set(WebCSRFHeader, q.csrf)
	}
	if q.origin != "" {
		req.Header.Set("Origin", q.origin)
	}
	if q.fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", q.fetchSite)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestAuth_AcceptsTheWebCookie(t *testing.T) {
	r, tok := cookieRig(t)
	if code := (cookieReq{method: http.MethodGet, path: "/api/x", cookie: tok}).send(r); code != http.StatusOK {
		t.Fatalf("GET with the cookie: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/x", cookie: tok, origin: cookieRigOrigin, fetchSite: "same-origin"}).send(r); code != http.StatusOK {
		t.Fatalf("GET with the cookie from the app's own page: %d", code)
	}
}

func TestAuth_CookieWritesNeedTheCSRFHeader(t *testing.T) {
	r, tok := cookieRig(t)
	if code := (cookieReq{method: http.MethodPost, path: "/api/x", cookie: tok}).send(r); code != http.StatusForbidden {
		t.Fatalf("cookie POST without the header: %d", code)
	}
	if code := (cookieReq{method: http.MethodPost, path: "/api/x", cookie: tok, csrf: "true"}).send(r); code != http.StatusForbidden {
		t.Fatalf("cookie POST with a header value other than 1: %d", code)
	}
	if code := (cookieReq{method: http.MethodPost, path: "/api/x", cookie: tok, csrf: "1"}).send(r); code != http.StatusOK {
		t.Fatalf("cookie POST with the header: %d", code)
	}
	if code := (cookieReq{method: http.MethodPost, path: "/api/x", bearer: tok}).send(r); code != http.StatusOK {
		t.Fatalf("the app's bearer POST must not need it: %d", code)
	}
}

func TestAdminAuth_NeverAcceptsTheWebCookie(t *testing.T) {
	r, tok := cookieRig(t)
	if code := (cookieReq{method: http.MethodGet, path: "/api/admin/x", cookie: tok, csrf: "1", origin: cookieRigOrigin, fetchSite: "same-origin"}).send(r); code != http.StatusUnauthorized {
		t.Fatalf("admin route with only the web cookie: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/admin/x", bearer: tok}).send(r); code != http.StatusOK {
		t.Fatalf("admin route with the bearer: %d", code)
	}
}

// Sibling sites (admin, landing) are same-SITE, so the browser sends them the
// cookie; the origin checks are what keep them from using it.
func TestAuth_CookieFromAnotherOriginIsRefused(t *testing.T) {
	r, tok := cookieRig(t)
	if code := (cookieReq{method: http.MethodGet, path: "/api/x", cookie: tok, origin: "https://admin.tsaconnectworld.com"}).send(r); code != http.StatusForbidden {
		t.Fatalf("cookie with a sibling Origin: %d", code)
	}
	if code := (cookieReq{method: http.MethodPost, path: "/api/x", cookie: tok, csrf: "1", origin: "https://admin.tsaconnectworld.com"}).send(r); code != http.StatusForbidden {
		t.Fatalf("cookie write with a sibling Origin: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/x", cookie: tok, fetchSite: "same-site"}).send(r); code != http.StatusForbidden {
		t.Fatalf("cookie with Sec-Fetch-Site same-site: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/x", bearer: tok, origin: "https://admin.tsaconnectworld.com", fetchSite: "cross-site"}).send(r); code != http.StatusOK {
		t.Fatalf("the bearer path ignores Origin: %d", code)
	}
}

func TestAuth_CookieIsIgnoredWhenTheWebAppIsOff(t *testing.T) {
	r, tok := cookieRigWith(t, &config.Config{JWTSecret: "s3cret"})
	if code := (cookieReq{method: http.MethodGet, path: "/api/x", cookie: tok}).send(r); code != http.StatusUnauthorized {
		t.Fatalf("cookie with WEB_APP_ORIGIN unset: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/x", bearer: tok}).send(r); code != http.StatusOK {
		t.Fatalf("bearer with WEB_APP_ORIGIN unset: %d", code)
	}
}

func TestSetWebCookie_IsHttpOnlySecureStrict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	SetWebCookie(c, WebAccessCookie, "v", "/api", time.Hour)
	ClearWebCookie(c, WebRefreshCookie, "/api/auth/web")
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatalf("cookies: %v", cookies)
	}
	for _, ck := range cookies {
		if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Domain != "" {
			t.Fatalf("cookie %s: HttpOnly=%v Secure=%v SameSite=%v Domain=%q", ck.Name, ck.HttpOnly, ck.Secure, ck.SameSite, ck.Domain)
		}
	}
	if cookies[0].Path != "/api" || cookies[0].MaxAge != 3600 {
		t.Fatalf("access cookie: path %q max-age %d", cookies[0].Path, cookies[0].MaxAge)
	}
	if cookies[1].Path != "/api/auth/web" || cookies[1].MaxAge >= 0 {
		t.Fatalf("cleared cookie: path %q max-age %d", cookies[1].Path, cookies[1].MaxAge)
	}
}

// A sibling or apex site can plant a second tsa_at with a Domain attribute; a
// request carrying two is treated as carrying no session.
func TestAuth_TwoAccessCookiesAreNoSession(t *testing.T) {
	r, tok := cookieRig(t)
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.AddCookie(&http.Cookie{Name: WebAccessCookie, Value: tok})
	req.AddCookie(&http.Cookie{Name: WebAccessCookie, Value: tok})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), `"code"`) {
		t.Fatalf("two access cookies: %d %s", w.Code, w.Body.String())
	}
}

func TestAuth_MarksWebSessions(t *testing.T) {
	r, tok := cookieRig(t)
	if code := (cookieReq{method: http.MethodGet, path: "/api/web", cookie: tok}).send(r); code != http.StatusOK {
		t.Fatalf("a cookie session is not marked: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/web", bearer: tok, cookie: tok}).send(r); code != http.StatusNoContent {
		t.Fatalf("a bearer request is marked as a web session: %d", code)
	}
}

func TestRequireAdminRoles_RefuseAWebSession(t *testing.T) {
	r, tok := cookieRig(t)
	if code := (cookieReq{method: http.MethodGet, path: "/api/staff", cookie: tok}).send(r); code != http.StatusUnauthorized {
		t.Fatalf("RequireP2PAdmin with an admin's web session: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/staff", bearer: tok}).send(r); code != http.StatusOK {
		t.Fatalf("RequireP2PAdmin with the admin's bearer: %d", code)
	}
	config.DB.Exec(`UPDATE users SET role = 'super_admin'`)
	if code := (cookieReq{method: http.MethodGet, path: "/api/super", cookie: tok}).send(r); code != http.StatusUnauthorized {
		t.Fatalf("RequireSuperAdmin with a super admin's web session: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/super", bearer: tok}).send(r); code != http.StatusOK {
		t.Fatalf("RequireSuperAdmin with the super admin's bearer: %d", code)
	}
}

// A token minted for a web session carries WebTokenClaim. Sent as a bearer
// header it is still a web session: refused by every staff gate as no token,
// while an app token (no claim) is unchanged.
func TestWebToken_AsABearerIsRefusedByStaffGates(t *testing.T) {
	cfg := &config.Config{JWTSecret: "s3cret", WebAppOrigin: cookieRigOrigin}
	r, appTok := cookieRigWith(t, cfg)
	var id string
	config.DB.Raw(`SELECT id FROM users LIMIT 1`).Scan(&id)
	webTok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"userId": id, WebTokenClaim: true,
		"exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(cfg.JWTSecret))
	config.DB.Exec(`UPDATE users SET role = 'super_admin'`)

	for _, path := range []string{"/api/admin/x", "/api/staff", "/api/super"} {
		if code := (cookieReq{method: http.MethodGet, path: path, bearer: webTok}).send(r); code != http.StatusUnauthorized {
			t.Errorf("web token as a bearer on %s: %d, want 401", path, code)
		}
		if code := (cookieReq{method: http.MethodGet, path: path, bearer: appTok}).send(r); code != http.StatusOK {
			t.Errorf("app token on %s: %d, want 200", path, code)
		}
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/web", bearer: webTok}).send(r); code != http.StatusOK {
		t.Fatalf("a web token as a bearer is not marked as a web session: %d", code)
	}
	if code := (cookieReq{method: http.MethodGet, path: "/api/web", bearer: appTok}).send(r); code != http.StatusNoContent {
		t.Fatalf("an app token is marked as a web session: %d", code)
	}
}
