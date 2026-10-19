package routes

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// payRoutesUsersDDL is the users table Auth/AdminAuth need through
// config.DB — every column models.User carries, since GORM's Create issues
// an INSERT naming every mapped column (see the identical fixture in
// merchant_request_handler_test.go).
const payRoutesUsersDDL = `CREATE TABLE users (
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
	suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME, declared_given_names TEXT, declared_surname TEXT
)`

// newPayRoutesRig builds a real PayHandler over SQLite and runs it through
// the real SetupRoutes, exactly as production wires it — every other
// handler is nil, matching cart_routes_test.go's pattern. config.DB is set
// because Auth/AdminAuth/RequireP2PAdmin all read the package-level DB, not
// anything passed into SetupRoutes.
func newPayRoutesRig(t *testing.T) (*gin.Engine, *config.Config, *gorm.DB, *handlers.PayHandler) {
	t.Helper()
	return newPayRoutesRigWith(t, nil)
}

// payRoutesWiring turns on more of the handler before the routes are set up,
// as cmd/server does for an optional feature.
type payRoutesWiring func(db *gorm.DB, merchants *services.PayMerchantService, payments *services.PayPaymentService, payh *handlers.PayHandler)

// newPayRoutesRigWith is newPayRoutesRig with wire (nil: none) run on the
// handler before SetupRoutes.
func newPayRoutesRigWith(t *testing.T, wire payRoutesWiring) (*gin.Engine, *config.Config, *gorm.DB, *handlers.PayHandler) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(payRoutesUsersDDL).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{}, &models.PayAuditEvent{}, &models.PayOpenLink{}, &models.PayRetiredSlug{}); err != nil {
		t.Fatal(err)
	}
	config.DB = db

	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	merchants := services.NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	payments := services.NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now)
	payh := handlers.NewPayHandler(merchants, payments, nil, false).WithOpenLinks(payments)
	if wire != nil {
		wire(db, merchants, payments, payh)
	}

	cfg := &config.Config{JWTSecret: "pay-routes-test-secret"}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	SetupRoutes(r, cfg, &handlers.Handlers{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, payh)
	return r, cfg, db, payh
}

func payRoutesUser(t *testing.T, db *gorm.DB, role string) *models.User {
	t.Helper()
	u := &models.User{
		ID:       uuid.New(),
		Name:     "Test User",
		Username: "u_" + uuid.New().String()[:8],
		Email:    "u_" + uuid.New().String()[:8] + "@example.com",
		Password: "$2a$10$fakehash",
		Role:     role,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	return u
}

func payRoutesToken(t *testing.T, secret string, userID uuid.UUID) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"userId": userID.String(),
		"exp":    time.Now().Add(time.Hour).Unix(),
	})
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func payRoutesDo(r *gin.Engine, method, path, bearer string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Every gate this task wires stays wired through the real router: JWT auth
// on the app's checkout route, the P2P-admin role gate on the merchant
// admin console, and the merchant-key gate on the merchant API — proving
// the middleware's context keys are the ones the handlers actually read.
func TestPayRoutes_AuthAndKeyGatesAreWired(t *testing.T) {
	r, cfg, db, payh := newPayRoutesRig(t)

	if w := payRoutesDo(r, "GET", "/api/pay/checkout/"+uuid.New().String(), "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no jwt on checkout: %d %s", w.Code, w.Body.String())
	}

	userAcct := payRoutesUser(t, db, models.RoleUser)
	supportAcct := payRoutesUser(t, db, models.RoleSupport)
	userTok := payRoutesToken(t, cfg.JWTSecret, userAcct.ID)
	supportTok := payRoutesToken(t, cfg.JWTSecret, supportAcct.ID)

	if w := payRoutesDo(r, "GET", "/api/admin/pay/merchants", userTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("a plain user on admin merchants: %d %s", w.Code, w.Body.String())
	}
	if w := payRoutesDo(r, "GET", "/api/admin/pay/merchants", supportTok, nil); w.Code != http.StatusForbidden {
		t.Fatalf("support on admin merchants: %d %s", w.Code, w.Body.String())
	}

	if w := payRoutesDo(r, "POST", "/api/pay/v1/payments", "", map[string]any{"amount": 500_000, "reference": "R1"}); w.Code != http.StatusUnauthorized {
		t.Fatalf("no key create payment: %d %s", w.Code, w.Body.String())
	}

	m := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: uuid.New(), BusinessName: "Sky Air",
		Status: models.PayMerchantActive, SettlementMode: models.PaySettlementFiat, BankCode: "058",
		AccountNumber: "0123456789", CustomerFeeSharePercent: 100, MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000,
		WebhookSecretVersion: 1, ReturnDomains: "skyair.ng"}
	if err := db.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	keys, err := payh.MerchantService().IssueKeys(context.Background(), uuid.New(), m.ID, services.PayKeysTest)
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{"amount": 500_000, "currency": "NGN", "reference": "R1", "returnUrl": "https://skyair.ng/done"}
	if w := payRoutesDo(r, "POST", "/api/pay/v1/payments", keys.TestKey, body); w.Code != http.StatusCreated {
		t.Fatalf("a real test key create: %d %s", w.Code, w.Body.String())
	}

	// Revoking a merchant's credential is super admin only.
	revoke := "/api/admin/pay/merchants/" + m.ID.String() + "/revoke-keys"
	revokeBody := map[string]any{"scope": "test", "reason": "leaked"}
	adminTok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleAdmin).ID)
	if w := payRoutesDo(r, "POST", revoke, adminTok, revokeBody); w.Code != http.StatusForbidden {
		t.Fatalf("a plain admin revoking: %d %s", w.Code, w.Body.String())
	}
	superTok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleSuperAdmin).ID)
	if w := payRoutesDo(r, "POST", revoke, superTok, revokeBody); w.Code != http.StatusOK {
		t.Fatalf("a super admin revoking: %d %s", w.Code, w.Body.String())
	}
	if w := payRoutesDo(r, "POST", "/api/pay/v1/payments", keys.TestKey, body); w.Code != http.StatusUnauthorized {
		t.Fatalf("the revoked test key still creates payments: %d %s", w.Code, w.Body.String())
	}
}

