package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// setupDeletionTestDB creates an in-memory SQLite DB with the tables the
// account-deletion flow touches, and points the package-level config.DB at it
// (the handler reads config.DB, matching the rest of user_handler.go).
func setupDeletionTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	stmts := []string{
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL, username TEXT NOT NULL UNIQUE, email TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL, role TEXT DEFAULT 'user', phone_number TEXT,
			country TEXT, state TEXT, city TEXT, address TEXT, profile_photo TEXT,
			referral_code TEXT, referred_by TEXT, didit_session_id TEXT,
			verification_status TEXT DEFAULT 'pending', verification_notes TEXT,
			account_status TEXT DEFAULT 'active', last_login DATETIME,
			login_attempts INTEGER DEFAULT 0, lock_until DATETIME, wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0, mute_notifications INTEGER DEFAULT 0,
			mute_email INTEGER DEFAULT 0, tp_balance REAL DEFAULT 0,
			cashback_balance REAL DEFAULT 0, last_settled_global_index INTEGER,
			email_verified INTEGER DEFAULT 0, deleted_at DATETIME,
			created_at DATETIME, updated_at DATETIME,
			suspension_reason TEXT,
			suspended_at DATETIME,
			suspended_by TEXT,
			suspended_from_status TEXT, verified_legal_name TEXT, verified_legal_name_at DATETIME
		)`,
		`CREATE TABLE account_deletion_requests (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, reason TEXT,
			status TEXT NOT NULL DEFAULT 'pending', purge_after DATETIME,
			admin_note TEXT, reviewed_by TEXT, reviewed_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE p2p_trades (
			id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, status TEXT
		)`,
		`CREATE TABLE orders (
			id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, status TEXT
		)`,
		`CREATE TABLE device_tokens (
			id TEXT PRIMARY KEY, user_id TEXT, token TEXT, platform TEXT,
			created_at DATETIME, updated_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	prev := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = prev })

	return db
}

// mkDeletionUser inserts a user whose password is "correct-horse".
func mkDeletionUser(t *testing.T, db *gorm.DB, username, role string) *models.User {
	t.Helper()
	hashed, err := models.HashPassword("correct-horse")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := &models.User{
		ID:            uuid.New(),
		Name:          "Test " + username,
		Username:      username,
		Email:         username + "@example.com",
		Password:      hashed,
		Role:          role,
		AccountStatus: models.AccountStatusActive,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func newDeletionContext(method, path string, body interface{}, user *models.User) (*httptest.ResponseRecorder, *gin.Context) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reqBody []byte
	if body != nil {
		reqBody, _ = json.Marshal(body)
	}
	c.Request, _ = http.NewRequest(method, path, bytes.NewBuffer(reqBody))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.ContentLength = int64(len(reqBody))
	if user != nil {
		c.Set("user", user)
	}
	return w, c
}

// collectEvents subscribes to the bus and returns a func that drains the
// events seen so far. The bus dispatches on its own goroutine, so callers
// need a brief settle before reading.
func collectEvents(bus *events.Bus) func() []events.Event {
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	var seen []events.Event
	bus.Subscribe("*", func(e events.Event) {
		<-mu
		seen = append(seen, e)
		mu <- struct{}{}
	})
	return func() []events.Event {
		time.Sleep(50 * time.Millisecond)
		<-mu
		out := append([]events.Event(nil), seen...)
		mu <- struct{}{}
		return out
	}
}

// ---------- RequestAccountDeletion ----------

func TestRequestAccountDeletion_Unauthenticated(t *testing.T) {
	setupDeletionTestDB(t)
	h := &Handlers{}

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse"}, nil)
	h.RequestAccountDeletion(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
}

func TestRequestAccountDeletion_RequiresPassword(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "nopass", models.RoleUser)
	h := &Handlers{}

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"reason": "leaving"}, user)
	h.RequestAccountDeletion(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	// The account must be untouched.
	var fresh models.User
	db.First(&fresh, "id = ?", user.ID)
	if fresh.AccountStatus != models.AccountStatusActive {
		t.Fatalf("account status changed to %q on a rejected request", fresh.AccountStatus)
	}
}

