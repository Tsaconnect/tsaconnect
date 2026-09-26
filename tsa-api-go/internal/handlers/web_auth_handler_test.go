package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/middleware"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// webAuthUsersDDL is every column models.User carries (the same fixture as
// routes/pay_routes_test.go), since GORM's Create names every mapped column.
const webAuthUsersDDL = `CREATE TABLE users (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	username TEXT NOT NULL UNIQUE,
	email TEXT NOT NULL UNIQUE,
	password TEXT NOT NULL,
	role TEXT DEFAULT 'user',
	phone_number TEXT,
	country TEXT, state TEXT, city TEXT, address TEXT,
	profile_photo TEXT, referral_code TEXT, referred_by TEXT,
	didit_session_id TEXT,
	verification_status TEXT DEFAULT 'pending',
	verification_notes TEXT, account_status TEXT DEFAULT 'active',
	last_login DATETIME, login_attempts INTEGER DEFAULT 0,
	lock_until DATETIME, wallet_address TEXT,
	seed_phrase_backed_up INTEGER DEFAULT 0,
	mute_notifications INTEGER DEFAULT 0,
	mute_email INTEGER DEFAULT 0,
	tp_balance REAL DEFAULT 0,
	cashback_balance REAL DEFAULT 0,
	last_settled_global_index INTEGER,
	email_verified INTEGER DEFAULT 0,
	deleted_at DATETIME, submitted_for_verification_at DATETIME,
	created_at DATETIME, updated_at DATETIME,
	suspension_reason TEXT,
	suspended_at DATETIME,
	suspended_by TEXT,
	suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
)`

const webAppOrigin = "https://app.test"

type webMailer struct {
	to, message string
	sent        int
	err         error
}

func (m *webMailer) Send(to, _, _, _, message, _, _ string) error {
	m.to, m.message = to, message
	m.sent++
	return m.err
}

func (m *webMailer) code(t *testing.T) string {
	t.Helper()
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(m.message)
	if code == "" {
		t.Fatalf("no code in the email: %q", m.message)
	}
	return code
}

type webAuthRig struct {
	r    *gin.Engine
	h    *Handlers
	db   *gorm.DB
	mail *webMailer
	user *models.User
}

func newWebAuthRig(t *testing.T) *webAuthRig {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{webAuthUsersDDL, refreshTokensTestDDL} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.AutoMigrate(&models.WebSigninChallenge{}, &models.WebTrustedBrowser{}); err != nil {
		t.Fatal(err)
	}
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })

	hashed, err := bcrypt.GenerateFromPassword([]byte("correct-horse"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	user := &models.User{
		ID: uuid.New(), Name: "Ada Obi", Username: "ada", Email: "ada@skyair.ng",
		Password: string(hashed), Role: "user", AccountStatus: models.AccountStatusActive,
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}

	mail := &webMailer{}
	h := &Handlers{Config: &config.Config{JWTSecret: "s3cret", WebAppOrigin: webAppOrigin}, WebCodeMailer: mail}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	web := r.Group("/api/auth/web", middleware.WebAuthGuard(h.Config))
	web.POST("/login", h.WebLogin)
	web.POST("/verify", h.WebVerify)
	web.POST("/refresh", h.WebRefresh)
	web.POST("/logout", h.WebLogout)
	web.GET("/me", middleware.Auth(h.Config), h.WebMe)
	return &webAuthRig{r: r, h: h, db: db, mail: mail, user: user}
}

// jar carries Set-Cookie values between requests, as the browser would.
type jar map[string]string

type webReq struct {
	body      any
	noCSRF    bool
	origin    string
	fetchSite string
}

func (j jar) do(r *gin.Engine, method, path string, o webReq) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if o.body != nil {
		_ = json.NewEncoder(&buf).Encode(o.body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if !o.noCSRF {
		req.Header.Set(middleware.WebCSRFHeader, "1")
	}
	if o.origin != "" {
		req.Header.Set("Origin", o.origin)
	}
	if o.fetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", o.fetchSite)
	}
	for k, v := range j {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(j, c.Name)
		} else {
			j[c.Name] = c.Value
		}
	}
	return w
}

func (rig *webAuthRig) login(j jar, password string) *httptest.ResponseRecorder {
	return j.do(rig.r, http.MethodPost, "/api/auth/web/login", webReq{body: map[string]string{"email": "ada@skyair.ng", "password": password}})
}

