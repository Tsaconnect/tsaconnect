package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	panicCookie = "tsa_session=cookie-secret-4f1c9a"
	panicBearer = "Bearer bearer-secret-77d2e0"
	panicQuery  = "query-secret-a91b"
)

// panicRequest is a signed-in request carrying a session cookie, a bearer token
// and a token in its query string — everything a panic log must not contain.
func panicRequest() *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/api/pay/merchant/actions/9b2e/confirm?token="+panicQuery, strings.NewReader(`{"code":"123456"}`))
	req.Header.Set("Cookie", panicCookie)
	req.Header.Set("Authorization", panicBearer)
	return req
}

// assertSafePanicLog fails when a panic's log line lost what locates the fault
// or kept anything that authenticates the caller.
func assertSafePanicLog(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("log %q: want %q", out, want)
		}
	}
	for _, leaked := range []string{"cookie-secret-4f1c9a", "bearer-secret-77d2e0", panicQuery, "Cookie", "Authorization"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaked %q:\n%s", leaked, out)
		}
	}
}

func TestRecover_AnswersTheErrorEnvelopeAndLogsNoCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logs := captureLog(t)
	user := models.User{ID: uuid.New()}
	r := gin.New()
	r.Use(Recover())
	r.POST("/api/pay/merchant/actions/:id/confirm", func(c *gin.Context) {
		c.Set("user", user)
		panic("nil merchant")
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, panicRequest())

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["success"] != false || body["message"] == "" {
		t.Fatalf("body %s: want the standard error envelope", w.Body.String())
	}
	assertSafePanicLog(t, logs.String(), "[panic] POST /api/pay/merchant/actions/:id/confirm", "user="+user.ID.String(),
		"nil merchant", "recover_test.go")
}

func TestRecover_AResponseAlreadyStartedIsLeftAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	captureLog(t)
	r := gin.New()
	r.Use(Recover())
	r.GET("/x", func(c *gin.Context) {
		c.String(http.StatusOK, "partial")
		panic("late")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusOK || w.Body.String() != "partial" {
		t.Fatalf("status %d body %q: a started response must not get a second body", w.Code, w.Body.String())
	}
}