// The naira start routes sit behind the app's JWT and, with no order service
// wired (Paycrest off), answer 503 rather than 404: the app can tell "not
// available right now" from "no such route".
func TestPayRoutes_NairaStartRoutesAreAuthedAndDormantSafe(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	id := uuid.New().String()
	routes := []struct{ method, path string }{
		{"GET", "/api/pay/checkout/" + id + "/quote?sourceChainId=56&sourceToken=0x0&sourceAddress=0xa"},
		{"POST", "/api/pay/checkout/" + id + "/direct"},
		{"POST", "/api/pay/checkout/" + id + "/converted"},
	}
	for _, rt := range routes {
		if w := payRoutesDo(r, rt.method, rt.path, "", nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a jwt: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
	tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	for _, rt := range routes {
		w := payRoutesDo(r, rt.method, rt.path, tok, map[string]any{"chainKey": "base", "token": "USDC"})
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Live payments aren't available right now") {
			t.Fatalf("%s %s unwired: %d %s", rt.method, rt.path, w.Code, w.Body.String())
		}
	}
}

// The owner routes are behind the user's session, and changing where the
// merchant is paid has its own tight limit, so a password cannot be guessed
// through it however the lockout is configured.
func TestPayRoutes_OwnerSettlementIsAuthedAndTightlyLimited(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/api/pay/merchant"},
		{http.MethodPut, "/api/pay/merchant/settlement"},
		{http.MethodPost, "/api/pay/merchant/actions/" + uuid.NewString() + "/confirm"},
		{http.MethodGet, "/api/pay/merchant/payments"},
		{http.MethodGet, "/api/pay/merchant/payments/" + uuid.NewString()},
		{http.MethodPost, "/api/pay/merchant/keys"},
		{http.MethodPut, "/api/pay/merchant/endpoints"},
		{http.MethodPost, "/api/pay/merchant/webhook-secret"},
		{http.MethodGet, "/api/pay/merchant/banks"},
		{http.MethodGet, "/api/pay/merchant/banks/resolve"},
		{http.MethodGet, "/api/pay/merchant/settlement-options"},
		{http.MethodPost, "/api/pay/merchant/links"},
		{http.MethodGet, "/api/pay/merchant/links"},
		{http.MethodPost, "/api/pay/merchant/links/" + uuid.NewString() + "/cancel"},
		{http.MethodPost, "/api/pay/merchant/open-links"},
		{http.MethodGet, "/api/pay/merchant/open-links"},
		{http.MethodGet, "/api/pay/merchant/open-links/" + uuid.NewString()},
		{http.MethodPut, "/api/pay/merchant/open-links/" + uuid.NewString()},
		{http.MethodPost, "/api/pay/merchant/open-links/" + uuid.NewString() + "/replace"},
	} {
		if w := payRoutesDo(r, rt.method, rt.path, "", map[string]any{}); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a session: %d", rt.method, rt.path, w.Code)
		}
	}
	u := payRoutesUser(t, db, models.RoleUser)
	tok := payRoutesToken(t, cfg.JWTSecret, u.ID)
	codes := []int{}
	for i := 0; i < 6; i++ {
		codes = append(codes, payRoutesDo(r, http.MethodPut, "/api/pay/merchant/settlement", tok, map[string]any{"password": "x"}).Code)
	}
	if codes[5] != http.StatusTooManyRequests {
		t.Fatalf("codes %v: the sixth change within 15 minutes must be refused", codes)
	}
}

