package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupIdempotencyDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// SQLite-compatible DDL — gorm's AutoMigrate would emit pg-isms.
	if err := db.Exec(`CREATE TABLE request_idempotencies (
		key TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		request_hash TEXT NOT NULL,
		response_status INTEGER NOT NULL,
		response_body TEXT,
		response_headers TEXT,
		created_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	return db
}

// makeRouter builds a Gin router with the idempotency middleware applied to a
// single /do endpoint. authUser is injected into context by a tiny stand-in
// for the real auth middleware.
func makeRouter(db *gorm.DB, authUser models.User, callCount *atomic.Int32) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/do",
		func(c *gin.Context) {
			c.Set("user", authUser)
			c.Next()
		},
		Idempotency(db),
		func(c *gin.Context) {
			callCount.Add(1)
			c.JSON(http.StatusOK, gin.H{"result": "ok", "n": callCount.Load()})
		},
	)
	return r
}

func TestIdempotency_NoHeader_PassesThrough(t *testing.T) {
	db := setupIdempotencyDB(t)
	var n atomic.Int32
	user := models.User{ID: uuid.New()}
	r := makeRouter(db, user, &n)

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":1}`))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	}
	if got := n.Load(); got != 3 {
		t.Fatalf("expected handler called 3 times; got %d", got)
	}

	// No row should be persisted.
	var count int64
	db.Model(&models.RequestIdempotency{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected zero idempotency rows; got %d", count)
	}
}

func TestIdempotency_SameKey_ReplaysCachedResponse(t *testing.T) {
	db := setupIdempotencyDB(t)
	var n atomic.Int32
	user := models.User{ID: uuid.New()}
	r := makeRouter(db, user, &n)

	makeReq := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "k-1")
		r.ServeHTTP(w, req)
		return w
	}

	w1 := makeReq()
	if w1.Code != http.StatusOK {
		t.Fatalf("first call: expected 200, got %d", w1.Code)
	}
	body1 := w1.Body.String()

	w2 := makeReq()
	if w2.Code != http.StatusOK {
		t.Fatalf("second call: expected 200, got %d", w2.Code)
	}
	if w2.Body.String() != body1 {
		t.Fatalf("replay body mismatch:\nfirst=%s\nsecond=%s", body1, w2.Body.String())
	}
	if w2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("expected Idempotent-Replayed: true on replay")
	}
	if got := n.Load(); got != 1 {
		t.Fatalf("handler ran %d times; expected 1 (cached replay)", got)
	}
}

func TestIdempotency_SameKeyDifferentBody_Returns409(t *testing.T) {
	db := setupIdempotencyDB(t)
	var n atomic.Int32
	user := models.User{ID: uuid.New()}
	r := makeRouter(db, user, &n)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":1}`))
	req.Header.Set("Idempotency-Key", "k-2")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("initial: expected 200, got %d", w.Code)
	}

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":2}`))
	req2.Header.Set("Idempotency-Key", "k-2")
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("replay-with-different-body: expected 409, got %d", w2.Code)
	}
}

func TestIdempotency_KeyReusedByDifferentUser_Returns409(t *testing.T) {
	db := setupIdempotencyDB(t)
	var n atomic.Int32

	userA := models.User{ID: uuid.New()}
	rA := makeRouter(db, userA, &n)
	wA := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":1}`))
	reqA.Header.Set("Idempotency-Key", "shared")
	rA.ServeHTTP(wA, reqA)
	if wA.Code != http.StatusOK {
		t.Fatalf("userA initial: expected 200, got %d", wA.Code)
	}

	userB := models.User{ID: uuid.New()}
	rB := makeRouter(db, userB, &n)
	wB := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":1}`))
	reqB.Header.Set("Idempotency-Key", "shared")
	rB.ServeHTTP(wB, reqB)
	if wB.Code != http.StatusConflict {
		t.Fatalf("userB collision: expected 409, got %d", wB.Code)
	}
}

func TestIdempotency_NonSuccessNotCached(t *testing.T) {
	db := setupIdempotencyDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	user := models.User{ID: uuid.New()}
	var n atomic.Int32

	r.POST("/fail",
		func(c *gin.Context) {
			c.Set("user", user)
			c.Next()
		},
		Idempotency(db),
		func(c *gin.Context) {
			n.Add(1)
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "nope"})
		},
	)

	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/fail", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "fail-1")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422, got %d", w.Code)
		}
	}
	// Handler must run both times — 4xx must NOT be cached.
	if got := n.Load(); got != 2 {
		t.Fatalf("handler ran %d times; expected 2 (4xx not cached)", got)
	}
}

