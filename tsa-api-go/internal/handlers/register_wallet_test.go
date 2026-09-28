// Tests for the register/unlink boundary on POST /api/wallet/register.
//
// Unlinking is destructive: it clears users.wallet_address, and every
// entitlement read then reports "Set up a wallet first". So the handler must
// distinguish a request that DELIBERATELY sends an empty address (the user
// deleted their last wallet) from one that simply omitted the field. Modelling
// the field as a plain string collapses those two cases into the zero value,
// which is how a malformed request would silently detach an account.

package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const registeredWallet = "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"

func newRegisterRig(t *testing.T, registered string) (*gin.Engine, *models.User, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// created_at/updated_at are required: GORM stamps updated_at on every
	// Update, so omitting the column fails the write with "no such column".
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY, username TEXT, name TEXT, wallet_address TEXT,
		account_status TEXT DEFAULT 'active',
		created_at DATETIME, updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
	if err := db.Exec(`CREATE TABLE wallets (
		id TEXT PRIMARY KEY, user_id TEXT, selected_asset TEXT, addresses TEXT,
		security TEXT, settings TEXT, transaction_limit REAL, daily_limit REAL,
		last_synced DATETIME, created_at DATETIME, updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create wallets: %v", err)
	}
	ensureWalletLinkChallenges(t, db)

	user := &models.User{ID: uuid.New(), Username: "holder", Name: "Holder", WalletAddress: registered}
	if err := db.Exec(`INSERT INTO users (id, username, name, wallet_address) VALUES (?,?,?,?)`,
		user.ID.String(), user.Username, user.Name, user.WalletAddress).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	config.DB = db

	h := &Handlers{}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("user", user); c.Next() })
	r.POST("/wallet/register", h.RegisterWalletAddress)
	return r, user, db
}

func postRegister(t *testing.T, r *gin.Engine, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/wallet/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func storedWallet(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var got string
	if err := db.Raw(`SELECT wallet_address FROM users WHERE id = ?`, id.String()).Scan(&got).Error; err != nil {
		t.Fatalf("read wallet_address: %v", err)
	}
	return got
}

// The bug this pins: a body with no walletAddress must NOT be read as "unlink".
func TestRegisterWallet_OmittedFieldIsRejectedNotTreatedAsUnlink(t *testing.T) {
	r, user, db := newRegisterRig(t, registeredWallet)

	if code := postRegister(t, r, `{}`); code != http.StatusBadRequest {
		t.Fatalf("a body with no walletAddress must be 400, got %d", code)
	}
	if got := storedWallet(t, db, user.ID); got != registeredWallet {
		t.Fatalf("the registered wallet must be untouched, got %q", got)
	}
	if user.WalletAddress != registeredWallet {
		t.Fatalf("in-memory user must be untouched, got %q", user.WalletAddress)
	}
}

// An explicit empty string is the deliberate unlink the app sends when the user
// deletes their last wallet.
func TestRegisterWallet_ExplicitEmptyStringUnlinks(t *testing.T) {
	r, user, db := newRegisterRig(t, registeredWallet)

	if code := postRegister(t, r, `{"walletAddress":""}`); code != http.StatusOK {
		t.Fatalf("an explicit empty address must unlink, got %d", code)
	}
	if got := storedWallet(t, db, user.ID); got != "" {
		t.Fatalf("wallet_address should be cleared, got %q", got)
	}
	if user.WalletAddress != "" {
		t.Fatalf("in-memory user should be cleared, got %q", user.WalletAddress)
	}
}

// Whitespace is trimmed to empty, so it takes the unlink path rather than being
// stored as an address that could never be signed for.
func TestRegisterWallet_WhitespaceIsNeverStoredAsAnAddress(t *testing.T) {
	r, user, db := newRegisterRig(t, registeredWallet)

	postRegister(t, r, `{"walletAddress":"   "}`)

	if got := storedWallet(t, db, user.ID); strings.TrimSpace(got) != got || got == "   " {
		t.Fatalf("whitespace must never be stored as a wallet address, got %q", got)
	}
}

func TestRegisterWallet_MalformedAddressIsRejected(t *testing.T) {
	r, user, db := newRegisterRig(t, registeredWallet)

	if code := postRegister(t, r, `{"walletAddress":"not-an-address"}`); code != http.StatusBadRequest {
		t.Fatalf("malformed address must be 400, got %d", code)
	}
	if got := storedWallet(t, db, user.ID); got != registeredWallet {
		t.Fatalf("a rejected request must not change the stored address, got %q", got)
	}
}

func TestRegisterWallet_ValidAddressIsStored(t *testing.T) {
	r, user, db := newRegisterRig(t, "")
	wallet := newTestWallet(t)

	// Without proof of ownership, nothing is linked.
	body, _ := json.Marshal(map[string]string{"walletAddress": wallet.Address})
	if code := postRegister(t, r, string(body)); code != http.StatusForbidden {
		t.Fatalf("an unproven address must be refused (403), got %d", code)
	}

	proof := signedProof(t, db, user.ID, wallet)
	body, _ = json.Marshal(map[string]string{
		"walletAddress": wallet.Address, "walletNonce": proof.Nonce, "walletSignature": proof.Signature,
	})
	if code := postRegister(t, r, string(body)); code != http.StatusOK {
		t.Fatalf("a proven address must register, got %d", code)
	}
	if got := storedWallet(t, db, user.ID); got != wallet.Address {
		t.Fatalf("expected the address to be stored, got %q", got)
	}
}

// A wallet address previously linked to an account that was deleted must NOT
// block a new account (or re-created account) from registering that same
// non-custodial address.
func TestRegisterWallet_AddressFromDeletedAccountCanBeClaimedByNewUser(t *testing.T) {
	r, user, db := newRegisterRig(t, "")
	wallet := newTestWallet(t)

	// An old deleted user who still holds the wallet, in another letter case.
	deletedUserID := uuid.New()
	if err := db.Exec(`INSERT INTO users (id, username, name, wallet_address, account_status) VALUES (?,?,?,?,?)`,
		deletedUserID.String(), "old_user", "Old User", strings.ToLower(wallet.Address), models.AccountStatusDeleted).Error; err != nil {
		t.Fatalf("seed deleted user: %v", err)
	}

	proof := signedProof(t, db, user.ID, wallet)
	body, _ := json.Marshal(map[string]string{
		"walletAddress": wallet.Address, "walletNonce": proof.Nonce, "walletSignature": proof.Signature,
	})
	if code := postRegister(t, r, string(body)); code != http.StatusOK {
		t.Fatalf("registering address from a deleted account must succeed (200), got %d", code)
	}
	if got := storedWallet(t, db, user.ID); got != wallet.Address {
		t.Fatalf("expected active user to now hold the address, got %q", got)
	}
	// The deleted user's reference should be released
	if got := storedWallet(t, db, deletedUserID); got != "" {
		t.Fatalf("expected deleted user's wallet_address to be cleared, got %q", got)
	}
}
