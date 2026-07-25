package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// RequireSuperAdmin must admit ONLY super_admin — a plain admin (who can view
// the IT orders console) must be refused the money-movement actions.
func TestRequireSuperAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)

	run := func(setUser func(c *gin.Context)) int {
		r := gin.New()
		if setUser != nil {
			r.Use(func(c *gin.Context) { setUser(c); c.Next() })
		}
		r.POST("/act", RequireSuperAdmin(), func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/act", nil))
		return w.Code
	}

	cases := []struct {
		name   string
		set    func(c *gin.Context)
		expect int
	}{
		{"no user", nil, http.StatusUnauthorized},
		{"admin refused", func(c *gin.Context) {
			c.Set("user", models.User{Role: models.RoleAdmin})
		}, http.StatusForbidden},
		{"support refused", func(c *gin.Context) {
			c.Set("user", models.User{Role: models.RoleSupport})
		}, http.StatusForbidden},
		{"super_admin allowed", func(c *gin.Context) {
			c.Set("user", models.User{Role: models.RoleSuperAdmin})
		}, http.StatusOK},
		{"super_admin via pointer", func(c *gin.Context) {
			c.Set("user", &models.User{Role: models.RoleSuperAdmin})
		}, http.StatusOK},
	}
	for _, tc := range cases {
		if got := run(tc.set); got != tc.expect {
			t.Errorf("%s: status = %d, want %d", tc.name, got, tc.expect)
		}
	}
}
