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
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// setupFeeConfigHandlerDB builds an in-memory SQLite DB with just the
// fee_configs table (raw DDL — the handler reads/writes only this table; a nil
// FeeConfigService means the cache-invalidate step is skipped).
func setupFeeConfigHandlerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE fee_configs (
		id TEXT PRIMARY KEY,
		feature TEXT NOT NULL,
		token_symbol TEXT NOT NULL,
		bps INTEGER NOT NULL DEFAULT 0,
		flat_amount_usd REAL NOT NULL DEFAULT 0,
		mcgp_exempt INTEGER NOT NULL DEFAULT 0,
		active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("ddl: %v", err)
	}
	return db
}

func seedFeeConfigRow(t *testing.T, db *gorm.DB) models.FeeConfig {
	t.Helper()
	row := models.FeeConfig{
		ID:          uuid.New(),
		Feature:     models.FeatureProduct,
		TokenSymbol: models.FeeTokenAny,
		BPS:         50,
		Active:      true,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed fee config: %v", err)
	}
	return row
}

// doUpdate fires a PUT /api/admin/fee-config/:id against the handler with the
// given config (flag state) and returns the recorder.
func doUpdate(t *testing.T, db *gorm.DB, cfg *config.Config, id uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewFeeConfigHandler(db, nil, cfg) // nil service ⇒ cache-invalidate skipped
	r := gin.New()
	r.PUT("/api/admin/fee-config/:id", h.UpdateFeeConfig)

	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/admin/fee-config/"+id.String(), bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestUpdateFeeConfig_RejectedWhenFlagOff is FIX-BEFORE-MERGE B: while
// FEE_MODEL_V2_ENABLED is OFF, a fee-rate edit must be rejected (409) with a
// clear message, and the underlying row must be UNCHANGED — the cart quote still
// reads the hardcoded constants, so a config edit would diverge quote-vs-charge.
func TestUpdateFeeConfig_RejectedWhenFlagOff(t *testing.T) {
	db := setupFeeConfigHandlerDB(t)
	row := seedFeeConfigRow(t, db)
	cfgOff := &config.Config{FeeModelV2Enabled: false}

	w := doUpdate(t, db, cfgOff, row.ID, map[string]any{"bps": 999})
	if w.Code != http.StatusConflict {
		t.Fatalf("flag OFF: status = %d, want 409\nbody=%s", w.Code, w.Body.String())
	}

	// Row must be untouched.
	var got models.FeeConfig
	if err := db.First(&got, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.BPS != 50 {
		t.Fatalf("flag OFF must not mutate the row: bps = %d, want 50", got.BPS)
	}
}

// TestUpdateFeeConfig_AllowedWhenFlagOn is the positive case: with
// FEE_MODEL_V2_ENABLED ON the same edit succeeds (200) and persists.
func TestUpdateFeeConfig_AllowedWhenFlagOn(t *testing.T) {
	db := setupFeeConfigHandlerDB(t)
	row := seedFeeConfigRow(t, db)
	cfgOn := &config.Config{FeeModelV2Enabled: true}

	w := doUpdate(t, db, cfgOn, row.ID, map[string]any{"bps": 999})
	if w.Code != http.StatusOK {
		t.Fatalf("flag ON: status = %d, want 200\nbody=%s", w.Code, w.Body.String())
	}

	var got models.FeeConfig
	if err := db.First(&got, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.BPS != 999 {
		t.Fatalf("flag ON should persist the edit: bps = %d, want 999", got.BPS)
	}
}