func TestIdempotency_TooLongKey_Returns400(t *testing.T) {
	db := setupIdempotencyDB(t)
	var n atomic.Int32
	user := models.User{ID: uuid.New()}
	r := makeRouter(db, user, &n)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", strings.Repeat("x", maxIdempotencyKeyLen+1))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for oversized key, got %d", w.Code)
	}
}

func TestIdempotency_NoAuth_Returns401(t *testing.T) {
	db := setupIdempotencyDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/do", Idempotency(db), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{}`))
	req.Header.Set("Idempotency-Key", "k")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when auth missing; got %d", w.Code)
	}
}

func TestIdempotency_409DoesNotInsertRow(t *testing.T) {
	// Regression guard: payload-mismatch 409 must not create a second row.
	db := setupIdempotencyDB(t)
	var n atomic.Int32
	user := models.User{ID: uuid.New()}
	r := makeRouter(db, user, &n)

	// First request — creates the row.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":1}`))
	req.Header.Set("Idempotency-Key", "k-409")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("setup: expected 200, got %d", w.Code)
	}

	// Second request — same key, different body → 409 expected.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":2}`))
	req2.Header.Set("Idempotency-Key", "k-409")
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d", w2.Code)
	}

	var count int64
	db.Model(&models.RequestIdempotency{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly 1 row after 409; got %d", count)
	}
}

func TestIdempotency_NonJSONResponse_NotCached(t *testing.T) {
	// Handlers that emit non-JSON (HTML, CSV, raw text) MUST NOT corrupt the
	// JSONB-typed cache. The middleware should skip caching and log.
	db := setupIdempotencyDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	user := models.User{ID: uuid.New()}
	var n atomic.Int32

	r.POST("/html",
		func(c *gin.Context) {
			c.Set("user", user)
			c.Next()
		},
		Idempotency(db),
		func(c *gin.Context) {
			n.Add(1)
			c.Data(http.StatusOK, "text/html", []byte("<html>hi</html>"))
		},
	)

	// Two calls with the same key — both should run the handler (no cache).
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/html", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "html-key")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	}
	if got := n.Load(); got != 2 {
		t.Fatalf("handler ran %d times; expected 2 (non-JSON must not cache)", got)
	}

	var count int64
	db.Model(&models.RequestIdempotency{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected zero rows for non-JSON responses; got %d", count)
	}
}

func TestIdempotency_HeadersPreservedOnReplay(t *testing.T) {
	// Replay must restore captured headers — 201-with-Location, Set-Cookie,
	// custom headers, etc. — not just status + body.
	db := setupIdempotencyDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	user := models.User{ID: uuid.New()}
	var n atomic.Int32

	r.POST("/created",
		func(c *gin.Context) {
			c.Set("user", user)
			c.Next()
		},
		Idempotency(db),
		func(c *gin.Context) {
			n.Add(1)
			c.Header("Location", "/things/42")
			c.Header("X-Custom", "preserved")
			c.JSON(http.StatusCreated, gin.H{"id": 42})
		},
	)

	makeReq := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/created", strings.NewReader(`{}`))
		req.Header.Set("Idempotency-Key", "headers-key")
		r.ServeHTTP(w, req)
		return w
	}

	w1 := makeReq()
	if w1.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d", w1.Code)
	}

	w2 := makeReq()
	if w2.Code != http.StatusCreated {
		t.Fatalf("replay: expected 201, got %d", w2.Code)
	}
	if loc := w2.Header().Get("Location"); loc != "/things/42" {
		t.Fatalf("replay lost Location header; got %q", loc)
	}
	if got := w2.Header().Get("X-Custom"); got != "preserved" {
		t.Fatalf("replay lost X-Custom header; got %q", got)
	}
	if w2.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("expected Idempotent-Replayed: true")
	}
}