func (rig *webAuthRig) verify(j jar, code string, remember bool) *httptest.ResponseRecorder {
	return j.do(rig.r, http.MethodPost, "/api/auth/web/verify", webReq{body: map[string]any{"code": code, "rememberBrowser": remember}})
}

// signIn runs login + verify on a new browser and returns its jar.
func (rig *webAuthRig) signIn(t *testing.T, remember bool) jar {
	t.Helper()
	j := jar{}
	if w := rig.login(j, "correct-horse"); w.Code != http.StatusOK {
		t.Fatalf("login %d %s", w.Code, w.Body.String())
	}
	if w := rig.verify(j, rig.mail.code(t), remember); w.Code != http.StatusOK {
		t.Fatalf("verify %d %s", w.Code, w.Body.String())
	}
	return j
}

func wrongCode(right string) string {
	if right == "000000" {
		return "111111"
	}
	return "000000"
}

func TestWebAuth_NewBrowserNeedsTheEmailedCode(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	w := rig.login(j, "correct-horse")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"sentTo":"a***@skyair.ng"`) || j[middleware.WebAccessCookie] != "" || j[middleware.WebChallengeCookie] == "" {
		t.Fatalf("login %d %s jar %v: a new browser must get a challenge, not a session", w.Code, w.Body.String(), j)
	}
	if rig.mail.to != "ada@skyair.ng" {
		t.Fatalf("code sent to %q", rig.mail.to)
	}
	code := rig.mail.code(t)
	if w := rig.verify(j, wrongCode(code), false); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"code":"code_invalid"`) || j[middleware.WebAccessCookie] != "" {
		t.Fatalf("wrong code: %d %s", w.Code, w.Body.String())
	}
	w = rig.verify(j, code, true)
	if w.Code != http.StatusOK || j[middleware.WebAccessCookie] == "" || j[middleware.WebRefreshCookie] == "" || j[middleware.WebTrustedCookie] == "" || j[middleware.WebChallengeCookie] != "" {
		t.Fatalf("verify %d %s jar %v", w.Code, w.Body.String(), j)
	}
	if !strings.Contains(w.Body.String(), `"email":"ada@skyair.ng"`) {
		t.Fatalf("verify body: %s", w.Body.String())
	}
	if w := j.do(rig.r, http.MethodGet, "/api/auth/web/me", webReq{}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"user":{`) || !strings.Contains(w.Body.String(), "ada@skyair.ng") {
		t.Fatalf("me %d %s", w.Code, w.Body.String())
	}
}

func TestWebAuth_CookiesAreHttpOnlySecureStrictOnTheirPaths(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	login := rig.login(j, "correct-horse")
	verify := rig.verify(j, rig.mail.code(t), true)
	want := map[string]struct {
		path   string
		maxAge int
	}{
		middleware.WebChallengeCookie: {"/api/auth/web", 15 * 60},
		middleware.WebAccessCookie:    {"/api", 60 * 60},
		middleware.WebRefreshCookie:   {"/api/auth/web", 30 * 24 * 60 * 60},
		middleware.WebTrustedCookie:   {"/api/auth/web", 90 * 24 * 60 * 60},
	}
	seen := map[string]bool{}
	for _, c := range append(login.Result().Cookies(), verify.Result().Cookies()...) {
		if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Domain != "" {
			t.Fatalf("cookie %s is not HttpOnly/Secure/Strict/host-only: %+v", c.Name, c)
		}
		w, ok := want[c.Name]
		if !ok {
			t.Fatalf("unexpected cookie %s", c.Name)
		}
		if c.Path != w.path {
			t.Fatalf("cookie %s path %q, want %q", c.Name, c.Path, w.path)
		}
		if c.MaxAge > 0 {
			if c.MaxAge != w.maxAge {
				t.Fatalf("cookie %s max-age %d, want %d", c.Name, c.MaxAge, w.maxAge)
			}
			seen[c.Name] = true
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("cookies set: %v", seen)
	}
}

func TestWebAuth_ATrustedBrowserSkipsTheCode(t *testing.T) {
	rig := newWebAuthRig(t)
	j := rig.signIn(t, true)
	if w := j.do(rig.r, http.MethodPost, "/api/auth/web/logout", webReq{}); w.Code != http.StatusOK {
		t.Fatalf("logout %d", w.Code)
	}
	if j[middleware.WebAccessCookie] != "" || j[middleware.WebRefreshCookie] != "" || j[middleware.WebTrustedCookie] == "" {
		t.Fatalf("after logout jar %v: the session goes, the trusted browser stays", j)
	}
	rig.mail.message, rig.mail.sent = "", 0
	w := rig.login(j, "correct-horse")
	if w.Code != http.StatusOK || j[middleware.WebAccessCookie] == "" || rig.mail.sent != 0 || !strings.Contains(w.Body.String(), `"user":{`) {
		t.Fatalf("trusted login %d %s: no code, straight in", w.Code, w.Body.String())
	}
}

// Only the hash of the trusted-browser token is stored, and a cookie that does
// not hash to a stored row for this user is not trusted.
func TestWebAuth_ATrustedBrowserIsTheTokenNotJustTheUser(t *testing.T) {
	rig := newWebAuthRig(t)
	j := rig.signIn(t, true)
	raw := j[middleware.WebTrustedCookie]
	var tb models.WebTrustedBrowser
	if err := rig.db.First(&tb, "user_id = ?", rig.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if tb.TokenHash == raw || tb.TokenHash != hashToken(raw) {
		t.Fatalf("stored %q for cookie %q: must be the token's sha256", tb.TokenHash, raw)
	}

	for name, cookie := range map[string]string{"forged": "forged-token", "the stored hash": tb.TokenHash} {
		other := jar{middleware.WebTrustedCookie: cookie}
		rig.mail.sent = 0
		if w := rig.login(other, "correct-horse"); w.Code != http.StatusOK || other[middleware.WebAccessCookie] != "" || rig.mail.sent != 1 {
			t.Fatalf("%s trusted cookie: %d %s jar %v", name, w.Code, w.Body.String(), other)
		}
	}

	rig.db.Model(&models.WebTrustedBrowser{}).Where("id = ?", tb.ID).Update("expires_at", time.Now().Add(-time.Minute))
	expired := jar{middleware.WebTrustedCookie: raw}
	if w := rig.login(expired, "correct-horse"); w.Code != http.StatusOK || expired[middleware.WebAccessCookie] != "" {
		t.Fatalf("expired trusted browser: %d jar %v", w.Code, expired)
	}
}

func TestWebAuth_ForgettingTheBrowserIsTheDefault(t *testing.T) {
	rig := newWebAuthRig(t)
	j := rig.signIn(t, false)
	var n int64
	rig.db.Model(&models.WebTrustedBrowser{}).Count(&n)
	if n != 0 || j[middleware.WebTrustedCookie] != "" {
		t.Fatalf("rememberBrowser=false still trusted it: %d rows, jar %v", n, j)
	}
}

func TestWebAuth_RefreshAndLogoutNeedTheHeaderAndRotate(t *testing.T) {
	rig := newWebAuthRig(t)
	j := rig.signIn(t, false)
	old := j[middleware.WebRefreshCookie]
	if w := j.do(rig.r, http.MethodPost, "/api/auth/web/refresh", webReq{noCSRF: true}); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"csrf"`) {
		t.Fatalf("refresh without the header: %d %s", w.Code, w.Body.String())
	}
	if w := j.do(rig.r, http.MethodPost, "/api/auth/web/refresh", webReq{}); w.Code != http.StatusOK || j[middleware.WebRefreshCookie] == old {
		t.Fatalf("refresh %d: the refresh token must rotate", w.Code)
	}
	replay := jar{middleware.WebRefreshCookie: old, middleware.WebAccessCookie: "kept"}
	w := replay.do(rig.r, http.MethodPost, "/api/auth/web/refresh", webReq{})
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"session_expired"`) {
		t.Fatalf("a rotated-out refresh token still worked: %d", w.Code)
	}
	// A losing concurrent refresh must not sign the browser out: the winner's
	// cookies are already in it.
	if len(w.Result().Cookies()) != 0 || replay[middleware.WebAccessCookie] != "kept" {
		t.Fatalf("a failed refresh cleared cookies: %v", w.Result().Cookies())
	}

	if w := j.do(rig.r, http.MethodPost, "/api/auth/web/logout", webReq{noCSRF: true}); w.Code != http.StatusForbidden {
		t.Fatalf("logout without the header: %d", w.Code)
	}
	live := j[middleware.WebRefreshCookie]
	if w := j.do(rig.r, http.MethodPost, "/api/auth/web/logout", webReq{}); w.Code != http.StatusOK {
		t.Fatalf("logout: %d", w.Code)
	}
	var revoked bool
	rig.db.Model(&models.RefreshToken{}).Select("revoked").Where("token_hash = ?", hashToken(live)).Scan(&revoked)
	if !revoked {
		t.Fatal("logout left its refresh token live")
	}
	if w := (jar{middleware.WebRefreshCookie: live}).do(rig.r, http.MethodPost, "/api/auth/web/refresh", webReq{}); w.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: %d", w.Code)
	}
}

func TestWebAuth_LoginWithoutTheHeaderOrFromAnotherOriginIsRefused(t *testing.T) {
	rig := newWebAuthRig(t)
	body := map[string]string{"email": "ada@skyair.ng", "password": "correct-horse"}
	cases := []struct {
		name string
		req  webReq
		code string
	}{
		{"no header", webReq{body: body, noCSRF: true}, "csrf"},
		{"sibling origin", webReq{body: body, origin: "https://admin.tsaconnectworld.com"}, "origin"},
		{"same-site fetch", webReq{body: body, fetchSite: "same-site"}, "origin"},
	}
	for _, tc := range cases {
		j := jar{}
		w := j.do(rig.r, http.MethodPost, "/api/auth/web/login", tc.req)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) || rig.mail.sent != 0 || len(j) != 0 {
			t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
	j := jar{}
	if w := j.do(rig.r, http.MethodPost, "/api/auth/web/login", webReq{body: body, origin: webAppOrigin, fetchSite: "same-origin"}); w.Code != http.StatusOK {
		t.Fatalf("from the app's own page: %d %s", w.Code, w.Body.String())
	}
}

func TestWebAuth_WrongPasswordAndLockout(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	w := rig.login(j, "guess")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"invalid_credentials"`) {
		t.Fatalf("wrong password: %d %s", w.Code, w.Body.String())
	}
	for i := 0; i < 4; i++ {
		rig.login(j, "guess")
	}
	w = rig.login(j, "correct-horse")
	if w.Code != http.StatusLocked || !strings.Contains(w.Body.String(), `"code":"account_locked"`) || rig.mail.sent != 0 {
		t.Fatalf("locked account: %d %s", w.Code, w.Body.String())
	}
	unknown := j.do(rig.r, http.MethodPost, "/api/auth/web/login", webReq{body: map[string]string{"email": "nobody@skyair.ng", "password": "x"}})
	if unknown.Code != http.StatusUnauthorized || !strings.Contains(unknown.Body.String(), `"code":"invalid_credentials"`) {
		t.Fatalf("unknown user: %d %s", unknown.Code, unknown.Body.String())
	}
}

