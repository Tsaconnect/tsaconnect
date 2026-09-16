package handlers

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// The fee-ledger harness does not carry fee_configs; raw DDL, as everywhere
// else here, because the model's Postgres defaults do not parse under SQLite.
func withFeeConfigTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Exec(`CREATE TABLE fee_configs (
		id TEXT PRIMARY KEY, feature TEXT NOT NULL, token_symbol TEXT NOT NULL, chain_id INTEGER,
		bps INTEGER NOT NULL DEFAULT 0, flat_amount_usd REAL NOT NULL DEFAULT 0,
		mcgp_exempt INTEGER NOT NULL DEFAULT 0, active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME, updated_at DATETIME, UNIQUE(feature, token_symbol, chain_id))`).Error; err != nil {
		t.Fatalf("create fee_configs: %v", err)
	}
}

// The rate must follow fee_config — what admins edit and the FeeManager
// reconciler keeps honest. Reading the env constant instead is how the ledger
// keeps booking 3% after someone changes the rate in the console.
func TestAllSwap_BooksTheAdminConfiguredRate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 1000.0, true) // env fallback says 300 bps
	withFeeConfigTable(t, db)
	h.FeeConfigService = services.NewFeeConfigService(db)
	if err := db.Create(&models.FeeConfig{
		Feature: models.FeatureAllswap, TokenSymbol: models.FeeTokenAny, BPS: 150,
	}).Error; err != nil {
		t.Fatalf("seed rate: %v", err)
	}
	if err := h.FeeConfigService.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	user := mkWUser(t, db, "asrate", nil)

	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "rate-1", "fromToken": "USDC", "toToken": "ETH",
		"fromAmount": "1000", "chainId": 1, "fromSymbol": "USDC",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// 1.5% of $1,000 = $15 — the configured rate, not the 3% env default.
	if got := settledFee(t, db, "rate-1").FeeUSD; got < 14.999 || got > 15.001 {
		t.Fatalf("feeUSD=%v, want 15 (the 150 bps in fee_config, not the env 300)", got)
	}
}

// Rates are configurable per chain as well as per token. Looking one up without
// the chain silently ignores a chain-specific rate an admin has set — the exact
// drift that moving the lookup to fee_config was meant to remove.
func TestAllSwap_BooksThePerChainRate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 1000.0, true)
	withFeeConfigTable(t, db)
	h.FeeConfigService = services.NewFeeConfigService(db)

	const chain int64 = 42161
	rows := []models.FeeConfig{
		{Feature: models.FeatureAllswap, TokenSymbol: models.FeeTokenAny, BPS: 300},
		{Feature: models.FeatureAllswap, TokenSymbol: models.FeeTokenAny, ChainID: &[]int64{chain}[0], BPS: 100},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed rate %d: %v", i, err)
		}
	}
	if err := h.FeeConfigService.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	user := mkWUser(t, db, "aschain", nil)

	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "chain-1", "fromToken": "USDC", "toToken": "ETH",
		"fromAmount": "1000", "chainId": chain, "fromSymbol": "USDC",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// 1% of $1,000 = $10 — the Arbitrum-specific row, not the 3% catch-all.
	if got := settledFee(t, db, "chain-1").FeeUSD; got < 9.999 || got > 10.001 {
		t.Fatalf("feeUSD=%v, want 10 (the per-chain 100 bps, not the 300 catch-all)", got)
	}
}

// LiFi collects per chain. On a chain with no fee wallet the quote is served
// fee-less so the swap still works — which means NOTHING was collected, and
// booking the rate anyway would invent income. This is the same over-recording
// the conservative rate was removed for, arriving by another route.
func TestAllSwap_BooksNothingWhenLiFiCollectedNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := setupFeeLedgerTestDB(t)
	h := newAllSwapHandler(t, db, 0, false)
	h.requeryQuoteUSD = func(p settleQuoteParams) requeryResult {
		return requeryResult{Reachable: true, FeeCollectable: false}
	}
	// A price IS available, so the only thing stopping an accrual is knowing
	// that the route earned nothing.
	h.PriceService = services.NewPriceService()
	h.PriceService.SeedPriceForTest("USDC", services.PriceData{USD: 1.0})
	user := mkWUser(t, db, "asnofee", nil)

	w := doSettle(t, h, user, map[string]interface{}{
		"quoteId": "nofee-1", "fromToken": "bitcoin", "toToken": "USDC",
		"fromAmount": "1000000000", "fromSymbol": "USDC", "fromDecimals": 6,
		"chainId": 20000000000001,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	sourceID := deterministicUUID("allswap:nofee-1")
	if got := feeGuardCount(db, "allswap", sourceID); got != 0 {
		t.Fatalf("booked a fee LiFi never collected: %d guard rows", got)
	}
}
