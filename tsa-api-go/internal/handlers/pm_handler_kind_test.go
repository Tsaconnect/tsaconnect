package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestListPMs_KindFilter(t *testing.T) {
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
	if code, rows := list(""); code != 200 || len(rows) != 2 {
		t.Fatalf("all = %d %v", code, rows)
	}
	code, rows := list("?kind=lp")
	if code != 200 || len(rows) != 1 || rows[0]["kind"] != "lp" {
		t.Fatalf("lp = %d %v", code, rows)
	}
	if _, rows := list("?kind=merchant"); len(rows) != 1 || rows[0]["kind"] == "lp" {
		t.Fatalf("merchants = %v", rows)
	}
	if code, _ := list("?kind=other"); code != http.StatusBadRequest {
		t.Fatalf("bad kind = %d", code)
	}
}
