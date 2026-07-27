package handlers

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// setupFeeSummaryDB creates just the fee_distributions table the summary reads.
// near compares money totals with a cent's tolerance — the sums are float64
// aggregates, so exact equality is a flaky assertion, not a stricter one.
func near(got, want float64) bool { return math.Abs(got-want) < 0.005 }

func setupFeeSummaryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	addFeeV2Tables(t, db)
	return db
}

func insertFeeRow(t *testing.T, db *gorm.DB, feature, sourceType string, feeUSD, systemUSD float64, at time.Time) {
	t.Helper()
	err := db.Exec(`INSERT INTO fee_distributions
		(id, source_type, source_id, user_id, feature, token, fee_usd, system_usd,
		 buyer_cashback_usd, upline_cashback_usd, provider_cashback_usd, tp_pool_usd,
		 global_pool_usd, tp_per_usd, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		uuid.New().String(), sourceType, uuid.New().String(), uuid.New().String(),
		feature, "USDT", feeUSD, systemUSD, 0.1, 0.1, 0, 0.2, 0, 1, at).Error
	if err != nil {
		t.Fatalf("insert fee row: %v", err)
	}
}

func feeSummaryCtx(t *testing.T, query string, admin *models.User) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req, _ := http.NewRequest(http.MethodGet, "/?"+query, nil)
	c.Request = req
	if admin != nil {
		c.Set("user", admin)
	}
	return c, w
}

type feeSummaryBody struct {
	Rows []struct {
		Feature    string  `json:"feature"`
		SourceType string  `json:"sourceType"`
		Count      int64   `json:"count"`
		FeeUSD     float64 `json:"feeUsd"`
		SystemUSD  float64 `json:"systemUsd"`
	} `json:"rows"`
	Totals struct {
		Count     int64   `json:"count"`
		FeeUSD    float64 `json:"feeUsd"`
		SystemUSD float64 `json:"systemUsd"`
	} `json:"totals"`
}

func decodeFeeSummary(t *testing.T, w *httptest.ResponseRecorder) feeSummaryBody {
	t.Helper()
	var resp struct {
		Success bool           `json:"success"`
		Data    feeSummaryBody `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, w.Body.String())
	}
	if !resp.Success {
		t.Fatalf("success = false; body=%s", w.Body.String())
	}
	return resp.Data
}

func TestGetFeeSummary_GroupsBySectionAndSource(t *testing.T) {
	db := setupFeeSummaryDB(t)
	now := time.Now()
	// Two p2p releases, one checkout, one instant transfer.
	insertFeeRow(t, db, "p2p", "p2p_release", 1.00, 0.60, now)
	insertFeeRow(t, db, "p2p", "p2p_release", 2.00, 1.20, now)
	insertFeeRow(t, db, "products", "checkout", 5.00, 3.00, now)
	insertFeeRow(t, db, "instant_transfer", "it_intent", 0.50, 0.30, now)

	h := &TPAdminHandler{DB: db}
	admin := &models.User{ID: uuid.New(), Role: models.RoleAdmin}
	c, w := feeSummaryCtx(t, "", admin)
	h.GetFeeSummary(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	got := decodeFeeSummary(t, w)

	if len(got.Rows) != 3 {
		t.Fatalf("rows = %d, want 3 (one per section+source); %+v", len(got.Rows), got.Rows)
	}
	// Ordered by system share, so the checkout line leads.
	if got.Rows[0].Feature != "products" || got.Rows[0].SourceType != "checkout" {
		t.Fatalf("rows not ordered by system share: %+v", got.Rows)
	}
	var p2pRow *struct {
		Feature    string  `json:"feature"`
		SourceType string  `json:"sourceType"`
		Count      int64   `json:"count"`
		FeeUSD     float64 `json:"feeUsd"`
		SystemUSD  float64 `json:"systemUsd"`
	}
	for i := range got.Rows {
		if got.Rows[i].Feature == "p2p" {
			p2pRow = &got.Rows[i]
		}
	}
	if p2pRow == nil {
		t.Fatalf("no p2p row; %+v", got.Rows)
	}
	if p2pRow.Count != 2 || !near(p2pRow.FeeUSD, 3.00) || !near(p2pRow.SystemUSD, 1.80) {
		t.Fatalf("p2p aggregate wrong: %+v", *p2pRow)
	}

	if got.Totals.Count != 4 || !near(got.Totals.FeeUSD, 8.50) || !near(got.Totals.SystemUSD, 5.10) {
		t.Fatalf("totals wrong: %+v", got.Totals)
	}
}

func TestGetFeeSummary_HonoursDateWindow(t *testing.T) {
	db := setupFeeSummaryDB(t)
	old := time.Now().AddDate(0, 0, -30)
	insertFeeRow(t, db, "p2p", "p2p_release", 9.00, 9.00, old)
	insertFeeRow(t, db, "p2p", "p2p_release", 1.00, 0.60, time.Now())

	h := &TPAdminHandler{DB: db}
	admin := &models.User{ID: uuid.New(), Role: models.RoleSuperAdmin}
	from := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	c, w := feeSummaryCtx(t, "from="+from, admin)
	h.GetFeeSummary(c)
	got := decodeFeeSummary(t, w)

	if got.Totals.Count != 1 || !near(got.Totals.SystemUSD, 0.60) {
		t.Fatalf("date window not applied: %+v", got.Totals)
	}
}

func TestGetFeeSummary_RequiresAdmin(t *testing.T) {
	h := &TPAdminHandler{DB: setupFeeSummaryDB(t)}

	c, w := feeSummaryCtx(t, "", nil)
	h.GetFeeSummary(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: status = %d, want 401", w.Code)
	}

	c2, w2 := feeSummaryCtx(t, "", &models.User{ID: uuid.New(), Role: models.RoleUser})
	h.GetFeeSummary(c2)
	if w2.Code != http.StatusForbidden {
		t.Fatalf("plain user: status = %d, want 403", w2.Code)
	}
}
