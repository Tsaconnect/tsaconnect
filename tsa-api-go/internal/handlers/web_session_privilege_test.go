package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Spec §4: a web session never reaches an admin power, even for an admin's
// account. The admin console signs in with a bearer token; that path is
// unchanged.

func getWith(r *gin.Engine, path string, cookies jar, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range cookies {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestWebSession_AnAdminsCookieCannotListUsers(t *testing.T) {
	rig := newWebAuthRig(t)
	rig.db.Model(&models.User{}).Where("id = ?", rig.user.ID).Update("role", models.RoleAdmin)
	session := rig.signIn(t, false)

	r := gin.New()
	r.GET("/api/users", middleware.Auth(rig.h.Config), rig.h.GetAllUsers)
	r.GET("/api/users/:id", middleware.Auth(rig.h.Config), rig.h.GetUserByID)

	for _, path := range []string{"/api/users", "/api/users/" + rig.user.ID.String()} {
		if w := getWith(r, path, jar{middleware.WebAccessCookie: session[middleware.WebAccessCookie]}, ""); w.Code != http.StatusForbidden {
			t.Fatalf("GET %s with an admin's web session: %d %s", path, w.Code, w.Body.String())
		}
	}

	bearer, err := generateToken(rig.user.ID, rig.user.Email, rig.h.Config.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/users", "/api/users/" + rig.user.ID.String()} {
		if w := getWith(r, path, nil, bearer); w.Code != http.StatusOK {
			t.Fatalf("GET %s with the admin's bearer: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestWebSession_AnAdminsCookieGetsNoOrderOverride(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	admin := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	db.Model(&models.User{}).Where("id = ?", admin.ID).Update("role", models.RoleAdmin)
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	cfg := &config.Config{JWTSecret: "s3cret", WebAppOrigin: webAppOrigin}
	tok, err := generateToken(admin.ID, admin.Email, cfg.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.GET("/api/orders/:id", middleware.Auth(cfg), ch.GetOrderDetail)
	path := "/api/orders/" + order.ID.String()

	if w := getWith(r, path, jar{middleware.WebAccessCookie: tok}, ""); w.Code != http.StatusForbidden {
		t.Fatalf("an admin's web session read someone else's order: %d %s", w.Code, w.Body.String())
	}
	if w := getWith(r, path, nil, tok); w.Code != http.StatusOK {
		t.Fatalf("the admin's bearer: %d %s", w.Code, w.Body.String())
	}
}

func TestWebSession_ActingUserHasNoStaffRole(t *testing.T) {
	cfg := &config.Config{JWTSecret: "s3cret", WebAppOrigin: webAppOrigin}
	rig := newWebAuthRig(t)
	rig.db.Model(&models.User{}).Where("id = ?", rig.user.ID).Update("role", models.RoleSuperAdmin)
	tok, _ := generateToken(rig.user.ID, rig.user.Email, cfg.JWTSecret)

	seen := map[string]string{}
	r := gin.New()
	r.GET("/api/x", middleware.Auth(cfg), func(c *gin.Context) {
		u := getUserFromContext(c)
		seen[c.GetHeader("X-Case")] = actingUser(c, u).Role
		if hasAdminPrivilege(c, u) || hasSuperAdminPrivilege(c, u) {
			seen[c.GetHeader("X-Case")] += "+privileged"
		}
		c.Status(http.StatusOK)
	})
	for name, cookie := range map[string]bool{"cookie": true, "bearer": false} {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.Header.Set("X-Case", name)
		if cookie {
			req.AddCookie(&http.Cookie{Name: middleware.WebAccessCookie, Value: tok})
		} else {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	if seen["cookie"] != models.RoleUser || seen["bearer"] != models.RoleSuperAdmin+"+privileged" {
		t.Fatalf("roles seen: %v", seen)
	}
}

// A web access token is a web session wherever it is sent: presented as a
// bearer header it still gets no staff power. An app token is unchanged.
func TestWebSession_AWebTokenAsABearerIsStillAWebSession(t *testing.T) {
	rig := newWebAuthRig(t)
	rig.db.Model(&models.User{}).Where("id = ?", rig.user.ID).Update("role", models.RoleSuperAdmin)
	session := rig.signIn(t, false)

	r := gin.New()
	r.GET("/api/users", middleware.Auth(rig.h.Config), rig.h.GetAllUsers)
	r.GET("/api/admin/x", middleware.AdminAuth(rig.h.Config), func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/api/staff", middleware.Auth(rig.h.Config), middleware.RequireP2PAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/api/super", middleware.Auth(rig.h.Config), middleware.RequireSuperAdmin(), func(c *gin.Context) { c.Status(http.StatusOK) })

	check := func(label, bearer string, want map[string]int) {
		t.Helper()
		for path, code := range want {
			if w := getWith(r, path, nil, bearer); w.Code != code {
				t.Errorf("%s bearer on %s: %d %s, want %d", label, path, w.Code, w.Body.String(), code)
			}
		}
	}
	refused := map[string]int{"/api/admin/x": http.StatusUnauthorized, "/api/staff": http.StatusUnauthorized,
		"/api/super": http.StatusUnauthorized, "/api/users": http.StatusForbidden}
	check("web", session[middleware.WebAccessCookie], refused)

	// A refreshed web session is still a web session.
	if w := session.do(rig.r, http.MethodPost, "/api/auth/web/refresh", webReq{}); w.Code != http.StatusOK {
		t.Fatalf("refresh %d %s", w.Code, w.Body.String())
	}
	check("refreshed web", session[middleware.WebAccessCookie], refused)

	app, _, err := rig.h.issueSession(rig.user, appSession)
	if err != nil {
		t.Fatal(err)
	}
	check("app", app, map[string]int{"/api/admin/x": http.StatusOK, "/api/staff": http.StatusOK,
		"/api/super": http.StatusOK, "/api/users": http.StatusOK})
}

// A web refresh token rotates only on the web, so it cannot be traded at the
// app's refresh endpoint for a token without the web claim; nor can an app
// refresh token be planted as the web's refresh cookie.
func TestWebSession_RefreshTokensStayOnTheirFrontDoor(t *testing.T) {
	rig := newWebAuthRig(t)
	session := rig.signIn(t, false)
	r := gin.New()
	r.POST("/api/auth/refresh", rig.h.RefreshToken)
	post := func(refresh string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", strings.NewReader(`{"refreshToken":"`+refresh+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	if w := post(session[middleware.WebRefreshCookie]); w.Code != http.StatusUnauthorized {
		t.Fatalf("a web refresh token at the app's refresh: %d %s", w.Code, w.Body.String())
	}

	_, appRefresh, err := rig.h.issueSession(rig.user, appSession)
	if err != nil {
		t.Fatal(err)
	}
	planted := jar{middleware.WebRefreshCookie: appRefresh}
	if w := planted.do(rig.r, http.MethodPost, "/api/auth/web/refresh", webReq{}); w.Code != http.StatusUnauthorized ||
		!strings.Contains(w.Body.String(), `"code":"session_expired"`) {
		t.Fatalf("an app refresh token at the web's refresh: %d %s", w.Code, w.Body.String())
	}
	if w := post(appRefresh); w.Code != http.StatusOK {
		t.Fatalf("the app's own refresh: %d %s", w.Code, w.Body.String())
	}
}
