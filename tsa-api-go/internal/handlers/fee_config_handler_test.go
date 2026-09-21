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
	// chain_id mirrors the configurable-FeeManager design §4 migration (nullable;
	// NULL = legacy global row).
	if err := db.Exec(`CREATE TABLE fee_configs (
		id TEXT PRIMARY KEY,
		feature TEXT NOT NULL,
		token_symbol TEXT NOT NULL,
		chain_id INTEGER,
		direction TEXT,
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
	return seedFeeConfigRowFor(t, db, models.FeatureServiceOther)
}

func seedFeeConfigRowFor(t *testing.T, db *gorm.DB, feature string) models.FeeConfig {
	t.Helper()
	row := models.FeeConfig{
		ID:          uuid.New(),
		Feature:     feature,
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

// TestUpdateFeeConfig_PersistsEdit is the positive case: a fee-rate edit
// succeeds (200) and persists. (The fee_config table is the live source of truth
// now that the v1→v2 cutover gate has been retired.)
func TestUpdateFeeConfig_PersistsEdit(t *testing.T) {
	db := setupFeeConfigHandlerDB(t)
	row := seedFeeConfigRow(t, db)
	cfg := &config.Config{}

	w := doUpdate(t, db, cfg, row.ID, map[string]any{"bps": 999})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody=%s", w.Code, w.Body.String())
	}

	var got models.FeeConfig
	if err := db.First(&got, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.BPS != 999 {
		t.Fatalf("should persist the edit: bps = %d, want 999", got.BPS)
	}
}

// A rate a contract reads from FeeManager must not be edited in the replica
// alone: the backend would quote and approve one amount while the contract
// pulls another (a short approve reverts the escrow).
func TestUpdateFeeConfig_RefusesOnChainFeatures(t *testing.T) {
	for feature := range models.OnChainFeeFeatures {
		t.Run(feature, func(t *testing.T) {
			db := setupFeeConfigHandlerDB(t)
			row := seedFeeConfigRowFor(t, db, feature)

			w := doUpdate(t, db, &config.Config{}, row.ID, map[string]any{"bps": 150})
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409\nbody=%s", w.Code, w.Body.String())
			}
			var got models.FeeConfig
			db.First(&got, "id = ?", row.ID)
			if got.BPS != row.BPS {
				t.Fatalf("refused edit still changed bps: %d → %d", row.BPS, got.BPS)
			}
		})
	}
}
