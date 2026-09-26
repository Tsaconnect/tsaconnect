package middleware

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Concurrent requests on one key are each counted: exactly maxRequests get
// through. Run with -race, this is also the check that the count is never
// read and written unsynchronised.
func TestRateLimiterBy_CountsConcurrentRequestsExactly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/", RateLimiterBy(50, time.Minute, func(*gin.Context) string { return "one" }), func(c *gin.Context) { c.Status(http.StatusOK) })

	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if w.Code == http.StatusOK {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 50 {
		t.Fatalf("%d requests got through, want 50", ok.Load())
	}
}
