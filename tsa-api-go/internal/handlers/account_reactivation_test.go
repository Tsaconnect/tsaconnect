package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
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

func setupReactivationTestDB(t *testing.T) *gorm.DB {
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
			country TEXT, state TEXT, city TEXT, address TEXT, profile_photo TEXT,
			verification_status TEXT DEFAULT 'pending',
			account_status TEXT DEFAULT 'active', last_login DATETIME,
			login_attempts INTEGER DEFAULT 0, lock_until DATETIME, wallet_address TEXT,
			email_verified INTEGER DEFAULT 0, deleted_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE account_deletion_requests (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, reason TEXT,
			status TEXT NOT NULL DEFAULT 'pending', purge_after DATETIME,
			admin_note TEXT, reviewed_by TEXT, reviewed_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE refresh_tokens (
			id TEXT PRIMARY KEY, user_id TEXT, token_hash TEXT,
			expires_at DATETIME, revoked INTEGER DEFAULT 0, device_info TEXT, web INTEGER NOT NULL DEFAULT 0, created_at DATETIME
		)`,
		`CREATE TABLE p2p_trades (
			id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, status TEXT
		)`,
		`CREATE TABLE orders (
			id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, status TEXT
		)`,
		`CREATE TABLE device_tokens (
			id TEXT PRIMARY KEY, user_id TEXT, token TEXT, platform TEXT,
			created_at DATETIME, updated_at DATETIME
		)`,
	}
	for _, sql := range stmts {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatalf("exec %q: %v", sql, err)
		}
	}
	// Account deletion's blocker check and erasure read the ramp tables.
	if err := db.AutoMigrate(&models.RampSellIntent{}, &models.PaycrestOrderRecord{}, &models.PayMerchant{}, &models.PayPayment{}, &models.WebTrustedBrowser{}); err != nil {
		t.Fatalf("migrate ramp tables: %v", err)
	}

	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	return db
}

func insertReactivationUser(t *testing.T, db *gorm.DB, user *models.User) {
	t.Helper()
	hashed, err := bcrypt.GenerateFromPassword([]byte("StrongPass1!"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user.Password = string(hashed)
	if err := db.Exec(`INSERT INTO users (id, name, username, email, password, account_status, deleted_at, role) VALUES (?,?,?,?,?,?,?,?)`,
		user.ID.String(), user.Name, user.Username, user.Email, user.Password, user.AccountStatus, user.DeletedAt, user.Role).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
}

func insertDeletionRequest(t *testing.T, db *gorm.DB, req *models.AccountDeletionRequest) {
	t.Helper()
	if err := db.Exec(`INSERT INTO account_deletion_requests (id, user_id, reason, status, purge_after, created_at, updated_at) VALUES (?,?,?,?,?,?,?)`,
		req.ID.String(), req.UserID.String(), req.Reason, req.Status, req.PurgeAfter, req.CreatedAt, req.UpdatedAt).Error; err != nil {
		t.Fatalf("insert deletion request: %v", err)
	}
}

func TestLogin_WithinGracePeriod_ReturnsGracePeriodCodeAndCanRestore(t *testing.T) {
	db := setupReactivationTestDB(t)
	now := time.Now()
	purgeAfter := now.AddDate(0, 0, 25) // 25 days left

	user := models.User{
		ID:            uuid.New(),
		Name:          "Grace User",
		Username:      "grace_user",
		Email:         "grace@example.com",
		AccountStatus: models.AccountStatusDeleted,
		DeletedAt:     &now,
	}
	insertReactivationUser(t, db, &user)

	delReq := models.AccountDeletionRequest{
		ID:         uuid.New(),
		UserID:     user.ID,
		Reason:     "Taking a break",
		Status:     models.AccountDeletionStatusPending,
		PurgeAfter: purgeAfter,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	insertDeletionRequest(t, db, &delReq)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"grace@example.com","password":"StrongPass1!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Data    struct {
			CanRestore    bool `json:"canRestore"`
			GraceDaysLeft int  `json:"graceDaysLeft"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if resp.Code != "ACCOUNT_DELETED_GRACE_PERIOD" {
		t.Errorf("expected code ACCOUNT_DELETED_GRACE_PERIOD, got %q", resp.Code)
	}
	if !resp.Data.CanRestore {
		t.Errorf("expected canRestore=true")
	}
	if resp.Data.GraceDaysLeft <= 0 {
		t.Errorf("expected graceDaysLeft > 0, got %d", resp.Data.GraceDaysLeft)
	}
}

func TestLogin_PastGracePeriod_ReturnsAccountDeleted(t *testing.T) {
	db := setupReactivationTestDB(t)
	now := time.Now()
	expiredPurge := now.AddDate(0, 0, -5) // expired 5 days ago

	user := models.User{
		ID:            uuid.New(),
		Name:          "Expired User",
		Username:      "expired_user",
		Email:         "expired@example.com",
		AccountStatus: models.AccountStatusDeleted,
		DeletedAt:     &now,
	}
	insertReactivationUser(t, db, &user)

	delReq := models.AccountDeletionRequest{
		ID:         uuid.New(),
		UserID:     user.ID,
		Reason:     "Leaving",
		Status:     models.AccountDeletionStatusPending,
		PurgeAfter: expiredPurge,
		CreatedAt:  now.AddDate(0, 0, -35),
		UpdatedAt:  now.AddDate(0, 0, -35),
	}
	insertDeletionRequest(t, db, &delReq)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"expired@example.com","password":"StrongPass1!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got %d", w.Code)
	}

	var resp struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Code != "ACCOUNT_DELETED" {
		t.Errorf("expected code ACCOUNT_DELETED, got %q", resp.Code)
	}
}

