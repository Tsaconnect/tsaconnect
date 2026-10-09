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
		`CREATE TABLE refresh_tokens (
			id TEXT PRIMARY KEY, user_id TEXT, token_hash TEXT,
			expires_at DATETIME, revoked INTEGER DEFAULT 0, device_info TEXT, web INTEGER NOT NULL DEFAULT 0, created_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	// The ramp tables come from their models, so the blocker and erasure
	// queries are tested against the real column names.
	if err := db.AutoMigrate(&models.RampSellIntent{}, &models.PaycrestOrderRecord{}, &models.PayMerchant{}, &models.PayPayment{}, &models.WebTrustedBrowser{}); err != nil {
		t.Fatalf("migrate ramp tables: %v", err)
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

// ---------- Ramp: sales and converted purchases in flight ----------

func seedSellIntent(t *testing.T, db *gorm.DB, userID uuid.UUID, status string) *models.RampSellIntent {
	t.Helper()
	in := &models.RampSellIntent{UserID: userID, SourceAddress: "0xsrc", WalletAddress: "0xwallet",
		SourceChainID: 56, SourceTokenAddress: "0xtoken", SourceAmount: "1", FiatCode: "NGN",
		BankCode: "044", AccountNumber: "0123456789", AccountName: "ADA OBI", Status: status}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("seed sell intent: %v", err)
	}
	return in
}

func seedPaycrestOrder(t *testing.T, db *gorm.DB, userID uuid.UUID, direction, routeKind, status string) *models.PaycrestOrderRecord {
	t.Helper()
	o := &models.PaycrestOrderRecord{UserID: userID, Direction: direction, Status: status,
		Reference: "ref-" + uuid.NewString(), PaycrestID: "pc-1", FiatCode: "NGN", FiatAmount: "15000",
		Token: "USDC", ChainKey: "base", Network: "base", RouteKind: routeKind, WalletAddress: "0xwallet",
		TxHash: "0xtx"}
	if direction == models.PaycrestDirectionSell {
		o.PayoutBankCode, o.PayoutInstitution = "044", "ABNGNGLA"
		o.PayoutAccount, o.PayoutAccountName = "0123456789", "ADA OBI"
	} else {
		// The PROVIDER's collection account she paid into — not her data.
		o.PayInstitution, o.PayAccountNumber, o.PayAccountName = "Opay", "9988776655", "PROVIDER LTD"
	}
	if err := db.Create(o).Error; err != nil {
		t.Fatalf("seed paycrest order: %v", err)
	}
	return o
}

func requestDeletion(t *testing.T, user *models.User) *httptest.ResponseRecorder {
	t.Helper()
	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse"}, user)
	(&Handlers{}).RequestAccountDeletion(c)
	return w
}

func blockersIn(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var resp struct {
		Data struct {
			Blockers []string `json:"blockers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Data.Blockers
}

func TestRequestAccountDeletion_BlockedByOpenSellIntent(t *testing.T) {
	for _, status := range models.SellIntentOpenStatuses {
		t.Run(status, func(t *testing.T) {
			db := setupDeletionTestDB(t)
			user := mkDeletionUser(t, db, "seller", models.RoleUser)
			seedSellIntent(t, db, user.ID, status)

			w := requestDeletion(t, user)
			if w.Code != http.StatusConflict {
				t.Fatalf("want 409, got %d — %s", w.Code, w.Body.String())
			}
			if b := blockersIn(t, w); len(b) != 1 || b[0] != "1 sale to your bank still in progress" {
				t.Fatalf("blockers = %q", b)
			}
			var fresh models.User
			db.First(&fresh, "id = ?", user.ID)
			if fresh.AccountStatus != models.AccountStatusActive {
				t.Error("account was deleted mid-sale")
			}
		})
	}
}

