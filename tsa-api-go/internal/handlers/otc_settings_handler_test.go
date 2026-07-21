package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupOTCSettingsTestDB creates an in-memory SQLite DB with the otc_settings
// table (raw DDL avoids Postgres' gen_random_uuid() default).
func setupOTCSettingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS otc_settings (
		id TEXT PRIMARY KEY,
		buy_enabled INTEGER NOT NULL DEFAULT 1,
		sell_enabled INTEGER NOT NULL DEFAULT 1,
		logo_url TEXT,
		asset_class TEXT,
		category TEXT NOT NULL DEFAULT 'direct',
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("ddl: %v", err)
	}
	return db
}

func otcSettingsCtx(t *testing.T, method, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(method, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	return c, w
}

func decodeOTCSettings(t *testing.T, w *httptest.ResponseRecorder) models.OTCSettings {
	t.Helper()
	var resp struct {
		Success bool               `json:"success"`
		Data    models.OTCSettings `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatalf("success = false; body=%s", w.Body.String())
	}
	return resp.Data
}

func TestGetOTCSettings_SeedsSingletonDefaults(t *testing.T) {
	h := NewOTCSettingsHandler(setupOTCSettingsTestDB(t))

	c, w := otcSettingsCtx(t, http.MethodGet, "")
	h.GetOTCSettings(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := decodeOTCSettings(t, w)
	if !got.BuyEnabled || !got.SellEnabled || got.Category != models.OTCCategoryDirect {
		t.Fatalf("seed defaults wrong: %+v", got)
	}

	// Second read must reuse the same row, not create another.
	c2, w2 := otcSettingsCtx(t, http.MethodGet, "")
	h.GetOTCSettings(c2)
	if got2 := decodeOTCSettings(t, w2); got2.ID != got.ID {
		t.Fatalf("singleton violated: %s != %s", got2.ID, got.ID)
	}
	var count int64
	h.DB.Model(&models.OTCSettings{}).Count(&count)
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
}

func TestUpdateOTCSettings_PartialUpdatePersistsFalse(t *testing.T) {
	h := NewOTCSettingsHandler(setupOTCSettingsTestDB(t))

	// Pause buying only; leave everything else untouched.
	c, w := otcSettingsCtx(t, http.MethodPut, `{"buyEnabled":false}`)
	h.UpdateOTCSettings(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := decodeOTCSettings(t, w)
	if got.BuyEnabled || !got.SellEnabled {
		t.Fatalf("buy pause not persisted: %+v", got)
	}

	// Metadata edit must not resurrect the paused flag.
	c2, w2 := otcSettingsCtx(t, http.MethodPut, `{"logoUrl":" https://cdn/x.png ","assetClass":"RWA","category":"presale"}`)
	h.UpdateOTCSettings(c2)
	got2 := decodeOTCSettings(t, w2)
	if got2.BuyEnabled {
		t.Fatalf("buyEnabled flipped back on metadata edit: %+v", got2)
	}
	if got2.LogoURL != "https://cdn/x.png" || got2.AssetClass != "RWA" || got2.Category != models.OTCCategoryPresale {
		t.Fatalf("metadata not persisted/trimmed: %+v", got2)
	}
}

func TestUpdateOTCSettings_Rejections(t *testing.T) {
	h := NewOTCSettingsHandler(setupOTCSettingsTestDB(t))

	c, w := otcSettingsCtx(t, http.MethodPut, `{"category":"weird"}`)
	h.UpdateOTCSettings(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid category: status = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	c2, w2 := otcSettingsCtx(t, http.MethodPut, `{}`)
	h.UpdateOTCSettings(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("empty body: status = %d, want 400; body=%s", w2.Code, w2.Body.String())
	}
}
