package services

import (
	"context"
	"errors"
	"math"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// fakeOTC implements otcBuyPricePerToken for the MCGPPriceFromOTC adapter test.
type fakeOTC struct {
	price *big.Int
	err   error
}

func (f *fakeOTC) GetBuyPricePerToken() (*big.Int, error) { return f.price, f.err }

func big0() *big.Int   { return big.NewInt(0) }
func big1e6() *big.Int { return big.NewInt(1_000_000) }

// setupRewardTestDB opens an in-memory SQLite DB with the SP3/SP4 tables (so the
// global-share settler runs) PLUS the SP5 payout tables, via raw DDL (avoiding
// PostgreSQL gen_random_uuid() defaults). All IDs are set in Go.
func setupRewardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	stmts := []string{
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
		`CREATE TABLE tp_earnings (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			source_user_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			fee_amount_usd REAL NOT NULL,
			percentage REAL NOT NULL,
			tp_earned REAL NOT NULL,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE cashback_earnings (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			source_user_id TEXT NOT NULL,
			source_type TEXT NOT NULL,
			source_id TEXT NOT NULL,
			amount_usd REAL NOT NULL,
			tx_hash TEXT,
			created_at DATETIME NOT NULL
		)`,
		`CREATE TABLE global_tp_pool (
			singleton INTEGER PRIMARY KEY,
			accrued_usd REAL NOT NULL DEFAULT 0,
			accrued_tp REAL NOT NULL DEFAULT 0,
			per_user_index_scaled INTEGER NOT NULL DEFAULT 0,
			indexed_tp REAL NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE reward_entries (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			source_level INTEGER NOT NULL DEFAULT 0,
			source_id TEXT NOT NULL DEFAULT '00000000-0000-0000-0000-000000000000',
			reward_usd REAL NOT NULL,
			mcgp_amount REAL NOT NULL,
			rate_usd_per_mcgp REAL NOT NULL,
			created_at DATETIME NOT NULL,
			UNIQUE(user_id, kind, source_level, source_id)
		)`,
		`CREATE TABLE reward_claimables (
			user_id TEXT PRIMARY KEY,
			claimable_mcgp REAL NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE user_reward_states (
			user_id TEXT PRIMARY KEY,
			highest_awarded_level INTEGER NOT NULL DEFAULT 0,
			dirty INTEGER NOT NULL DEFAULT 0,
			last_processed_at DATETIME,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE prize_dues (
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
			t.Fatalf("ddl: %v", err)
		}
	}
	return db
}

// mkRewardUser inserts a user with a given tp_balance.
func mkRewardUser(t *testing.T, db *gorm.DB, tp float64) models.User {
	t.Helper()
	u := models.User{
		ID:            uuid.New(),
		Name:          "u",
		Username:      "u" + uuid.New().String()[:8],
		Email:         uuid.New().String()[:8] + "@test.com",
		Password:      "x",
		AccountStatus: models.AccountStatusActive,
		TPBalance:     tp,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// addCashback inserts a CashbackEarning row (USD).
func addCashback(t *testing.T, db *gorm.DB, userID uuid.UUID, usd float64) models.CashbackEarning {
	t.Helper()
	cb := models.CashbackEarning{
		ID:           uuid.New(),
		UserID:       userID,
		SourceUserID: userID,
		SourceType:   "checkout",
		SourceID:     uuid.New(),
		AmountUSD:    usd,
		CreatedAt:    time.Now(),
	}
	if err := db.Create(&cb).Error; err != nil {
		t.Fatalf("create cashback: %v", err)
	}
	return cb
}

// fixedPrice returns a price function that always reports usd/MCGP.
func fixedPrice(usd float64) mcgpPriceFunc {
	return func(_ context.Context) (float64, error) { return usd, nil }
}

// unavailablePrice always reports the price as unavailable.
func unavailablePrice() mcgpPriceFunc {
	return func(_ context.Context) (float64, error) { return 0, ErrMCGPPriceUnavailable }
}

// noopSettler is a globalShareSettler that credits nothing (tp_balance already set).
type noopSettler struct{ calls int }

func (n *noopSettler) SettleUserGlobalShare(_ context.Context, _ uuid.UUID) (float64, error) {
	n.calls++
	return 0, nil
}

func claimable(t *testing.T, db *gorm.DB, userID uuid.UUID) float64 {
	t.Helper()
	var row models.RewardClaimable
	err := db.First(&row, "user_id = ?", userID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0
	}
	if err != nil {
		t.Fatalf("load claimable: %v", err)
	}
	return row.ClaimableMCGP
}

func countRewardEntries(t *testing.T, db *gorm.DB, userID uuid.UUID) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.RewardEntry{}).Where("user_id = ?", userID).Count(&n).Error; err != nil {
		t.Fatalf("count entries: %v", err)
	}
	return n
}

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// --- LevelFor boundaries ---

func TestLevelFor_Boundaries(t *testing.T) {
	cases := []struct {
		tp   float64
		want int
	}{
		{0, 0},
		{0.999, 0},
		{1, 1},
		{9.999, 1},
		{10, 2},
		{99, 2},
		{100, 3},
		{1e5, 6},   // 10^5 = level 6
		{1e14, 15}, // top level
		{1e15, 15}, // capped at 15
		{1e20, 15},
	}
	for _, c := range cases {
		if got := models.LevelFor(c.tp); got != c.want {
			t.Fatalf("LevelFor(%g) = %d, want %d", c.tp, got, c.want)
		}
	}
}

func TestRewardUSDForLevel(t *testing.T) {
	// 10^(n-1)/100
	cases := map[int]float64{1: 0.01, 2: 0.10, 3: 1.0, 6: 1000, 15: 1e12}
	for n, want := range cases {
		if got := models.RewardUSDForLevel(n); !approxEq(got, want) {
			t.Fatalf("RewardUSDForLevel(%d) = %g, want %g", n, got, want)
		}
	}
	if models.RewardUSDForLevel(0) != 0 || models.RewardUSDForLevel(16) != 0 {
		t.Fatalf("out-of-range reward must be 0")
	}
}

func TestPhysicalPrize_FloorAtL6(t *testing.T) {
	for n := 1; n <= 5; n++ {
		if models.LevelHasPhysicalPrize(n) {
			t.Fatalf("level %d must NOT have a physical prize", n)
		}
	}
	for n := 6; n <= 15; n++ {
		if !models.LevelHasPhysicalPrize(n) {
			t.Fatalf("level %d must have a physical prize", n)
		}
	}
}

// --- Crossing several levels at once awards the SUM (cumulative), once ---

func TestProcessUserRewards_CumulativeMultiLevel(t *testing.T) {
	db := setupRewardTestDB(t)
	// tp = 100 => level 3, crossing L1+L2+L3 from scratch.
	u := mkRewardUser(t, db, 100)
	svc := NewRewardService(db, nil, &noopSettler{}, fixedPrice(2.0)) // $2 per MCGP

	res, err := svc.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.CurrentLevel != 3 {
		t.Fatalf("current level = %d, want 3", res.CurrentLevel)
	}
	if res.LevelsAwarded != 3 {
		t.Fatalf("levels awarded = %d, want 3 (L1+L2+L3)", res.LevelsAwarded)
	}
	// rewardUSD = 0.01 + 0.10 + 1.00 = 1.11 ; at $2/MCGP => 0.555 MCGP
	wantUSD := 0.01 + 0.10 + 1.00
	wantMCGP := wantUSD / 2.0
	if !approxEq(claimable(t, db, u.ID), wantMCGP) {
		t.Fatalf("claimable = %g, want %g", claimable(t, db, u.ID), wantMCGP)
	}
	if got := countRewardEntries(t, db, u.ID); got != 3 {
		t.Fatalf("reward entries = %d, want 3", got)
	}
}

// --- Idempotent: re-run awards nothing new ---

func TestProcessUserRewards_Idempotent(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 100) // level 3
	svc := NewRewardService(db, nil, &noopSettler{}, fixedPrice(1.0))

	if _, err := svc.ProcessUserRewards(context.Background(), u.ID); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first := claimable(t, db, u.ID)

	res, err := svc.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.LevelsAwarded != 0 {
		t.Fatalf("re-run awarded %d levels, want 0", res.LevelsAwarded)
	}
	if !approxEq(claimable(t, db, u.ID), first) {
		t.Fatalf("claimable changed on re-run: %g -> %g", first, claimable(t, db, u.ID))
	}
	if got := countRewardEntries(t, db, u.ID); got != 3 {
		t.Fatalf("entries after re-run = %d, want 3", got)
	}
}

// Incremental crossing: a later TP increase awards only the NEW levels.
func TestProcessUserRewards_IncrementalCrossing(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 5) // level 1
	svc := NewRewardService(db, nil, &noopSettler{}, fixedPrice(1.0))

	res, _ := svc.ProcessUserRewards(context.Background(), u.ID)
	if res.LevelsAwarded != 1 || res.CurrentLevel != 1 {
		t.Fatalf("first: awarded=%d level=%d, want 1/1", res.LevelsAwarded, res.CurrentLevel)
	}

	// Bump to level 3.
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).Update("tp_balance", 100).Error; err != nil {
		t.Fatalf("bump: %v", err)
	}
	res, _ = svc.ProcessUserRewards(context.Background(), u.ID)
	if res.LevelsAwarded != 2 || res.CurrentLevel != 3 {
		t.Fatalf("second: awarded=%d level=%d, want 2 (L2+L3) /3", res.LevelsAwarded, res.CurrentLevel)
	}
	if got := countRewardEntries(t, db, u.ID); got != 3 {
		t.Fatalf("total entries = %d, want 3", got)
	}
}

// --- Cashback USD->MCGP once per row ---

func TestProcessUserRewards_CashbackConvertedOncePerRow(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 0) // no level rewards
	addCashback(t, db, u.ID, 4.0)
	addCashback(t, db, u.ID, 6.0)
	svc := NewRewardService(db, nil, &noopSettler{}, fixedPrice(2.0)) // $2 per MCGP

	res, err := svc.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.CashbacksConverted != 2 {
		t.Fatalf("converted = %d, want 2", res.CashbacksConverted)
	}
	// (4 + 6) USD / $2 = 5 MCGP
	if !approxEq(claimable(t, db, u.ID), 5.0) {
		t.Fatalf("claimable = %g, want 5", claimable(t, db, u.ID))
	}

	// Re-run: nothing new.
	res, _ = svc.ProcessUserRewards(context.Background(), u.ID)
	if res.CashbacksConverted != 0 {
		t.Fatalf("re-run converted = %d, want 0", res.CashbacksConverted)
	}
	if !approxEq(claimable(t, db, u.ID), 5.0) {
		t.Fatalf("claimable changed: %g", claimable(t, db, u.ID))
	}

	// A NEW cashback row converts on the next run.
	addCashback(t, db, u.ID, 2.0)
	res, _ = svc.ProcessUserRewards(context.Background(), u.ID)
	if res.CashbacksConverted != 1 {
		t.Fatalf("new cashback converted = %d, want 1", res.CashbacksConverted)
	}
	if !approxEq(claimable(t, db, u.ID), 6.0) { // +2/2 = +1
		t.Fatalf("claimable = %g, want 6", claimable(t, db, u.ID))
	}
}

// --- OTC-unavailable DEFERS (no entry, retried later, no zero-rate) ---

func TestProcessUserRewards_PriceUnavailableDefers(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 100) // level 3
	addCashback(t, db, u.ID, 10.0)
	svc := NewRewardService(db, nil, &noopSettler{}, unavailablePrice())

	res, err := svc.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("process must NOT error on price-unavailable: %v", err)
	}
	if !res.Deferred {
		t.Fatalf("expected Deferred=true")
	}
	if res.LevelsAwarded != 0 || res.CashbacksConverted != 0 {
		t.Fatalf("nothing should convert: awarded=%d cashback=%d", res.LevelsAwarded, res.CashbacksConverted)
	}
	if got := countRewardEntries(t, db, u.ID); got != 0 {
		t.Fatalf("NO reward entries on defer, got %d (zero-rate lock!)", got)
	}
	if claimable(t, db, u.ID) != 0 {
		t.Fatalf("claimable must be 0 on defer")
	}
	// Checkpoint NOT advanced; user left dirty.
	var st models.UserRewardState
	if err := db.First(&st, "user_id = ?", u.ID).Error; err != nil {
		t.Fatalf("load state: %v", err)
	}
	if st.HighestAwardedLevel != 0 {
		t.Fatalf("checkpoint advanced on defer: %d", st.HighestAwardedLevel)
	}
	if !st.Dirty {
		t.Fatalf("deferred user must stay dirty for retry")
	}

	// Price comes back: the SAME run awards everything at the live rate, no zero
	// rate was ever locked.
	svc2 := NewRewardService(db, nil, &noopSettler{}, fixedPrice(1.0))
	res, err = svc2.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.LevelsAwarded != 3 || res.CashbacksConverted != 1 {
		t.Fatalf("retry awarded=%d cashback=%d, want 3/1", res.LevelsAwarded, res.CashbacksConverted)
	}
	// All locked rates are the live (1.0) rate — never 0.
	var entries []models.RewardEntry
	if err := db.Where("user_id = ?", u.ID).Find(&entries).Error; err != nil {
		t.Fatalf("load entries: %v", err)
	}
	for _, e := range entries {
		if e.RateUSDPerMCGP <= 0 {
			t.Fatalf("entry locked a non-positive rate: %+v", e)
		}
	}
}

// --- claimable_mcgp == Σ(reward+cashback MCGP entries) (conservation) ---

func TestProcessUserRewards_ClaimableConservation(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 1000) // level 4
	addCashback(t, db, u.ID, 3.5)
	addCashback(t, db, u.ID, 1.25)
	svc := NewRewardService(db, nil, &noopSettler{}, fixedPrice(0.5)) // $0.50 per MCGP

	if _, err := svc.ProcessUserRewards(context.Background(), u.ID); err != nil {
		t.Fatalf("process: %v", err)
	}

	var entries []models.RewardEntry
	if err := db.Where("user_id = ?", u.ID).Find(&entries).Error; err != nil {
		t.Fatalf("load entries: %v", err)
	}
	var sum float64
	for _, e := range entries {
		sum += e.MCGPAmount
	}
	if !approxEq(sum, claimable(t, db, u.ID)) {
		t.Fatalf("conservation broken: Σentries=%g claimable=%g", sum, claimable(t, db, u.ID))
	}
}

// --- L6 crossing records prize_due + no auto-pay ---

func TestProcessUserRewards_L6PrizeDueNoAutoPay(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 1e6) // 10^6 => level 7 (crosses L6 and L7)
	svc := NewRewardService(db, nil, &noopSettler{}, fixedPrice(1.0))

	res, err := svc.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if res.CurrentLevel != 7 {
		t.Fatalf("level = %d, want 7", res.CurrentLevel)
	}
	// L6 and L7 are physical-prize levels.
	if res.PrizesRecorded != 2 {
		t.Fatalf("prizes recorded = %d, want 2 (L6+L7)", res.PrizesRecorded)
	}
	var prizes []models.PrizeDue
	if err := db.Where("user_id = ?", u.ID).Order("level").Find(&prizes).Error; err != nil {
		t.Fatalf("load prizes: %v", err)
	}
	if len(prizes) != 2 {
		t.Fatalf("prize_due rows = %d, want 2", len(prizes))
	}
	for _, p := range prizes {
		if p.Status != models.PrizeDueStatusPending {
			t.Fatalf("prize must be pending (manual fulfilment, NOT auto-paid): %+v", p)
		}
		if p.Prize == "" {
			t.Fatalf("prize label empty for level %d", p.Level)
		}
	}
	// Re-run does not duplicate prize rows.
	if _, err := svc.ProcessUserRewards(context.Background(), u.ID); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	var n int64
	db.Model(&models.PrizeDue{}).Where("user_id = ?", u.ID).Count(&n)
	if n != 2 {
		t.Fatalf("prize rows after re-run = %d, want 2 (idempotent)", n)
	}
}

// --- level-up event emitted ---

func TestProcessUserRewards_LevelUpEventEmitted(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 10) // level 2 (L1 + L2)
	bus := events.NewBus()
	defer bus.Stop()

	var (
		mu        sync.Mutex
		levelUps  []events.Event
		prizeEmit []events.Event
	)
	done := make(chan struct{}, 4)
	bus.Subscribe(events.TPLevelUp, func(ev events.Event) {
		mu.Lock()
		levelUps = append(levelUps, ev)
		mu.Unlock()
		done <- struct{}{}
	})
	bus.Subscribe(events.TPPrizeDue, func(ev events.Event) {
		mu.Lock()
		prizeEmit = append(prizeEmit, ev)
		mu.Unlock()
		done <- struct{}{}
	})

	svc := NewRewardService(db, bus, &noopSettler{}, fixedPrice(1.0))
	if _, err := svc.ProcessUserRewards(context.Background(), u.ID); err != nil {
		t.Fatalf("process: %v", err)
	}

	// Wait for the 2 async level-up events (L1, L2); no prize events (< L6).
	waitFor(t, done, 2)

	mu.Lock()
	defer mu.Unlock()
	if len(levelUps) != 2 {
		t.Fatalf("level-up events = %d, want 2", len(levelUps))
	}
	if len(prizeEmit) != 0 {
		t.Fatalf("prize events = %d, want 0 (below L6)", len(prizeEmit))
	}
	// Event payload carries the level + locked MCGP.
	seen := map[int]bool{}
	for _, ev := range levelUps {
		lvl, _ := ev.Data["level"].(int)
		seen[lvl] = true
		if ev.UserID != u.ID {
			t.Fatalf("event user = %s, want %s", ev.UserID, u.ID)
		}
		if mcgp, _ := ev.Data["mcgpAmount"].(float64); mcgp <= 0 {
			t.Fatalf("event missing mcgpAmount: %+v", ev.Data)
		}
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("expected level-up events for L1 and L2, got %v", seen)
	}
}

// --- settler is invoked FIRST so tp_balance is current before level compute ---

func TestProcessUserRewards_SettlesGlobalShareFirst(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 0)
	// A settler that credits 100 TP (pushing the user to level 3) when called.
	s := &creditingSettler{db: db, userID: u.ID, creditTP: 100}
	svc := NewRewardService(db, nil, s, fixedPrice(1.0))

	res, err := svc.ProcessUserRewards(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("settler called %d times, want 1", s.calls)
	}
	if res.CurrentLevel != 3 {
		t.Fatalf("level = %d, want 3 (settler credited 100 TP first)", res.CurrentLevel)
	}
	if res.LevelsAwarded != 3 {
		t.Fatalf("awarded = %d, want 3", res.LevelsAwarded)
	}
}

// creditingSettler simulates SP4 crediting TP into tp_balance when invoked.
type creditingSettler struct {
	db       *gorm.DB
	userID   uuid.UUID
	creditTP float64
	calls    int
}

func (c *creditingSettler) SettleUserGlobalShare(_ context.Context, _ uuid.UUID) (float64, error) {
	c.calls++
	if err := c.db.Model(&models.User{}).Where("id = ?", c.userID).
		Update("tp_balance", c.creditTP).Error; err != nil {
		return 0, err
	}
	return c.creditTP, nil
}

// --- MarkUserDirty / DirtyUserIDs / ProcessDirtyUsers ---

func TestRewardService_DirtyTracking(t *testing.T) {
	db := setupRewardTestDB(t)
	u1 := mkRewardUser(t, db, 100)
	u2 := mkRewardUser(t, db, 1)
	svc := NewRewardService(db, nil, &noopSettler{}, fixedPrice(1.0))
	ctx := context.Background()

	if err := svc.MarkUserDirty(ctx, u1.ID); err != nil {
		t.Fatalf("mark u1: %v", err)
	}
	if err := svc.MarkUserDirty(ctx, u2.ID); err != nil {
		t.Fatalf("mark u2: %v", err)
	}
	// Idempotent re-mark.
	if err := svc.MarkUserDirty(ctx, u1.ID); err != nil {
		t.Fatalf("re-mark u1: %v", err)
	}

	ids, err := svc.DirtyUserIDs(ctx, 10)
	if err != nil {
		t.Fatalf("dirty ids: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("dirty ids = %d, want 2", len(ids))
	}

	processed, failed, err := svc.ProcessDirtyUsers(ctx, 10)
	if err != nil {
		t.Fatalf("process dirty: %v", err)
	}
	if processed != 2 || failed != 0 {
		t.Fatalf("processed=%d failed=%d, want 2/0", processed, failed)
	}
	// Both processed successfully (price available) => dirty cleared.
	ids, _ = svc.DirtyUserIDs(ctx, 10)
	if len(ids) != 0 {
		t.Fatalf("dirty users after process = %d, want 0", len(ids))
	}
	// Their rewards landed.
	if claimable(t, db, u1.ID) <= 0 || claimable(t, db, u2.ID) <= 0 {
		t.Fatalf("expected claimable for both processed users")
	}
}

// A price-unavailable batch run leaves the user dirty for retry.
func TestProcessDirtyUsers_DeferredStaysDirty(t *testing.T) {
	db := setupRewardTestDB(t)
	u := mkRewardUser(t, db, 100)
	svc := NewRewardService(db, nil, &noopSettler{}, unavailablePrice())
	ctx := context.Background()
	if err := svc.MarkUserDirty(ctx, u.ID); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if _, _, err := svc.ProcessDirtyUsers(ctx, 10); err != nil {
		t.Fatalf("process: %v", err)
	}
	ids, _ := svc.DirtyUserIDs(ctx, 10)
	if len(ids) != 1 {
		t.Fatalf("deferred user must stay dirty, dirty=%d", len(ids))
	}
}

// waitFor blocks until n signals arrive on done or a timeout elapses.
func waitFor(t *testing.T, done <-chan struct{}, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("timed out waiting for %d events (got %d)", n, i)
		}
	}
}

// MCGPPriceFromOTC adapter: nil OTC / error / zero price all surface as
// unavailable (defer), never a zero rate.
func TestMCGPPriceFromOTC_UnavailablePaths(t *testing.T) {
	// nil otc
	if _, err := MCGPPriceFromOTC(nil)(context.Background()); !errors.Is(err, ErrMCGPPriceUnavailable) {
		t.Fatalf("nil otc must be unavailable, got %v", err)
	}
	// error
	if _, err := MCGPPriceFromOTC(&fakeOTC{err: errors.New("revert")})(context.Background()); !errors.Is(err, ErrMCGPPriceUnavailable) {
		t.Fatalf("otc error must be unavailable, got %v", err)
	}
	// zero price
	if _, err := MCGPPriceFromOTC(&fakeOTC{price: big0()})(context.Background()); !errors.Is(err, ErrMCGPPriceUnavailable) {
		t.Fatalf("zero price must be unavailable, got %v", err)
	}
	// valid: 1_000_000 wei (6-decimal USDC) => $1.00 per MCGP
	usd, err := MCGPPriceFromOTC(&fakeOTC{price: big1e6()})(context.Background())
	if err != nil {
		t.Fatalf("valid price errored: %v", err)
	}
	if !approxEq(usd, 1.0) {
		t.Fatalf("price = %g, want 1.0", usd)
	}
}