func TestRequestAccountDeletion_BlockedByOpenConvertedOrder(t *testing.T) {
	for _, status := range []string{models.PaycrestStatusAwaitingPayment, models.PaycrestStatusConverting,
		models.PaycrestStatusRefunding, models.PaycrestStatusRecoveryPending} {
		t.Run(status, func(t *testing.T) {
			db := setupDeletionTestDB(t)
			user := mkDeletionUser(t, db, "buyer", models.RoleUser)
			seedPaycrestOrder(t, db, user.ID, models.PaycrestDirectionBuy, models.RouteKindConverted, status)

			w := requestDeletion(t, user)
			if w.Code != http.StatusConflict {
				t.Fatalf("want 409, got %d — %s", w.Code, w.Body.String())
			}
			if b := blockersIn(t, w); len(b) != 1 || b[0] != "1 purchase still being delivered" {
				t.Fatalf("blockers = %q", b)
			}
		})
	}
}

// Finished sales and delivered purchases are history, not commitments — and
// another user's open sale is not hers.
func TestRequestAccountDeletion_FinishedRampActivityDoesNotBlock(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "done", models.RoleUser)
	seedSellIntent(t, db, user.ID, models.SellIntentFailed)
	seedSellIntent(t, db, user.ID, models.SellIntentOrdered)
	seedSellIntent(t, db, user.ID, models.SellIntentDelivered)
	for _, status := range models.PaycrestTerminalStatuses() {
		seedPaycrestOrder(t, db, user.ID, models.PaycrestDirectionBuy, models.RouteKindConverted, status)
	}
	stranger := mkDeletionUser(t, db, "stranger", models.RoleUser)
	seedSellIntent(t, db, stranger.ID, models.SellIntentSwapping)
	seedPaycrestOrder(t, db, stranger.ID, models.PaycrestDirectionBuy, models.RouteKindConverted, models.PaycrestStatusConverting)

	if w := requestDeletion(t, user); w.Code != http.StatusOK {
		t.Fatalf("finished ramp activity blocked deletion: got %d — %s", w.Code, w.Body.String())
	}
}

// ---------- Permanent erasure: bank details ----------

func completeDeletion(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	admin := mkDeletionUser(t, db, "root-"+uuid.NewString()[:8], models.RoleAdmin)
	req := models.AccountDeletionRequest{ID: uuid.New(), UserID: userID,
		Status: models.AccountDeletionStatusPending, PurgeAfter: time.Now()}
	if err := db.Create(&req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	w, c := newDeletionContext("POST", "/x", nil, admin)
	c.Params = gin.Params{{Key: "id", Value: req.ID.String()}}
	(&Handlers{}).CompleteAccountDeletionRequest(c)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: %d — %s", w.Code, w.Body.String())
	}
}

