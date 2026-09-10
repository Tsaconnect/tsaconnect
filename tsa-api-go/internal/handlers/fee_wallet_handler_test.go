package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
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
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS fee_wallet_assignments (
		id TEXT PRIMARY KEY,
		address TEXT NOT NULL,
		previous_address TEXT,
		assigned_by TEXT,
		note TEXT,
		created_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("ddl fee_wallet_assignments: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
		)`).Error; err != nil {
		t.Fatalf("ddl users: %v", err)
	}
	return db
}

// stubEscrows stands in for the per-chain escrow registry's on-chain reads.
type stubEscrows struct {
	sinks []services.FeeSinkStatus
}

func (s stubEscrows) SystemWallets() []services.FeeSinkStatus { return s.sinks }

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
	OnChain         []struct {
		ChainID      int64  `json:"chainId"`
		ChainKey     string `json:"chainKey"`
		Contract     string `json:"contract"`
		Verified     bool   `json:"verified"`
		SystemWallet string `json:"systemWallet"`
		Matches      bool   `json:"matches"`
		Reason       string `json:"reason"`
	} `json:"onChain"`
	History []struct {
		Address         string `json:"address"`
		PreviousAddress string `json:"previousAddress"`
		AssignedByName  string `json:"assignedByName"`
		Note            string `json:"note"`
	} `json:"history"`
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

// anyAdmin is the plain-admin caller the read endpoints accept.
func anyAdmin() *models.User {
	return &models.User{ID: uuid.New(), Role: models.RoleAdmin}
}

func superAdmin(id uuid.UUID) *models.User {
	return &models.User{ID: id, Username: "ops-lead", Role: models.RoleSuperAdmin}
}

func TestGetFeeWallet_SeedsFromConfigOnce(t *testing.T) {
	h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet, nil)

	c, w := feeWalletCtx(t, http.MethodGet, "", anyAdmin())
	h.GetFeeWallet(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := decodeFeeWallet(t, w)
	if got.Address != seedWallet {
		t.Fatalf("address = %q, want the configured seed %q", got.Address, seedWallet)
	}
	// No escrow registry wired: no rows at all, rather than a green verdict
	// covering contracts nobody read.
	if len(got.OnChain) != 0 {
		t.Fatalf("onChain rows = %d with no registry configured: %+v", len(got.OnChain), got.OnChain)
	}
	// The seed itself is recorded, so the trail starts where the wallet did.
	if len(got.History) != 1 || got.History[0].Address != seedWallet {
		t.Fatalf("seed not recorded in history: %+v", got.History)
	}

	// Second read reuses the row — a second seed would silently reset an
	// assignment made in between.
	c2, w2 := feeWalletCtx(t, http.MethodGet, "", anyAdmin())
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
	admin := superAdmin(uuid.New())
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

	// The reassignment is appended to the history with what it replaced —
	// newest first, seed row behind it.
	if len(got.History) != 2 {
		t.Fatalf("history rows = %d, want 2 (seed + reassignment): %+v", len(got.History), got.History)
	}
	if got.History[0].Address != newAddr || got.History[0].PreviousAddress != seedWallet {
		t.Fatalf("history head wrong: %+v", got.History[0])
	}
	if got.History[0].AssignedByName != "ops-lead" {
		t.Fatalf("history not attributed: %+v", got.History[0])
	}

	// The change must survive a re-read (Updates(map), not a struct write).
	c2, w2 := feeWalletCtx(t, http.MethodGet, "", anyAdmin())
	h.GetFeeWallet(c2)
	if reread := decodeFeeWallet(t, w2); reread.Address != newAddr {
		t.Fatalf("address after re-read = %q, want %q", reread.Address, newAddr)
	}
}

func TestUpdateFeeWallet_RejectsMalformedAddress(t *testing.T) {
	h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet, nil)
	admin := superAdmin(uuid.New())

	for _, body := range []string{
		`{"address":"not-an-address"}`,
		`{"address":"0x123"}`,
		`{"address":""}`,
		`{}`,
	} {
		c, w := feeWalletCtx(t, http.MethodPut, body, admin)
		h.UpdateFeeWallet(c)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400; body=%s", body, w.Code, w.Body.String())
		}
	}

	// A rejected write must leave the previous assignment alone.
	c, w := feeWalletCtx(t, http.MethodGet, "", anyAdmin())
	h.GetFeeWallet(c)
	if got := decodeFeeWallet(t, w); got.Address != seedWallet {
		t.Fatalf("address = %q after rejected writes, want %q", got.Address, seedWallet)
	}
}

func TestGetFeeWallet_OnChainComparisonIsPerContract(t *testing.T) {
	const sonicEscrow = "0x1Dc4000000000000000000000000000000003cE1"
	const bscEscrow = "0x54e3000000000000000000000000000000007959"

	// Sonic pays the assigned wallet; BSC still pays the old one; a third chain
	// can't be read at all. Each must be reported on its own terms — an
	// aggregate verdict would let the BSC drift hide behind Sonic's match.
	h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet, stubEscrows{sinks: []services.FeeSinkStatus{
		{ChainID: 146, ChainKey: "sonic", Contract: sonicEscrow,
			SystemWallet: "0xaf326d5d242c9a55590540f14658adddd3586a8d"},
		{ChainID: 97, ChainKey: "bsc", Contract: bscEscrow,
			SystemWallet: "0x2222222222222222222222222222222222222222"},
		{ChainID: 1, ChainKey: "ethereum", Contract: "0x3333333333333333333333333333333333333333",
			Reason: "dial tcp: connection refused"},
	}})

	c, w := feeWalletCtx(t, http.MethodGet, "", anyAdmin())
	h.GetFeeWallet(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an RPC outage must not fail the panel; body=%s", w.Code, w.Body.String())
	}
	got := decodeFeeWallet(t, w)
	if len(got.OnChain) != 3 {
		t.Fatalf("onChain rows = %d, want one per configured escrow: %+v", len(got.OnChain), got.OnChain)
	}

	byChain := map[int64]int{}
	for i, r := range got.OnChain {
		byChain[r.ChainID] = i
	}

	sonic := got.OnChain[byChain[146]]
	if !sonic.Verified || !sonic.Matches {
		t.Fatalf("sonic should match the assigned wallet regardless of address casing: %+v", sonic)
	}
	if sonic.ChainKey != "sonic" || sonic.Contract != sonicEscrow {
		t.Fatalf("sonic row must name the chain and contract it checked: %+v", sonic)
	}

	bsc := got.OnChain[byChain[97]]
	if !bsc.Verified {
		t.Fatalf("bsc read succeeded, so it is verified: %+v", bsc)
	}
	if bsc.Matches {
		t.Fatalf("bsc pays a different wallet and must not report a match: %+v", bsc)
	}

	eth := got.OnChain[byChain[1]]
	if eth.Verified || eth.Reason == "" {
		t.Fatalf("unreadable contract must stay visible as unverified with a reason: %+v", eth)
	}
	if eth.Matches {
		t.Fatalf("unread contract must never report a match: %+v", eth)
	}
}

func TestFeeWallet_RoleGates(t *testing.T) {
	h := NewFeeWalletHandler(setupFeeWalletTestDB(t), seedWallet, nil)
	body := `{"address":"0x1111111111111111111111111111111111111111"}`

	// The route middleware gates these too; the handler repeats the check so
	// a rewiring mistake can't expose fee routing.
	c, w := feeWalletCtx(t, http.MethodGet, "", nil)
	h.GetFeeWallet(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET: status = %d, want 401", w.Code)
	}

	c2, w2 := feeWalletCtx(t, http.MethodGet, "", &models.User{ID: uuid.New(), Role: models.RoleUser})
	h.GetFeeWallet(c2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("plain user GET: status = %d, want 403", w2.Code)
	}

	// A plain admin may read the panel but not reassign the wallet.
	c3, w3 := feeWalletCtx(t, http.MethodPut, body, anyAdmin())
	h.UpdateFeeWallet(c3)
	if w3.Code != http.StatusForbidden {
		t.Fatalf("admin PUT: status = %d, want 403 (super_admin only)", w3.Code)
	}

	c4, w4 := feeWalletCtx(t, http.MethodGet, "", anyAdmin())
	h.GetFeeWallet(c4)
	if got := decodeFeeWallet(t, w4); got.Address != seedWallet {
		t.Fatalf("address = %q after a forbidden write, want %q", got.Address, seedWallet)
	}
}