// The password was right, so the lockout counter resets even though the
// session waits on the code.
func TestWebAuth_ARightPasswordResetsTheLockoutCounter(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	for i := 0; i < 3; i++ {
		rig.login(j, "guess")
	}
	if w := rig.login(j, "correct-horse"); w.Code != http.StatusOK {
		t.Fatalf("login %d", w.Code)
	}
	var u models.User
	rig.db.First(&u, "id = ?", rig.user.ID)
	if u.LoginAttempts != 0 || u.LockUntil != nil {
		t.Fatalf("attempts %d lock %v after the right password", u.LoginAttempts, u.LockUntil)
	}
	if u.LastLogin != nil {
		t.Fatal("a pending challenge is not a login yet")
	}
}

// Review Focus 5.
func TestWebAuth_SignInWithoutAMailerIsAClear503(t *testing.T) {
	rig := newWebAuthRig(t)
	rig.mail.err = errors.New("mailjet down")
	j := jar{}
	w := rig.login(j, "correct-horse")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"code_unavailable"`) || j[middleware.WebChallengeCookie] != "" {
		t.Fatalf("mailer down: status %d body %s jar %v", w.Code, w.Body.String(), j)
	}
	var n int64
	rig.db.Model(&models.WebSigninChallenge{}).Count(&n)
	if n != 0 {
		t.Fatalf("a challenge nobody got the code for was kept: %d", n)
	}

	rig.h.WebCodeMailer = nil
	w = rig.login(j, "correct-horse")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"code_unavailable"`) || len(j) != 0 {
		t.Fatalf("no mailer: status %d body %s jar %v", w.Code, w.Body.String(), j)
	}
}

