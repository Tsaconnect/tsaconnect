package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// refreshTokensTestDDL gives refresh_tokens a generated, hyphenated uuid id, as
// Postgres's gen_random_uuid() default does in production: rotation revokes the
// old token by id.
const refreshTokensTestDDL = `CREATE TABLE refresh_tokens (
	id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-' || hex(randomblob(2)) || '-' || hex(randomblob(2)) || '-' || hex(randomblob(6)))),
	user_id TEXT, token_hash TEXT UNIQUE, expires_at DATETIME, revoked INTEGER DEFAULT 0, device_info TEXT, web INTEGER NOT NULL DEFAULT 0, created_at DATETIME
)`

// newRefreshRig is the app's login + refresh over SQLite.
func newRefreshRig(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := setupAuthTestDB(t)
	if err := db.Exec(`DROP TABLE refresh_tokens`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(refreshTokensTestDDL).Error; err != nil {
		t.Fatal(err)
	}
	insertAuthUser(t, db, &models.User{ID: uuid.New(), Name: "Ada", Username: "ada", Email: "ada@example.com", AccountStatus: models.AccountStatusActive})

	h := &Handlers{Config: &config.Config{JWTSecret: "test-secret-that-is-at-least-32-bytes-long"}}
	r := gin.New()
	r.POST("/api/auth/login", h.Login)
	r.POST("/api/auth/refresh", h.RefreshToken)
	return r, db
}

func postRefreshJSON(r *gin.Engine, path, body string) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func refreshTokenOf(t *testing.T, body map[string]any) string {
	t.Helper()
	data, _ := body["data"].(map[string]any)
	tok, _ := data["refreshToken"].(string)
	if tok == "" {
		t.Fatalf("no refresh token in %v", body)
	}
	return tok
}

func TestRefreshToken_ATokenRotatedTwiceFailsTheSecondTime(t *testing.T) {
	r, _ := newRefreshRig(t)
	code, body := postRefreshJSON(r, "/api/auth/login", `{"identifier":"ada@example.com","password":"StrongPass1!"}`)
	if code != http.StatusOK {
		t.Fatalf("login: %d %v", code, body)
	}
	first := refreshTokenOf(t, body)

	code, body = postRefreshJSON(r, "/api/auth/refresh", `{"refreshToken":"`+first+`"}`)
	if code != http.StatusOK {
		t.Fatalf("first rotation: %d %v", code, body)
	}
	second := refreshTokenOf(t, body)
	if second == first {
		t.Fatal("rotation returned the same refresh token")
	}

	code, body = postRefreshJSON(r, "/api/auth/refresh", `{"refreshToken":"`+first+`"}`)
	if code != http.StatusUnauthorized || body["message"] != "Invalid or expired refresh token" {
		t.Fatalf("second rotation of the same token: %d %v", code, body)
	}
	if code, _ := postRefreshJSON(r, "/api/auth/refresh", `{"refreshToken":"`+second+`"}`); code != http.StatusOK {
		t.Fatalf("the rotated-in token must still work: %d", code)
	}
}

// Two refreshes race with the same token: both read it as live, only one may
// rotate it. The hook revokes the token right after the loser's lookup, the way
// the winner's commit would land.
func TestRefreshToken_ALosingConcurrentRotationIsRefused(t *testing.T) {
	r, db := newRefreshRig(t)
	_, body := postRefreshJSON(r, "/api/auth/login", `{"identifier":"ada@example.com","password":"StrongPass1!"}`)
	raw := refreshTokenOf(t, body)

	fired := false
	if err := db.Callback().Query().After("gorm:query").Register("test:winner_rotates", func(tx *gorm.DB) {
		if fired || tx.Statement.Table != "refresh_tokens" {
			return
		}
		fired = true
		tx.Session(&gorm.Session{NewDB: true}).Exec(`UPDATE refresh_tokens SET revoked = 1 WHERE token_hash = ?`, hashToken(raw))
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("test:winner_rotates") })

	code, body := postRefreshJSON(r, "/api/auth/refresh", `{"refreshToken":"`+raw+`"}`)
	if !fired {
		t.Fatal("the race hook never ran")
	}
	if code != http.StatusUnauthorized {
		t.Fatalf("the losing rotation: %d %v", code, body)
	}
	var live int64
	db.Model(&models.RefreshToken{}).Where("revoked = ?", false).Count(&live)
	if live != 0 {
		t.Fatalf("the losing rotation minted a refresh token: %d live", live)
	}
}
