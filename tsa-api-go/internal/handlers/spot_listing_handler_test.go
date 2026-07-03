package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupSpotTestDB creates an in-memory SQLite DB with the spot_listings table
// (raw DDL avoids Postgres' gen_random_uuid() default) and points config.DB at it.
func setupSpotTestDB(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS spot_listings (
			id TEXT PRIMARY KEY,
			base_symbol TEXT NOT NULL,
			quote_symbol TEXT NOT NULL,
			chain TEXT NOT NULL,
			route TEXT DEFAULT 'lifi',
			logo_url TEXT,
			enabled INTEGER NOT NULL DEFAULT 1,
			display_order INTEGER,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_spot_pair
			ON spot_listings (base_symbol, quote_symbol, chain)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	config.DB = db
}

func spotCtx(t *testing.T, method, body string, user *models.User) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(method, "/", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req
	if user != nil {
		c.Set("user", user)
	}
	return c, w
}

func adminUser() *models.User    { return &models.User{Role: models.RoleAdmin} }
func merchantUser() *models.User { return &models.User{Role: models.RoleMerchant} }

// newSpotHandlers builds a Handlers with a nil PriceService so enrichment never
// hits the network in tests (missing price → nil, which is the intended behaviour).
func newSpotHandlers() *Handlers { return &Handlers{} }

func TestCreateSpotListing_AdminHappyPath(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"mcgp","quoteSymbol":"usdc","chain":"sonic","route":"mcgp"}`, adminUser())
	h.CreateSpotListing(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var count int64
	config.DB.Model(&models.SpotListing{}).Count(&count)
	if count != 1 {
		t.Fatalf("row count = %d, want 1", count)
	}
	var row models.SpotListing
	config.DB.First(&row)
	if row.ID.String() == "" || row.BaseSymbol != "MCGP" || row.QuoteSymbol != "USDC" || row.Chain != "sonic" || row.Route != "mcgp" {
		t.Fatalf("row normalized wrong: %+v", row)
	}
}

func TestCreateSpotListing_NonAdminForbidden(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	// merchant is admitted by adminAuth but must be rejected by the inline re-check.
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"MCGP","quoteSymbol":"USDC","chain":"sonic"}`, merchantUser())
	h.CreateSpotListing(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	var count int64
	config.DB.Model(&models.SpotListing{}).Count(&count)
	if count != 0 {
		t.Fatalf("no row should be created; got %d", count)
	}
}

func TestCreateSpotListing_InvalidChain(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"MCGP","quoteSymbol":"USDC","chain":"dogechain"}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unsupported chain", w.Code)
	}
}

func TestCreateSpotListing_DuplicatePair(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	body := `{"baseSymbol":"MCGP","quoteSymbol":"USDC","chain":"sonic","route":"mcgp"}`

	c1, w1 := spotCtx(t, http.MethodPost, body, adminUser())
	h.CreateSpotListing(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, want 201", w1.Code)
	}
	c2, w2 := spotCtx(t, http.MethodPost, body, adminUser())
	h.CreateSpotListing(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("duplicate create status = %d, want 400", w2.Code)
	}
}

func TestGetSpotListings_EnabledOnlyAndNullPrice(t *testing.T) {
	setupSpotTestDB(t)
	// One enabled + one disabled row.
	config.DB.Create(&models.SpotListing{BaseSymbol: "MCGP", QuoteSymbol: "USDC", Chain: "sonic", Route: "mcgp", Enabled: true, DisplayOrder: 0})
	config.DB.Create(&models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "bsc", Route: "lifi", Enabled: false, DisplayOrder: 1})

	h := newSpotHandlers() // nil PriceService → no network, price null
	c, w := spotCtx(t, http.MethodGet, ``, adminUser())
	h.GetSpotListings(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Listings []struct {
				BaseSymbol string   `json:"baseSymbol"`
				Price      *float64 `json:"price"`
				Change24h  *float64 `json:"change24h"`
			} `json:"listings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	if len(resp.Data.Listings) != 1 {
		t.Fatalf("listings = %d, want 1 (disabled excluded)", len(resp.Data.Listings))
	}
	l := resp.Data.Listings[0]
	if l.BaseSymbol != "MCGP" {
		t.Fatalf("baseSymbol = %q, want MCGP", l.BaseSymbol)
	}
	if l.Price != nil || l.Change24h != nil {
		t.Fatalf("price/change should be null without a price source; got %v/%v", l.Price, l.Change24h)
	}
}