func TestWebAuth_AnExpiredChallengeIsRefused(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	rig.login(j, "correct-horse")
	rig.db.Model(&models.WebSigninChallenge{}).Where("user_id = ?", rig.user.ID).Update("expires_at", time.Now().Add(-time.Minute))
	w := rig.verify(j, rig.mail.code(t), false)
	if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), `"code":"challenge_expired"`) || j[middleware.WebAccessCookie] != "" {
		t.Fatalf("expired: %d %s", w.Code, w.Body.String())
	}
	if w := (jar{}).do(rig.r, http.MethodPost, "/api/auth/web/verify", webReq{body: map[string]any{"code": "123456"}}); w.Code != http.StatusGone {
		t.Fatalf("no challenge cookie: %d", w.Code)
	}
}

// The redeem steps themselves (spend, compare, drop, single-use claim) are
// tested once, on services.RedeemEmailCode; this is how verify answers each.
func TestWebAuth_FiveWrongCodesSpendTheChallenge(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	rig.login(j, "correct-horse")
	code := rig.mail.code(t)
	for i := 0; i < 5; i++ {
		if w := rig.verify(j, wrongCode(code), false); w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("wrong code %d: %d", i+1, w.Code)
		}
	}
	if w := rig.verify(j, code, false); w.Code != http.StatusGone || j[middleware.WebAccessCookie] != "" {
		t.Fatalf("the right code after five wrong ones: %d %s", w.Code, w.Body.String())
	}
}

