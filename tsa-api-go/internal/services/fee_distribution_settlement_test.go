package services

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// --- SP4 global-pool settlement helpers ---

// seedGlobalPool sets the singleton pool's accrued TP directly (bypassing the
// SP3 accrual path) so the settlement math can be tested in isolation. accruedTP
// is the total TP ever routed into the pool.
func seedGlobalPool(t *testing.T, db *gorm.DB, accruedTP float64) {
	t.Helper()
	pool := models.GlobalTPPool{
		Singleton:  models.GlobalTPPoolSingletonID,
		AccruedUSD: accruedTP, // 1:1 at tpPerUSD=1; USD value not under test here
		AccruedTP:  accruedTP,
	}
	if err := db.Save(&pool).Error; err != nil {
		t.Fatalf("seed global pool: %v", err)
	}
}

// addGlobalAccrual increments the pool's accrued TP (simulates more SP3 accrual
// between epochs).
func addGlobalAccrual(t *testing.T, db *gorm.DB, deltaTP float64) {
	t.Helper()
	if err := db.Model(&models.GlobalTPPool{}).
		Where("singleton = ?", models.GlobalTPPoolSingletonID).
		Updates(map[string]interface{}{
			"accrued_tp":  gorm.Expr("accrued_tp + ?", deltaTP),
			"accrued_usd": gorm.Expr("accrued_usd + ?", deltaTP),
		}).Error; err != nil {
		t.Fatalf("add global accrual: %v", err)
	}
}

// mkFeeUserAtIndex simulates a SIGNUP after settlement has begun: it creates a
// user whose last_settled_global_index is seeded to the CURRENT cumulative index
// (what the auth_handler does), so the user shares only future epochs.
func mkFeeUserAtIndex(t *testing.T, db *gorm.DB, svc *FeeDistributionService, username string) models.User {
	t.Helper()
	idx, err := svc.CurrentGlobalIndex(context.Background())
	if err != nil {
		t.Fatalf("current index: %v", err)
	}
	u := mkFeeUser(t, db, username, nil, models.AccountStatusActive)
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).
		Update("last_settled_global_index", idx).Error; err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}
	u.LastSettledGlobalIndex = &idx
	return u
}

func globalIndex(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var pool models.GlobalTPPool
	if err := db.First(&pool, "singleton = ?", models.GlobalTPPoolSingletonID).Error; err != nil {
		t.Fatalf("load pool: %v", err)
	}
	return pool.PerUserIndexScaled
}

func countEpochs(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.GlobalTPEpoch{}).Count(&n).Error; err != nil {
		t.Fatalf("count epochs: %v", err)
	}
	return n
}

// --- Tests ---

// Index increments by floor(delta·scale / userCount) per epoch; an audit row is
// written; pool.IndexedTP advances by the folded amount.
func TestSettleGlobalPoolEpoch_IndexIncrement(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	// 4 registered users.
	for i := 0; i < 4; i++ {
		mkFeeUser(t, db, "u"+uuidShort(), nil, models.AccountStatusActive)
	}
	seedGlobalPool(t, db, 10.0) // $10 TP in the pool

	res, err := svc.SettleGlobalPoolEpoch(context.Background())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !res.Settled {
		t.Fatalf("expected settled epoch")
	}
	wantPerUser := int64(10.0 * float64(models.GlobalTPIndexScale) / 4) // 2.5 TP each
	if res.PerUserScaled != wantPerUser {
		t.Fatalf("perUserScaled = %d, want %d", res.PerUserScaled, wantPerUser)
	}
	if got := globalIndex(t, db); got != wantPerUser {
		t.Fatalf("index = %d, want %d", got, wantPerUser)
	}
	if res.UserCount != 4 {
		t.Fatalf("userCount = %d, want 4", res.UserCount)
	}
	if n := countEpochs(t, db); n != 1 {
		t.Fatalf("epoch rows = %d, want 1", n)
	}
}