// A double-tapped "Request payment" makes one link: the create route replays a
// repeated Idempotency-Key. It also has its own per-user limit.
func TestPayRoutes_OwnerLinkCreateIsIdempotentAndLimited(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	if err := db.Exec(`CREATE TABLE request_idempotencies (key TEXT PRIMARY KEY, user_id TEXT NOT NULL,
		request_hash TEXT NOT NULL, response_status INTEGER NOT NULL, response_body TEXT, response_headers TEXT,
		created_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	u := payRoutesUser(t, db, models.RoleUser)
	if err := db.Create(&models.PayMerchant{MerchantRequestID: uuid.New(), UserID: u.ID, BusinessName: "Kemi Cakes",
		Status: models.PayMerchantActive, SettlementMode: models.PaySettlementFiat, BankCode: "058", AccountNumber: "0123456789",
		AccountName: "KEMI CAKES", CustomerFeeSharePercent: 100, MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000,
		WebhookSecretVersion: 1}).Error; err != nil {
		t.Fatal(err)
	}
	tok := payRoutesToken(t, cfg.JWTSecret, u.ID)
	send := func(key string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		json.NewEncoder(&buf).Encode(map[string]any{"amount": 2_500_000, "description": "Wedding cake deposit"})
		req := httptest.NewRequest(http.MethodPost, "/api/pay/merchant/links?mode=test", &buf)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	first, again := send("tap-1"), send("tap-1")
	if first.Code != http.StatusCreated || again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" ||
		first.Body.String() != again.Body.String() {
		t.Fatalf("first %d, again %d %q", first.Code, again.Code, again.Header().Get("Idempotent-Replayed"))
	}
	var links int64
	db.Model(&models.PayPayment{}).Where("source = ?", models.PaySourceLink).Count(&links)
	if links != 1 {
		t.Fatalf("a replayed key made %d links", links)
	}
	for i := 2; i <= 30; i++ {
		if w := send(""); w.Code == http.StatusTooManyRequests {
			t.Fatalf("link %d in a minute refused", i)
		}
	}
	if w := send(""); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 31st link in a minute: %d", w.Code)
	}
}

// Each guarded request (payouts, keys, webhooks, the webhook secret) checks the
// password, so each has its own tight limit: using up one never spends another.
func TestPayRoutes_OwnerGuardedRequestsEachHaveTheirOwnLimit(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	for _, rt := range []struct{ method, path string }{
		{http.MethodPut, "/api/pay/merchant/settlement"},
		{http.MethodPost, "/api/pay/merchant/keys"},
		{http.MethodPut, "/api/pay/merchant/endpoints"},
		{http.MethodPost, "/api/pay/merchant/webhook-secret"},
	} {
		codes := []int{}
		for i := 0; i < 6; i++ {
			codes = append(codes, payRoutesDo(r, rt.method, rt.path, tok, map[string]any{"password": "x"}).Code)
		}
		if codes[4] == http.StatusTooManyRequests || codes[5] != http.StatusTooManyRequests {
			t.Fatalf("%s %s codes %v: five within 15 minutes, then refused", rt.method, rt.path, codes)
		}
	}
}

// The owner area's pages read several routes at once, so the group allows 120 a
// minute; the bank name enquiry, which calls a provider, keeps 30.
func TestPayRoutes_OwnerReadLimits(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	for i := 1; i <= 30; i++ {
		if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant/banks/resolve?bankCode=044&accountNumber=0123456789", tok, nil); w.Code == http.StatusTooManyRequests {
			t.Fatalf("resolve %d refused", i)
		}
	}
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant/banks/resolve?bankCode=044&accountNumber=0123456789", tok, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 31st resolve in a minute: %d", w.Code)
	}
	for i := 32; i <= 120; i++ {
		if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant", tok, nil); w.Code == http.StatusTooManyRequests {
			t.Fatalf("owner read %d refused", i)
		}
	}
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant", tok, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 121st owner request in a minute: %d", w.Code)
	}
}

// An owner's answers are that owner's account (keys and secrets included) and
// are never cached — signed in or not, whatever the outcome.
func TestPayRoutes_OwnerAnswersAreNeverCached(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	for name, bearer := range map[string]string{"signed out": "", "signed in": tok} {
		w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant", bearer, nil)
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != "Cookie, Origin" {
			t.Errorf("%s (%d): Cache-Control %q Vary %q", name, w.Code, w.Header().Get("Cache-Control"), w.Header().Get("Vary"))
		}
	}
	// The checkout routes are not the owner's: this is scoped, not global.
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/checkout/"+uuid.NewString(), "", nil); w.Header().Get("Cache-Control") == "no-store" {
		t.Error("no-store leaked beyond the owner routes")
	}
}

// Her payment history: signed in only, never cached, her payments under data.payments.
func TestPayRoutes_PayerHistoryIsAuthedUncachedAndHers(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}); err != nil {
		t.Fatal(err)
	}
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/history", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no jwt: %d %s", w.Code, w.Body.String())
	}
	user := payRoutesUser(t, db, models.RoleUser)
	w := payRoutesDo(r, http.MethodGet, "/api/pay/history", payRoutesToken(t, cfg.JWTSecret, user.ID), nil)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("history: %d cache %q %s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			Payments []map[string]any `json:"payments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || !body.Success || body.Data.Payments == nil {
		t.Fatalf("shape: %s (err %v)", w.Body.String(), err)
	}
}

