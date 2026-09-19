package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newCallerWalletTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY, username TEXT, name TEXT, wallet_address TEXT,
		account_status TEXT DEFAULT 'active',
		created_at DATETIME, updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create users table: %v", err)
	}
	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })
	return db
}

func insertTestUser(t *testing.T, db *gorm.DB, user *models.User) {
	t.Helper()
	if err := db.Exec(`INSERT INTO users (id, username, name, wallet_address, account_status) VALUES (?,?,?,?,?)`,
		user.ID.String(), user.Username, user.Name, user.WalletAddress, user.AccountStatus).Error; err != nil {
		t.Fatalf("insert user: %v", err)
	}
}

func storedUserWallet(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var got string
	if err := db.Raw(`SELECT wallet_address FROM users WHERE id = ?`, id.String()).Scan(&got).Error; err != nil {
		t.Fatalf("read wallet_address: %v", err)
	}
	return got
}

func testGinContext() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/", nil)
	return c, w
}

func TestCallerWallet_ActiveWalletUsedWhenUserWalletEmpty(t *testing.T) {
	db := newCallerWalletTestDB(t)
	user := models.User{
		ID:            uuid.New(),
		Username:      "new_user",
		WalletAddress: "",
		AccountStatus: models.AccountStatusActive,
	}
	insertTestUser(t, db, &user)

	c, w := testGinContext()
	activeWallet := "0x1111111111111111111111111111111111111111"

	wallet, ok := callerWallet(c, &user, activeWallet)
	if !ok {
		t.Fatalf("expected callerWallet to succeed, got error code %d body %s", w.Code, w.Body.String())
	}
	if wallet != activeWallet {
		t.Fatalf("expected resolved wallet %q, got %q", activeWallet, wallet)
	}
	if user.WalletAddress != activeWallet {
		t.Fatalf("expected in-memory user.WalletAddress to be updated, got %q", user.WalletAddress)
	}

	// Verify database was synced
	if stored := storedUserWallet(t, db, user.ID); stored != activeWallet {
		t.Fatalf("expected db wallet_address to be %q, got %q", activeWallet, stored)
	}
}

func TestCallerWallet_ActiveWalletSwitched(t *testing.T) {
	db := newCallerWalletTestDB(t)
	oldWallet := "0x1111111111111111111111111111111111111111"
	newWallet := "0x2222222222222222222222222222222222222222"
	user := models.User{
		ID:            uuid.New(),
		Username:      "switching_user",
		WalletAddress: oldWallet,
		AccountStatus: models.AccountStatusActive,
	}
	insertTestUser(t, db, &user)

	c, w := testGinContext()
	wallet, ok := callerWallet(c, &user, newWallet)
	if !ok {
		t.Fatalf("expected switch to succeed, got %d %s", w.Code, w.Body.String())
	}
	if wallet != newWallet {
		t.Fatalf("expected resolved wallet %q, got %q", newWallet, wallet)
	}

	if stored := storedUserWallet(t, db, user.ID); stored != newWallet {
		t.Fatalf("expected db wallet updated to %q, got %q", newWallet, stored)
	}
}

func TestCallerWallet_FallbackToRegisteredWhenClaimedEmpty(t *testing.T) {
	_ = newCallerWalletTestDB(t)
	registered := "0x1111111111111111111111111111111111111111"
	user := models.User{
		ID:            uuid.New(),
		Username:      "existing_user",
		WalletAddress: registered,
		AccountStatus: models.AccountStatusActive,
	}

	c, _ := testGinContext()
	wallet, ok := callerWallet(c, &user, "")
	if !ok {
		t.Fatalf("expected fallback to succeed")
	}
	if wallet != registered {
		t.Fatalf("expected %q, got %q", registered, wallet)
	}
}

func TestCallerWallet_BothEmptyFails(t *testing.T) {
	_ = newCallerWalletTestDB(t)
	user := models.User{
		ID:            uuid.New(),
		Username:      "no_wallet_user",
		WalletAddress: "",
		AccountStatus: models.AccountStatusActive,
	}

	c, w := testGinContext()
	_, ok := callerWallet(c, &user, "")
	if ok {
		t.Fatalf("expected failure when both empty")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestCallerWallet_MalformedClaimedFails(t *testing.T) {
	_ = newCallerWalletTestDB(t)
	user := models.User{
		ID:            uuid.New(),
		Username:      "user",
		WalletAddress: "0x1111111111111111111111111111111111111111",
		AccountStatus: models.AccountStatusActive,
	}

	c, w := testGinContext()
	_, ok := callerWallet(c, &user, "not-a-valid-eth-address")
	if ok {
		t.Fatalf("expected failure on malformed address")
	}
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", w.Code)
	}
}

func TestCallerWallet_ClaimedConflictWithOtherActiveUserFails(t *testing.T) {
	db := newCallerWalletTestDB(t)
	claimedAddr := "0x3333333333333333333333333333333333333333"

	otherUser := models.User{
		ID:            uuid.New(),
		Username:      "other_active",
		WalletAddress: claimedAddr,
		AccountStatus: models.AccountStatusActive,
	}
	insertTestUser(t, db, &otherUser)

	currentUser := models.User{
		ID:            uuid.New(),
		Username:      "current_user",
		WalletAddress: "",
		AccountStatus: models.AccountStatusActive,
	}
	insertTestUser(t, db, &currentUser)

	c, w := testGinContext()
	_, ok := callerWallet(c, &currentUser, claimedAddr)
	if ok {
		t.Fatalf("expected conflict failure")
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict, got %d", w.Code)
	}
}

func TestCallerWallet_ClaimedPreviouslyHeldByDeletedAccountSucceeds(t *testing.T) {
	db := newCallerWalletTestDB(t)
	claimedAddr := "0x4444444444444444444444444444444444444444"

	deletedUser := models.User{
		ID:            uuid.New(),
		Username:      "deleted_user",
		WalletAddress: claimedAddr,
		AccountStatus: models.AccountStatusDeleted,
	}
	insertTestUser(t, db, &deletedUser)

	currentUser := models.User{
		ID:            uuid.New(),
		Username:      "active_user",
		WalletAddress: "",
		AccountStatus: models.AccountStatusActive,
	}
	insertTestUser(t, db, &currentUser)

	c, w := testGinContext()
	wallet, ok := callerWallet(c, &currentUser, claimedAddr)
	if !ok {
		t.Fatalf("expected success claiming address from deleted user, got %d %s", w.Code, w.Body.String())
	}
	if wallet != claimedAddr {
		t.Fatalf("expected %q, got %q", claimedAddr, wallet)
	}

	// Verify deleted user had wallet address cleared
	if stored := storedUserWallet(t, db, deletedUser.ID); stored != "" {
		t.Fatalf("expected deleted user wallet_address cleared, got %q", stored)
	}
}
