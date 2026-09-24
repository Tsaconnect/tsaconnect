package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	stmts := []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT, username TEXT UNIQUE, email TEXT UNIQUE, password TEXT,
			role TEXT DEFAULT 'user', phone_number TEXT,
			verification_status TEXT DEFAULT 'pending',
			account_status TEXT DEFAULT 'active', last_login DATETIME,
			login_attempts INTEGER DEFAULT 0, lock_until DATETIME, wallet_address TEXT,
			email_verified INTEGER DEFAULT 0, deleted_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE refresh_tokens (
			id TEXT PRIMARY KEY, user_id TEXT, token_hash TEXT,
			expires_at DATETIME, revoked INTEGER DEFAULT 0, device_info TEXT, created_at DATETIME
		)`,
		`CREATE TABLE account_deletion_requests (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, reason TEXT,
			status TEXT NOT NULL DEFAULT 'pending', purge_after DATETIME,
			admin_note TEXT, reviewed_by TEXT, reviewed_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
	}
	for _, sql := range stmts {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}

	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	return db
}

func insertAuthUser(t *testing.T, db *gorm.DB, user *models.User) {
	t.Helper()
	hashed, err := bcrypt.GenerateFromPassword([]byte("StrongPass1!"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user.Password = string(hashed)
	if err := db.Exec(`INSERT INTO users (id, name, username, email, password, account_status, deleted_at) VALUES (?,?,?,?,?,?,?)`,
		user.ID.String(), user.Name, user.Username, user.Email, user.Password, user.AccountStatus, user.DeletedAt).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
}

func TestLogin_RejectsDeletedAccount(t *testing.T) {
	db := setupAuthTestDB(t)
	now := time.Now()
	deletedUser := models.User{
		ID:            uuid.New(),
		Name:          "Deleted User",
		Username:      "deleted_user",
		Email:         "deleted@example.com",
		AccountStatus: models.AccountStatusDeleted,
		DeletedAt:     &now,
	}
	insertAuthUser(t, db, &deletedUser)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"deleted@example.com","password":"StrongPass1!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for deleted account login, got %d (body: %s)", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if resp.Success {
		t.Errorf("expected success=false, got true")
	}
	if resp.Code != "ACCOUNT_DELETED" {
		t.Errorf("expected code ACCOUNT_DELETED, got %q", resp.Code)
	}
}

func TestLogin_RejectsSuspendedAccount(t *testing.T) {
	db := setupAuthTestDB(t)
	suspendedUser := models.User{
		ID:            uuid.New(),
		Name:          "Suspended User",
		Username:      "suspended_user",
		Email:         "suspended@example.com",
		AccountStatus: models.AccountStatusSuspended,
	}
	insertAuthUser(t, db, &suspendedUser)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"suspended@example.com","password":"StrongPass1!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for suspended account login, got %d", w.Code)
	}
}

func TestLogin_BadPasswordDoesNotLeakAccountStatus(t *testing.T) {
	db := setupAuthTestDB(t)
	now := time.Now()
	deletedUser := models.User{
		ID:            uuid.New(),
		Name:          "Deleted User",
		Username:      "deleted_user",
		Email:         "deleted@example.com",
		AccountStatus: models.AccountStatusDeleted,
		DeletedAt:     &now,
	}
	insertAuthUser(t, db, &deletedUser)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"deleted@example.com","password":"WrongPassword!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)

	// Must return 401 Unauthorized for bad password, not revealing account status
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized on bad password, got %d", w.Code)
	}
}

func TestLogin_ActiveAccountSucceeds(t *testing.T) {
	db := setupAuthTestDB(t)
	activeUser := models.User{
		ID:            uuid.New(),
		Name:          "Active User",
		Username:      "active_user",
		Email:         "active@example.com",
		AccountStatus: models.AccountStatusActive,
	}
	insertAuthUser(t, db, &activeUser)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"active@example.com","password":"StrongPass1!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for active account, got %d: %s", w.Code, w.Body.String())
	}
}

// The app branches on these refusals: their bytes must not change.
func TestLogin_RefusalBodiesAreUnchanged(t *testing.T) {
	db := setupAuthTestDB(t)
	h := &Handlers{Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"}}
	for _, u := range []models.User{
		{ID: uuid.New(), Name: "Deleted", Username: "gone", Email: "gone@example.com", AccountStatus: models.AccountStatusDeleted},
		{ID: uuid.New(), Name: "Suspended", Username: "held", Email: "held@example.com", AccountStatus: models.AccountStatusSuspended},
	} {
		insertAuthUser(t, db, &u)
	}
	cases := map[string]string{
		"gone@example.com": `{"code":"ACCOUNT_DELETED","message":"This account has been deleted. Please contact support if you wish to restore it.","success":false}`,
		"held@example.com": `{"code":"ACCOUNT_NOT_ACTIVE","message":"Account is suspended. Please contact support.","success":false}`,
	}
	for identifier, want := range cases {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/login",
			bytes.NewReader([]byte(`{"identifier":"`+identifier+`","password":"StrongPass1!"}`)))
		c.Request.Header.Set("Content-Type", "application/json")
		h.Login(c)
		if w.Code != http.StatusForbidden || w.Body.String() != want {
			t.Errorf("%s: %d %s, want 403 %s", identifier, w.Code, w.Body.String(), want)
		}
	}
}