// payOpenPaymentFrom starts a payment through slug from the visitor at ip.
func payOpenPaymentFrom(r *gin.Engine, slug, ip string) int {
	req := httptest.NewRequest(http.MethodPost, "/api/pay/public/open/"+slug+"/payments", strings.NewReader(`{"amount":450000}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// Starting a payment through an open link needs no session, so it is limited
// three ways, narrowest first: 20 a minute per visitor, 300 a minute per link,
// 2000 a minute across every link. A request one limiter refuses never reaches
// the next, so no visitor can spend another's budget.
func TestPayRoutes_OpenLinkPaymentsArePublicAndLimited(t *testing.T) {
	r, _, _, _ := newPayRoutesRig(t)
	ip := func(i int) string { return fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255) }
	for i := 1; i <= 25; i++ {
		code := payOpenPaymentFrom(r, "shop-a", "203.0.113.1")
		if (i <= 20 && code != http.StatusNotFound) || (i > 20 && code != http.StatusTooManyRequests) {
			t.Fatalf("request %d from one visitor: %d", i, code)
		}
	}
	for i := 1; i <= 301; i++ {
		code := payOpenPaymentFrom(r, "shop-b", ip(i))
		if (i <= 300 && code == http.StatusTooManyRequests) || (i > 300 && code != http.StatusTooManyRequests) {
			t.Fatalf("request %d to one link: %d", i, code)
		}
	}
	// 320 have reached the route-wide limiter — not the 5 the visitor's own
	// limit refused, nor the one the link's refused — so 1680 more fit.
	for i := 1; i <= 1680; i++ {
		if code := payOpenPaymentFrom(r, fmt.Sprintf("shop-%d", i), ip(10_000+i)); code == http.StatusTooManyRequests {
			t.Fatalf("request %d across links refused: a refused request spent the shared budget", i)
		}
	}
	if code := payOpenPaymentFrom(r, "shop-last", ip(99_999)); code != http.StatusTooManyRequests {
		t.Fatalf("the 2001st across every link in a minute: %d", code)
	}
}

// A slug no link could have — a NUL, a stray byte — is not found, answered
// before any lookup (which would otherwise fail in the database).
func TestPayRoutes_AnImpossibleSlugIsNotFound(t *testing.T) {
	r, _, _, _ := newPayRoutesRig(t)
	for _, slug := range []string{"%00", "%FF", "shop%00a", "bad_slug", "-shop", strings.Repeat("a", 65)} {
		if code := payOpenPaymentFrom(r, slug, "203.0.113.7"); code != http.StatusNotFound {
			t.Fatalf("POST %q: %d", slug, code)
		}
		w := payRoutesDo(r, http.MethodGet, "/api/pay/public/open/"+slug, "", nil)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"link_not_found"`) {
			t.Fatalf("GET %q: %d %s", slug, w.Code, w.Body.String())
		}
	}
}

