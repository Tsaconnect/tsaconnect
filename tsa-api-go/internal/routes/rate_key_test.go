package routes

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// TestPerUserRateKey: authenticated requests are limited per user (either
// shape the auth middleware stores); anonymous ones fall back to the client IP.
func TestPerUserRateKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	id := uuid.New()
	for name, user := range map[string]any{"value": models.User{ID: id}, "pointer": &models.User{ID: id}} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Set("user", user)
		if got := perUserRateKey(c); got != id.String() {
			t.Errorf("%s: key = %q, want the user id", name, got)
		}
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if got := perUserRateKey(c); got != "" {
		t.Errorf("anonymous key = %q, want \"\" (IP fallback)", got)
	}
}
