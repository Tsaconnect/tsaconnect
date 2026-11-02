package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// captureLog sends the standard logger to a buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// A query string can carry a secret or personal data: only the path is logged.
func TestRequestLogger_LogsThePathNeverTheQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := captureLog(t)
	r := gin.New()
	r.Use(RequestLogger())
	r.GET("/ws/notifications", func(c *gin.Context) { c.Status(http.StatusSwitchingProtocols) })
	r.GET("/api/pay/merchant/banks/resolve", func(c *gin.Context) { c.Status(http.StatusOK) })

	for _, target := range []string{
		"/ws/notifications?token=eyJhbGciOiJIUzI1NiJ9.c2VjcmV0.sig",
		"/api/pay/merchant/banks/resolve?bankCode=044&accountNumber=9876543210",
	} {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, target, nil))
	}

	out := logs.String()
	for _, want := range []string{"GET /ws/notifications 101", "GET /api/pay/merchant/banks/resolve 200"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q: want a line with %q", out, want)
		}
	}
	for _, leaked := range []string{"token=", "eyJhbGciOiJIUzI1NiJ9", "9876543210", "?"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log %q: the query string leaked (%q)", out, leaked)
		}
	}
}

// A route that authenticates by a secret in its path must not log that secret.
func TestRequestLogger_MasksASecretPathSegment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := captureLog(t)
	r := gin.New()
	r.Use(RequestLogger())
	r.Any("/api/bills/clubkonnect/callback/:secret", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/api/bills/orders/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/bills/clubkonnect/callback/TOPSECRETVALUE", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/bills/orders/abc-123", nil))

	out := logs.String()
	if strings.Contains(out, "TOPSECRETVALUE") {
		t.Errorf("log %q: the path secret leaked", out)
	}
	for _, want := range []string{"/api/bills/clubkonnect/callback/:secret 200", "/api/bills/orders/abc-123 200"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q: want a line with %q", out, want)
		}
	}
}