// Two epochs accumulate: the index is the SUM of both epochs' per-user shares,
// and a user present across both gets BOTH shares on a single settle.
func TestSettleGlobalPoolEpoch_TwoEpochsAccumulate(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	users := make([]models.User, 5)
	for i := 0; i < 5; i++ {
		users[i] = mkFeeUser(t, db, "u"+uuidShort(), nil, models.AccountStatusActive)
	}

	// Epoch 1: $10 / 5 = 2 TP each.
	seedGlobalPool(t, db, 10.0)
	if _, err := svc.SettleGlobalPoolEpoch(context.Background()); err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	scale := float64(models.GlobalTPIndexScale)
	want1 := int64(10.0 * scale / 5) // per-user epoch 1
	if got := globalIndex(t, db); got != want1 {
		t.Fatalf("index after epoch1 = %d, want %d", got, want1)
	}

	// Epoch 2: +$5 accrued → 1 TP each.
	addGlobalAccrual(t, db, 5.0)
	if _, err := svc.SettleGlobalPoolEpoch(context.Background()); err != nil {
		t.Fatalf("epoch2: %v", err)
	}
	want2 := int64(5.0 * scale / 5)
	if got := globalIndex(t, db); got != want1+want2 {
		t.Fatalf("index after epoch2 = %d, want %d", got, want1+want2)
	}
	if n := countEpochs(t, db); n != 2 {
		t.Fatalf("epoch rows = %d, want 2", n)
	}

	// A user present across BOTH epochs is owed both shares (2 + 1 = 3 TP).
	credited, err := svc.SettleUserGlobalShare(context.Background(), users[0].ID)
	if err != nil {
		t.Fatalf("settle user: %v", err)
	}
	wantTP := 2.0 + 1.0
	if !approx(credited, wantTP) {
		t.Fatalf("credited = %v, want %v", credited, wantTP)
	}
	if got := tpBalance(t, db, users[0].ID); !approx(got, wantTP) {
		t.Fatalf("tp_balance = %v, want %v", got, wantTP)
	}
}

// NEW-USER FAIRNESS: a user who joins after epoch 1 (checkpoint initialized at
// the current index) gets ONLY epoch-2's share — never epoch 1's.
func TestSettleUserGlobalShare_NewUserFairness(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	// 4 incumbents present from the start.
	incumbents := make([]models.User, 4)
	for i := range incumbents {
		incumbents[i] = mkFeeUser(t, db, "old"+uuidShort(), nil, models.AccountStatusActive)
	}

	// Epoch 1: $8 / 4 = 2 TP each (denominator = 4 — newcomer not yet present).
	seedGlobalPool(t, db, 8.0)
	if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
		t.Fatalf("epoch1: %v", err)
	}

	// Newcomer JOINS after epoch 1. The signup path seeds their checkpoint to the
	// CURRENT index at join time (simulated here) → owed 0 for epoch 1.
	newcomer := mkFeeUserAtIndex(t, db, svc, "new"+uuidShort())
	pending0, err := svc.PendingUserGlobalShare(ctx, newcomer.ID)
	if err != nil {
		t.Fatalf("pending newcomer: %v", err)
	}
	if pending0 != 0 {
		t.Fatalf("newcomer epoch1 pending = %v, want 0 (new-user fairness)", pending0)
	}

	// Epoch 2: +$10 accrued, denominator now 5 → 2 TP each.
	addGlobalAccrual(t, db, 10.0)
	if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
		t.Fatalf("epoch2: %v", err)
	}

	// Newcomer is owed ONLY epoch 2 (2 TP), not epoch1+epoch2 (4 TP).
	creditedNew, err := svc.SettleUserGlobalShare(ctx, newcomer.ID)
	if err != nil {
		t.Fatalf("settle newcomer: %v", err)
	}
	if !approx(creditedNew, 2.0) {
		t.Fatalf("newcomer epoch2 credit = %v, want 2 (only post-join epoch)", creditedNew)
	}

	// An incumbent present across BOTH epochs is owed 2 + 2 = 4 TP.
	creditedOld, err := svc.SettleUserGlobalShare(ctx, incumbents[0].ID)
	if err != nil {
		t.Fatalf("settle incumbent: %v", err)
	}
	if !approx(creditedOld, 4.0) {
		t.Fatalf("incumbent credit = %v, want 4 (both epochs)", creditedOld)
	}
}