func TestReactivateAccount_WithinGracePeriod_RestoresAccountAndIssuesToken(t *testing.T) {
	db := setupReactivationTestDB(t)
	now := time.Now()
	purgeAfter := now.AddDate(0, 0, 20)

	user := models.User{
		ID:            uuid.New(),
		Name:          "Reactivate User",
		Username:      "reactivate_user",
		Email:         "reactivate@example.com",
		AccountStatus: models.AccountStatusDeleted,
		DeletedAt:     &now,
	}
	insertReactivationUser(t, db, &user)

	delReq := models.AccountDeletionRequest{
		ID:         uuid.New(),
		UserID:     user.ID,
		Reason:     "Deleted by mistake",
		Status:     models.AccountDeletionStatusPending,
		PurgeAfter: purgeAfter,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	insertDeletionRequest(t, db, &delReq)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"reactivate@example.com","password":"StrongPass1!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/reactivate", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.ReactivateAccount(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			Token         string `json:"token"`
			AccountStatus string `json:"accountStatus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !resp.Success {
		t.Errorf("expected success=true")
	}
	if resp.Data.Token == "" {
		t.Errorf("expected valid JWT token")
	}
	if resp.Data.AccountStatus != models.AccountStatusActive {
		t.Errorf("expected accountStatus=active, got %s", resp.Data.AccountStatus)
	}

	// Verify database was updated
	var updatedUser models.User
	if err := db.First(&updatedUser, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("read user: %v", err)
	}
	if updatedUser.AccountStatus != models.AccountStatusActive {
		t.Errorf("expected db user active, got %s", updatedUser.AccountStatus)
	}
	if updatedUser.DeletedAt != nil {
		t.Errorf("expected db user deleted_at cleared, got %v", updatedUser.DeletedAt)
	}

	var updatedReq models.AccountDeletionRequest
	if err := db.First(&updatedReq, "id = ?", delReq.ID).Error; err != nil {
		t.Fatalf("read request: %v", err)
	}
	if updatedReq.Status != models.AccountDeletionStatusRestored {
		t.Errorf("expected db request restored, got %s", updatedReq.Status)
	}
}

func TestCompleteAccountDeletion_AnonymizesUserPII(t *testing.T) {
	db := setupReactivationTestDB(t)
	now := time.Now()

	admin := models.User{
		ID:            uuid.New(),
		Name:          "Admin User",
		Username:      "admin",
		Email:         "admin@example.com",
		Role:          models.RoleAdmin,
		AccountStatus: models.AccountStatusActive,
	}
	insertReactivationUser(t, db, &admin)

	userToPurge := models.User{
		ID:            uuid.New(),
		Name:          "John Doe",
		Username:      "johndoe",
		Email:         "john@example.com",
		Role:          models.RoleUser,
		AccountStatus: models.AccountStatusDeleted,
		DeletedAt:     &now,
	}
	insertReactivationUser(t, db, &userToPurge)

	delReq := models.AccountDeletionRequest{
		ID:         uuid.New(),
		UserID:     userToPurge.ID,
		Reason:     "Purge me",
		Status:     models.AccountDeletionStatusPending,
		PurgeAfter: now.AddDate(0, 0, -1),
		CreatedAt:  now.AddDate(0, 0, -31),
		UpdatedAt:  now.AddDate(0, 0, -31),
	}
	insertDeletionRequest(t, db, &delReq)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: delReq.ID.String()}}
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/admin/account-deletion-requests/"+delReq.ID.String()+"/complete", bytes.NewReader([]byte(`{"note":"anonymized"}`)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", &admin)

	h.CompleteAccountDeletionRequest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// Verify user PII was anonymized
	var purgedUser models.User
	if err := db.First(&purgedUser, "id = ?", userToPurge.ID).Error; err != nil {
		t.Fatalf("read user: %v", err)
	}

	if purgedUser.Email == "john@example.com" {
		t.Errorf("expected original email to be wiped, still %q", purgedUser.Email)
	}
	if purgedUser.Username == "johndoe" {
		t.Errorf("expected original username to be wiped, still %q", purgedUser.Username)
	}
	usernameRegex := regexp.MustCompile(`^[a-zA-Z0-9_]{3,20}$`)
	if !usernameRegex.MatchString(purgedUser.Username) {
		t.Errorf("anonymized username %q violates username regex ^[a-zA-Z0-9_]{3,20}$", purgedUser.Username)
	}
	if purgedUser.Name != "Deleted User" {
		t.Errorf("expected name 'Deleted User', got %q", purgedUser.Name)
	}
}

func TestReactivateAccount_LocksOutAfterFailedAttempts(t *testing.T) {
	db := setupReactivationTestDB(t)
	now := time.Now()
	purgeAfter := now.AddDate(0, 0, 10)

	user := models.User{
		ID:            uuid.New(),
		Name:          "Brute Target",
		Username:      "brute_target",
		Email:         "brute@example.com",
		AccountStatus: models.AccountStatusDeleted,
		DeletedAt:     &now,
	}
	insertReactivationUser(t, db, &user)

	delReq := models.AccountDeletionRequest{
		ID:         uuid.New(),
		UserID:     user.ID,
		Status:     models.AccountDeletionStatusPending,
		PurgeAfter: purgeAfter,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	insertDeletionRequest(t, db, &delReq)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"brute@example.com","password":"WrongPassword!"}`)

	// Attempt 5 bad passwords (max is 5)
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/reactivate", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		h.ReactivateAccount(c)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: expected 401 Unauthorized, got %d", i+1, w.Code)
		}
	}

	// 6th attempt must be 423 Locked
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/reactivate", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.ReactivateAccount(c)

	if w.Code != http.StatusLocked {
		t.Fatalf("expected 423 Locked on 6th attempt, got %d", w.Code)
	}
}