// The landing site is another origin, and its JSON POST is preflighted: the
// global CORS middleware must answer it for the landing origin — and only it.
func TestPayRoutes_TheLandingMayStartAnOpenLinkPayment(t *testing.T) {
	_, _, _, payh := newPayRoutesRig(t)
	cfg := &config.Config{JWTSecret: "pay-routes-test-secret", LandingURL: "https://tsaconnectworld.com"}
	r := gin.New()
	r.Use(middleware.CORS(cfg))
	SetupRoutes(r, cfg, &handlers.Handlers{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, payh)

	preflight := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodOptions, "/api/pay/public/open/shop-a/payments", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "content-type")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	w := preflight("https://tsaconnectworld.com")
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "https://tsaconnectworld.com" ||
		!strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), "POST") ||
		!strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Content-Type") {
		t.Fatalf("landing preflight: %d %v", w.Code, w.Header())
	}
	if w := preflight("https://evil.example"); w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("another origin was allowed: %v", w.Header())
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pay/public/open/shop-a/payments", strings.NewReader(`{"amount":450000}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://tsaconnectworld.com")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Header().Get("Access-Control-Allow-Origin") != "https://tsaconnectworld.com" {
		t.Fatalf("the POST itself: %d %v", w.Code, w.Header())
	}
}

// payRoutesOwner is a signed-up user who owns an active, naira-settled merchant.
func payRoutesOwner(t *testing.T, db *gorm.DB) (*models.User, *models.PayMerchant) {
	t.Helper()
	u := payRoutesUser(t, db, models.RoleUser)
	m := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: u.ID, BusinessName: "Kemi Cakes",
		Status: models.PayMerchantActive, SettlementMode: models.PaySettlementFiat, BankCode: "058", AccountNumber: "0123456789",
		AccountName: "KEMI CAKES", CustomerFeeSharePercent: 100, MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000,
		WebhookSecretVersion: 1}
	if err := db.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	return u, m
}

// /payments/summary is its own route, not a payment id read as "summary".
func TestPayRoutes_SummaryIsItsOwnRoute(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant/payments/summary", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	u, _ := payRoutesOwner(t, db)
	tok := payRoutesToken(t, cfg.JWTSecret, u.ID)
	w := payRoutesDo(r, http.MethodGet, "/api/pay/merchant/payments/summary?mode=live&from=2026-10-01&to=2026-10-31", tok, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"byStatus"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestPayRoutes_ExportIsItsOwnRouteAndLimitedTenAMinute(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	u, _ := payRoutesOwner(t, db)
	tok := payRoutesToken(t, cfg.JWTSecret, u.ID)
	path := "/api/pay/merchant/payments/export?mode=live&from=2026-10-01&to=2026-10-31"
	w := payRoutesDo(r, http.MethodGet, path, tok, nil)
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/csv; charset=utf-8" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	for i := 2; i <= 10; i++ {
		if w := payRoutesDo(r, http.MethodGet, path, tok, nil); w.Code == http.StatusTooManyRequests {
			t.Fatalf("export %d in a minute refused", i)
		}
	}
	if w := payRoutesDo(r, http.MethodGet, path, tok, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 11th export in a minute: %d", w.Code)
	}
}

func TestPayRoutes_ResendIsAuthedAndLimitedTenAMinute(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	path := "/api/pay/merchant/deliveries/" + uuid.NewString() + "/resend?mode=live"
	if w := payRoutesDo(r, http.MethodPost, path, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no session: %d", w.Code)
	}
	u, _ := payRoutesOwner(t, db)
	tok := payRoutesToken(t, cfg.JWTSecret, u.ID)
	for i := 1; i <= 10; i++ {
		if w := payRoutesDo(r, http.MethodPost, path, tok, nil); w.Code == http.StatusTooManyRequests {
			t.Fatalf("resend %d in a minute refused", i)
		}
	}
	if w := payRoutesDo(r, http.MethodPost, path, tok, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("the 11th resend in a minute: %d", w.Code)
	}
}

func TestPayRoutes_ReferenceLookupNeedsTheMerchantKey(t *testing.T) {
	r, _, db, payh := newPayRoutesRig(t)
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/v1/payments?reference=R1", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no key: %d", w.Code)
	}
	_, m := payRoutesOwner(t, db)
	keys, err := payh.MerchantService().IssueKeys(context.Background(), uuid.New(), m.ID, services.PayKeysTest)
	if err != nil {
		t.Fatal(err)
	}
	if w := payRoutesDo(r, http.MethodPost, "/api/pay/v1/payments", keys.TestKey, map[string]any{"amount": 500_000, "reference": "R1"}); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	w := payRoutesDo(r, http.MethodGet, "/api/pay/v1/payments?reference=R1", keys.TestKey, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":{`) || !strings.Contains(w.Body.String(), `"reference":"R1"`) {
		t.Fatalf("lookup: %d %s", w.Code, w.Body.String())
	}
	if w := payRoutesDo(r, http.MethodGet, "/api/pay/v1/payments?reference=R2", keys.TestKey, nil); w.Code != http.StatusNotFound {
		t.Fatalf("unknown reference: %d", w.Code)
	}
}

