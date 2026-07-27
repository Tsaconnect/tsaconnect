package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

const seedWallet = "0xaF326D5D242C9A55590540f14658adDDd3586A8d"

// setupFeeWalletTestDB creates an in-memory SQLite DB with the fee_wallet_settings
// and users tables (raw DDL avoids Postgres' gen_random_uuid() default).
func setupFeeWalletTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS fee_wallet_settings (
		id TEXT PRIMARY KEY,
		address TEXT NOT NULL,
		updated_by TEXT,
		note TEXT,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("ddl fee_wallet_settings: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT
	)`).Error; err != nil {
		t.Fatalf("ddl users: %v", err)
	}
	return db
}

// stubEscrow stands in for the P2P escrow service's on-chain read.
type stubEscrow struct {
	addr    string
	onChain string
	err     error
}

func (s stubEscrow) ContractAddress() string           { return s.addr }
func (s stubEscrow) ReadSystemWallet() (string, error) { return s.onChain, s.err }

func feeWalletCtx(t *testing.T, method, body string, admin *models.User) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(method, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	if admin != nil {
		c.Set("user", admin)
	}
	return c, w
}

type feeWalletBody struct {
	Address         string `json:"address"`
	Note            string `json:"note"`
	UpdatedBy       string `json:"updatedBy"`
	UpdatedByName   string `json:"updatedByName"`
	ConfiguredValue string `json:"configuredValue"`
	OnChain         struct {
		Verified bool   `json:"verified"`
		Contract string `json:"contract"`
		Address  string `json:"address"`
		Matches  bool   `json:"matches"`
		Reason   string `json:"reason"`
	} `json:"onChain"`
}

func decodeFeeWallet(t *testing.T, w *httptest.ResponseRecorder) feeWalletBody {
	t.Helper()
	var resp struct {
		Success bool          `json:"success"`
		Data    feeWalletBody `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatalf("success = false; body=%s", w.Body.String())
	}
	return resp.Data
}

func TestGetFeeWallet_SeedsFromConfigOnce(t *testing.T) {
	h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet, nil)

	c, w := feeWalletCtx(t, http.MethodGet, "", nil)
	h.GetFeeWallet(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := decodeFeeWallet(t, w)
	if got.Address != seedWallet {
		t.Fatalf("address = %q, want the configured seed %q", got.Address, seedWallet)
	}
	// No escrow wired: the panel must say so rather than claim a match.
	if got.OnChain.Verified {
		t.Fatalf("onChain.verified = true with no escrow configured: %+v", got.OnChain)
	}

	// Second read reuses the row — a second seed would silently reset an
	// assignment made in between.
	c2, w2 := feeWalletCtx(t, http.MethodGet, "", nil)
	h.GetFeeWallet(c2)
	decodeFeeWallet(t, w2)
	var count int64
	h.DB.Model(&models.FeeWalletSetting{}).Count(&count)
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
}

func TestUpdateFeeWallet_PersistsAndAttributes(t *testing.T) {
	db := setupFeeWalletTestDB(t)
	admin := &models.User{ID: uuid.New(), Username: "ops-lead"}
	if err := db.Exec(`INSERT INTO users (id, username) VALUES (?, ?)`, admin.ID, admin.Username).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h := NewFeeWalletHandler(db, seedWallet, nil)

	newAddr := "0x1111111111111111111111111111111111111111"
	c, w := feeWalletCtx(t, http.MethodPut,
		`{"address":"  `+newAddr+`  ","note":"rotated after key change"}`, admin)
	h.UpdateFeeWallet(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := decodeFeeWallet(t, w)
	if got.Address != newAddr {
		t.Fatalf("address = %q, want %q (trimmed)", got.Address, newAddr)
	}
	if got.UpdatedBy != admin.ID.String() || got.UpdatedByName != "ops-lead" {
		t.Fatalf("assignment not attributed: updatedBy=%q name=%q", got.UpdatedBy, got.UpdatedByName)
	}
	if got.Note != "rotated after key change" {
		t.Fatalf("note = %q", got.Note)
	}
	// configuredValue keeps reporting the env default so the panel can show
	// that the live assignment has diverged from the deploy-time value.
	if got.ConfiguredValue != seedWallet {
		t.Fatalf("configuredValue = %q, want %q", got.ConfiguredValue, seedWallet)
	}

	// The change must survive a re-read (Updates(map), not a struct write).
	c2, w2 := feeWalletCtx(t, http.MethodGet, "", nil)
	h.GetFeeWallet(c2)
	if reread := decodeFeeWallet(t, w2); reread.Address != newAddr {
		t.Fatalf("address after re-read = %q, want %q", reread.Address, newAddr)
	}
}

func TestUpdateFeeWallet_RejectsMalformedAddress(t *testing.T) {
	h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet, nil)

	for _, body := range []string{
		`{"address":"not-an-address"}`,
		`{"address":"0x123"}`,
		`{"address":""}`,
		`{}`,
	} {
		c, w := feeWalletCtx(t, http.MethodPut, body, nil)
		h.UpdateFeeWallet(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400; body=%s", body, w.Code, w.Body.String())
		}
	}

	// A rejected write must leave the previous assignment alone.
	c, w := feeWalletCtx(t, http.MethodGet, "", nil)
	h.GetFeeWallet(c)
	if got := decodeFeeWallet(t, w); got.Address != seedWallet {
		t.Fatalf("address = %q after rejected writes, want %q", got.Address, seedWallet)
	}
}

func TestGetFeeWallet_OnChainComparison(t *testing.T) {
	const contract = "0x1Dc4000000000000000000000000000000003cE1"

	t.Run("mismatch is reported", func(t *testing.T) {
		h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet,
			stubEscrow{addr: contract, onChain: "0x2222222222222222222222222222222222222222"})
		c, w := feeWalletCtx(t, http.MethodGet, "", nil)
		h.GetFeeWallet(c)
		got := decodeFeeWallet(t, w)
		if !got.OnChain.Verified {
			t.Fatalf("verified = false, want true; %+v", got.OnChain)
		}
		if got.OnChain.Matches {
			t.Fatalf("matches = true for a different on-chain wallet; %+v", got.OnChain)
		}
	})

	t.Run("match ignores address casing", func(t *testing.T) {
		h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet,
			stubEscrow{addr: contract, onChain: "0xaf326d5d242c9a55590540f14658adddd3586a8d"})
		c, w := feeWalletCtx(t, http.MethodGet, "", nil)
		h.GetFeeWallet(c)
		got := decodeFeeWallet(t, w)
		if !got.OnChain.Matches {
			t.Fatalf("matches = false for the same address in different case; %+v", got.OnChain)
		}
	})

	t.Run("rpc failure degrades to unverified", func(t *testing.T) {
		h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet,
			stubEscrow{addr: contract, err: errors.New("dial tcp: connection refused")})
		c, w := feeWalletCtx(t, http.MethodGet, "", nil)
		h.GetFeeWallet(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 — an RPC outage must not fail the panel", w.Code)
		}
		got := decodeFeeWallet(t, w)
		if got.OnChain.Verified || got.OnChain.Reason == "" {
			t.Fatalf("expected unverified with a reason; %+v", got.OnChain)
		}
	})
}
