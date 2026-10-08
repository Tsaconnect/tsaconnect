package handlers

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestMapServiceError_LiquidityLimitIs409WithHeadroom(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	headroom, _ := new(big.Int).SetString("100500000000000000000", 10) // 100.5 USDT, 18 dp
	if !mapServiceError(c, &services.LiquidityLimitError{Code: services.LiquidityAboveMaxCode, Headroom: headroom, Decimals: 18}) {
		t.Fatal("not handled")
	}
	var body struct {
		Code string `json:"code"`
		Data struct {
			Headroom      string `json:"headroom"`
			HeadroomUnits string `json:"headroomUnits"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusConflict || body.Code != "above_liquidity_max" || body.Data.Headroom != "100.5" || body.Data.HeadroomUnits != headroom.String() {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	mapServiceError(c, services.ErrPoolClosed)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("pool closed = %d", w.Code)
	}
}
