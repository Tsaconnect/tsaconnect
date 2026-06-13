package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

// addRewardSummaryTables augments a TP test DB with the SP5 reward tables the
// summary service reads (reward_claimables + prize_dues). tp_earnings / users
// already exist via setupTPTestDB.
func addRewardSummaryTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS reward_claimables (
			user_id TEXT PRIMARY KEY,
			claimable_mcgp REAL NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS prize_dues (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			level INTEGER NOT NULL,
			prize TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			created_at DATETIME NOT NULL,
			UNIQUE(user_id, level)
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create reward table: %v", err)
		}
	}
}

// TestGetRewardSummary_HappyPath drives the GET /api/rewards/summary handler
// end-to-end (no refresher/merkle, so the read is pure DB state) and asserts the
// JSON envelope + the key fields the app consumes.
func TestGetRewardSummary_HappyPath(t *testing.T) {
	db := setupTPTestDB(t)
	addRewardSummaryTables(t, db)

	// tp_balance 25 ⇒ crossed L1(1),L2(10); at L2, next L3(100).
	user := createTPTestUser(t, db, "Sam", "sam_summary", nil)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("tp_balance", 25.0)
	user.TPBalance = 25.0

	// Earnings at gen0 + gen2; a claimable balance; an L6 prize due.
	for _, e := range []struct {
		gen int
		tp  float64
	}{{0, 12}, {2, 8}} {
		earning := models.TPEarning{
			ID: uuid.New(), UserID: user.ID, SourceUserID: uuid.New(),
			SourceType: "checkout", SourceID: uuid.New(), Generation: e.gen,
			FeeAmountUSD: 1, Percentage: 0.354, TPEarned: e.tp, CreatedAt: time.Now(),
		}
		if err := db.Create(&earning).Error; err != nil {
			t.Fatalf("create earning: %v", err)
		}
	}
	if err := db.Create(&models.RewardClaimable{
		UserID: user.ID, ClaimableMCGP: 3.5, UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed claimable: %v", err)
	}
	if err := db.Create(&models.PrizeDue{
		ID: uuid.New(), UserID: user.ID, Level: 6, Prize: models.PrizeForLevel(6),
		Status: models.PrizeDueStatusPending, CreatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatalf("seed prize: %v", err)
	}

	svc := services.NewRewardSummaryService(db, nil, nil, nil)
	h := NewRewardSummaryHandler(svc, nil)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &user)
	c.Request, _ = http.NewRequest("GET", "/rewards/summary", nil)

	h.GetRewardSummary(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool                   `json:"success"`
		Data    services.RewardSummary `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !resp.Success {
		t.Fatalf("success = false: %s", w.Body.String())
	}
	d := resp.Data
	if d.TPBalance != 25.0 {
		t.Errorf("tpBalance = %v, want 25", d.TPBalance)
	}
	if d.CurrentLevel != 2 {
		t.Errorf("currentLevel = %d, want 2", d.CurrentLevel)
	}
	if d.NextLevelThresholdTP != 100 {
		t.Errorf("nextLevelThresholdTP = %v, want 100", d.NextLevelThresholdTP)
	}
	if d.ClaimableMCGP != 3.5 {
		t.Errorf("claimableMcgp = %v, want 3.5", d.ClaimableMCGP)
	}
	if d.TotalClaimedMCGP != 0 {
		t.Errorf("totalClaimedMcgp = %v, want 0", d.TotalClaimedMCGP)
	}
	if len(d.Levels) != models.TPLevelCount {
		t.Errorf("levels len = %d, want %d", len(d.Levels), models.TPLevelCount)
	}
	if len(d.PerGeneration) != 11 {
		t.Fatalf("perGeneration len = %d, want 11", len(d.PerGeneration))
	}
	if d.PerGeneration[0].EarnedTP != 12 || d.PerGeneration[2].EarnedTP != 8 {
		t.Errorf("perGeneration gen0=%v gen2=%v, want 12 and 8",
			d.PerGeneration[0].EarnedTP, d.PerGeneration[2].EarnedTP)
	}
	if len(d.PrizesDue) != 1 || d.PrizesDue[0].Level != 6 {
		t.Errorf("prizesDue = %+v, want one L6", d.PrizesDue)
	}
}

// TestGetRewardSummary_Unauthorized ensures the handler 401s without a user.
func TestGetRewardSummary_Unauthorized(t *testing.T) {
	svc := services.NewRewardSummaryService(setupTPTestDB(t), nil, nil, nil)
	h := NewRewardSummaryHandler(svc, nil)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest("GET", "/rewards/summary", nil)

	h.GetRewardSummary(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

// TestPrepareClaim_DormantReturnsNullTx ensures prepare-claim returns a clean
// "nothing to claim" payload (unsignedTx=null, included=false) when the merkle
// distributor is dormant — matching /rewards/claim-data's behavior.
func TestPrepareClaim_DormantReturnsNullTx(t *testing.T) {
	db := setupTPTestDB(t)
	user := createTPTestUser(t, db, "Dee", "dee_claim", nil)

	// merkle nil ⇒ dormant branch.
	h := NewRewardSummaryHandler(services.NewRewardSummaryService(db, nil, nil, nil), nil)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", &user)
	c.Request, _ = http.NewRequest("GET", "/rewards/prepare-claim", nil)

	h.PrepareClaim(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Success bool `json:"success"`
		Data    struct {
			ClaimData  services.ClaimData `json:"claimData"`
			UnsignedTx *json.RawMessage   `json:"unsignedTx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.Data.ClaimData.Included {
		t.Errorf("included = true, want false when dormant")
	}
	if resp.Data.UnsignedTx != nil {
		t.Errorf("unsignedTx = %s, want null when dormant", string(*resp.Data.UnsignedTx))
	}
}
