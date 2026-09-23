package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

func TestPayMerchantAuth(t *testing.T) {
	auth := func(_ context.Context, raw string) (*models.PayMerchant, string, error) {
		switch raw {
		case "tsapk_live_good":
			return &models.PayMerchant{}, models.PayModeLive, nil
		case "tsapk_test_good":
			return &models.PayMerchant{}, models.PayModeTest, nil
		case "tsapk_live_suspended":
			return nil, "", services.ErrPayMerchantSuspended
		}
		return nil, "", services.ErrPayKeyInvalid
	}
	for _, tc := range []struct {
		header, code string
		live         bool
		want         int
	}{
		{"", "invalid_key", false, 401},
		{"Bearer tsapk_live_bad", "invalid_key", true, 401},
		{"tsapk_live_good", "invalid_key", true, 401}, // no Bearer
		{"Bearer tsapk_live_suspended", "merchant_suspended", true, 403},
		{"Bearer tsapk_live_good", "live_disabled", false, 403},
		{"Bearer tsapk_live_good", "", true, 200},
		{"Bearer tsapk_test_good", "", false, 200},
	} {
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/x", PayMerchantAuth(auth, tc.live), func(c *gin.Context) {
			if _, ok := c.Get("payMerchant"); !ok {
				t.Fatal("payMerchant not set")
			}
			c.Status(http.StatusOK)
		})
		req := httptest.NewRequest("GET", "/x", nil)
		if tc.header != "" {
			req.Header.Set("Authorization", tc.header)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want || (tc.code != "" && !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`)) {
			t.Fatalf("%q live=%v: %d %s", tc.header, tc.live, w.Code, w.Body.String())
		}
	}
}
