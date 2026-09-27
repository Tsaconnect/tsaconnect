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

// An IPv6 client is limited by its /64 — every address it can pick from — and
// an IPv4 one by its address, as before.
func TestClientKey_IPv6ByItsSlash64(t *testing.T) {
	gin.SetMode(gin.TestMode)
	key := func(remote string) string {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Request.RemoteAddr = remote
		return ClientKey(c)
	}
	if a, b := key("[2001:db8:1:2:aaaa::1]:443"), key("[2001:db8:1:2:ffff:1:2:3]:443"); a != b || a != "2001:db8:1:2::/64" {
		t.Fatalf("one /64: %q vs %q", a, b)
	}
	if a, b := key("[2001:db8:1:2::1]:443"), key("[2001:db8:1:3::1]:443"); a == b {
		t.Fatalf("two /64s share a key: %q", a)
	}
	if got := key("203.0.113.9:443"); got != "203.0.113.9" {
		t.Fatalf("IPv4: %q", got)
	}

	r := gin.New()
	r.GET("/", RateLimiter(2, time.Minute), func(c *gin.Context) { c.Status(http.StatusOK) })
	hit := func(remote string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	hit("[2001:db8:9::1]:1")
	hit("[2001:db8:9::2]:1")
	if code := hit("[2001:db8:9::3]:1"); code != http.StatusTooManyRequests {
		t.Fatalf("a third address in the same /64: %d", code)
	}
}