func TestRequestAccountDeletion_WrongPassword(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "wrongpass", models.RoleUser)
	h := &Handlers{}

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "not-my-password"}, user)
	h.RequestAccountDeletion(c)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	var count int64
	db.Model(&models.AccountDeletionRequest{}).Count(&count)
	if count != 0 {
		t.Fatalf("a deletion request was filed despite a bad password (%d rows)", count)
	}
}

func TestRequestAccountDeletion_SoftDeletesAndFilesRequest(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "leaver", models.RoleUser)
	bus := events.NewBus()
	defer bus.Stop()
	h := &Handlers{EventBus: bus}

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse", "reason": "not using it"}, user)
	h.RequestAccountDeletion(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d — body %s", w.Code, w.Body.String())
	}

	var fresh models.User
	if err := db.First(&fresh, "id = ?", user.ID).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if fresh.AccountStatus != models.AccountStatusDeleted {
		t.Errorf("account_status = %q, want %q", fresh.AccountStatus, models.AccountStatusDeleted)
	}
	if fresh.DeletedAt == nil {
		t.Error("deleted_at is nil — the account was not soft-deleted")
	}

	var req models.AccountDeletionRequest
	if err := db.First(&req, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("no deletion request filed: %v", err)
	}
	if req.Status != models.AccountDeletionStatusPending {
		t.Errorf("status = %q, want pending", req.Status)
	}
	if req.Reason != "not using it" {
		t.Errorf("reason = %q, want %q", req.Reason, "not using it")
	}
	// Grace window must be in the future, ~30 days out.
	wantPurge := time.Now().AddDate(0, 0, models.AccountDeletionGraceDays)
	if diff := req.PurgeAfter.Sub(wantPurge); diff > time.Minute || diff < -time.Minute {
		t.Errorf("purge_after = %v, want ~%v", req.PurgeAfter, wantPurge)
	}
}

func TestRequestAccountDeletion_PrunesDeviceTokens(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "pushy", models.RoleUser)
	if err := db.Exec(
		`INSERT INTO device_tokens (id, user_id, token, platform) VALUES (?, ?, ?, ?)`,
		uuid.New().String(), user.ID.String(), "ExponentPushToken[abc]", "android",
	).Error; err != nil {
		t.Fatalf("seed device token: %v", err)
	}
	h := &Handlers{}

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse"}, user)
	h.RequestAccountDeletion(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var tokens int64
	db.Table("device_tokens").Where("user_id = ?", user.ID).Count(&tokens)
	if tokens != 0 {
		t.Errorf("device tokens still registered after deletion: %d", tokens)
	}
}

func TestRequestAccountDeletion_NotifiesUserAndAdmins(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "departing", models.RoleUser)
	admin := mkDeletionUser(t, db, "root", models.RoleAdmin)
	superAdmin := mkDeletionUser(t, db, "boss", models.RoleSuperAdmin)
	mkDeletionUser(t, db, "bystander", models.RoleUser) // must NOT be notified

	bus := events.NewBus()
	defer bus.Stop()
	drain := collectEvents(bus)
	h := &Handlers{EventBus: bus}

	_, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse"}, user)
	h.RequestAccountDeletion(c)

	notified := map[uuid.UUID]bool{}
	for _, e := range drain() {
		if e.Type == events.AccountDeletionRequested {
			notified[e.UserID] = true
		}
	}

	for _, want := range []*models.User{user, admin, superAdmin} {
		if !notified[want.ID] {
			t.Errorf("no deletion event for %s (%s)", want.Username, want.Role)
		}
	}
	if len(notified) != 3 {
		t.Errorf("notified %d recipients, want exactly 3 (user + 2 admins)", len(notified))
	}
}

