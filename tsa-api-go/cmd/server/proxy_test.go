package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestClientIPIgnoresUntrustedForwardedFor pins the property the rate limiter and the
// audit log depend on: a client cannot choose the IP the API attributes to it.
func TestClientIPIgnoresUntrustedForwardedFor(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	if err := router.SetTrustedProxies([]string{"127.0.0.1"}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}

	var seen string
	router.GET("/whoami", func(c *gin.Context) { seen = c.ClientIP(); c.Status(200) })

	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.RemoteAddr = "203.0.113.9:54321" // a real client, NOT the trusted proxy
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	router.ServeHTTP(httptest.NewRecorder(), req)

	if seen == "1.2.3.4" {
		t.Errorf("ClientIP() honoured X-Forwarded-For from an untrusted peer: %q.\n"+
			"Rate limiting and the auth audit log are forgeable.", seen)
	}
	if seen != "203.0.113.9" {
		t.Errorf("ClientIP() = %q, want the real peer address 203.0.113.9", seen)
	}
}
