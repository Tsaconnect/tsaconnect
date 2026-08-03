package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
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
			didit_session_id TEXT,
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
		`CREATE TABLE tp_earnings (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, source_user_id TEXT NOT NULL,
			source_type TEXT NOT NULL, source_id TEXT NOT NULL, generation INTEGER NOT NULL,
			fee_amount_usd REAL NOT NULL DEFAULT 0, percentage REAL NOT NULL DEFAULT 0,
			tp_earned REAL NOT NULL, created_at DATETIME
		)`,
		`CREATE TABLE user_reward_states (
			user_id TEXT PRIMARY KEY, highest_awarded_level INTEGER DEFAULT 0,
			dirty INTEGER DEFAULT 0, last_processed_at DATETIME,
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

// No name/username: identity is derived from the token. inviterUsername is
// omitted because the test user has no referrer.
const validSignupBody = `{"kind":"signup","phoneNumber":"08030000000","phoneNetwork":"mtn"}`

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

func TestSubmitRewardClaim_RejectsBadNetworkAndPhone(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	user := mkRcUser(t, db, "ada", true, models.RoleUser)

	c, w := spotCtx(t, http.MethodPost,
		`{"kind":"signup","phoneNumber":"08030000000","phoneNetwork":"vodafone"}`, user)
	h.SubmitRewardClaim(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for unknown network", w.Code)
	}

	c2, w2 := spotCtx(t, http.MethodPost, `{"kind":"signup","phoneNetwork":"mtn"}`, user)
	h.SubmitRewardClaim(c2)
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for missing phone", w2.Code)
	}

	// Malformed number — this is the airtime destination.
	c3, w3 := spotCtx(t, http.MethodPost,
		`{"kind":"signup","phoneNumber":"123","phoneNetwork":"mtn"}`, user)
	h.SubmitRewardClaim(c3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for malformed phone", w3.Code)
	}
}

// The regression test for the identity-spoofing bug: a body claiming someone
// else's identity must not reach the stored row. The previous happy-path test
// used username "ada" for a user named "ada", so it passed either way.
func TestSubmitRewardClaim_IdentityIsDerivedNotSubmitted(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	victim := mkRcUser(t, db, "topreferrer", true, models.RoleUser)
	attacker := mkRcUser(t, db, "attacker", true, models.RoleUser)
	_ = victim

	c, w := spotCtx(t, http.MethodPost,
		`{"kind":"signup","name":"Top Referrer","username":"topreferrer",
		  "phoneNumber":"08030000000","phoneNetwork":"mtn"}`, attacker)
	h.SubmitRewardClaim(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var row models.RewardClaim
	db.First(&row)
	if row.Username != "attacker" {
		t.Fatalf("username = %q, want %q — submitted identity must be ignored", row.Username, "attacker")
	}
	if row.Name != "attacker" {
		t.Fatalf("name = %q, want the authenticated user's name", row.Name)
	}
	if row.UserID != attacker.ID {
		t.Fatalf("claim bound to the wrong user")
	}
}

func TestSubmitRewardClaim_RejectsMismatchedInviter(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	realInviter := mkRcUser(t, db, "realinviter", true, models.RoleUser)
	user := mkRcUser(t, db, "ada", true, models.RoleUser)
	db.Model(&models.User{}).Where("id = ?", user.ID).Update("referred_by", realInviter.ID)
	user.ReferredBy = &realInviter.ID

	c, w := spotCtx(t, http.MethodPost,
		`{"kind":"signup","phoneNumber":"08030000000","phoneNetwork":"mtn","inviterUsername":"someoneelse"}`, user)
	h.SubmitRewardClaim(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a mismatched inviter; body=%s", w.Code, w.Body.String())
	}

	// The true inviter is accepted.
	c2, w2 := spotCtx(t, http.MethodPost,
		`{"kind":"signup","phoneNumber":"08030000000","phoneNetwork":"mtn","inviterUsername":"realinviter"}`, user)
	h.SubmitRewardClaim(c2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 for the real inviter; body=%s", w2.Code, w2.Body.String())
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
			Claim       *models.RewardClaim             `json:"claim"`
			Eligibility services.RewardClaimEligibility `json:"eligibility"`
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
			DownlinesToGen int              `json:"downlinesToGen"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data.Downlines) != 2 {
		t.Fatalf("downlines = %d, want 2", len(resp.Data.Downlines))
	}
	if resp.Data.DownlinesToGen != 2 {
		t.Fatalf("downlinesToGen = %d, want 2", resp.Data.DownlinesToGen)
	}
	// The explorer's whole purpose is showing WHO is verified — assert the
	// distinction actually surfaces rather than just the row count.
	verified := 0
	for _, d := range resp.Data.Downlines {
		if active, ok := d["active"].(bool); ok && active {
			verified++
		}
	}
	if verified != 2 {
		t.Fatalf("active downlines = %d, want 2 (both accounts are active)", verified)
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
		{"a, b, c,", 3},    // trailing comma must not inflate
		{"a\nb\nc", 3},     // newline separated
		{"  a  ,  b  ", 2}, // padded
		{",,,", 0},         // separators only
		// Distinct, case-insensitively: repeats must not satisfy the minimum.
		{"a,a,a,a,a", 1},
		{"a,A,  a ", 1},
		// A name containing a space is ONE entry, not two.
		{"John Doe,Jane Roe", 2},
	}
	for _, tc := range cases {
		if got := countInvited(tc.in); got != tc.want {
			t.Fatalf("countInvited(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestRewardClaimStateMachine(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{models.RewardClaimStatusPending, models.RewardClaimStatusApproved, true},
		{models.RewardClaimStatusPending, models.RewardClaimStatusRejected, true},
		{models.RewardClaimStatusApproved, models.RewardClaimStatusPaid, true},
		{models.RewardClaimStatusApproved, models.RewardClaimStatusRejected, true},
		// Terminal states — the double-payout guards.
		{models.RewardClaimStatusPaid, models.RewardClaimStatusPending, false},
		{models.RewardClaimStatusPaid, models.RewardClaimStatusApproved, false},
		{models.RewardClaimStatusRejected, models.RewardClaimStatusApproved, false},
		// No self-transitions: re-approving would re-fire the award.
		{models.RewardClaimStatusApproved, models.RewardClaimStatusApproved, false},
		{models.RewardClaimStatusPending, models.RewardClaimStatusPending, false},
	}
	for _, tc := range cases {
		if got := canTransition(tc.from, tc.to); got != tc.want {
			t.Fatalf("canTransition(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestUpdateRewardClaimStatus_RejectsIllegalTransition(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	regular := mkRcUser(t, db, "ada", true, models.RoleUser)
	admin := mkRcUser(t, db, "root", true, models.RoleAdmin)

	claim := models.RewardClaim{
		UserID: regular.ID, Kind: models.RewardClaimKindSignup,
		Status: models.RewardClaimStatusPaid, Name: "Ada", Username: "ada",
	}
	db.Create(&claim)

	c, w := spotCtx(t, http.MethodPatch, `{"status":"pending"}`, admin)
	c.Params = gin.Params{{Key: "id", Value: claim.ID.String()}}
	h.UpdateRewardClaimStatus(c)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 reverting a paid claim; body=%s", w.Code, w.Body.String())
	}
	var row models.RewardClaim
	db.First(&row, "id = ?", claim.ID)
	if row.Status != models.RewardClaimStatusPaid {
		t.Fatalf("status = %q, want it unchanged at paid", row.Status)
	}
}

func TestUpdateRewardClaimStatus_PreservesReviewNotesWhenOmitted(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	regular := mkRcUser(t, db, "ada", true, models.RoleUser)
	admin := mkRcUser(t, db, "root", true, models.RoleAdmin)

	claim := models.RewardClaim{
		UserID: regular.ID, Kind: models.RewardClaimKindSignup,
		Status: models.RewardClaimStatusPending, Name: "Ada", Username: "ada",
		ReviewNotes: "checked the referrals",
	}
	db.Create(&claim)

	// Body omits reviewNotes entirely — the earlier note must survive.
	c, w := spotCtx(t, http.MethodPatch, `{"status":"approved"}`, admin)
	c.Params = gin.Params{{Key: "id", Value: claim.ID.String()}}
	h.UpdateRewardClaimStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var row models.RewardClaim
	db.First(&row, "id = ?", claim.ID)
	if row.ReviewNotes != "checked the referrals" {
		t.Fatalf("reviewNotes = %q, want the prior note preserved", row.ReviewNotes)
	}
}

func TestUpdateRewardClaimStatus_ApprovalAwardsTPOnce(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	regular := mkRcUser(t, db, "ada", true, models.RoleUser)
	admin := mkRcUser(t, db, "root", true, models.RoleAdmin)

	claim := models.RewardClaim{
		UserID: regular.ID, Kind: models.RewardClaimKindSignup,
		Status: models.RewardClaimStatusPending, Name: "Ada", Username: "ada",
	}
	db.Create(&claim)

	c, w := spotCtx(t, http.MethodPatch, `{"status":"approved"}`, admin)
	c.Params = gin.Params{{Key: "id", Value: claim.ID.String()}}
	h.UpdateRewardClaimStatus(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var user models.User
	db.First(&user, "id = ?", regular.ID)
	if user.TPBalance != services.SignupRewardTP {
		t.Fatalf("tp_balance = %v, want %v", user.TPBalance, services.SignupRewardTP)
	}
	var earnings int64
	db.Model(&models.TPEarning{}).Where("source_id = ?", claim.ID).Count(&earnings)
	if earnings != 1 {
		t.Fatalf("tp_earnings rows = %d, want 1", earnings)
	}

	// Idempotent: awarding again (e.g. a retry) must not double-credit.
	if err := h.Service.AwardSignupTP(c.Request.Context(), &claim); err != nil {
		t.Fatalf("second award: %v", err)
	}
	db.First(&user, "id = ?", regular.ID)
	if user.TPBalance != services.SignupRewardTP {
		t.Fatalf("tp_balance = %v after retry, want it unchanged", user.TPBalance)
	}
}

func TestSubmitRewardClaim_OnboardingHappyPath(t *testing.T) {
	db := setupRewardClaimTestDB(t)
	h := newRewardClaimHandler(t, db)
	user := mkRcUser(t, db, "ada", true, models.RoleUser)
	mkCompletedTxFor(t, db, user.ID)
	// Ten KYC-verified direct referrals satisfy the gate.
	for i := 0; i < models.MinOnboardingReferrals; i++ {
		r := mkRcUser(t, db, "ref"+string(rune('a'+i)), true, models.RoleUser)
		db.Model(&models.User{}).Where("id = ?", r.ID).Update("referred_by", user.ID)
	}

	names := make([]string, 0, models.MinOnboardingReferrals)
	for i := 0; i < models.MinOnboardingReferrals; i++ {
		names = append(names, "ref"+string(rune('a'+i)))
	}
	body := `{"kind":"onboarding","payoutMethod":"bank","payoutDetails":"0123456789 GTB",
	          "invitedUsernames":"` + strings.Join(names, ",") + `"}`

	c, w := spotCtx(t, http.MethodPost, body, user)
	h.SubmitRewardClaim(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", w.Code, w.Body.String())
	}
	var row models.RewardClaim
	db.First(&row, "kind = ?", models.RewardClaimKindOnboarding)
	if row.PayoutMethod != "bank" || row.PayoutDetails != "0123456789 GTB" {
		t.Fatalf("payout fields not persisted: %+v", row)
	}
	if countInvited(row.InvitedUsernames) != models.MinOnboardingReferrals {
		t.Fatalf("stored %d usernames, want %d", countInvited(row.InvitedUsernames), models.MinOnboardingReferrals)
	}
}

func mkCompletedTxFor(t *testing.T, db *gorm.DB, userID uuid.UUID) {
	t.Helper()
	if err := db.Exec(
		`INSERT INTO transactions (id, user_id, type, status, amount) VALUES (?, ?, 'deposit', ?, 1)`,
		uuid.New().String(), userID.String(), models.TransactionStatusCompleted,
	).Error; err != nil {
		t.Fatalf("insert tx: %v", err)
	}
}