func TestWebAuth_AChallengeIsUsedOnce(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	rig.login(j, "correct-horse")
	code := rig.mail.code(t)
	challenge := j[middleware.WebChallengeCookie]
	if w := rig.verify(j, code, false); w.Code != http.StatusOK {
		t.Fatalf("verify: %d", w.Code)
	}
	again := jar{middleware.WebChallengeCookie: challenge}
	if w := rig.verify(again, code, false); w.Code != http.StatusGone || again[middleware.WebAccessCookie] != "" {
		t.Fatalf("the same challenge twice: %d %s", w.Code, w.Body.String())
	}
}

func TestWebAuth_VerifyRefusesAnAccountSuspendedSinceLogin(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	if w := rig.login(j, "correct-horse"); w.Code != http.StatusOK {
		t.Fatalf("login %d", w.Code)
	}
	rig.db.Model(&models.User{}).Where("id = ?", rig.user.ID).Update("account_status", models.AccountStatusSuspended)
	w := rig.verify(j, rig.mail.code(t), true)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"account_not_active"`) ||
		j[middleware.WebAccessCookie] != "" || j[middleware.WebTrustedCookie] != "" {
		t.Fatalf("verify for a suspended account: %d %s jar %v", w.Code, w.Body.String(), j)
	}
	if n := liveRefreshTokens(t, rig.db, rig.user.ID); n != 0 {
		t.Fatalf("a suspended account got %d refresh tokens", n)
	}
}

func TestWebAuth_RefreshOnAnInactiveAccountIsSessionExpired(t *testing.T) {
	rig := newWebAuthRig(t)
	j := rig.signIn(t, false)
	rig.db.Model(&models.User{}).Where("id = ?", rig.user.ID).Update("account_status", models.AccountStatusSuspended)
	w := j.do(rig.r, http.MethodPost, "/api/auth/web/refresh", webReq{})
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"session_expired"`) {
		t.Fatalf("refresh on a suspended account: %d %s", w.Code, w.Body.String())
	}
}

