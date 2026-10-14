package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
			quote_chain TEXT,
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

// The app's Spot Trade desk routes same-chain pairs on Solana and Sui, so a
// listing may name them. Guards the widening in models.SpotChains.
func TestCreateSpotListing_NonEVMChainsAccepted(t *testing.T) {
	for _, chain := range []string{"solana", "sui", "robinhood"} {
		t.Run(chain, func(t *testing.T) {
			setupSpotTestDB(t)
			h := newSpotHandlers()
			body := `{"baseSymbol":"USDC","quoteSymbol":"USDT","chain":"` + chain + `"}`
			c, w := spotCtx(t, http.MethodPost, body, adminUser())
			h.CreateSpotListing(c)
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201 for chain %q; body = %s", w.Code, chain, w.Body.String())
			}
		})
	}
}

// Bitcoin carries exactly one token, so a listing that keeps both legs on it
// resolves BTC and then finds no quote token at all. Only allswap, which names
// the quote leg's own chain, can express such a pair. Omitting route is the
// dangerous case: create silently defaults it to lifi.
func TestCreateSpotListing_BitcoinRejectedOffAllSwap(t *testing.T) {
	bodies := map[string]string{
		"route omitted": `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc"}`,
		"lifi":          `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"lifi"}`,
		"mcgp":          `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"mcgp"}`,
		// A quote chain does not rescue a route that ignores it.
		"lifi with quoteChain": `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"lifi","quoteChain":"ethereum"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			setupSpotTestDB(t)
			h := newSpotHandlers()
			c, w := spotCtx(t, http.MethodPost, body, adminUser())
			h.CreateSpotListing(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			var count int64
			config.DB.Model(&models.SpotListing{}).Count(&count)
			if count != 0 {
				t.Fatalf("rejected listing must not persist; got %d rows", count)
			}
		})
	}
}

// spotErrMessage pulls the human-readable reason out of an error envelope.
func spotErrMessage(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v; body = %s", err, w.Body.String())
	}
	return resp.Message
}

// A named quoteChain has to be one the quote asset can live on. An unknown
// chain, or Bitcoin under a stablecoin, is a listing that can never be quoted.
//
// The messages are asserted, not just the status: "not a chain we know" and
// "that asset is not on that chain" are different admin mistakes.
func TestCreateSpotListing_AllSwapQuoteChainValidated(t *testing.T) {
	cases := map[string]struct{ body, wantMsg string }{
		"unknown": {`{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"allswap","quoteChain":"dogechain"}`, "Unsupported quoteChain"},
		// Rejected even when the base leg is an ordinary EVM chain.
		"evm base, unknown quote": {`{"baseSymbol":"USDT","quoteSymbol":"BTC","chain":"ethereum","route":"allswap","quoteChain":"aptos"}`, "Unsupported quoteChain"},
		"stablecoin on bitcoin":   {`{"baseSymbol":"ETH","quoteSymbol":"USDT","chain":"ethereum","route":"allswap","quoteChain":"btc"}`, "USDT does not exist on \"btc\""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setupSpotTestDB(t)
			h := newSpotHandlers()
			c, w := spotCtx(t, http.MethodPost, tc.body, adminUser())
			h.CreateSpotListing(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if msg := spotErrMessage(t, w); !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("message = %q, want it to name %q", msg, tc.wantMsg)
			}
			var count int64
			config.DB.Model(&models.SpotListing{}).Count(&count)
			if count != 0 {
				t.Fatalf("rejected listing must not persist; got %d rows", count)
			}
		})
	}
}

// The listing BTC exists for: base on Bitcoin, quote on an EVM chain. Both
// chains must survive the write normalised, because the desk resolves a leg per
// chain and a stray "ETHEREUM" matches no chain key.
func TestCreateSpotListing_BitcoinAllSwapRoundTrip(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	body := `{"baseSymbol":"btc","quoteSymbol":"usdt","chain":"BTC","route":"AllSwap","quoteChain":" ETHEREUM "}`
	c, w := spotCtx(t, http.MethodPost, body, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data models.SpotListing `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body = %s", err, w.Body.String())
	}
	if resp.Data.Chain != "btc" || resp.Data.QuoteChain != "ethereum" || resp.Data.Route != models.SpotRouteAllSwap {
		t.Fatalf("create response = %+v, want btc/ethereum/allswap", resp.Data)
	}

	var row models.SpotListing
	config.DB.First(&row)
	if row.BaseSymbol != "BTC" || row.QuoteSymbol != "USDT" {
		t.Fatalf("symbols = %s/%s, want BTC/USDT", row.BaseSymbol, row.QuoteSymbol)
	}
	if row.Chain != "btc" || row.QuoteChain != "ethereum" || row.Route != models.SpotRouteAllSwap {
		t.Fatalf("persisted row = %+v, want chain btc, quoteChain ethereum, route allswap", row)
	}
}

