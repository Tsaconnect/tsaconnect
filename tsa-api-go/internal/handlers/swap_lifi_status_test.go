package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ojimcy/tsa-api-go/internal/services"
)

// getSwapStatusVia runs GET /swap/lifi/status against a LiFi stub answering body.
func getSwapStatusVia(t *testing.T, lifiBody string) map[string]any {
	t.Helper()
	lifi := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(lifiBody))
	}))
	t.Cleanup(lifi.Close)

	h := &SwapLiFiHandler{StatusClient: services.NewLiFiStatusClient(lifi.URL, "")}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/swap/lifi/status", h.GetSwapStatus)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/swap/lifi/status?txHash=0xsrc", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return env.Data
}

// DONE alone does not say she got what was quoted: the app needs LiFi's
// qualifier to tell a refund or a substituted token from a delivery.
func TestGetSwapStatus_CarriesSubstatus(t *testing.T) {
	for _, sub := range []string{"COMPLETED", "REFUNDED", "PARTIAL"} {
		t.Run(sub, func(t *testing.T) {
			data := getSwapStatusVia(t, `{"status":"DONE","substatus":"`+sub+`"}`)
			if data["status"] != "DONE" {
				t.Errorf("status = %v, want DONE", data["status"])
			}
			if data["substatus"] != sub {
				t.Errorf("substatus = %v, want %s", data["substatus"], sub)
			}
		})
	}
}

func TestGetSwapStatus_OmitsAnAbsentSubstatus(t *testing.T) {
	data := getSwapStatusVia(t, `{"status":"PENDING"}`)
	if _, ok := data["substatus"]; ok {
		t.Errorf("substatus present on a PENDING with none: %v", data["substatus"])
	}
}
