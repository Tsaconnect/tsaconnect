package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// Production runs gin in debug mode (ENV is not "production"), which is when
// gin's own Recovery dumps every request header. Whatever the router logs — to
// the standard logger or to gin's writers — must hold no session cookie, bearer
// token or query-string token, and a recovered panic still answers 500.
func TestRouter_APanicLogsNoCredentials(t *testing.T) {
	var logs bytes.Buffer
	prevLog, prevOut, prevErr, prevMode := log.Writer(), gin.DefaultWriter, gin.DefaultErrorWriter, gin.Mode()
	log.SetOutput(&logs)
	gin.DefaultWriter, gin.DefaultErrorWriter = &logs, &logs
	gin.SetMode(gin.DebugMode)
	t.Cleanup(func() {
		log.SetOutput(prevLog)
		gin.DefaultWriter, gin.DefaultErrorWriter = prevOut, prevErr
		gin.SetMode(prevMode)
	})

	router := newRouter()
	router.GET("/ws/notifications", func(c *gin.Context) { panic("hub gone") })
	req := httptest.NewRequest(http.MethodGet, "/ws/notifications?token=query-secret-a91b", nil)
	req.Header.Set("Cookie", "tsa_session=cookie-secret-4f1c9a")
	req.Header.Set("Authorization", "Bearer bearer-secret-77d2e0")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), `"success":false`) {
		t.Errorf("status %d body %s: want 500 with the error envelope", w.Code, w.Body.String())
	}
	out := logs.String()
	for _, want := range []string{"[panic] GET /ws/notifications", "hub gone", "GET /ws/notifications 500"} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q: want %q", out, want)
		}
	}
	for _, leaked := range []string{"cookie-secret-4f1c9a", "bearer-secret-77d2e0", "query-secret-a91b"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaked %q:\n%s", leaked, out)
		}
	}
}