// quoteChain describes a leg only allswap routes. Storing one on mcgp/lifi would
// leave the row claiming a second chain that nothing reads, so it is dropped —
// not refused, or an admin console that PUTs the whole object back could never
// move a listing off allswap.
func TestCreateSpotListing_QuoteChainDroppedOffAllSwap(t *testing.T) {
	for _, route := range []string{models.SpotRouteLiFi, models.SpotRouteMCGP} {
		t.Run(route, func(t *testing.T) {
			setupSpotTestDB(t)
			h := newSpotHandlers()
			body := `{"baseSymbol":"MCGP","quoteSymbol":"USDC","chain":"sonic","route":"` + route + `","quoteChain":"ethereum"}`
			c, w := spotCtx(t, http.MethodPost, body, adminUser())
			h.CreateSpotListing(c)
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
			}
			var row models.SpotListing
			config.DB.First(&row)
			if row.QuoteChain != "" {
				t.Fatalf("quoteChain = %q, want empty on route %s", row.QuoteChain, route)
			}
		})
	}
}

// All Swap is cross-chain capable, not cross-chain only: a same-chain allswap
// pair still quotes and still trades, so refusing it would reject a working
// listing over a preference about which route names it.
func TestCreateSpotListing_AllSwapSameChainAllowed(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"ETH","quoteSymbol":"USDT","chain":"ethereum","route":"allswap","quoteChain":"ethereum"}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row)
	if row.QuoteChain != "ethereum" {
		t.Fatalf("quoteChain = %q, want ethereum", row.QuoteChain)
	}
}

// A single-token chain cannot host both legs, whatever the route.
//
// The same-chain allowance above and the single-token rule are each correct
// alone and wrong together: "both legs may share a chain" only holds for a
// chain carrying two tokens. Bitcoin on ONE leg stays legal — that is the
// entire reason allswap exists — so this pins both halves.
func TestCreateSpotListing_AllSwapRefusesBitcoinOnBothLegs(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"allswap","quoteChain":"btc"}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	var n int64
	config.DB.Model(&models.SpotListing{}).Count(&n)
	if n != 0 {
		t.Fatalf("%d listing(s) written despite the refusal", n)
	}
}

// No quoteChain is auto, not a mistake: the app picks the chain the user holds
// the quote asset on, so the listing stores nothing for the second leg.
func TestCreateSpotListing_AllSwapWithoutQuoteChainIsAuto(t *testing.T) {
	for name, body := range map[string]string{
		"missing": `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"allswap"}`,
		"blank":   `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"allswap","quoteChain":"   "}`,
	} {
		t.Run(name, func(t *testing.T) {
			setupSpotTestDB(t)
			h := newSpotHandlers()
			c, w := spotCtx(t, http.MethodPost, body, adminUser())
			h.CreateSpotListing(c)
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
			}
			var row models.SpotListing
			config.DB.First(&row)
			if row.Chain != "btc" || row.QuoteChain != "" || row.Route != models.SpotRouteAllSwap {
				t.Fatalf("row = %+v, want btc on allswap with an empty (auto) quote chain", row)
			}
		})
	}
}

// Bitcoin is a fine quote chain when BTC is what is being quoted.
func TestCreateSpotListing_BitcoinQuoteChainForBTCQuote(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"ETH","quoteSymbol":"BTC","chain":"ethereum","route":"allswap","quoteChain":"btc"}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
}