// A trusted-browser cookie that trusts no account any more is cleared at the
// next sign-in; one that another account on the browser still trusts is kept.
func TestWebAuth_AStaleTrustedCookieIsCleared(t *testing.T) {
	rig := newWebAuthRig(t)
	j := rig.signIn(t, true)
	raw := j[middleware.WebTrustedCookie]
	rig.db.Model(&models.WebTrustedBrowser{}).Where("user_id = ?", rig.user.ID).Update("expires_at", time.Now().Add(-time.Minute))
	stale := jar{middleware.WebTrustedCookie: raw}
	w := rig.login(stale, "correct-horse")
	if w.Code != http.StatusOK || stale[middleware.WebTrustedCookie] != "" || stale[middleware.WebChallengeCookie] == "" {
		t.Fatalf("stale trusted cookie: %d jar %v", w.Code, stale)
	}
	forged := jar{middleware.WebTrustedCookie: "forged-token"}
	if w := rig.login(forged, "correct-horse"); w.Code != http.StatusOK || forged[middleware.WebTrustedCookie] != "" {
		t.Fatalf("forged trusted cookie: %d jar %v", w.Code, forged)
	}

	// Ada's browser is trusted again; Ben signs in on it and must not wipe that.
	rig.db.Model(&models.WebTrustedBrowser{}).Where("user_id = ?", rig.user.ID).Update("expires_at", time.Now().Add(time.Hour))
	hashed, _ := bcrypt.GenerateFromPassword([]byte("other-horse"), bcrypt.MinCost)
	ben := &models.User{ID: uuid.New(), Name: "Ben", Username: "ben", Email: "ben@skyair.ng", Password: string(hashed),
		Role: "user", AccountStatus: models.AccountStatusActive}
	if err := rig.db.Create(ben).Error; err != nil {
		t.Fatal(err)
	}
	shared := jar{middleware.WebTrustedCookie: raw}
	w = shared.do(rig.r, http.MethodPost, "/api/auth/web/login", webReq{body: map[string]string{"email": "ben@skyair.ng", "password": "other-horse"}})
	if w.Code != http.StatusOK || shared[middleware.WebTrustedCookie] != raw {
		t.Fatalf("another account's trusted cookie was cleared: %d jar %v", w.Code, shared)
	}
}

func TestWebAuth_LogoutAbandonsAPendingSignIn(t *testing.T) {
	rig := newWebAuthRig(t)
	j := jar{}
	rig.login(j, "correct-horse")
	if j[middleware.WebChallengeCookie] == "" {
		t.Fatal("no challenge cookie after login")
	}
	if w := j.do(rig.r, http.MethodPost, "/api/auth/web/logout", webReq{}); w.Code != http.StatusOK || j[middleware.WebChallengeCookie] != "" {
		t.Fatalf("logout %d jar %v: the pending challenge cookie must go", w.Code, j)
	}
}

func TestWebAuth_MeNeedsTheSession(t *testing.T) {
	rig := newWebAuthRig(t)
	if w := (jar{}).do(rig.r, http.MethodGet, "/api/auth/web/me", webReq{}); w.Code != http.StatusUnauthorized {
		t.Fatalf("me without a session: %d", w.Code)
	}
}

// ---- A new password or a deleted account forgets every trusted browser ----
// ---- and ends every session ----

func liveRefreshTokens(t *testing.T, db *gorm.DB, userID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.RefreshToken{}).Where("user_id = ? AND revoked = ?", userID, false).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func trustedCount(t *testing.T, db *gorm.DB, userID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.WebTrustedBrowser{}).Where("user_id = ?", userID).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestWebAuth_ChangingThePasswordForgetsTrustedBrowsers(t *testing.T) {
	rig := newWebAuthRig(t)
	rig.signIn(t, true)
	if liveRefreshTokens(t, rig.db, rig.user.ID) == 0 {
		t.Fatal("setup: no live refresh token")
	}
	if trustedCount(t, rig.db, rig.user.ID) != 1 {
		t.Fatal("setup: no trusted browser")
	}
	var user models.User
	rig.db.First(&user, "id = ?", rig.user.ID)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/api/users/change-password", strings.NewReader(`{"currentPassword":"correct-horse","newPassword":"N3wPassw0rd"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", user)
	rig.h.ChangePassword(c)
	if w.Code != http.StatusOK {
		t.Fatalf("change password: %d %s", w.Code, w.Body.String())
	}
	if n := trustedCount(t, rig.db, rig.user.ID); n != 0 {
		t.Fatalf("%d trusted browsers survived a password change", n)
	}
	if n := liveRefreshTokens(t, rig.db, rig.user.ID); n != 0 {
		t.Fatalf("%d refresh tokens survived a password change", n)
	}
}

