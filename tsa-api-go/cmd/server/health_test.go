package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestHealthHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		pingErr    error
		wantStatus int
		wantDB     string
	}{
		{"healthy when the database answers", nil, http.StatusOK, "connected"},
		{"503 when the database is down", errors.New("connection refused"),
			http.StatusServiceUnavailable, "disconnected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/health", healthHandler(func() error { return tt.pingErr }, "abc1234"))

			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}

			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if body["database"] != tt.wantDB {
				t.Errorf("database = %v, want %q", body["database"], tt.wantDB)
			}
			// The deploy asserts the running release by this field; without it the
			// health check passes for any process listening on the port.
			if body["commit"] != "abc1234" {
				t.Errorf("commit = %v, want %q", body["commit"], "abc1234")
			}
		})
	}
}