func TestRequestAccountDeletion_BlockedByOpenTrade(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "trader", models.RoleUser)
	if err := db.Exec(
		`INSERT INTO p2p_trades (id, buyer_id, seller_id, status) VALUES (?, ?, ?, ?)`,
		uuid.New().String(), user.ID.String(), uuid.New().String(), models.P2PTradeStatusPaid,
	).Error; err != nil {
		t.Fatalf("seed trade: %v", err)
	}
	h := &Handlers{}

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse"}, user)
	h.RequestAccountDeletion(c)

	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d — body %s", w.Code, w.Body.String())
	}
	var fresh models.User
	db.First(&fresh, "id = ?", user.ID)
	if fresh.AccountStatus != models.AccountStatusActive {
		t.Error("account was deleted despite an open trade")
	}
}

func TestRequestAccountDeletion_BlockedByOpenOrder(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "shopper", models.RoleUser)
	if err := db.Exec(
		`INSERT INTO orders (id, buyer_id, seller_id, status) VALUES (?, ?, ?, ?)`,
		uuid.New().String(), user.ID.String(), uuid.New().String(), models.OrderStatusShipped,
	).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}
	h := &Handlers{}

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse"}, user)
	h.RequestAccountDeletion(c)

	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", w.Code)
	}
}

func TestRequestAccountDeletion_SettledTradeDoesNotBlock(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "settled", models.RoleUser)
	for _, status := range []string{
		models.P2PTradeStatusReleased,
		models.P2PTradeStatusCancelled,
		models.P2PTradeStatusResolved,
	} {
		if err := db.Exec(
			`INSERT INTO p2p_trades (id, buyer_id, seller_id, status) VALUES (?, ?, ?, ?)`,
			uuid.New().String(), user.ID.String(), uuid.New().String(), status,
		).Error; err != nil {
			t.Fatalf("seed trade: %v", err)
		}
	}
	h := &Handlers{}

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse"}, user)
	h.RequestAccountDeletion(c)

	if w.Code != http.StatusOK {
		t.Fatalf("terminal trades blocked deletion: got %d — %s", w.Code, w.Body.String())
	}
}

// ---------- Admin review ----------