// CreateUserWithGlobalCheckpoint is the transactional signup anchor that closes
// the TOCTOU race against SettleGlobalPoolEpoch. It must (a) seed a no-pool
// signup with a NULL checkpoint (earn-from-origin), and (b) seed a post-fold
// signup with the CURRENT index so the user is owed 0 for every pre-join epoch
// (new-user fairness + conservation: they can't claim a share of an epoch whose
// denominator excluded them).
func TestCreateUserWithGlobalCheckpoint_AnchorsJoinTimeIndex(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	// (a) Sign up BEFORE any pool exists → NULL checkpoint (pre-SP4/earn-from-origin).
	preUser := models.User{
		ID: uuid.New(), Name: "pre", Username: "pre" + uuidShort(),
		Email: "pre" + uuidShort() + "@test.com", Password: "x",
		ReferralCode: "pre" + uuidShort(), AccountStatus: models.AccountStatusActive,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := svc.CreateUserWithGlobalCheckpoint(ctx, &preUser); err != nil {
		t.Fatalf("create pre-pool user: %v", err)
	}
	if preUser.LastSettledGlobalIndex != nil {
		t.Fatalf("pre-pool signup checkpoint = %v, want NULL (no pool yet)", *preUser.LastSettledGlobalIndex)
	}

	// Now incumbents earn an epoch.
	incumbents := make([]models.User, 3)
	for i := range incumbents {
		incumbents[i] = mkFeeUser(t, db, "old"+uuidShort(), nil, models.AccountStatusActive)
	}
	// preUser also counts toward the denominator; epoch 1 = $8 / 4 = 2 TP each.
	seedGlobalPool(t, db, 8.0)
	if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
		t.Fatalf("epoch1: %v", err)
	}
	idxAfterE1 := globalIndex(t, db)

	// (b) Sign up AFTER epoch 1 via the transactional anchor.
	newUser := models.User{
		ID: uuid.New(), Name: "new", Username: "new" + uuidShort(),
		Email: "new" + uuidShort() + "@test.com", Password: "x",
		ReferralCode: "new" + uuidShort(), AccountStatus: models.AccountStatusActive,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := svc.CreateUserWithGlobalCheckpoint(ctx, &newUser); err != nil {
		t.Fatalf("create post-fold user: %v", err)
	}
	// Checkpoint must equal the index at join time (the denominator boundary for
	// the user's first eligible epoch), not NULL/0.
	if newUser.LastSettledGlobalIndex == nil {
		t.Fatalf("post-fold signup checkpoint = NULL, want %d", idxAfterE1)
	}
	if *newUser.LastSettledGlobalIndex != idxAfterE1 {
		t.Fatalf("post-fold signup checkpoint = %d, want %d", *newUser.LastSettledGlobalIndex, idxAfterE1)
	}
	// Persisted checkpoint matches too (the INSERT happened in the same txn).
	var persisted models.User
	if err := db.Select("id, last_settled_global_index").First(&persisted, "id = ?", newUser.ID).Error; err != nil {
		t.Fatalf("reload new user: %v", err)
	}
	if persisted.LastSettledGlobalIndex == nil || *persisted.LastSettledGlobalIndex != idxAfterE1 {
		t.Fatalf("persisted checkpoint = %v, want %d", persisted.LastSettledGlobalIndex, idxAfterE1)
	}

	// CONSERVATION/FAIRNESS: the new user is owed 0 for epoch 1 (excluded from its
	// denominator). They share only epochs folded AFTER they joined.
	if pending, err := svc.PendingUserGlobalShare(ctx, newUser.ID); err != nil {
		t.Fatalf("pending new user: %v", err)
	} else if pending != 0 {
		t.Fatalf("post-fold signup epoch1 pending = %v, want 0 (excluded from fold)", pending)
	}

	// Epoch 2 with the newcomer included: +$10 / 5 = 2 TP each; newcomer gets 2.
	addGlobalAccrual(t, db, 10.0)
	if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
		t.Fatalf("epoch2: %v", err)
	}
	credited, err := svc.SettleUserGlobalShare(ctx, newUser.ID)
	if err != nil {
		t.Fatalf("settle new user: %v", err)
	}
	if !approx(credited, 2.0) {
		t.Fatalf("new user credit = %v, want 2 (epoch2 only)", credited)
	}
}

