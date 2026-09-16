package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

const (
	feeSonicC int64 = 146
)

// stubChainReader is a handler-package FeeManagerChainReader stub.
type stubChainReader struct {
	rates  map[string]services.FeeManagerRate
	txOK   map[string]bool
	rateOK bool
}

func newStubChainReader() *stubChainReader {
	return &stubChainReader{rates: map[string]services.FeeManagerRate{}, txOK: map[string]bool{}, rateOK: true}
}

func feeKey(chainID int64, feature, token string) string {
	return strconv.FormatInt(chainID, 10) + "|" + feature + "|" + strings.ToUpper(token)
}

func (s *stubChainReader) setRate(chainID int64, feature, token string, r services.FeeManagerRate) {
	s.rates[feeKey(chainID, feature, token)] = r
}

func (s *stubChainReader) RateFor(_ context.Context, chainID int64, _, feature, token string) (services.FeeManagerRate, error) {
	if r, ok := s.rates[feeKey(chainID, feature, token)]; ok {
		return r, nil
	}
	return services.FeeManagerRate{}, nil
}

func (s *stubChainReader) TxSucceeded(_ context.Context, _ int64, txHash string) (bool, error) {
	return s.txOK[txHash], nil
}

// setupFeeAdminTestDB builds the intent + fee_config tables (SQLite in-memory).
func setupFeeAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE fee_rate_intents (
		id TEXT PRIMARY KEY, feature TEXT NOT NULL, token TEXT NOT NULL, chain_id INTEGER,
		bps INTEGER NOT NULL DEFAULT 0, flat_usd REAL NOT NULL DEFAULT 0,
		mcgp_exempt INTEGER NOT NULL DEFAULT 0, active INTEGER NOT NULL DEFAULT 1,
		state TEXT NOT NULL DEFAULT 'draft', created_at DATETIME, updated_at DATETIME,
		UNIQUE(feature, token, chain_id))`).Error; err != nil {
		t.Fatalf("create fee_rate_intents: %v", err)
	}
	if err := db.Exec(`CREATE TABLE fee_configs (
		id TEXT PRIMARY KEY, feature TEXT NOT NULL, token_symbol TEXT NOT NULL, chain_id INTEGER,
		direction TEXT,
		bps INTEGER NOT NULL DEFAULT 0, flat_amount_usd REAL NOT NULL DEFAULT 0,
		mcgp_exempt INTEGER NOT NULL DEFAULT 0, active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME, updated_at DATETIME, UNIQUE(feature, token_symbol, chain_id, direction))`).Error; err != nil {
		t.Fatalf("create fee_configs: %v", err)
	}
	return db
}

func feeAdminCfg(chains ...int64) *config.Config {
	c := &config.Config{FeeManagerChains: map[int64]config.FeeManagerChainConfig{}}
	for _, id := range chains {
		c.FeeManagerChains[id] = config.FeeManagerChainConfig{ChainID: id, ChainKey: "sonic", Address: "0xFeeManager"}
	}
	return c
}

func drive(method, path string, body any, fn func(*gin.Context)) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	c.Request, _ = http.NewRequest(method, path, rdr)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user", &models.User{ID: uuid.New(), Role: models.RoleSuperAdmin})
	fn(c)
	return w
}

// createIntentForTest authors an intent and returns its ID.
func createIntentForTest(t *testing.T, h *FeeAdminHandler, def map[string]any) string {
	t.Helper()
	w := drive("POST", "/admin/fees/intent", map[string]any{
		"feature": models.FeatureWithdrawal, "token": "*", "default": def,
	}, h.CreateIntent)
	if w.Code != http.StatusCreated {
		t.Fatalf("create intent = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			IntentID string `json:"intentId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode intent resp: %v", err)
	}
	return resp.Data.IntentID
}