func TestListAccountDeletionRequests_FiltersDue(t *testing.T) {
	db := setupDeletionTestDB(t)
	admin := mkDeletionUser(t, db, "root", models.RoleAdmin)

	due := models.AccountDeletionRequest{
		ID: uuid.New(), UserID: uuid.New(), Status: models.AccountDeletionStatusPending,
		PurgeAfter: time.Now().AddDate(0, 0, -1),
	}
	notDue := models.AccountDeletionRequest{
		ID: uuid.New(), UserID: uuid.New(), Status: models.AccountDeletionStatusPending,
		PurgeAfter: time.Now().AddDate(0, 0, 10),
	}
	if err := db.Create(&due).Error; err != nil {
		t.Fatalf("seed due: %v", err)
	}
	if err := db.Create(&notDue).Error; err != nil {
		t.Fatalf("seed notDue: %v", err)
	}

	h := &Handlers{}
	w, c := newDeletionContext("GET", "/api/admin/account-deletion-requests?due=true", nil, admin)
	c.Request.URL.RawQuery = "due=true"
	h.ListAccountDeletionRequests(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var resp struct {
		Data struct {
			Total    int64 `json:"total"`
			Requests []struct {
				ID string `json:"id"`
			} `json:"requests"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Data.Total != 1 || len(resp.Data.Requests) != 1 {
		t.Fatalf("want exactly the 1 due request, got total=%d len=%d", resp.Data.Total, len(resp.Data.Requests))
	}
	if resp.Data.Requests[0].ID != due.ID.String() {
		t.Errorf("returned the wrong request: %s", resp.Data.Requests[0].ID)
	}
}

func TestCompleteAccountDeletionRequest_RequiresAdminRole(t *testing.T) {
	db := setupDeletionTestDB(t)
	// adminAuth admits merchant/support too, so the handler must re-check.
	merchant := mkDeletionUser(t, db, "shopkeeper", models.RoleMerchant)
	req := models.AccountDeletionRequest{
		ID: uuid.New(), UserID: uuid.New(), Status: models.AccountDeletionStatusPending,
		PurgeAfter: time.Now(),
	}
	db.Create(&req)

	h := &Handlers{}
	w, c := newDeletionContext("POST", "/x", nil, merchant)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.CompleteAccountDeletionRequest(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for a merchant, got %d", w.Code)
	}
}

func TestCompleteAccountDeletionRequest_MarksCompleted(t *testing.T) {
	db := setupDeletionTestDB(t)
	admin := mkDeletionUser(t, db, "root", models.RoleAdmin)
	req := models.AccountDeletionRequest{
		ID: uuid.New(), UserID: uuid.New(), Status: models.AccountDeletionStatusPending,
		PurgeAfter: time.Now(),
	}
	db.Create(&req)

	h := &Handlers{}
	w, c := newDeletionContext("POST", "/x", map[string]string{"note": "PII anonymised"}, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.CompleteAccountDeletionRequest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d — %s", w.Code, w.Body.String())
	}
	var fresh models.AccountDeletionRequest
	db.First(&fresh, "id = ?", req.ID)
	if fresh.Status != models.AccountDeletionStatusCompleted {
		t.Errorf("status = %q, want completed", fresh.Status)
	}
	if fresh.AdminNote != "PII anonymised" {
		t.Errorf("admin note = %q", fresh.AdminNote)
	}
	if fresh.ReviewedBy == nil || *fresh.ReviewedBy != admin.ID {
		t.Error("reviewed_by not recorded")
	}
}

func TestCompleteAccountDeletionRequest_RejectsAlreadyActioned(t *testing.T) {
	db := setupDeletionTestDB(t)
	admin := mkDeletionUser(t, db, "root", models.RoleAdmin)
	req := models.AccountDeletionRequest{
		ID: uuid.New(), UserID: uuid.New(), Status: models.AccountDeletionStatusCompleted,
		PurgeAfter: time.Now(),
	}
	db.Create(&req)

	h := &Handlers{}
	w, c := newDeletionContext("POST", "/x", nil, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.CompleteAccountDeletionRequest(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 on a re-action, got %d", w.Code)
	}
}

func TestRestoreAccountDeletionRequest_ReactivatesUser(t *testing.T) {
	db := setupDeletionTestDB(t)
	admin := mkDeletionUser(t, db, "root", models.RoleAdmin)
	victim := mkDeletionUser(t, db, "regretful", models.RoleUser)

	// Put the user in the post-deletion state.
	now := time.Now()
	db.Model(&models.User{}).Where("id = ?", victim.ID).Updates(map[string]interface{}{
		"account_status": models.AccountStatusDeleted,
		"deleted_at":     now,
	})
	req := models.AccountDeletionRequest{
		ID: uuid.New(), UserID: victim.ID, Status: models.AccountDeletionStatusPending,
		PurgeAfter: now.AddDate(0, 0, 30),
	}
	db.Create(&req)

	bus := events.NewBus()
	defer bus.Stop()
	drain := collectEvents(bus)
	h := &Handlers{EventBus: bus}

	w, c := newDeletionContext("POST", "/x", map[string]string{"note": "user called support"}, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	h.RestoreAccountDeletionRequest(c)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d — %s", w.Code, w.Body.String())
	}

	var fresh models.User
	db.First(&fresh, "id = ?", victim.ID)
	if fresh.AccountStatus != models.AccountStatusActive {
		t.Errorf("account_status = %q, want active", fresh.AccountStatus)
	}
	if fresh.DeletedAt != nil {
		t.Errorf("deleted_at = %v, want nil", fresh.DeletedAt)
	}

	var restored bool
	for _, e := range drain() {
		if e.Type == events.AccountDeletionRestored && e.UserID == victim.ID {
			restored = true
		}
	}
	if !restored {
		t.Error("the restored user was never notified")
	}
}