// SettleUserGlobalShare credits + advances + writes a TPEarning(global_pool),
// and is IDEMPOTENT on re-call (owed 0 the second time, no extra ledger row).
func TestSettleUserGlobalShare_CreditsAdvancesIdempotent(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	users := make([]models.User, 2)
	for i := range users {
		users[i] = mkFeeUser(t, db, "u"+uuidShort(), nil, models.AccountStatusActive)
	}
	seedGlobalPool(t, db, 6.0) // 6 / 2 = 3 TP each
	if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
		t.Fatalf("settle epoch: %v", err)
	}

	// First settle: credits 3 TP, writes a global_pool TPEarning, advances ckpt.
	credited, err := svc.SettleUserGlobalShare(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("settle1: %v", err)
	}
	if !approx(credited, 3.0) {
		t.Fatalf("first credit = %v, want 3", credited)
	}
	if got := tpBalance(t, db, users[0].ID); !approx(got, 3.0) {
		t.Fatalf("tp_balance = %v, want 3", got)
	}
	var earnings int64
	db.Model(&models.TPEarning{}).
		Where("user_id = ? AND source_type = ?", users[0].ID, TPSourceGlobalPool).
		Count(&earnings)
	if earnings != 1 {
		t.Fatalf("global_pool earnings = %d, want 1", earnings)
	}

	// Re-call with no new epoch: owed 0, no extra credit, no extra ledger row.
	credited2, err := svc.SettleUserGlobalShare(ctx, users[0].ID)
	if err != nil {
		t.Fatalf("settle2: %v", err)
	}
	if credited2 != 0 {
		t.Fatalf("idempotent re-call credit = %v, want 0", credited2)
	}
	if got := tpBalance(t, db, users[0].ID); !approx(got, 3.0) {
		t.Fatalf("tp_balance after re-call = %v, want 3 (unchanged)", got)
	}
	db.Model(&models.TPEarning{}).
		Where("user_id = ? AND source_type = ?", users[0].ID, TPSourceGlobalPool).
		Count(&earnings)
	if earnings != 1 {
		t.Fatalf("global_pool earnings after re-call = %d, want 1 (no new row)", earnings)
	}
}

// userCount == 0 is a no-op (no index advance, no epoch row).
func TestSettleGlobalPoolEpoch_NoOp_ZeroUsers(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)

	seedGlobalPool(t, db, 100.0) // pool has TP but no users to split among
	res, err := svc.SettleGlobalPoolEpoch(context.Background())
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if res.Settled {
		t.Fatalf("expected no-op with zero users")
	}
	if got := globalIndex(t, db); got != 0 {
		t.Fatalf("index advanced on zero-user no-op: %d", got)
	}
	if n := countEpochs(t, db); n != 0 {
		t.Fatalf("epoch row written on zero-user no-op: %d", n)
	}
}

// delta == 0 is a no-op (idempotent re-run after a prior settle folds nothing).
func TestSettleGlobalPoolEpoch_NoOp_ZeroDelta(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		mkFeeUser(t, db, "u"+uuidShort(), nil, models.AccountStatusActive)
	}
	seedGlobalPool(t, db, 9.0)
	if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
		t.Fatalf("first settle: %v", err)
	}
	idxAfter1 := globalIndex(t, db)

	// Re-run with NO new accrual: delta == 0 → no-op.
	res, err := svc.SettleGlobalPoolEpoch(ctx)
	if err != nil {
		t.Fatalf("second settle: %v", err)
	}
	if res.Settled {
		t.Fatalf("expected zero-delta no-op")
	}
	if got := globalIndex(t, db); got != idxAfter1 {
		t.Fatalf("index changed on zero-delta re-run: %d != %d", got, idxAfter1)
	}
	if n := countEpochs(t, db); n != 1 {
		t.Fatalf("extra epoch row on zero-delta re-run: %d, want 1", n)
	}
}

