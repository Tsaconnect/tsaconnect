package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/config"
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

// A web app whose Netlify proxy cannot be verified is an ALERT at boot — never a
// refusal: production ran with the origin set and no secret.
func TestNetlifyProxyAlert(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg   config.Config
		alert bool
	}{
		"web app on, no secret":   {config.Config{WebAppOrigin: "https://app.test"}, true},
		"web app on, secret set":  {config.Config{WebAppOrigin: "https://app.test", NetlifyProxySecret: "s"}, false},
		"web app off, no secret":  {config.Config{}, false},
		"web app off, secret set": {config.Config{NetlifyProxySecret: "s"}, false},
	} {
		got := netlifyProxyAlert(&tc.cfg)
		if tc.alert != (got != "") || (tc.alert && !strings.HasPrefix(got, "[web] ALERT netlify_proxy_secret_unset=1 ")) {
			t.Errorf("%s: alert %q", name, got)
		}
	}
}
