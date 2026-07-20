package handlers

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupRewardClaimTestDB builds the in-memory schema the reward-claim handler
// touches. Raw DDL (not AutoMigrate) for the usual Postgres-default reason.
func setupRewardClaimTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	stmts := []string{
		// Full column set — GORM writes every model field on Create, so a
		// trimmed table fails with "no column named …".
		`CREATE TABLE users (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			username TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL,
			role TEXT DEFAULT 'user',
			phone_number TEXT,
			country TEXT,
			state TEXT,
			city TEXT,
			address TEXT,
			profile_photo TEXT,
			referral_code TEXT,
			referred_by TEXT,
			persona_inquiry_id TEXT,
			verification_status TEXT DEFAULT 'pending',
			verification_notes TEXT,
			account_status TEXT DEFAULT 'active',
			last_login DATETIME,
			login_attempts INTEGER DEFAULT 0,
			lock_until DATETIME,
			wallet_address TEXT,
			seed_phrase_backed_up INTEGER DEFAULT 0,
			mute_notifications INTEGER DEFAULT 0,
			mute_email INTEGER DEFAULT 0,
			tp_balance REAL DEFAULT 0,
			cashback_balance REAL DEFAULT 0,
			last_settled_global_index INTEGER,
			email_verified INTEGER DEFAULT 0,
			deleted_at DATETIME,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE TABLE reward_claims (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'pending',
			name TEXT NOT NULL,
			username TEXT NOT NULL,
			phone_number TEXT,
			phone_network TEXT,
			inviter_username TEXT,
			social_links TEXT,
			payout_method TEXT,
			payout_details TEXT,
			invited_usernames TEXT,
			reviewed_by TEXT,
			review_notes TEXT,
			created_at DATETIME,
			updated_at DATETIME
		)`,
		`CREATE UNIQUE INDEX idx_reward_claim_user_kind ON reward_claims (user_id, kind)`,
		`CREATE TABLE reward_flags (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, reason TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'admin', status TEXT NOT NULL DEFAULT 'active',
			flagged_by TEXT, cleared_by TEXT, cleared_at DATETIME,
			created_at DATETIME, updated_at DATETIME
		)`,
		`CREATE TABLE transactions (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, type TEXT, status TEXT,
			amount REAL, created_at DATETIME, updated_at DATETIME
		)`,
	}
	for _, s := range stmts {
		if err := db.Exec(s).Error; err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

func newRewardClaimHandler(t *testing.T, db *gorm.DB) *RewardClaimHandler {
	t.Helper()
	graph := services.NewReferralGraphService(db)
	elig := services.NewRewardEligibilityService(db, graph)
	svc := services.NewRewardClaimService(db, elig, graph)
	return NewRewardClaimHandler(db, svc, graph)
}

// mkRcUser inserts a user; verified controls the KYC gate.
func mkRcUser(t *testing.T, db *gorm.DB, username string, verified bool, role string) *models.User {
	t.Helper()
	status := models.VerificationStatusPending
	if verified {
		status = models.VerificationStatusVerified
	}
	u := models.User{
		ID: uuid.New(), Name: username, Username: username,
		Email: username + "@test.com", Password: "x", Role: role,
		ReferralCode: username, VerificationStatus: status,
		AccountStatus: models.AccountStatusActive,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return &u
}

const validSignupBody = `{"kind":"signup","name":"Ada","username":"ada",
	"phoneNumber":"08030000000","phoneNetwork":"mtn","inviterUsername":"bob"}`

func TestSubmitRewardClaim_SignupHappyPath(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	user := mkRcUser(t, db, "ada", true, models.RoleUser)

	c, w := spotCtx(t, http.MethodPost, validSignupBody, user)
	h.SubmitRewardClaim(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var row models.RewardClaim
	if err := db.First(&row).Error; err != nil {
		t.Fatalf("no row created: %v", err)
	}
	if row.Kind != models.RewardClaimKindSignup || row.Status != models.RewardClaimStatusPending {
		t.Fatalf("unexpected row: %+v", row)
	}
	if row.PhoneNetwork != "mtn" {
		t.Fatalf("phoneNetwork = %q, want mtn", row.PhoneNetwork)
	}
}

func TestSubmitRewardClaim_BlockedWhenNotKycVerified(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	user := mkRcUser(t, db, "ada", false, models.RoleUser)

	c, w := spotCtx(t, http.MethodPost, validSignupBody, user)
	h.SubmitRewardClaim(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for unverified KYC; body=%s", w.Code, w.Body.String())
	}
	var n int64
	db.Model(&models.RewardClaim{}).Count(&n)
	if n != 0 {
		t.Fatalf("no row should be created; got %d", n)
	}
}

func TestSubmitRewardClaim_RejectsBadNetworkAndMissingPhone(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	user := mkRcUser(t, db, "ada", true, models.RoleUser)

	c, w := spotCtx(t, http.MethodPost,
		`{"kind":"signup","name":"Ada","username":"ada","phoneNumber":"0803","phoneNetwork":"vodafone"}`, user)
	h.SubmitRewardClaim(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown network", w.Code)
	}

	c2, w2 := spotCtx(t, http.MethodPost,
		`{"kind":"signup","name":"Ada","username":"ada","phoneNetwork":"mtn"}`, user)
	h.SubmitRewardClaim(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for missing phone", w2.Code)
	}
}

func TestSubmitRewardClaim_OneTimePerKind(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	user := mkRcUser(t, db, "ada", true, models.RoleUser)

	c1, w1 := spotCtx(t, http.MethodPost, validSignupBody, user)
	h.SubmitRewardClaim(c1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first submit = %d, want 201", w1.Code)
	}
	// Second attempt is caught by the eligibility gate (403) before the unique
	// index; either way it must not create a second row.
	c2, w2 := spotCtx(t, http.MethodPost, validSignupBody, user)
	h.SubmitRewardClaim(c2)
	if w2.Code == http.StatusCreated {
		t.Fatalf("second submit should be rejected, got 201")
	}
	var n int64
	db.Model(&models.RewardClaim{}).Count(&n)
	if n != 1 {
		t.Fatalf("row count = %d, want 1", n)
	}
}

func TestSubmitRewardClaim_OnboardingNeedsTenUsernames(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	user := mkRcUser(t, db, "ada", true, models.RoleUser)

	// Only 3 usernames listed — rejected on the field rule before eligibility.
	c, w := spotCtx(t, http.MethodPost,
		`{"kind":"onboarding","name":"Ada","username":"ada","payoutMethod":"bank",
		  "payoutDetails":"0123456789 GTB","invitedUsernames":"a,b,c"}`, user)
	h.SubmitRewardClaim(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for too few usernames; body=%s", w.Code, w.Body.String())
	}
}

func TestGetMyRewardClaims_ReportsBothFormsWithEligibility(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	user := mkRcUser(t, db, "ada", true, models.RoleUser)

	c, w := spotCtx(t, http.MethodGet, ``, user)
	h.GetMyRewardClaims(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data map[string]struct {
			Claim       *models.RewardClaim               `json:"claim"`
			Eligibility services.RewardClaimEligibility   `json:"eligibility"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	signup, ok := resp.Data[models.RewardClaimKindSignup]
	if !ok {
		t.Fatal("missing signup entry")
	}
	if signup.Claim != nil {
		t.Fatal("no claim submitted yet — claim should be null")
	}
	if !signup.Eligibility.Eligible {
		t.Fatalf("verified user should be signup-eligible; reasons=%v", signup.Eligibility.Reasons)
	}
	onboarding := resp.Data[models.RewardClaimKindOnboarding]
	if onboarding.Eligibility.Eligible {
		t.Fatal("onboarding should be locked without referrals/transaction")
	}
	if onboarding.Eligibility.RequiredReferrals != models.MinOnboardingReferrals {
		t.Fatalf("requiredReferrals = %d, want %d",
			onboarding.Eligibility.RequiredReferrals, models.MinOnboardingReferrals)
	}
}

func TestListRewardClaimsAdmin_RequiresAdminAndFilters(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	regular := mkRcUser(t, db, "ada", true, models.RoleUser)
	admin := mkRcUser(t, db, "root", true, models.RoleAdmin)

	db.Create(&models.RewardClaim{
		UserID: regular.ID, Kind: models.RewardClaimKindSignup,
		Status: models.RewardClaimStatusPending, Name: "Ada", Username: "ada",
	})

	// Non-admin blocked.
	c, w := spotCtx(t, http.MethodGet, ``, regular)
	h.ListRewardClaimsAdmin(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin status = %d, want 403", w.Code)
	}

	// Admin, filtered to a kind with no rows.
	c2, w2 := spotCtx(t, http.MethodGet, ``, admin)
	c2.Request.URL.RawQuery = "kind=onboarding"
	h.ListRewardClaimsAdmin(c2)
	if w2.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200", w2.Code)
	}
	var resp struct {
		Data struct {
			Claims []models.RewardClaim `json:"claims"`
		} `json:"data"`
	}
	json.Unmarshal(w2.Body.Bytes(), &resp)
	if len(resp.Data.Claims) != 0 {
		t.Fatalf("onboarding filter should exclude the signup row; got %d", len(resp.Data.Claims))
	}
}

func TestGetDownlinesAdmin_ResolvesByUsername(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	admin := mkRcUser(t, db, "root", true, models.RoleAdmin)
	inviter := mkRcUser(t, db, "inviter", true, models.RoleUser)

	// Two direct referrals, one verified one not.
	for i, name := range []string{"down1", "down2"} {
		u := mkRcUser(t, db, name, i == 0, models.RoleUser)
		db.Model(&models.User{}).Where("id = ?", u.ID).Update("referred_by", inviter.ID)
	}

	c, w := spotCtx(t, http.MethodGet, ``, admin)
	c.Request.URL.RawQuery = "username=inviter"
	h.GetDownlinesAdmin(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Downlines      []map[string]any `json:"downlines"`
			TotalDownlines int              `json:"totalDownlines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Downlines) != 2 {
		t.Fatalf("downlines = %d, want 2", len(resp.Data.Downlines))
	}
	if resp.Data.TotalDownlines != 2 {
		t.Fatalf("totalDownlines = %d, want 2", resp.Data.TotalDownlines)
	}
}

func TestGetDownlinesAdmin_UnknownUser404(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	admin := mkRcUser(t, db, "root", true, models.RoleAdmin)

	c, w := spotCtx(t, http.MethodGet, ``, admin)
	c.Request.URL.RawQuery = "username=ghost"
	h.GetDownlinesAdmin(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestUpdateRewardClaimStatus_AdminApproves(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	regular := mkRcUser(t, db, "ada", true, models.RoleUser)
	admin := mkRcUser(t, db, "root", true, models.RoleAdmin)

	claim := models.RewardClaim{
		UserID: regular.ID, Kind: models.RewardClaimKindSignup,
		Status: models.RewardClaimStatusPending, Name: "Ada", Username: "ada",
	}
	db.Create(&claim)

	c, w := spotCtx(t, http.MethodPatch, `{"status":"approved","reviewNotes":"ok"}`, admin)
	c.Params = gin.Params{{Key: "id", Value: claim.ID.String()}}
	h.UpdateRewardClaimStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var row models.RewardClaim
	db.First(&row, "id = ?", claim.ID)
	if row.Status != models.RewardClaimStatusApproved {
		t.Fatalf("status = %q, want approved", row.Status)
	}
	if row.ReviewedBy == nil || *row.ReviewedBy != admin.ID {
		t.Fatalf("reviewedBy not recorded: %v", row.ReviewedBy)
	}
}

func TestCountInvited(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"a,b,c", 3},
		{"a, b, c,", 3},          // trailing comma must not inflate
		{"a\nb\nc", 3},           // newline separated
		{"  a  ,  b  ", 2},       // padded
		{",,,", 0},               // separators only
	}
	for _, tc := range cases {
		if got := countInvited(tc.in); got != tc.want {
			t.Fatalf("countInvited(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
