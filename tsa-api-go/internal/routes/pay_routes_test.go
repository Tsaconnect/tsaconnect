package routes

import (
	"bytes"
	"context"
	"encoding/json"
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
	suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
)`

// newPayRoutesRig builds a real PayHandler over SQLite and runs it through
// the real SetupRoutes, exactly as production wires it — every other
// handler is nil, matching cart_routes_test.go's pattern. config.DB is set
// because Auth/AdminAuth/RequireP2PAdmin all read the package-level DB, not
// anything passed into SetupRoutes.
func newPayRoutesRig(t *testing.T) (*gin.Engine, *config.Config, *gorm.DB, *handlers.PayHandler) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(payRoutesUsersDDL).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.PayMerchant{}, &models.PayPayment{}, &models.PayWebhookDelivery{}); err != nil {
		t.Fatal(err)
	}
	config.DB = db

	now := func() time.Time { return time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) }
	merchants := services.NewPayMerchantService(db, nil, nil, []byte(strings.Repeat("k", 32)), now)
	payments := services.NewPayPaymentService(db, merchants, 100, "https://tsaconnectworld.com/pay", now)
	payh := handlers.NewPayHandler(merchants, payments, nil, false)

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
}