// OVERFLOW GUARD (finding #2): the cumulative int64 index must never wrap. When
// the current index is so close to math.MaxInt64 that adding this epoch's
// perUserScaled would overflow, the settler MUST refuse to fold (returning an
// error + ops alert) rather than silently wrapping the index NEGATIVE — a wrap
// would make every future owedScaled <= 0 and permanently halt all TP
// distribution. The pool/index must be left untouched so the condition surfaces.
func TestSettleGlobalPoolEpoch_OverflowGuard(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	// One user so perUserScaled == deltaScaled (no division dust to reason about).
	u := mkFeeUser(t, db, "ovf"+uuidShort(), nil, models.AccountStatusActive)

	// Seed a pool whose accrued TP folds to a positive perUserScaled, then pin the
	// index one notch below the boundary so this fold would overflow. 10 TP over one
	// user => perUserScaled = 10 * GlobalTPIndexScale. Set the index to MaxInt64 -
	// perUserScaled + 1, so currentIndex > MaxInt64 - perUserScaled triggers the guard.
	const poolTP = 10.0
	seedGlobalPool(t, db, poolTP)
	perUserScaled := int64(poolTP * float64(models.GlobalTPIndexScale))
	pinnedIndex := int64(math.MaxInt64) - perUserScaled + 1 // one past the safe boundary
	if err := db.Model(&models.GlobalTPPool{}).
		Where("singleton = ?", models.GlobalTPPoolSingletonID).
		Update("per_user_index_scaled", pinnedIndex).Error; err != nil {
		t.Fatalf("pin index: %v", err)
	}
	// Anchor the user's checkpoint at the pinned index so a non-zero pending below
	// could ONLY come from the (refused) fold — not from the synthetic history we
	// pinned for the boundary setup.
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).
		Update("last_settled_global_index", pinnedIndex).Error; err != nil {
		t.Fatalf("anchor user checkpoint: %v", err)
	}

	res, err := svc.SettleGlobalPoolEpoch(ctx)
	if err == nil {
		t.Fatalf("expected overflow guard to error, got Settled=%v (index would have wrapped negative)", res.Settled)
	}
	if res.Settled {
		t.Fatalf("overflow epoch must not be marked settled")
	}

	// The index must be UNCHANGED (no wrap, no advance) and no epoch row written —
	// the ledger is left intact for ops to remediate.
	if got := globalIndex(t, db); got != pinnedIndex {
		t.Fatalf("index changed on overflow: got %d, want %d (must stay pinned, never wrap)", got, pinnedIndex)
	}
	if got := globalIndex(t, db); got <= 0 {
		t.Fatalf("index wrapped non-positive on overflow: %d", got)
	}
	if n := countEpochs(t, db); n != 0 {
		t.Fatalf("epoch row written on overflow: %d, want 0", n)
	}
	// No user is credited — the user's pending share is unaffected by the refused fold.
	if pending, err := svc.PendingUserGlobalShare(ctx, u.ID); err != nil {
		t.Fatalf("pending after overflow: %v", err)
	} else if pending != 0 {
		t.Fatalf("user credited from a refused overflow fold: pending=%v, want 0", pending)
	}
}

// NORMAL ACCRUAL IS UNAFFECTED by the overflow guard: a fold whose resulting
// index stays comfortably below math.MaxInt64 settles exactly as before. This
// pins the guard to the true boundary (it must not reject legitimate folds).
func TestSettleGlobalPoolEpoch_OverflowGuard_NormalAccrualUnaffected(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	u := mkFeeUser(t, db, "ok"+uuidShort(), nil, models.AccountStatusActive)

	// Pin the index to the LAST safe value: currentIndex == MaxInt64 - perUserScaled
	// must still fold (the guard rejects only currentIndex > MaxInt64 - perUserScaled).
	const poolTP = 10.0
	seedGlobalPool(t, db, poolTP)
	perUserScaled := int64(poolTP * float64(models.GlobalTPIndexScale))
	pinnedIndex := int64(math.MaxInt64) - perUserScaled // exactly the safe boundary
	if err := db.Model(&models.GlobalTPPool{}).
		Where("singleton = ?", models.GlobalTPPoolSingletonID).
		Update("per_user_index_scaled", pinnedIndex).Error; err != nil {
		t.Fatalf("pin index: %v", err)
	}
	// Anchor the user's checkpoint at the pinned index so this epoch's credit is
	// exactly the folded share (poolTP), not the synthetic full-index history we
	// pinned for the boundary setup.
	if err := db.Model(&models.User{}).Where("id = ?", u.ID).
		Update("last_settled_global_index", pinnedIndex).Error; err != nil {
		t.Fatalf("anchor user checkpoint: %v", err)
	}

	res, err := svc.SettleGlobalPoolEpoch(ctx)
	if err != nil {
		t.Fatalf("boundary-safe fold must succeed, got error: %v", err)
	}
	if !res.Settled {
		t.Fatalf("expected boundary-safe epoch to settle")
	}
	if got := globalIndex(t, db); got != int64(math.MaxInt64) {
		t.Fatalf("index = %d, want MaxInt64 (%d) after the last safe fold", got, int64(math.MaxInt64))
	}
	if got := globalIndex(t, db); got <= 0 {
		t.Fatalf("index wrapped non-positive: %d", got)
	}

	// The user is credited the folded share as usual — guard does not suppress it.
	credited, err := svc.SettleUserGlobalShare(ctx, u.ID)
	if err != nil {
		t.Fatalf("settle user: %v", err)
	}
	if !approx(credited, poolTP) {
		t.Fatalf("credited = %v, want %v (normal accrual unaffected by the guard)", credited, poolTP)
	}
}

