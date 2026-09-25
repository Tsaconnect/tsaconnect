package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// fakeCoinGecko serves n OHLC rows and n market_chart points for any coin.
func fakeCoinGecko(t *testing.T, n int) *services.PriceService {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		if strings.HasSuffix(r.URL.Path, "/market_chart") {
			b.WriteString(`{"prices":[`)
			for i := 0; i < n; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, "[%d,%d]", i, 100+i)
			}
			b.WriteString("]}")
		} else {
			b.WriteString("[")
			for i := 0; i < n; i++ {
				if i > 0 {
					b.WriteString(",")
				}
				fmt.Fprintf(&b, "[%d,1,2,0.5,1.5]", i)
			}
			b.WriteString("]")
		}
		w.Write([]byte(b.String()))
	}))
	t.Cleanup(srv.Close)
	ps := services.NewPriceService()
	ps.SetBaseURLForTest(srv.URL)
	return ps
}

func serveMarket(t *testing.T, ps *services.PriceService, url string) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &Handlers{PriceService: ps}
	r := gin.New()
	r.GET("/market/history/:symbol", h.GetAssetPriceHistory)
	r.GET("/market/ohlc/:symbol", h.GetAssetOHLC)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return w.Code, body
}

func TestGetAssetOHLC_CapsCandleCount(t *testing.T) {
	code, body := serveMarket(t, fakeCoinGecko(t, 180), "/market/ohlc/eth?days=30&candles=40")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	data := body["data"].(map[string]any)
	candles := data["candles"].([]any)
	if len(candles) == 0 || len(candles) > 40 {
		t.Fatalf("got %d candles, want 1..40", len(candles))
	}
	last := candles[len(candles)-1].(map[string]any)
	if last["timestamp"].(float64) != 179 {
		t.Errorf("newest candle timestamp = %v, want 179 (the latest bar survives aggregation)", last["timestamp"])
	}
	if data["symbol"] != "ETH" || data["days"].(float64) != 30 {
		t.Errorf("symbol/days = %v/%v", data["symbol"], data["days"])
	}
}

func TestGetAssetOHLC_UnknownSymbolIs404(t *testing.T) {
	code, body := serveMarket(t, fakeCoinGecko(t, 5), "/market/ohlc/NOPE")
	if code != http.StatusNotFound || body["success"] != false {
		t.Fatalf("status = %d, body = %v; want 404 success=false", code, body)
	}
}

func TestGetAssetPriceHistory_PointsCapsSeriesAndKeepsEnds(t *testing.T) {
	code, body := serveMarket(t, fakeCoinGecko(t, 288), "/market/history/usdc?days=1&points=60")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	hist := body["data"].(map[string]any)["history"].([]any)
	if len(hist) != 60 {
		t.Fatalf("got %d points, want 60", len(hist))
	}
	first := hist[0].(map[string]any)["timestamp"].(float64)
	last := hist[len(hist)-1].(map[string]any)["timestamp"].(float64)
	if first != 0 || last != 287 {
		t.Errorf("ends = %v..%v, want 0..287", first, last)
	}
}

func TestGetAssetPriceHistory_WithoutPointsKeepsLegacySampling(t *testing.T) {
	// Installed app builds send only ?days= and render one bar per point.
	_, body := serveMarket(t, fakeCoinGecko(t, 288), "/market/history/usdc?days=1")
	hist := body["data"].(map[string]any)["history"].([]any)
	if len(hist) < 12 || len(hist) > 13 {
		t.Fatalf("got %d points, want the legacy 1d sampling (~12)", len(hist))
	}
}

func TestGetAssetPriceHistory_DaysFallBackAndSnap(t *testing.T) {
	cases := map[string]float64{"days=0": 30, "days=-5": 30, "days=abc": 30, "days=20": 30, "days=2": 7, "days=9999": 365}
	for q, want := range cases {
		_, body := serveMarket(t, fakeCoinGecko(t, 10), "/market/history/eth?"+q)
		if got := body["data"].(map[string]any)["days"].(float64); got != want {
			t.Errorf("%s → days %v, want %v", q, got, want)
		}
	}
}