func TestCompleteAccountDeletionRequest_ErasesFinishedBankDetails(t *testing.T) {
	db := setupDeletionTestDB(t)
	victim := mkDeletionUser(t, db, "leaver", models.RoleUser)
	failed := seedSellIntent(t, db, victim.ID, models.SellIntentFailed)
	ordered := seedSellIntent(t, db, victim.ID, models.SellIntentOrdered)
	delivered := seedSellIntent(t, db, victim.ID, models.SellIntentDelivered)
	settled := seedPaycrestOrder(t, db, victim.ID, models.PaycrestDirectionSell, models.RouteKindDirect, models.PaycrestStatusSettled)
	expired := seedPaycrestOrder(t, db, victim.ID, models.PaycrestDirectionSell, models.RouteKindDirect, models.PaycrestStatusExpired)
	// Kept: an operator is still chasing an unmatched order, and a buy row's
	// pay_* fields are the provider's account, not hers.
	unmatched := seedPaycrestOrder(t, db, victim.ID, models.PaycrestDirectionSell, models.RouteKindDirect, models.PaycrestStatusUnmatched)
	buy := seedPaycrestOrder(t, db, victim.ID, models.PaycrestDirectionBuy, models.RouteKindDirect, models.PaycrestStatusSettled)
	other := mkDeletionUser(t, db, "stays", models.RoleUser)
	othersIntent := seedSellIntent(t, db, other.ID, models.SellIntentFailed)
	othersOrder := seedPaycrestOrder(t, db, other.ID, models.PaycrestDirectionSell, models.RouteKindDirect, models.PaycrestStatusSettled)

	completeDeletion(t, db, victim.ID)

	for _, id := range []uuid.UUID{failed.ID, ordered.ID, delivered.ID} {
		var in models.RampSellIntent
		db.First(&in, "id = ?", id)
		if in.BankCode != "" || in.AccountNumber != "" || in.AccountName != "" {
			t.Errorf("%s intent kept bank details: %q %q %q", in.Status, in.BankCode, in.AccountNumber, in.AccountName)
		}
		if in.SourceAmount != "1" || in.WalletAddress != "0xwallet" {
			t.Errorf("%s intent lost non-bank fields: %+v", in.Status, in)
		}
	}
	for _, id := range []uuid.UUID{settled.ID, expired.ID} {
		var o models.PaycrestOrderRecord
		db.First(&o, "id = ?", id)
		if o.PayoutBankCode != "" || o.PayoutInstitution != "" || o.PayoutAccount != "" || o.PayoutAccountName != "" {
			t.Errorf("%s order kept payout details: %+v", o.Status, o)
		}
		// The audit trail an operator reconciles by is untouched.
		if o.Reference == "" || o.PaycrestID != "pc-1" || o.FiatAmount != "15000" || o.TxHash != "0xtx" {
			t.Errorf("%s order lost its audit fields: %+v", o.Status, o)
		}
	}

	var um models.PaycrestOrderRecord
	db.First(&um, "id = ?", unmatched.ID)
	if um.PayoutAccount != "0123456789" || um.PayoutAccountName != "ADA OBI" {
		t.Errorf("an unmatched order under operator recovery was scrubbed: %+v", um)
	}
	var b models.PaycrestOrderRecord
	db.First(&b, "id = ?", buy.ID)
	if b.PayAccountNumber != "9988776655" || b.PayAccountName != "PROVIDER LTD" {
		t.Errorf("the provider's collection account was scrubbed: %+v", b)
	}
	var oi models.RampSellIntent
	db.First(&oi, "id = ?", othersIntent.ID)
	var oo models.PaycrestOrderRecord
	db.First(&oo, "id = ?", othersOrder.ID)
	if oi.AccountNumber != "0123456789" || oo.PayoutAccount != "0123456789" {
		t.Error("another user's bank details were scrubbed")
	}
}

// seedPayMerchant is her TSA Pay merchant account, live and settling to a bank.
func seedPayMerchant(t *testing.T, db *gorm.DB, userID uuid.UUID) *models.PayMerchant {
	t.Helper()
	live, test := "livehash-"+uuid.NewString(), "testhash-"+uuid.NewString()
	m := &models.PayMerchant{MerchantRequestID: uuid.New(), UserID: userID, BusinessName: "Sky Air",
		Status: models.PayMerchantActive, SettlementMode: models.PaySettlementFiat, BankCode: "058",
		AccountNumber: "0123456789", AccountName: "SKY AIR LTD", WalletAddress: "0xmerchant",
		WebhookURL: "https://hooks.skyair.ng/tsa", TestWebhookURL: "https://hooks.skyair.ng/test",
		MinAmountKobo: 100, MaxAmountKobo: 1_000_000_00, LiveKeyHash: &live, LiveKeyPrefix: "tsapk_live_ab",
		TestKeyHash: &test, TestKeyPrefix: "tsapk_test_ab"}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed merchant: %v", err)
	}
	return m
}