// PRECISION: tiny per-user shares must NOT round to zero — the scaled-int index
// (1e12) preserves shares far below what a float32 (or even naive float
// accumulation) would keep.
func TestSettleGlobalPoolEpoch_TinySharesPrecision(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	// (a) The index formula at realistic scale: a $0.30 pool over 1,000,000
	// users is 3e-7 TP each. float32 has only ~7 significant digits and would
	// struggle to accumulate this across epochs; the scaled int keeps it exact.
	const bigUserCount = 1_000_000
	deltaScaled := int64(0.30 * float64(models.GlobalTPIndexScale))
	perUserScaled := deltaScaled / int64(bigUserCount)
	if perUserScaled <= 0 {
		t.Fatalf("tiny per-user share rounded to zero: %d (lossy)", perUserScaled)
	}
	owedTP := float64(perUserScaled) / float64(models.GlobalTPIndexScale)
	if owedTP <= 0 || owedTP > 1e-6 {
		t.Fatalf("tiny owed TP = %v, want ~3e-7 (>0, sub-microTP)", owedTP)
	}
	if perUserScaled != 300_000 {
		t.Fatalf("perUserScaled = %d, want 300000 (3e-7 TP in pico-TP units)", perUserScaled)
	}

	// (b) End-to-end through the real settler: a sub-microTP pool over one user
	// produces a non-zero credit, not a float-rounded zero.
	u := mkFeeUser(t, db, "tiny"+uuidShort(), nil, models.AccountStatusActive)
	seedGlobalPool(t, db, 0.0000005) // 5e-7 TP, one user → 5e-7 each
	if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
		t.Fatalf("settle tiny: %v", err)
	}
	credited, err := svc.SettleUserGlobalShare(ctx, u.ID)
	if err != nil {
		t.Fatalf("settle user tiny: %v", err)
	}
	if credited <= 0 {
		t.Fatalf("tiny share rounded to zero on credit: %v", credited)
	}
	if !approx(credited, 0.0000005) {
		t.Fatalf("tiny credit = %v, want 5e-7", credited)
	}
}

// CONSERVATION: Σ(all users' settled global TP) ≤ total pool TP folded into the
// index. Integer-division dust stays in the pool; settlement never over-mints.
func TestGlobalPool_ConservationNoOverDistribution(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	// 7 users and a pool of 10 TP — 10/7 does NOT divide evenly, so dust must be
	// retained (7 * floor(10e12/7)/1e12 < 10).
	users := make([]models.User, 7)
	for i := range users {
		users[i] = mkFeeUser(t, db, "u"+uuidShort(), nil, models.AccountStatusActive)
	}
	const poolTP = 10.0
	seedGlobalPool(t, db, poolTP)

	res, err := svc.SettleGlobalPoolEpoch(ctx)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if !res.Settled {
		t.Fatalf("expected settled")
	}

	// Sum what every user is actually credited.
	var totalCredited float64
	for _, u := range users {
		c, err := svc.SettleUserGlobalShare(ctx, u.ID)
		if err != nil {
			t.Fatalf("settle %s: %v", u.ID, err)
		}
		totalCredited += c
	}

	// foldedTP is the TP marked indexed this epoch (== Σ credited, since every
	// user settled exactly once). It must be ≤ the pool's accrued TP, with the
	// difference being conserved dust.
	if totalCredited > poolTP+1e-12 {
		t.Fatalf("OVER-DISTRIBUTION: credited %v > pool %v", totalCredited, poolTP)
	}
	// Σ credited must equal exactly the TP folded this epoch (every user settled
	// once), and never exceed it.
	if !approx(totalCredited, res.DeltaTP) {
		t.Fatalf("credited %v != folded deltaTP %v", totalCredited, res.DeltaTP)
	}

	// The dust (poolTP − totalCredited) must remain in the pool: accrued_tp
	// unchanged, indexed_tp == folded amount, so accrued − indexed > 0.
	var pool models.GlobalTPPool
	if err := db.First(&pool, "singleton = ?", models.GlobalTPPoolSingletonID).Error; err != nil {
		t.Fatalf("load pool: %v", err)
	}
	remaining := pool.AccruedTP - pool.IndexedTP
	if remaining < -1e-12 {
		t.Fatalf("indexed exceeds accrued: accrued=%v indexed=%v", pool.AccruedTP, pool.IndexedTP)
	}
	if remaining <= 0 {
		t.Fatalf("expected positive retained dust for 10/7 split, got remaining=%v", remaining)
	}
	// The retained dust + credited must reconstruct the pool exactly.
	if !approx(totalCredited+remaining, poolTP) {
		t.Fatalf("credited(%v) + remaining(%v) != pool(%v)", totalCredited, remaining, poolTP)
	}
}

