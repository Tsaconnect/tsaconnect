package handlers

import (
	"net/http"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestUpdateFeeConfig_PoolSettingOutOfRangeIs400(t *testing.T) {
	db := setupFeeConfigHandlerDB(t)
	row := seedFeeConfigRowFor(t, db, models.FeaturePoolUserDailyMax)
	db.Model(&row).Updates(map[string]any{"bps": 0, "flat_amount_usd": 5000})
	w := doUpdate(t, db, &config.Config{}, row.ID, map[string]any{"flatAmountUsd": 250000})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400\n%s", w.Code, w.Body.String())
	}
	w = doUpdate(t, db, &config.Config{}, row.ID, map[string]any{"flatAmountUsd": 2500})
	if w.Code != http.StatusOK {
		t.Fatalf("in-range edit = %d\n%s", w.Code, w.Body.String())
	}
}

func TestCreateIntent_RefusesPoolSettings(t *testing.T) {
	db := setupFeeAdminTestDB(t)
	cfg := feeAdminCfg(feeSonicC)
	reader := newStubChainReader()
	h := NewFeeAdminHandler(cfg, services.NewFeeRateIntentService(db, cfg, reader), services.NewFeeConfigService(db), reader)
	w := drive("POST", "/admin/fees/intent", map[string]any{
		"feature": models.FeaturePoolNetworkFee, "token": "*", "default": map[string]any{"bps": 0},
	}, h.CreateIntent)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("authoring a pool setting on-chain = %d, want 400\n%s", w.Code, w.Body.String())
	}
}
