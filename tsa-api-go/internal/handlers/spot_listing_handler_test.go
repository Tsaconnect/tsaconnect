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
			category TEXT DEFAULT 'spot',
			pinned_position INTEGER,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_spot_pair
			ON spot_listings (base_symbol, quote_symbol, chain)`,
		`CREATE TABLE IF NOT EXISTS it_rates (
			id TEXT PRIMARY KEY,
			buy_price_usd_micros INTEGER NOT NULL,
			sell_discount_bps INTEGER NOT NULL DEFAULT 0,
			active INTEGER NOT NULL DEFAULT 1,
			updated_by TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
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

func TestCreateSpotListing_CategoryAndPin(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"AAPL","quoteSymbol":"USDC","chain":"base","category":"tstocks","pinnedPosition":4}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row)
	if row.Category != "tstocks" {
		t.Fatalf("category = %q, want tstocks", row.Category)
	}
	if row.PinnedPosition == nil || *row.PinnedPosition != 4 {
		t.Fatalf("pinnedPosition = %v, want 4", row.PinnedPosition)
	}
}

func TestCreateSpotListing_DefaultCategorySpot(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"MCGP","quoteSymbol":"USDC","chain":"sonic"}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row)
	if row.Category != models.SpotCategorySpot {
		t.Fatalf("category = %q, want spot (default)", row.Category)
	}
	if row.PinnedPosition != nil {
		t.Fatalf("pinnedPosition should default nil; got %v", *row.PinnedPosition)
	}
}

func TestCreateSpotListing_InvalidCategory(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"MCGP","quoteSymbol":"USDC","chain":"sonic","category":"memes"}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for invalid category", w.Code)
	}
}

func TestUpdateSpotListing_SetAndClearPin(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	p := 5
	listing := models.SpotListing{BaseSymbol: "ADA", QuoteSymbol: "USDC", Chain: "bsc", Enabled: true, Category: "spot", PinnedPosition: &p}
	config.DB.Create(&listing)

	// Change category + move the pin.
	c1, w1 := spotCtx(t, http.MethodPut, `{"category":"alpha","pinnedPosition":2}`, adminUser())
	c1.Params = gin.Params{{Key: "id", Value: listing.ID.String()}}
	h.UpdateSpotListing(c1)
	if w1.Code != http.StatusOK {
		t.Fatalf("update status = %d; body=%s", w1.Code, w1.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.Category != "alpha" || row.PinnedPosition == nil || *row.PinnedPosition != 2 {
		t.Fatalf("after update: category=%q pin=%v, want alpha/2", row.Category, row.PinnedPosition)
	}

	// pinnedPosition <= 0 clears the pin.
	c2, w2 := spotCtx(t, http.MethodPut, `{"pinnedPosition":0}`, adminUser())
	c2.Params = gin.Params{{Key: "id", Value: listing.ID.String()}}
	h.UpdateSpotListing(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("clear status = %d; body=%s", w2.Code, w2.Body.String())
	}
	config.DB.First(&row, "id = ?", listing.ID)
	if row.PinnedPosition != nil {
		t.Fatalf("pin should be cleared; got %v", *row.PinnedPosition)
	}
}

func TestGetSpotListings_RankedDefaultPinsWithoutPrices(t *testing.T) {
	setupSpotTestDB(t)
	// Deliberately adversarial display_order: ETH before BTC before MCGP.
	config.DB.Create(&models.SpotListing{BaseSymbol: "ETH", QuoteSymbol: "USDC", Chain: "ethereum", Enabled: true, DisplayOrder: 1, Category: "spot"})
	config.DB.Create(&models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "bsc", Enabled: true, DisplayOrder: 2, Category: "spot"})
	config.DB.Create(&models.SpotListing{BaseSymbol: "MCGP", QuoteSymbol: "USDC", Chain: "sonic", Route: "mcgp", Enabled: true, DisplayOrder: 3, Category: "spot"})

	h := newSpotHandlers() // nil PriceService — pins must still order MCGP/BTC/ETH
	c, w := spotCtx(t, http.MethodGet, ``, adminUser())
	h.GetSpotListings(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Listings []struct {
				BaseSymbol string `json:"baseSymbol"`
				Category   string `json:"category"`
			} `json:"listings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []string{"MCGP", "BTC", "ETH"}
	if len(resp.Data.Listings) != 3 {
		t.Fatalf("listings = %d, want 3", len(resp.Data.Listings))
	}
	for i, sym := range want {
		if resp.Data.Listings[i].BaseSymbol != sym {
			t.Fatalf("position %d = %s, want %s", i+1, resp.Data.Listings[i].BaseSymbol, sym)
		}
	}
	if resp.Data.Listings[0].Category != "spot" {
		t.Fatalf("category should serialize; got %q", resp.Data.Listings[0].Category)
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

func TestGetSpotListings_MCGPPriceFromITRate(t *testing.T) {
	setupSpotTestDB(t)
	config.DB.Create(&models.SpotListing{BaseSymbol: "MCGP", QuoteSymbol: "USDC", Chain: "sonic", Route: "mcgp", Enabled: true})
	// Active IT rate: $0.02 per MCGP (20000 micro-dollars).
	config.DB.Create(&models.ITRate{BuyPriceUsdMicros: 20000, Active: true})

	h := newSpotHandlers() // nil PriceService: MCGP must still price from the IT rate
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
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Listings) != 1 {
		t.Fatalf("listings = %d, want 1", len(resp.Data.Listings))
	}
	l := resp.Data.Listings[0]
	if l.Price == nil || *l.Price != 0.02 {
		t.Fatalf("MCGP price = %v, want 0.02 (from IT rate)", l.Price)
	}
	if l.Change24h != nil {
		t.Fatalf("MCGP should have no 24h change; got %v", *l.Change24h)
	}
}