func TestReactivateAccount_NonDeletedAccount_DoesNotLeakStatusOnBadPassword(t *testing.T) {
	db := setupReactivationTestDB(t)

	activeUser := models.User{
		ID:            uuid.New(),
		Name:          "Active Target",
		Username:      "active_target",
		Email:         "active_target@example.com",
		AccountStatus: models.AccountStatusActive,
	}
	insertReactivationUser(t, db, &activeUser)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	// Bad password on active account must return 401 Unauthorized, NOT 400 Account is not deleted
	body := []byte(`{"identifier":"active_target@example.com","password":"WrongPassword!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/reactivate", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.ReactivateAccount(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized (not leaking account state on bad password), got %d: %s", w.Code, w.Body.String())
	}
}

func TestReactivateAccount_ConcurrentAdminCompletion_FailsWithConflict(t *testing.T) {
	db := setupReactivationTestDB(t)
	now := time.Now()
	purgeAfter := now.AddDate(0, 0, 10)

	user := models.User{
		ID:            uuid.New(),
		Name:          "Conflict User",
		Username:      "conflict_user",
		Email:         "conflict@example.com",
		AccountStatus: models.AccountStatusDeleted,
		DeletedAt:     &now,
	}
	insertReactivationUser(t, db, &user)

	// Status is already completed by admin
	delReq := models.AccountDeletionRequest{
		ID:         uuid.New(),
		UserID:     user.ID,
		Status:     models.AccountDeletionStatusCompleted,
		PurgeAfter: purgeAfter,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	insertDeletionRequest(t, db, &delReq)

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	body := []byte(`{"identifier":"conflict@example.com","password":"StrongPass1!"}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/auth/reactivate", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.ReactivateAccount(c)

	// Since status is already completed, it cannot be restored and must be rejected
	if w.Code != http.StatusForbidden && w.Code != http.StatusConflict {
		t.Fatalf("expected 403 Forbidden or 409 Conflict, got %d: %s", w.Code, w.Body.String())
	}
}

func TestRequestAccountDeletion_RevokesRefreshTokensAndPreservesWallet(t *testing.T) {
	db := setupReactivationTestDB(t)
	now := time.Now()
	testWallet := "0x1234567890abcdef1234567890abcdef12345678"

	user := models.User{
		ID:            uuid.New(),
		Name:          "Token User",
		Username:      "token_user",
		Email:         "token@example.com",
		AccountStatus: models.AccountStatusActive,
		WalletAddress: testWallet,
	}
	insertReactivationUser(t, db, &user)
	if err := db.Exec(`UPDATE users SET wallet_address = ? WHERE id = ?`, testWallet, user.ID.String()).Error; err != nil {
		t.Fatalf("set wallet: %v", err)
	}

	// Insert an active refresh token
	rt := models.RefreshToken{
		ID:        uuid.New(),
		UserID:    user.ID,
		TokenHash: "hash123",
		ExpiresAt: now.Add(24 * time.Hour),
		Revoked:   false,
	}
	if err := db.Exec(`INSERT INTO refresh_tokens (id, user_id, token_hash, expires_at, revoked, created_at) VALUES (?,?,?,?,?,?)`,
		rt.ID.String(), rt.UserID.String(), rt.TokenHash, rt.ExpiresAt, 0, now).Error; err != nil {
		t.Fatalf("insert rt: %v", err)
	}

	h := &Handlers{
		Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"},
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/users/account/deletion-request", bytes.NewReader([]byte(`{"password":"StrongPass1!","reason":"test"}`)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", &user)

	h.RequestAccountDeletion(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	// 1. Refresh token must be revoked
	var storedRT models.RefreshToken
	if err := db.First(&storedRT, "id = ?", rt.ID).Error; err != nil {
		t.Fatalf("read rt: %v", err)
	}
	if !storedRT.Revoked {
		t.Errorf("expected refresh token to be revoked upon account deletion")
	}

	// 2. Wallet address must be preserved on soft delete
	var freshUser models.User
	if err := db.First(&freshUser, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("read user: %v", err)
	}
	if freshUser.WalletAddress != testWallet {
		t.Errorf("expected wallet_address to be preserved on soft delete, got %q", freshUser.WalletAddress)
	}
}