// seedRoutesPayment stores one payment with a known code, as a fresh merchant's.
func seedRoutesPayment(t *testing.T, db *gorm.DB, mode, status, code string) *models.PayPayment {
	t.Helper()
	m := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: uuid.New(), BusinessName: "Sky Air",
		Status: models.PayMerchantActive, SettlementMode: models.PaySettlementFiat, CustomerFeeSharePercent: 100,
		MinAmountKobo: 100_000, MaxAmountKobo: 500_000_000, WebhookSecretVersion: 1}
	if err := db.Create(m).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	p := &models.PayPayment{MerchantID: m.ID, Mode: mode, Reference: "R-" + code, AmountKobo: 500_000, Currency: "NGN",
		Source: models.PaySourceLink, Description: "Wedding cake deposit", FeeBps: 100, CustomerFeeSharePercent: 100,
		CustomerTotalKobo: 505_051, MerchantNetKobo: 500_000, SettlementMode: models.PaySettlementFiat,
		Status: status, PayCode: &code}
	if status == models.PayStatusSucceeded {
		p.SucceededAt = &now
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatal(err)
	}
	return p
}

// D10: the lookup needs a session, is never cached, and answers a code no
// payment could have exactly as one no payment has.
func TestPayRoutes_CodeLookup(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	p := seedRoutesPayment(t, db, models.PayModeTest, models.PayStatusPending, "7K2QFM9XWD")

	if w := payRoutesDo(r, "GET", "/api/pay/code/TSA-7K2QF-M9XWD", "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("no jwt: %d %s", w.Code, w.Body.String())
	}
	tok := payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID)
	w := payRoutesDo(r, "GET", "/api/pay/code/tsa-7k2qf-m9xwd", tok, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"`+p.ID.String()+`"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("lookup: %d %q %s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	unknown := payRoutesDo(r, "GET", "/api/pay/code/TSA-AAAAA-AAAAA", tok, nil)
	malformed := payRoutesDo(r, "GET", "/api/pay/code/not-a-code", tok, nil)
	if unknown.Code != http.StatusNotFound || malformed.Code != http.StatusNotFound || unknown.Body.String() != malformed.Body.String() {
		t.Fatalf("unknown %d %s / malformed %d %s", unknown.Code, unknown.Body.String(), malformed.Code, malformed.Body.String())
	}
}

// A receipt's code is shared publicly: once a payment has succeeded, only its
// payer gets the id back; anyone else gets the unknown-code 404, byte for byte.
func TestPayRoutes_CodeLookupHidesAPaidPaymentFromAllButItsPayer(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	p := seedRoutesPayment(t, db, models.PayModeLive, models.PayStatusSucceeded, "7K2QFM9XWD")
	payer, other := payRoutesUser(t, db, models.RoleUser), payRoutesUser(t, db, models.RoleUser)
	if err := db.Model(p).Update("payer_user_id", payer.ID).Error; err != nil {
		t.Fatal(err)
	}

	w := payRoutesDo(r, "GET", "/api/pay/code/TSA-7K2QF-M9XWD", payRoutesToken(t, cfg.JWTSecret, payer.ID), nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"`+p.ID.String()+`"`) {
		t.Fatalf("payer: %d %s", w.Code, w.Body.String())
	}
	otherTok := payRoutesToken(t, cfg.JWTSecret, other.ID)
	hidden := payRoutesDo(r, "GET", "/api/pay/code/TSA-7K2QF-M9XWD", otherTok, nil)
	unknown := payRoutesDo(r, "GET", "/api/pay/code/TSA-AAAAA-AAAAA", otherTok, nil)
	if hidden.Code != http.StatusNotFound || hidden.Body.String() != unknown.Body.String() {
		t.Fatalf("other user: %d %s / unknown %d %s", hidden.Code, hidden.Body.String(), unknown.Code, unknown.Body.String())
	}
}

// 20 a minute per user, 60 a minute per address (the IP limiter runs first,
// so it counts every request, the per-user refusals included).
func TestPayRoutes_CodeLookupIsLimitedPerUserAndPerAddress(t *testing.T) {
	r, cfg, db, _ := newPayRoutesRig(t)
	get := func(tok string) int { return payRoutesDo(r, "GET", "/api/pay/code/TSA-AAAAA-AAAAA", tok, nil).Code }
	user := func() string { return payRoutesToken(t, cfg.JWTSecret, payRoutesUser(t, db, models.RoleUser).ID) }

	first := user()
	for i := 0; i < 20; i++ {
		if c := get(first); c != http.StatusNotFound {
			t.Fatalf("try %d: %d", i+1, c)
		}
	}
	if c := get(first); c != http.StatusTooManyRequests {
		t.Fatalf("21st try by one user: %d", c)
	}
	// 21 so far from this address; two more users bring it to exactly 60.
	for _, n := range []int{20, 19} {
		tok := user()
		for i := 0; i < n; i++ {
			if c := get(tok); c != http.StatusNotFound {
				t.Fatalf("another user's try %d: %d", i+1, c)
			}
		}
	}
	if c := get(user()); c != http.StatusTooManyRequests {
		t.Fatalf("61st try from one address: %d", c)
	}
}

// D12: no session, never cached, and only a succeeded live payment answers —
// a test one is as unknown as a malformed code.
func TestPayRoutes_PublicReceipt(t *testing.T) {
	r, _, db, _ := newPayRoutesRig(t)
	seedRoutesPayment(t, db, models.PayModeLive, models.PayStatusSucceeded, "7K2QFM9XWD")
	seedRoutesPayment(t, db, models.PayModeTest, models.PayStatusSucceeded, "AB12CD34EF")

	w := payRoutesDo(r, "GET", "/api/pay/public/receipt/TSA-7K2QF-M9XWD", "", nil)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(w.Body.String(), `"code":"TSA-7K2QF-M9XWD"`) || !strings.Contains(w.Body.String(), `"merchantName":"Sky Air"`) {
		t.Fatalf("live receipt: %d %q %s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	var bodies []string
	for _, code := range []string{"TSA-AB12C-D34EF", "TSA-AAAAA-AAAAA", "x"} {
		w := payRoutesDo(r, "GET", "/api/pay/public/receipt/"+code, "", nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: %d %s", code, w.Code, w.Body.String())
		}
		bodies = append(bodies, w.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("404 bodies differ: %v", bodies)
	}
	// The status page beside it still answers by id.
	if w := payRoutesDo(r, "GET", "/api/pay/public/not-a-uuid", "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("/public/:id: %d", w.Code)
	}
}

func TestPayRoutes_PublicReceiptIsLimitedPerAddress(t *testing.T) {
	r, _, _, _ := newPayRoutesRig(t)
	for i := 0; i < 30; i++ {
		if c := payRoutesDo(r, "GET", "/api/pay/public/receipt/TSA-AAAAA-AAAAA", "", nil).Code; c != http.StatusNotFound {
			t.Fatalf("try %d: %d", i+1, c)
		}
	}
	if c := payRoutesDo(r, "GET", "/api/pay/public/receipt/TSA-AAAAA-AAAAA", "", nil).Code; c != http.StatusTooManyRequests {
		t.Fatalf("31st try: %d", c)
	}
}
