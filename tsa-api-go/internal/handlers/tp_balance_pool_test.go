package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// A user who has not transacted since the monthly pool payout still sees their
// share when they open the app: reading the balance settles it, once.
func TestGetTPBalance_SettlesPoolShareForIdleUser(t *testing.T) {
	db := setupTPTestDB(t)
	addFeeV2Tables(t, db)
	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)

	user := createTPTestUser(t, db, "Idle", "idle_user", nil)
	if err := db.Exec(`INSERT INTO global_tp_pool (singleton, per_user_index_scaled, updated_at) VALUES (?, ?, datetime('now'))`,
		models.GlobalTPPoolSingletonID, 3*models.GlobalTPIndexScale).Error; err != nil {
		t.Fatalf("seed pool index: %v", err)
	}

	dirty := map[uuid.UUID]int{}
	svc := services.NewFeeDistributionService(db, nil, 100)
	svc.SetDirtyHook(func(_ context.Context, id uuid.UUID) { dirty[id]++ })
	h := &Handlers{FeeService: svc}

	read := func() float64 {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		u := user
		c.Set("user", &u)
		c.Request, _ = http.NewRequest("GET", "/tp-balance", nil)
		h.GetTPBalance(c)
		var tp float64
		db.Model(&models.User{}).Where("id = ?", user.ID).Select("tp_balance").Scan(&tp)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tpBalance"`) {
			t.Fatalf("status %d body %s", w.Code, w.Body.String())
		}
		return tp
	}

	if got := read(); got != 3 {
		t.Fatalf("stored balance after first read = %v, want the 3 TP pool share", got)
	}
	if got := read(); got != 3 {
		t.Fatalf("a second read must not credit again, got %v", got)
	}
	if dirty[user.ID] != 1 {
		t.Fatalf("user marked for the level check %d times, want once", dirty[user.ID])
	}
}

func TestUpdateTPProgram_OmittedFieldsKeepCurrentValues(t *testing.T) {
	db := setupTPTestDB(t)
	if err := db.Exec(`CREATE TABLE tp_program_configs (
		id TEXT PRIMARY KEY, user_pct REAL NOT NULL, upline_pct REAL NOT NULL, pool_pct REAL NOT NULL,
		zero_fee_tp REAL NOT NULL, payout_day INTEGER NOT NULL,
		signup_bonus_tp REAL NOT NULL DEFAULT 1, onboarding_bonus_tp REAL NOT NULL DEFAULT 10,
		updated_by TEXT, created_at DATETIME, updated_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	prog := services.NewTPProgramConfigService(db)
	h := &TPAdminHandler{DB: db, Program: prog, Fees: services.NewFeeDistributionService(db, nil, 100)}
	addFeeV2Tables(t, db)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &models.User{ID: uuid.New(), Role: models.RoleAdmin})
	c.Request, _ = http.NewRequest("PUT", "/program", strings.NewReader(`{"userPct": 0.04}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.UpdateTPProgram(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	got := prog.Current()
	if got.UserPct != 0.04 || got.UplinePct != 0.05 || got.PoolPct != 0.02 || got.ZeroFeeTP != 0.001 || got.PayoutDay != 24 {
		t.Fatalf("only the user pct should change, got %+v", got)
	}
}