// TestConfirmWritesReplicaAfterVerifiedReadBack: a confirm whose receipt
// succeeds AND whose on-chain rateFor matches the intent writes the replica + the
// intent advances to applied (single chain).
func TestConfirmWritesReplicaAfterVerifiedReadBack(t *testing.T) {
	db := setupFeeAdminTestDB(t)
	reader := newStubChainReader()
	cfg := feeAdminCfg(feeSonicC)
	feeCfg := services.NewFeeConfigService(db)
	intents := services.NewFeeRateIntentService(db, cfg, reader)
	h := NewFeeAdminHandler(cfg, intents, feeCfg, reader)

	intentID := createIntentForTest(t, h, map[string]any{"bps": 60, "flatUsd": 0, "mcgpExempt": false, "active": true})

	// On-chain matches the intent (60 bps) and the tx succeeded.
	reader.setRate(feeSonicC, models.FeatureWithdrawal, "*", services.FeeManagerRate{BPS: 60, Active: true})
	reader.txOK["0xgood"] = true

	w := drive("POST", "/admin/fees/confirm", map[string]any{
		"intentId": intentID, "chainId": feeSonicC, "txHash": "0xgood",
	}, h.Confirm)
	if w.Code != http.StatusOK {
		t.Fatalf("confirm = %d, want 200: %s", w.Code, w.Body.String())
	}

	// Replica now resolves the Sonic withdrawal at 60.
	if bps, _, _, _ := feeCfg.RateFor(models.FeatureWithdrawal, "BTC", &[]int64{feeSonicC}[0]); bps != 60 {
		t.Fatalf("replica Sonic withdrawal = %d after confirm, want 60", bps)
	}
	// Intent advanced to applied.
	id, _ := uuid.Parse(intentID)
	g, _ := intents.Get(id)
	if g.State != models.FeeRateIntentApplied {
		t.Fatalf("intent state = %s after single-chain confirm, want applied", g.State)
	}
}

// TestConfirmRejects422OnFailedReceipt: a failed receipt → 422; replica untouched,
// intent stays pending.
func TestConfirmRejects422OnFailedReceipt(t *testing.T) {
	db := setupFeeAdminTestDB(t)
	reader := newStubChainReader()
	cfg := feeAdminCfg(feeSonicC)
	feeCfg := services.NewFeeConfigService(db)
	intents := services.NewFeeRateIntentService(db, cfg, reader)
	h := NewFeeAdminHandler(cfg, intents, feeCfg, reader)

	intentID := createIntentForTest(t, h, map[string]any{"bps": 60, "active": true})
	reader.setRate(feeSonicC, models.FeatureWithdrawal, "*", services.FeeManagerRate{BPS: 60, Active: true})
	// tx did NOT succeed (txOK defaults false).

	w := drive("POST", "/admin/fees/confirm", map[string]any{
		"intentId": intentID, "chainId": feeSonicC, "txHash": "0xbad",
	}, h.Confirm)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("confirm failed-receipt = %d, want 422: %s", w.Code, w.Body.String())
	}
	assertReplicaUntouchedAndPending(t, db, intents, intentID)
}

// TestConfirmRejects422OnRateMismatch: receipt OK but on-chain rateFor != intent
// → 422; replica untouched, intent stays pending.
func TestConfirmRejects422OnRateMismatch(t *testing.T) {
	db := setupFeeAdminTestDB(t)
	reader := newStubChainReader()
	cfg := feeAdminCfg(feeSonicC)
	feeCfg := services.NewFeeConfigService(db)
	intents := services.NewFeeRateIntentService(db, cfg, reader)
	h := NewFeeAdminHandler(cfg, intents, feeCfg, reader)

	intentID := createIntentForTest(t, h, map[string]any{"bps": 60, "active": true})
	// On-chain says 50 — does NOT match the intent's 60.
	reader.setRate(feeSonicC, models.FeatureWithdrawal, "*", services.FeeManagerRate{BPS: 50, Active: true})
	reader.txOK["0xgood"] = true

	w := drive("POST", "/admin/fees/confirm", map[string]any{
		"intentId": intentID, "chainId": feeSonicC, "txHash": "0xgood",
	}, h.Confirm)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("confirm rate-mismatch = %d, want 422: %s", w.Code, w.Body.String())
	}
	assertReplicaUntouchedAndPending(t, db, intents, intentID)
}

func assertReplicaUntouchedAndPending(t *testing.T, db *gorm.DB, intents *services.FeeRateIntentService, intentID string) {
	t.Helper()
	// No Sonic-specific replica row was written: a chain row at the new rate must
	// not exist (resolution falls back to the global seed 50, NOT a written 60).
	var count int64
	db.Model(&models.FeeConfig{}).
		Where("feature = ? AND token_symbol = ? AND chain_id = ?", models.FeatureWithdrawal, "*", feeSonicC).
		Count(&count)
	if count != 0 {
		t.Fatalf("a chain-specific replica row was written on a rejected confirm (count=%d) — replica must be untouched", count)
	}
	id, _ := uuid.Parse(intentID)
	g, _ := intents.Get(id)
	if g.State != models.FeeRateIntentPending {
		t.Fatalf("intent state = %s after a rejected confirm, want pending", g.State)
	}
}