func TestWebAuth_ResettingThePasswordForgetsTrustedBrowsers(t *testing.T) {
	rig := newWebAuthRig(t)
	rig.signIn(t, true)
	if liveRefreshTokens(t, rig.db, rig.user.ID) == 0 {
		t.Fatal("setup: no live refresh token")
	}
	if err := rig.db.Exec(`CREATE TABLE password_resets (id TEXT PRIMARY KEY, user_id TEXT, email TEXT, code TEXT, expires_at DATETIME, attempts INTEGER DEFAULT 0, used INTEGER DEFAULT 0, created_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	codeHash, _ := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.MinCost)
	if err := rig.db.Exec(`INSERT INTO password_resets (id, user_id, email, code, expires_at, created_at) VALUES (?,?,?,?,?,?)`,
		uuid.New().String(), rig.user.ID.String(), rig.user.Email, string(codeHash), time.Now().Add(10*time.Minute), time.Now()).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", strings.NewReader(`{"email":"ada@skyair.ng","code":"123456","newPassword":"N3wPassw0rd"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	rig.h.ResetPassword(c)
	if w.Code != http.StatusOK {
		t.Fatalf("reset password: %d %s", w.Code, w.Body.String())
	}
	if n := trustedCount(t, rig.db, rig.user.ID); n != 0 {
		t.Fatalf("%d trusted browsers survived a password reset", n)
	}
	if n := liveRefreshTokens(t, rig.db, rig.user.ID); n != 0 {
		t.Fatalf("%d refresh tokens survived a password reset", n)
	}
}

func TestRequestAccountDeletion_ForgetsTrustedBrowsers(t *testing.T) {
	db := setupDeletionTestDB(t)
	u := mkDeletionUser(t, db, "leaver", "user")
	if err := db.Create(&models.WebTrustedBrowser{UserID: u.ID, TokenHash: hashToken("tb"), ExpiresAt: time.Now().Add(time.Hour), LastSeenAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.RefreshToken{ID: uuid.New(), UserID: u.ID, TokenHash: hashToken("rt"), ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	w, c := newDeletionContext(http.MethodPost, "/api/users/me/deletion", map[string]string{"password": "correct-horse"}, u)
	(&Handlers{}).RequestAccountDeletion(c)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if n := trustedCount(t, db, u.ID); n != 0 {
		t.Fatalf("%d trusted browsers survived account deletion", n)
	}
	if n := liveRefreshTokens(t, db, u.ID); n != 0 {
		t.Fatalf("%d refresh tokens survived account deletion", n)
	}
}

// ---- Racing logins, housekeeping, planted duplicate cookies ----

// plantChallengeOnCreate inserts another login's challenge for the user just
// before each of this request's challenge inserts (up to times), the way a
// concurrent login's commit would land between our delete and create.
func plantChallengeOnCreate(t *testing.T, rig *webAuthRig, times int) *int {
	t.Helper()
	planted := 0
	if err := rig.db.Callback().Create().Before("gorm:create").Register("test:racing_login", func(tx *gorm.DB) {
		if planted >= times || tx.Statement.Table != "web_signin_challenges" {
			return
		}
		planted++
		tx.Session(&gorm.Session{NewDB: true}).Exec(`INSERT INTO web_signin_challenges (id, user_id, code_hash, attempts, expires_at, created_at) VALUES (?,?,?,0,?,?)`,
			uuid.New().String(), rig.user.ID.String(), "other", time.Now().Add(time.Minute), time.Now())
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rig.db.Callback().Create().Remove("test:racing_login") })
	return &planted
}

func TestWebAuth_ALoginThatLosesTheRaceOnceTakesOver(t *testing.T) {
	rig := newWebAuthRig(t)
	planted := plantChallengeOnCreate(t, rig, 1)
	j := jar{}
	w := rig.login(j, "correct-horse")
	if *planted != 1 || w.Code != http.StatusOK || j[middleware.WebChallengeCookie] == "" {
		t.Fatalf("planted %d, login %d %s", *planted, w.Code, w.Body.String())
	}
	var rows []models.WebSigninChallenge
	rig.db.Where("user_id = ?", rig.user.ID).Find(&rows)
	if len(rows) != 1 || rows[0].ID.String() != j[middleware.WebChallengeCookie] {
		t.Fatalf("challenges after the retry: %+v, cookie %s", rows, j[middleware.WebChallengeCookie])
	}
	if w := rig.verify(j, rig.mail.code(t), false); w.Code != http.StatusOK {
		t.Fatalf("verify after the retry: %d %s", w.Code, w.Body.String())
	}
}

func TestWebAuth_ALoginThatKeepsLosingTheRaceIsBusy(t *testing.T) {
	rig := newWebAuthRig(t)
	planted := plantChallengeOnCreate(t, rig, 100)
	j := jar{}
	w := rig.login(j, "correct-horse")
	if *planted != 2 || w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"signin_busy"`) || len(j) != 0 || rig.mail.sent != 0 {
		t.Fatalf("planted %d, login %d %s jar %v", *planted, w.Code, w.Body.String(), j)
	}
}

func TestWebAuth_ANewChallengeDropsTheOldOnes(t *testing.T) {
	rig := newWebAuthRig(t)
	if err := rig.db.Create(&models.WebSigninChallenge{UserID: rig.user.ID, CodeHash: "old", ExpiresAt: time.Now().Add(-time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	j := jar{}
	rig.login(j, "correct-horse")
	var rows []models.WebSigninChallenge
	rig.db.Where("user_id = ?", rig.user.ID).Find(&rows)
	if len(rows) != 1 || rows[0].ID.String() != j[middleware.WebChallengeCookie] {
		t.Fatalf("challenges: %+v", rows)
	}
}

func TestWebAuth_TrustedBrowsersAreCappedAtTenLiveOnes(t *testing.T) {
	rig := newWebAuthRig(t)
	now := time.Now()
	for i := 0; i < 12; i++ { // live, oldest first: i=0 is 12 days old
		tb := models.WebTrustedBrowser{UserID: rig.user.ID, TokenHash: hashToken(fmt.Sprintf("live-%d", i)), ExpiresAt: now.Add(30 * 24 * time.Hour), CreatedAt: now.Add(-time.Duration(12-i) * 24 * time.Hour)}
		if err := rig.db.Create(&tb).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		tb := models.WebTrustedBrowser{UserID: rig.user.ID, TokenHash: hashToken(fmt.Sprintf("expired-%d", i)), ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-time.Hour)}
		if err := rig.db.Create(&tb).Error; err != nil {
			t.Fatal(err)
		}
	}
	j := rig.signIn(t, true)

	var kept []models.WebTrustedBrowser
	rig.db.Where("user_id = ?", rig.user.ID).Find(&kept)
	if len(kept) != 10 {
		t.Fatalf("%d trusted browsers kept, want 10", len(kept))
	}
	hashes := map[string]bool{}
	for _, tb := range kept {
		hashes[tb.TokenHash] = true
		if !tb.ExpiresAt.After(now) {
			t.Fatalf("an expired trusted browser survived: %+v", tb)
		}
	}
	if !hashes[hashToken(j[middleware.WebTrustedCookie])] {
		t.Fatal("the browser just trusted was pruned")
	}
	for i := 0; i < 3; i++ {
		if hashes[hashToken(fmt.Sprintf("live-%d", i))] {
			t.Fatalf("live-%d (one of the three oldest) survived", i)
		}
	}
}

func TestWebAuth_PlantedDuplicateCookiesAreRefused(t *testing.T) {
	rig := newWebAuthRig(t)
	j := rig.signIn(t, true)

	refresh := func(values ...string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/web/refresh", nil)
		req.Header.Set(middleware.WebCSRFHeader, "1")
		for _, v := range values {
			req.AddCookie(&http.Cookie{Name: middleware.WebRefreshCookie, Value: v})
		}
		w := httptest.NewRecorder()
		rig.r.ServeHTTP(w, req)
		return w.Code
	}
	if code := refresh("planted", j[middleware.WebRefreshCookie]); code != http.StatusUnauthorized {
		t.Fatalf("refresh with two refresh cookies: %d", code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/auth/web/login", strings.NewReader(`{"email":"ada@skyair.ng","password":"correct-horse"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(middleware.WebCSRFHeader, "1")
	req.AddCookie(&http.Cookie{Name: middleware.WebTrustedCookie, Value: j[middleware.WebTrustedCookie]})
	req.AddCookie(&http.Cookie{Name: middleware.WebTrustedCookie, Value: "planted"})
	w := httptest.NewRecorder()
	rig.mail.sent = 0
	rig.r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"challenge"`) || rig.mail.sent != 1 {
		t.Fatalf("login with two trusted-browser cookies must ask for the code: %d %s", w.Code, w.Body.String())
	}
}