// A NULL-checkpoint user via PendingUserGlobalShare is owed 0 (read path mirrors
// the new-user-fairness write path) and a nil user id errors cleanly.
func TestPendingUserGlobalShare_NewUserAndErrors(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		mkFeeUser(t, db, "u"+uuidShort(), nil, models.AccountStatusActive)
	}
	seedGlobalPool(t, db, 9.0)
	if _, err := svc.SettleGlobalPoolEpoch(ctx); err != nil {
		t.Fatalf("settle: %v", err)
	}

	// Brand-new user (signup seeds checkpoint to current index): pending == 0,
	// they share only future epochs.
	fresh := mkFeeUserAtIndex(t, db, svc, "fresh"+uuidShort())
	pending, err := svc.PendingUserGlobalShare(ctx, fresh.ID)
	if err != nil {
		t.Fatalf("pending fresh: %v", err)
	}
	if pending != 0 {
		t.Fatalf("fresh pending = %v, want 0", pending)
	}

	// A pre-SP4 incumbent (NULL checkpoint, present since origin) is owed the
	// full settled amount — NULL is treated as 0, not as "new user".
	incumbent := mkFeeUser(t, db, "incumbent"+uuidShort(), nil, models.AccountStatusActive)
	pendingInc, err := svc.PendingUserGlobalShare(ctx, incumbent.ID)
	if err != nil {
		t.Fatalf("pending incumbent: %v", err)
	}
	if !approx(pendingInc, 3.0) {
		t.Fatalf("incumbent pending = %v, want 3 (NULL checkpoint earns from origin)", pendingInc)
	}

	// nil user id errors.
	if _, err := svc.SettleUserGlobalShare(ctx, uuid.Nil); err == nil {
		t.Fatalf("expected error on nil user id")
	}
}

// TestStrandedGlobalTP_CountsOnlySoftDeletedOwed verifies the drift gauge sums
// the owed global TP of SOFT-DELETED users (currentIndex − checkpoint) and
// excludes live users (whose owed is still claimable, not stranded).
func TestStrandedGlobalTP_CountsOnlySoftDeletedOwed(t *testing.T) {
	db := setupFeeDistTestDB(t)
	svc := NewFeeDistributionService(db, NewReferralGraphService(db), 1.0)
	ctx := context.Background()
	scale := float64(models.GlobalTPIndexScale)

	// Pool cumulative index at 3.0 TP.
	seedGlobalPool(t, db, 0)
	if err := db.Model(&models.GlobalTPPool{}).
		Where("singleton = ?", models.GlobalTPPoolSingletonID).
		Update("per_user_index_scaled", int64(3*scale)).Error; err != nil {
		t.Fatalf("set index: %v", err)
	}

	// Soft-deleted user checkpointed at 1.0 → owed (3−1)=2.0 TP is STRANDED.
	del := mkFeeUser(t, db, "del", nil, models.AccountStatusActive)
	if err := db.Model(&models.User{}).Where("id = ?", del.ID).
		Updates(map[string]interface{}{
			"last_settled_global_index": int64(1 * scale),
			"deleted_at":                time.Now(),
		}).Error; err != nil {
		t.Fatalf("setup deleted: %v", err)
	}

	// Live user checkpointed at 1.0 → also owed 2.0 but still claimable, NOT counted.
	live := mkFeeUser(t, db, "live", nil, models.AccountStatusActive)
	if err := db.Model(&models.User{}).Where("id = ?", live.ID).
		Update("last_settled_global_index", int64(1*scale)).Error; err != nil {
		t.Fatalf("setup live: %v", err)
	}

	stranded, err := svc.StrandedGlobalTP(ctx)
	if err != nil {
		t.Fatalf("stranded: %v", err)
	}
	if math.Abs(stranded-2.0) > 1e-9 {
		t.Fatalf("stranded = %v, want 2.0 (only the soft-deleted user's owed)", stranded)
	}
}