func TestCreateSpotListing_AllSwapAllowsBitcoinOnOneLeg(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"allswap","quoteChain":"ethereum"}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row)
	if row.Chain != "btc" || row.QuoteChain != "ethereum" {
		t.Fatalf("legs = %q/%q, want btc/ethereum", row.Chain, row.QuoteChain)
	}
}

// The update path validates post-update state as a set, so it must refuse the
// same combination reached one field at a time.
func TestUpdateSpotListing_RefusesBitcoinOnBothLegs(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	c, w := spotCtx(t, http.MethodPost, `{"baseSymbol":"BTC","quoteSymbol":"USDT","chain":"btc","route":"allswap","quoteChain":"ethereum"}`, adminUser())
	h.CreateSpotListing(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("seed status = %d, want 201; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row)

	uc, uw := spotCtx(t, http.MethodPut, `{"quoteChain":"btc"}`, adminUser())
	uc.Params = gin.Params{{Key: "id", Value: row.ID.String()}}
	h.UpdateSpotListing(uc)
	if uw.Code != http.StatusBadRequest {
		t.Fatalf("update status = %d, want 400; body = %s", uw.Code, uw.Body.String())
	}
	var after models.SpotListing
	config.DB.First(&after, "id = ?", row.ID)
	if after.QuoteChain != "ethereum" {
		t.Fatalf("quoteChain = %q, want it unchanged at ethereum", after.QuoteChain)
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

// --- update path -----------------------------------------------------------
//
// Update validates the same rules as create, and has to do it against the row
// as it will look afterwards: a PUT naming one field can still leave the pair
// unquotable through a field it never mentions.

// putSpot runs UpdateSpotListing against an existing row and returns the recorder.
func putSpot(t *testing.T, h *Handlers, id string, body string) *httptest.ResponseRecorder {
	t.Helper()
	c, w := spotCtx(t, http.MethodPut, body, adminUser())
	c.Params = gin.Params{{Key: "id", Value: id}}
	h.UpdateSpotListing(c)
	return w
}

// Moving an ordinary listing onto Bitcoin without also moving it to allswap
// leaves a pair whose quote leg has nowhere to resolve.
func TestUpdateSpotListing_ChainToBitcoinRejectedOffAllSwap(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "bsc", Route: models.SpotRouteLiFi, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	w := putSpot(t, h, listing.ID.String(), `{"chain":"btc"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.Chain != "bsc" {
		t.Fatalf("chain = %q, want bsc — a rejected update must not persist", row.Chain)
	}
}

// The cross-field case: the PUT names only the route, but dropping allswap
// strands the base leg on Bitcoin. Validating route on its own would accept it.
func TestUpdateSpotListing_RouteOffAllSwapStrandsBitcoin(t *testing.T) {
	for _, route := range []string{models.SpotRouteLiFi, models.SpotRouteMCGP} {
		t.Run(route, func(t *testing.T) {
			setupSpotTestDB(t)
			h := newSpotHandlers()
			listing := models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "btc", QuoteChain: "ethereum", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"}
			config.DB.Create(&listing)

			w := putSpot(t, h, listing.ID.String(), `{"route":"`+route+`"}`)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			var row models.SpotListing
			config.DB.First(&row, "id = ?", listing.ID)
			if row.Route != models.SpotRouteAllSwap || row.QuoteChain != "ethereum" {
				t.Fatalf("row = %+v, want route allswap and quoteChain ethereum untouched", row)
			}
		})
	}
}

// Switching an existing listing to allswap without naming the second chain
// leaves it on auto, the same as creating one that way.
func TestUpdateSpotListing_AllSwapWithoutQuoteChainIsAuto(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "ETH", QuoteSymbol: "USDT", Chain: "ethereum", Route: models.SpotRouteLiFi, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	w := putSpot(t, h, listing.ID.String(), `{"route":"allswap"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.Route != models.SpotRouteAllSwap || row.QuoteChain != "" {
		t.Fatalf("row = %+v, want route allswap and no quote chain", row)
	}
}

// An empty quoteChain on a PUT is the admin choosing auto, so it must clear a
// stored chain rather than read as "unchanged" the way other empty fields do.
// An absent one still leaves the stored chain alone.
func TestUpdateSpotListing_EmptyQuoteChainMovesToAuto(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "btc", QuoteChain: "ethereum", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	if w := putSpot(t, h, listing.ID.String(), `{"enabled":false}`); w.Code != http.StatusOK {
		t.Fatalf("toggle status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.QuoteChain != "ethereum" {
		t.Fatalf("quoteChain = %q after a PUT that never named it, want ethereum", row.QuoteChain)
	}

	if w := putSpot(t, h, listing.ID.String(), `{"quoteChain":""}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	config.DB.First(&row, "id = ?", listing.ID)
	if row.QuoteChain != "" {
		t.Fatalf("quoteChain = %q, want empty (auto)", row.QuoteChain)
	}
}

// A quote symbol change is checked against the stored quote chain, so a
// listing cannot be walked onto an impossible pair one field at a time.
func TestUpdateSpotListing_QuoteSymbolCheckedAgainstQuoteChain(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "ETH", QuoteSymbol: "BTC", Chain: "ethereum", QuoteChain: "btc", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	w := putSpot(t, h, listing.ID.String(), `{"quoteSymbol":"usdt"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.QuoteSymbol != "BTC" {
		t.Fatalf("quoteSymbol = %q, want BTC kept", row.QuoteSymbol)
	}
}

// An unknown quote chain is rejected on update too, and must not overwrite the
// working one already stored.
func TestUpdateSpotListing_UnknownQuoteChainRejected(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "btc", QuoteChain: "ethereum", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	w := putSpot(t, h, listing.ID.String(), `{"quoteChain":"dogechain"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.QuoteChain != "ethereum" {
		t.Fatalf("quoteChain = %q, want ethereum kept", row.QuoteChain)
	}
}

// Leaving allswap is legal when the base leg can stand alone, and the stale
// quote chain is cleared rather than left describing a leg nothing routes.
func TestUpdateSpotListing_RouteOffAllSwapClearsQuoteChain(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "USDT", QuoteSymbol: "USDC", Chain: "ethereum", QuoteChain: "polygon", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	w := putSpot(t, h, listing.ID.String(), `{"route":"lifi"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.Route != models.SpotRouteLiFi || row.QuoteChain != "" {
		t.Fatalf("row = %+v, want route lifi and cleared quote chain", row)
	}
}

// Moving the quote leg to another chain is a normal edit; both chains must come
// back normalised, from the response and from the row.
func TestUpdateSpotListing_MovesQuoteChain(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "btc", QuoteChain: "ethereum", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	w := putSpot(t, h, listing.ID.String(), `{"quoteChain":"POLYGON"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data models.SpotListing `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body = %s", err, w.Body.String())
	}
	if resp.Data.Chain != "btc" || resp.Data.QuoteChain != "polygon" {
		t.Fatalf("update response = %+v, want btc/polygon", resp.Data)
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.Chain != "btc" || row.QuoteChain != "polygon" {
		t.Fatalf("persisted row = %+v, want chain btc, quoteChain polygon", row)
	}
}

// A btc listing that stays on allswap is still editable in every unrelated way.
func TestUpdateSpotListing_BitcoinListingUnrelatedEdit(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "btc", QuoteChain: "ethereum", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	w := putSpot(t, h, listing.ID.String(), `{"category":"alpha","displayOrder":7}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.Category != "alpha" || row.DisplayOrder != 7 {
		t.Fatalf("row = %+v, want category alpha and displayOrder 7", row)
	}
	if row.Chain != "btc" || row.QuoteChain != "ethereum" || row.Route != models.SpotRouteAllSwap {
		t.Fatalf("row = %+v, want the btc legs untouched", row)
	}
}

// --- serialisation ---------------------------------------------------------

// The app decides cross-chain routing from quoteChain, so it has to be present
// on both reads — and on a row written before the column existed, whose
// quote_chain is NULL rather than an empty string.
// An auto listing stores no quote chain, but apps that predate auto read
// quoteChain alone and would ask for USDT on the base chain. The public read
// therefore names a working default and flags it; the admin read keeps the
// stored value so the console still shows Auto.
func TestSpotListings_AutoQuoteChainServedWithDefault(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	config.DB.Create(&models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "btc", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"})
	config.DB.Create(&models.SpotListing{BaseSymbol: "WBTC", QuoteSymbol: "USDC", Chain: "btc", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"})
	config.DB.Create(&models.SpotListing{BaseSymbol: "ETH", QuoteSymbol: "USDT", Chain: "ethereum", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"})
	config.DB.Create(&models.SpotListing{BaseSymbol: "SOL", QuoteSymbol: "USDC", Chain: "solana", QuoteChain: "polygon", Route: models.SpotRouteAllSwap, Enabled: true, Category: "spot"})

	type row struct {
		BaseSymbol     string `json:"baseSymbol"`
		QuoteChain     string `json:"quoteChain"`
		QuoteChainAuto bool   `json:"quoteChainAuto"`
	}
	decode := func(t *testing.T, w *httptest.ResponseRecorder) map[string]row {
		t.Helper()
		var resp struct {
			Data struct {
				Listings []row `json:"listings"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v; body = %s", err, w.Body.String())
		}
		out := map[string]row{}
		for _, l := range resp.Data.Listings {
			out[l.BaseSymbol] = l
		}
		return out
	}

	cPub, wPub := spotCtx(t, http.MethodGet, ``, adminUser())
	h.GetSpotListings(cPub)
	pub := decode(t, wPub)
	for sym, want := range map[string]row{
		"BTC":  {QuoteChain: "bsc", QuoteChainAuto: true},      // Bitcoin carries no USDT
		"WBTC": {QuoteChain: "base", QuoteChainAuto: true},     // …nor USDC
		"ETH":  {QuoteChain: "ethereum", QuoteChainAuto: true}, // base chain carries it
		"SOL":  {QuoteChain: "polygon", QuoteChainAuto: false}, // admin-named, binding
	} {
		if got := pub[sym]; got.QuoteChain != want.QuoteChain || got.QuoteChainAuto != want.QuoteChainAuto {
			t.Fatalf("public %s = %+v, want quoteChain %q auto %v", sym, got, want.QuoteChain, want.QuoteChainAuto)
		}
	}

	cAdm, wAdm := spotCtx(t, http.MethodGet, ``, adminUser())
	h.ListSpotListingsAdmin(cAdm)
	if got := decode(t, wAdm)["BTC"]; got.QuoteChain != "" {
		t.Fatalf("admin BTC quoteChain = %q, want empty so the console shows Auto", got.QuoteChain)
	}
}

func TestSpotListings_QuoteChainSerialisedOnBothReads(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	config.DB.Create(&models.SpotListing{BaseSymbol: "BTC", QuoteSymbol: "USDT", Chain: "btc", QuoteChain: "ethereum", Route: models.SpotRouteAllSwap, Enabled: true, DisplayOrder: 0, Category: "spot"})
	// A pre-migration row: inserted without the column, so quote_chain is NULL.
	if err := config.DB.Exec(`INSERT INTO spot_listings
		(id, base_symbol, quote_symbol, chain, route, enabled, display_order, category)
		VALUES ('b1f0a0d2-0000-4000-8000-000000000001','MCGP','USDC','sonic','mcgp',1,1,'spot')`).Error; err != nil {
		t.Fatalf("legacy insert: %v", err)
	}

	type row struct {
		BaseSymbol string `json:"baseSymbol"`
		Chain      string `json:"chain"`
		QuoteChain string `json:"quoteChain"`
	}
	decode := func(t *testing.T, w *httptest.ResponseRecorder) map[string]row {
		t.Helper()
		var resp struct {
			Data struct {
				Listings []row `json:"listings"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v; body = %s", err, w.Body.String())
		}
		out := map[string]row{}
		for _, l := range resp.Data.Listings {
			out[l.BaseSymbol] = l
		}
		return out
	}

	// The key must be present even when empty, so "same-chain listing" never
	// looks like "server that has never heard of quote chains".
	assertShape := func(t *testing.T, label string, body []byte, rows map[string]row) {
		t.Helper()
		if len(rows) != 2 {
			t.Fatalf("%s: listings = %d, want 2", label, len(rows))
		}
		if got := rows["BTC"].QuoteChain; got != "ethereum" {
			t.Fatalf("%s: BTC quoteChain = %q, want ethereum", label, got)
		}
		if got := rows["BTC"].Chain; got != "btc" {
			t.Fatalf("%s: BTC chain = %q, want btc", label, got)
		}
		if got := rows["MCGP"].QuoteChain; got != "" {
			t.Fatalf("%s: legacy MCGP quoteChain = %q, want empty", label, got)
		}
		if !bytes.Contains(body, []byte(`"quoteChain":""`)) {
			t.Fatalf("%s: quoteChain key must serialise even when empty; body = %s", label, body)
		}
	}

	cPub, wPub := spotCtx(t, http.MethodGet, ``, adminUser())
	h.GetSpotListings(cPub)
	if wPub.Code != http.StatusOK {
		t.Fatalf("public read status = %d; body = %s", wPub.Code, wPub.Body.String())
	}
	assertShape(t, "public", wPub.Body.Bytes(), decode(t, wPub))

	cAdm, wAdm := spotCtx(t, http.MethodGet, ``, adminUser())
	h.ListSpotListingsAdmin(cAdm)
	if wAdm.Code != http.StatusOK {
		t.Fatalf("admin read status = %d; body = %s", wAdm.Code, wAdm.Body.String())
	}
	assertShape(t, "admin", wAdm.Body.Bytes(), decode(t, wAdm))
}

// The routes that predate allswap keep working untouched, including a row
// written before quote_chain existed.
func TestSpotListings_LegacyRoutesUnaffected(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	for _, body := range []string{
		`{"baseSymbol":"MCGP","quoteSymbol":"USDC","chain":"sonic","route":"mcgp"}`,
		`{"baseSymbol":"ETH","quoteSymbol":"USDT","chain":"ethereum","route":"lifi"}`,
		`{"baseSymbol":"SOL","quoteSymbol":"USDC","chain":"solana"}`, // route defaults to lifi
	} {
		c, w := spotCtx(t, http.MethodPost, body, adminUser())
		h.CreateSpotListing(c)
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 for %s; body = %s", w.Code, body, w.Body.String())
		}
	}
	var rows []models.SpotListing
	config.DB.Find(&rows)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	for _, r := range rows {
		if r.QuoteChain != "" {
			t.Fatalf("%s: quoteChain = %q, want empty", r.BaseSymbol, r.QuoteChain)
		}
	}

	// A legacy row with a NULL quote_chain must still load and still update.
	if err := config.DB.Exec(`INSERT INTO spot_listings
		(id, base_symbol, quote_symbol, chain, route, enabled, display_order, category)
		VALUES ('b1f0a0d2-0000-4000-8000-000000000002','ADA','USDC','bsc','lifi',1,9,'spot')`).Error; err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	w := putSpot(t, h, "b1f0a0d2-0000-4000-8000-000000000002", `{"displayOrder":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy update status = %d; body = %s", w.Code, w.Body.String())
	}
	var legacy models.SpotListing
	config.DB.First(&legacy, "base_symbol = ?", "ADA")
	if legacy.DisplayOrder != 3 || legacy.QuoteChain != "" {
		t.Fatalf("legacy row = %+v, want displayOrder 3 and empty quote chain", legacy)
	}
}

// LiFi routes Tron, so a listing may name it — but only quoted in USDT: Tron has
// no usable USDC (Circle's is discontinued; LiFi's "USDC" there is an
// unverified 18-decimal token). The rule follows the quote LEG, wherever the
// route puts it.
func TestCreateSpotListing_TronQuotesInUSDTOnly(t *testing.T) {
	cases := map[string]struct {
		body string
		ok   bool
	}{
		"same-chain USDT":           {`{"baseSymbol":"TRX","quoteSymbol":"USDT","chain":"tron"}`, true},
		"same-chain USDC":           {`{"baseSymbol":"TRX","quoteSymbol":"USDC","chain":"tron"}`, false},
		"allswap auto on tron USDC": {`{"baseSymbol":"TRX","quoteSymbol":"USDC","chain":"tron","route":"allswap"}`, false},
		"allswap quote leg on tron": {`{"baseSymbol":"ETH","quoteSymbol":"USDC","chain":"ethereum","route":"allswap","quoteChain":"tron"}`, false},
		"allswap tron USDT quote":   {`{"baseSymbol":"ETH","quoteSymbol":"USDT","chain":"ethereum","route":"allswap","quoteChain":"tron"}`, true},
		// The base leg on Tron with the USDC leg elsewhere is fine.
		"tron base, USDC on base": {`{"baseSymbol":"TRX","quoteSymbol":"USDC","chain":"tron","route":"allswap","quoteChain":"base"}`, true},
		"allswap tron to tron":    {`{"baseSymbol":"TRX","quoteSymbol":"USDT","chain":"tron","route":"allswap","quoteChain":"tron"}`, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setupSpotTestDB(t)
			h := newSpotHandlers()
			c, w := spotCtx(t, http.MethodPost, tc.body, adminUser())
			h.CreateSpotListing(c)
			if tc.ok {
				if w.Code != http.StatusCreated {
					t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if msg := spotErrMessage(t, w); !strings.Contains(msg, "Tron pairs must quote in USDT") {
				t.Fatalf("message = %q, want the USDT-only reason", msg)
			}
		})
	}
}

// A PUT that only changes the quote symbol cannot walk a Tron listing onto USDC.
func TestUpdateSpotListing_TronQuoteSymbolToUSDCRejected(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "TRX", QuoteSymbol: "USDT", Chain: "tron", Route: models.SpotRouteLiFi, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	w := putSpot(t, h, listing.ID.String(), `{"quoteSymbol":"usdc"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.QuoteSymbol != "USDT" {
		t.Fatalf("quoteSymbol = %q, want USDT kept", row.QuoteSymbol)
	}
}

// The app pins exactly TRX and USDT on Tron, so a Tron base leg is TRX (USDT
// against USDT is meaningless, anything else is unpinned), and the MCGP desk,
// which only trades on Sonic, cannot carry a Tron pair at all.
func TestCreateSpotListing_TronBaseIsTRXAndNoMCGPRoute(t *testing.T) {
	cases := map[string]struct{ body, wantMsg string }{
		"USDT/USDT":               {`{"baseSymbol":"USDT","quoteSymbol":"USDT","chain":"tron"}`, "TRX as the base"},
		"unpinned base":           {`{"baseSymbol":"SUN","quoteSymbol":"USDT","chain":"tron"}`, "TRX as the base"},
		"USDT base, quote on bsc": {`{"baseSymbol":"USDT","quoteSymbol":"USDC","chain":"tron","route":"allswap","quoteChain":"bsc"}`, "TRX as the base"},
		"mcgp route":              {`{"baseSymbol":"TRX","quoteSymbol":"USDT","chain":"tron","route":"mcgp"}`, "MCGP desk"},
		"mcgp route, USDC":        {`{"baseSymbol":"MCGP","quoteSymbol":"USDC","chain":"tron","route":"mcgp"}`, "MCGP desk"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			setupSpotTestDB(t)
			h := newSpotHandlers()
			c, w := spotCtx(t, http.MethodPost, tc.body, adminUser())
			h.CreateSpotListing(c)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if msg := spotErrMessage(t, w); !strings.Contains(msg, tc.wantMsg) {
				t.Fatalf("message = %q, want it to name %q", msg, tc.wantMsg)
			}
		})
	}
}

// A PUT naming only the base symbol is checked against the stored Tron chain.
func TestUpdateSpotListing_TronBaseSymbolChecked(t *testing.T) {
	setupSpotTestDB(t)
	h := newSpotHandlers()
	listing := models.SpotListing{BaseSymbol: "TRX", QuoteSymbol: "USDT", Chain: "tron", Route: models.SpotRouteLiFi, Enabled: true, Category: "spot"}
	config.DB.Create(&listing)

	if w := putSpot(t, h, listing.ID.String(), `{"baseSymbol":"usdt"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if w := putSpot(t, h, listing.ID.String(), `{"route":"mcgp"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("route mcgp: status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	var row models.SpotListing
	config.DB.First(&row, "id = ?", listing.ID)
	if row.BaseSymbol != "TRX" || row.Route != models.SpotRouteLiFi {
		t.Fatalf("row = %+v, want TRX on lifi kept", row)
	}
}