func TestIdempotency_PreservesResponseBodyExactly(t *testing.T) {
	db := setupIdempotencyDB(t)
	user := models.User{ID: uuid.New()}
	var n atomic.Int32
	r := makeRouter(db, user, &n)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(`{"a":1}`))
	req.Header.Set("Idempotency-Key", "verify-body")
	r.ServeHTTP(w, req)

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["result"] != "ok" {
		t.Fatalf("body content lost; got %v", body)
	}

	// Manually verify a row is in the DB
	var row models.RequestIdempotency
	if err := db.Where("key = ?", "verify-body").First(&row).Error; err != nil {
		t.Fatalf("expected row; got %v", err)
	}
	// Body stored as JSON bytes
	if !bytes.Contains(row.ResponseBody, []byte(`"result":"ok"`)) {
		t.Fatalf("stored body missing result: %s", string(row.ResponseBody))
	}
}

// ?mode=live and ?mode=test with the same body are different requests: a key
// used for one is refused for the other, never replayed as it.
func TestIdempotency_SameKeyDifferentQuery_Returns409(t *testing.T) {
	db := setupIdempotencyDB(t)
	var n atomic.Int32
	r := makeRouter(db, models.User{ID: uuid.New()}, &n)
	send := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"a":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "k-query")
		r.ServeHTTP(w, req)
		return w
	}
	if w := send("/do?mode=test"); w.Code != http.StatusOK {
		t.Fatalf("first: %d", w.Code)
	}
	if w := send("/do?mode=test"); w.Code != http.StatusOK || w.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("the same query again must replay: %d", w.Code)
	}
	if w := send("/do?mode=live"); w.Code != http.StatusConflict {
		t.Fatalf("another query under the same key: %d", w.Code)
	}
	if n.Load() != 1 {
		t.Fatalf("handler ran %d times", n.Load())
	}
}

// A body carrying a password must not be stored as its plain sha256 — that is
// an offline guessing oracle for the password. The route leaves the field out
// of the fingerprint and still replays a retry, and still refuses the key for
// a request that differs in anything else.
func TestIdempotency_IgnoredFieldIsNotFingerprinted(t *testing.T) {
	db := setupIdempotencyDB(t)
	var n atomic.Int32
	user := models.User{ID: uuid.New()}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/do", func(c *gin.Context) { c.Set("user", user); c.Next() },
		Idempotency(db, IdempotencyIgnoringFields("password")),
		func(c *gin.Context) {
			n.Add(1)
			c.JSON(http.StatusCreated, gin.H{"n": n.Load()})
		})
	send := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/do", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", "k-pw")
		r.ServeHTTP(w, req)
		return w
	}
	raw := `{"payMethod":"stablecoin","password":"hunter2-Secret!"}`
	if w := send(raw); w.Code != http.StatusCreated {
		t.Fatalf("first: %d", w.Code)
	}
	var row models.RequestIdempotency
	if err := db.Where("key = ?", "k-pw").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.RequestHash == fingerprint(http.MethodPost, "/do", []byte(raw)) {
		t.Fatal("the stored fingerprint is the sha256 of the raw body, password included")
	}
	if row.RequestHash != fingerprint(http.MethodPost, "/do", []byte(`{"payMethod":"stablecoin"}`)) {
		t.Fatalf("fingerprint %s is not of the body without the password", row.RequestHash)
	}
	if w := send(raw); w.Code != http.StatusCreated || w.Header().Get("Idempotent-Replayed") != "true" || n.Load() != 1 {
		t.Fatalf("retry: %d replayed=%q calls=%d", w.Code, w.Header().Get("Idempotent-Replayed"), n.Load())
	}
	if w := send(`{"payMethod":"mcgp_spendable","password":"hunter2-Secret!"}`); w.Code != http.StatusConflict {
		t.Fatalf("a different request under the same key: %d", w.Code)
	}
}

// encoding/json binds "Password" to a password field as readily as "password",
// so the field is left out of the fingerprint whatever its case.
func TestIdempotency_IgnoredFieldMatchesAnyCase(t *testing.T) {
	cfg := idempotencyConfig{ignoreFields: []string{"password"}}
	for _, body := range []string{
		`{"payMethod":"stablecoin","Password":"hunter2-Secret!"}`,
		`{"payMethod":"stablecoin","PASSWORD":"hunter2-Secret!"}`,
		`{"payMethod":"stablecoin","password":"hunter2-Secret!"}`,
	} {
		if got := string(cfg.fingerprintBody([]byte(body))); got != `{"payMethod":"stablecoin"}` {
			t.Fatalf("%s fingerprinted as %s, want the password left out", body, got)
		}
	}
}