func seedPayPayment(t *testing.T, db *gorm.DB, merchantID uuid.UUID, payer *uuid.UUID, mode, status string) {
	t.Helper()
	p := &models.PayPayment{MerchantID: merchantID, Mode: mode, Reference: "PNR-" + uuid.NewString()[:8],
		AmountKobo: 5_000_000, Currency: "NGN", Status: status, PayerUserID: payer}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed payment: %v", err)
	}
}

func TestRequestAccountDeletion_BlockedByAPayPaymentInFlight(t *testing.T) {
	for name, seed := range map[string]func(t *testing.T, db *gorm.DB, user *models.User){
		"a customer is paying her merchant account": func(t *testing.T, db *gorm.DB, user *models.User) {
			m := seedPayMerchant(t, db, user.ID)
			payer := uuid.New()
			seedPayPayment(t, db, m.ID, &payer, models.PayModeLive, models.PayStatusProcessing)
		},
		"she is paying a merchant": func(t *testing.T, db *gorm.DB, user *models.User) {
			seedPayPayment(t, db, uuid.New(), &user.ID, models.PayModeLive, models.PayStatusProcessing)
		},
	} {
		t.Run(name, func(t *testing.T) {
			db := setupDeletionTestDB(t)
			user := mkDeletionUser(t, db, "payer", models.RoleUser)
			seed(t, db, user)
			w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
				map[string]string{"password": "correct-horse"}, user)
			(&Handlers{}).RequestAccountDeletion(c)
			if w.Code != http.StatusConflict {
				t.Fatalf("want 409, got %d — body %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestRequestAccountDeletion_SuspendsHerMerchantAccount(t *testing.T) {
	db := setupDeletionTestDB(t)
	user := mkDeletionUser(t, db, "owner", models.RoleUser)
	m := seedPayMerchant(t, db, user.ID)
	// Finished and test-mode payments are not money in flight.
	seedPayPayment(t, db, m.ID, nil, models.PayModeLive, models.PayStatusSucceeded)
	seedPayPayment(t, db, m.ID, &user.ID, models.PayModeTest, models.PayStatusProcessing)
	others := seedPayMerchant(t, db, uuid.New())

	w, c := newDeletionContext("POST", "/api/users/account/deletion-request",
		map[string]string{"password": "correct-horse"}, user)
	(&Handlers{}).RequestAccountDeletion(c)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d — body %s", w.Code, w.Body.String())
	}
	var got, other models.PayMerchant
	db.First(&got, "id = ?", m.ID)
	db.First(&other, "id = ?", others.ID)
	if got.Status != models.PayMerchantSuspended || got.Version != m.Version+1 {
		t.Fatalf("merchant = %+v: a deleted account's keys and payments still work", got)
	}
	if other.Status != models.PayMerchantActive {
		t.Fatal("another user's merchant was suspended")
	}
}

func TestCompleteAccountDeletionRequest_ErasesHerMerchantAccountsDestinationsAndKeys(t *testing.T) {
	db := setupDeletionTestDB(t)
	victim := mkDeletionUser(t, db, "leaver", models.RoleUser)
	m := seedPayMerchant(t, db, victim.ID)
	others := seedPayMerchant(t, db, uuid.New())

	completeDeletion(t, db, victim.ID)

	var got models.PayMerchant
	db.First(&got, "id = ?", m.ID)
	if got.Status != models.PayMerchantSuspended || got.SettlementMode != "" || got.BankCode != "" ||
		got.AccountNumber != "" || got.AccountName != "" || got.WalletAddress != "" ||
		got.WebhookURL != "" || got.TestWebhookURL != "" || got.LiveKeyHash != nil || got.TestKeyHash != nil ||
		got.LiveKeyPrefix != "" || got.TestKeyPrefix != "" {
		t.Fatalf("merchant kept a destination or credential after erasure: %+v", got)
	}
	var other models.PayMerchant
	db.First(&other, "id = ?", others.ID)
	if other.AccountNumber != "0123456789" || other.LiveKeyHash == nil {
		t.Fatal("another user's merchant account was scrubbed")
	}
}
