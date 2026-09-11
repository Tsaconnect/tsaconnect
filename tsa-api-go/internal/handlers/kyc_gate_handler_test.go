package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// createHandlerUsersTable adds the users table the money-in gate reads. Raw
// DDL, not AutoMigrate: models.User's id column carries a PostgreSQL-only
// `default:gen_random_uuid()` that SQLite's driver cannot parse (repo
// convention). verification_status defaults to 'pending' exactly as it does in
// production, so a fixture that forgets to seed a user gets the unverified
// behaviour rather than an accidental pass.
func createHandlerUsersTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE users (
		id TEXT PRIMARY KEY,
		name TEXT,
		email TEXT,
		wallet_address TEXT,
		verification_status TEXT DEFAULT 'pending'
	)`).Error; err != nil {
		t.Fatalf("create users: %v", err)
	}
}

func seedHandlerUserAt(t *testing.T, db *gorm.DB, status string) *models.User {
	t.Helper()
	u := &models.User{
		ID:                 uuid.New(),
		Name:               "JOHN DOE",
		WalletAddress:      "0xWALLET",
		VerificationStatus: status,
	}
	if err := db.Exec(
		`INSERT INTO users (id, name, email, wallet_address, verification_status) VALUES (?, ?, ?, ?, ?)`,
		u.ID, u.Name, u.ID.String()+"@test.com", u.WalletAddress, status,
	).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// The refusal must reach the app as 403 plus the machine code it routes on,
// and must not leave a half-open order behind.
func TestITBuy_UnverifiedUserIsRefusedWithTheKYCCode(t *testing.T) {
	for _, status := range []string{
		models.VerificationStatusPending,
		models.VerificationStatusInReview,
		models.VerificationStatusRejected,
	} {
		h, db, _ := newITBuyHandler(t)
		user := seedHandlerUserAt(t, db, status)

		w := doITBuy(t, h, user, "100000")

		if w.Code != http.StatusForbidden {
			t.Fatalf("status %q: HTTP %d, want 403 (body=%s)", status, w.Code, w.Body.String())
		}
		resp := decodeITResponse(t, w)
		if resp["code"] != "kyc_verification_required" {
			t.Fatalf("status %q: code = %v, want kyc_verification_required (body=%s)", status, resp["code"], w.Body.String())
		}
		var n int64
		db.Model(&models.ITIntent{}).Count(&n)
		if n != 0 {
			t.Fatalf("status %q: %d intent(s) created despite the refusal", status, n)
		}
	}
}

// An unverified user with no virtual account must be sent to verification, not
// to activate a rail that is being retired — the VA branch sits ahead of
// OpenBuyOrder and would otherwise answer first.
func TestITBuy_UnverifiedUserIsNotSentToVAActivation(t *testing.T) {
	h, db, _ := newITBuyHandler(t)
	user := seedHandlerUserAt(t, db, models.VerificationStatusPending)

	w := doITBuy(t, h, user, "100000")

	resp := decodeITResponse(t, w)
	if data, ok := resp["data"].(map[string]any); ok && data["needsActivation"] == true {
		t.Fatalf("unverified user was told to activate a virtual account instead of to verify: %s", w.Body.String())
	}
}

func TestITBuy_VerifiedUserIsAdmitted(t *testing.T) {
	h, db, it := newITBuyHandler(t)
	it.SetInboundCentral(true, testCentralAccount())
	user := seedHandlerUserAt(t, db, models.VerificationStatusVerified)

	w := doITBuy(t, h, user, "100000")

	if w.Code != http.StatusOK {
		t.Fatalf("verified user refused: HTTP %d (body=%s)", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.ITIntent{}).Count(&n)
	if n != 1 {
		t.Fatalf("intents = %d, want the order to have opened", n)
	}
}

// The stablecoin leg surfaces the same refusal through the shared ramp error
// mappers rather than its own branch, so both legs hand the app one code.
func TestStableMappers_KYCRefusalIs403WithTheSharedCode(t *testing.T) {
	if got := stableStatusFor(services.ErrKYCVerificationRequired); got != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", got)
	}
	if got := stableCodeFor(services.ErrKYCVerificationRequired); got != "kyc_verification_required" {
		t.Fatalf("code = %q, want kyc_verification_required", got)
	}
}

// P2P maps it through mapServiceError — 403, not the 500 an unmapped service
// error would have produced.
func TestP2PMapServiceError_KYCRefusalIs403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if !mapServiceError(c, services.ErrKYCVerificationRequired) {
		t.Fatal("mapServiceError did not handle the refusal")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body=%s)", w.Code, w.Body.String())
	}
}
