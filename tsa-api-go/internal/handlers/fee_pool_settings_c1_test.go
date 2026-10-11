package handlers

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func TestUpdateFeeConfig_PoolBpsSettingOutOfRangeIs400(t *testing.T) {
	db := setupFeeConfigHandlerDB(t)
	row := seedFeeConfigRowFor(t, db, models.FeaturePoolSpread) // bps 50
	w := doUpdate(t, db, &config.Config{}, row.ID, map[string]any{"bps": 301})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "between 0 and 300") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	w = doUpdate(t, db, &config.Config{}, row.ID, map[string]any{"bps": 75})
	if w.Code != http.StatusOK {
		t.Fatalf("valid edit: status=%d body=%s", w.Code, w.Body.String())
	}
}
