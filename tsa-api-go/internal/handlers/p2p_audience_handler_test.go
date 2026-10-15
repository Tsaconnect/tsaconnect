package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The audience refusals are 409s with the codes the app routes on.
func TestMapServiceError_AudienceCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for err, code := range map[error]string{
		services.ErrP2PDirectMoved: services.P2PDirectMovedCode,
		services.ErrPMNotOnDirect:  services.PMNotOnDirectCode,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		if !mapServiceError(c, fmt.Errorf("wrapped: %w", err)) {
			t.Fatalf("%v not mapped", err)
		}
		var body map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusConflict || body["code"] != code {
			t.Fatalf("%v → %d %v, want 409 %s", err, w.Code, body, code)
		}
	}
}
