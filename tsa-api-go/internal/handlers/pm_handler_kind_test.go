package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakeLPView struct{}

func (fakeLPView) AdminLPViews(_ context.Context, lps []models.PaymentMerchant) map[uuid.UUID]*services.LPAdminView {
	out := map[uuid.UUID]*services.LPAdminView{}
	for _, p := range lps {
		out[p.ID] = &services.LPAdminView{Stake: "40", OwedNgn: "0.00"}
	}
	return out
}

func TestListPMs_KindFilterAndLPView(t *testing.T) {
	db := newSQLiteForPMHandler(t)
	chain := int64(97)
	for _, pm := range []*models.PaymentMerchant{
		{UserID: uuid.New(), Name: "Alpha", Status: models.PaymentMerchantStatusActive},
		{UserID: uuid.New(), Name: "Ada LP", Kind: models.PaymentMerchantKindLP, PoolChainID: &chain, Status: models.PaymentMerchantStatusActive},
	} {
		if err := db.Create(pm).Error; err != nil {
			t.Fatalf("seed pm: %v", err)
		}
	}
	h := NewPMHandler(&fakePMAutomation{}, services.NewPMService(db))
	h.SetLiquidityView(fakeLPView{})

	list := func(q string) (int, []map[string]any) {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/admin/pm", h.ListPMs)
		w := httptest.NewRecorder()
		req, _ := http.NewRequest("GET", "/admin/pm"+q, nil)
		r.ServeHTTP(w, req)
		var resp struct {
			Data []map[string]any `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		return w.Code, resp.Data
	}
	if code, rows := list(""); code != 200 || len(rows) != 2 || rows[0]["lp"] != nil || rows[1]["lp"] != nil {
		t.Fatalf("all = %d %v (the unfiltered list never reads the chain, review I7)", code, rows)
	}
	code, rows := list("?kind=lp")
	if code != 200 || len(rows) != 1 || rows[0]["kind"] != "lp" || rows[0]["lp"] == nil {
		t.Fatalf("lp = %d %v", code, rows)
	}
	if _, rows := list("?kind=merchant"); len(rows) != 1 || rows[0]["lp"] != nil {
		t.Fatalf("merchants = %v", rows)
	}
	if code, _ := list("?kind=other"); code != http.StatusBadRequest {
		t.Fatalf("bad kind = %d", code)
	}
}
