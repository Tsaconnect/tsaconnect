package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// A browser client proving wallet ownership sends the proof in these headers;
// a preflight that does not allow them blocks every wallet change from the web.
func TestCORS_AllowsWalletProofHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CORS(&config.Config{FrontendURL: "https://app.example"}))
	r.POST("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodOptions, "/x", nil)
	req.Header.Set("Origin", "https://app.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	allowed := w.Header().Get("Access-Control-Allow-Headers")
	for _, h := range []string{"X-Wallet-Proof-Nonce", "X-Wallet-Proof-Signature"} {
		if !strings.Contains(allowed, h) {
			t.Errorf("Access-Control-Allow-Headers %q lacks %s", allowed, h)
		}
	}
}
