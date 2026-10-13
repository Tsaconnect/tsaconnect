package services

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// KeyLaneStore is the DB-authoritative outbox of a hot key's nonces
// (key_lane_nonces) and every signing of them (key_lane_txs). Every change to
// an allocation — allocate, speed up, record an outcome, mark mined — runs
// inside WithLock, which serialises it per (chain, address) across every
// process. WithLock bodies touch only the database (and sign locally): never
// the network.
type KeyLaneStore interface {
	// Durable: survives a restart and is shared by processes (the database).
	Durable() bool
	WithLock(ctx context.Context, chainID int64, address string, fn func(q KeyLaneTxn) error) error

	// Plain reads, outside any lock.
	Get(ctx context.Context, chainID int64, address string, nonce uint64) (*models.KeyLaneNonce, error)
	ByOp(ctx context.Context, chainID int64, address, opKey string) (*models.KeyLaneNonce, error)
	HasUnminedAbove(ctx context.Context, chainID int64, address string, nonce uint64) (bool, error)
	MaxNonce(ctx context.Context, chainID int64, address string) (int64, bool, error)
	Signings(ctx context.Context, allocID uuid.UUID) ([]models.KeyLaneTx, error)
	// SigningsOf is every signing of the SAME call as hash — its
	// allocation's signings with hash's own call fingerprint, refused ones
	// included (a "refused" broadcast may still have reached another node) —
	// oldest first; [hash] when the store does not know it. A cancel (a
	// self-transfer at the nonce) is another call: never among them.
	SigningsOf(ctx context.Context, chainID int64, address, hash string) ([]string, error)
	// FinalHash is the current signing of hash's call (the accepted speed-up),
	// else hash; never a signing of another call.
	FinalHash(ctx context.Context, chainID int64, address, hash string) (string, error)
	// Committed is what this lane's live allocations at or above fromNonce
	// will still spend: value + gas limit × price of each one's current signing.
	Committed(ctx context.Context, chainID int64, address string, fromNonce uint64) (*big.Int, error)
	// Allocation is the allocation a signing hash belongs to (nil if none).
	Allocation(ctx context.Context, chainID int64, address, hash string) (*models.KeyLaneNonce, error)
	// Cancelling is every allocation below the mined nonce still waiting to
	// be resolved as cancelled or mined.
	Cancelling(ctx context.Context, chainID int64, address string, below uint64) ([]models.KeyLaneNonce, error)
	// Hold is the lane's shared state (nil when none was ever written).
	Hold(ctx context.Context, chainID int64, address string) (*models.KeyLaneState, error)
}

// errStaleAllocation: an allocation update named a version (or a status)
// the row no longer has — a newer decision (another process, or the chain
// mining it) won. The caller re-reads; nothing is overwritten.
var errStaleAllocation = errors.New("key lane: the allocation changed since it was read")

// KeyLaneTxn is the store inside one lane's lock. Every method returns its
// statement's error; a caller returns it from the WithLock body (never
// ignores it), so a failed statement rolls the whole transaction back.
type KeyLaneTxn interface {
	Get(nonce uint64) (*models.KeyLaneNonce, error)
	ByID(id uuid.UUID) (*models.KeyLaneNonce, error)
	ByOp(opKey string) (*models.KeyLaneNonce, error)
	MaxNonce() (int64, bool, error)
	Committed(fromNonce uint64) (*big.Int, error)
	Create(a *models.KeyLaneNonce) error
	// Save writes a's mutable fields only if the row still has a.Version and
	// is not mined (errStaleAllocation otherwise), and bumps the version.
	Save(a *models.KeyLaneNonce) error
	RecordTx(row *models.KeyLaneTx) error
	SetTxStatus(id uuid.UUID, status string) error
	Signings(allocID uuid.UUID) ([]models.KeyLaneTx, error)
	// MarkMined marks allocations below mined as mined and drops the raw
	// bytes of their signings.
	MarkMined(mined uint64) error
	// RetireOp tombstones opKey if it holds no allocation (allocated=false);
	// Retired reports a tombstone. Both under the lane lock, so a retire and
	// an allocation for the same key can never both win.
	RetireOp(opKey string) (allocated bool, err error)
	Retired(opKey string) (bool, error)
	// Hold is the lane's shared hold; SetHold writes it (nil nonce clears).
	Hold() (*models.KeyLaneState, error)
	SetHold(nonce *uint64, reason string, at time.Time) error
	// DB is the transaction itself, for a caller's pre-broadcast write in the
	// same commit (nil on the in-memory store).
	DB() *gorm.DB
}

// liveAllocationStatuses are every allocation the mined nonce has not passed:
// each one's transaction may still spend.
var liveAllocationStatuses = []string{models.KeyLaneNonceSigned, models.KeyLaneNonceSent}

// liveTxStatuses are signings that may be (or become) the mined one.
var liveTxStatuses = []string{models.KeyLaneTxSigned, models.KeyLaneTxAccepted, models.KeyLaneTxUncertain}

// laneLockKey is the advisory-lock key of one lane.
func laneLockKey(chainID int64, address string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(fmt.Sprintf("key_lane:%d:%s", chainID, strings.ToLower(address))))
	return int64(h.Sum64())
}

// processLaneLocks emulates the advisory lock where the database has none
// (SQLite tests): one mutex per lane per process.
var processLaneLocks sync.Map

func processLaneLock(key int64) *sync.Mutex {
	m, _ := processLaneLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// currentSigning is an allocation's current signing among its signings
// (oldest first): the accepted one, else the latest live one, else the latest
// one (a refused signing is still the call the lane will send again).
func currentSigning(a *models.KeyLaneNonce, txs []models.KeyLaneTx) *models.KeyLaneTx {
	if a.AcceptedHash != "" {
		for i := range txs {
			if txs[i].Hash == a.AcceptedHash {
				return &txs[i]
			}
		}
	}
	for i := len(txs) - 1; i >= 0; i-- {
		if containsString(liveTxStatuses, txs[i].Status) {
			return &txs[i]
		}
	}
	if len(txs) > 0 {
		return &txs[len(txs)-1]
	}
	return nil
}

// sameCall is the signings of txs carrying the call fingerprint fp.
func sameCall(txs []models.KeyLaneTx, fp string) []models.KeyLaneTx {
	var out []models.KeyLaneTx
	for _, t := range txs {
		if callFingerprint(blockchain.UnsignedTx{To: t.To, Data: t.Data, Value: t.Value}) == fp {
			out = append(out, t)
		}
	}
	return out
}

// signingsOfCall is the hashes of hash's call among txs (refused excluded),
// or [hash] when hash is not among them.
func signingsOfCall(txs []models.KeyLaneTx, hash string) []string {
	var fp string
	for _, t := range txs {
		if strings.EqualFold(t.Hash, hash) {
			fp = callFingerprint(blockchain.UnsignedTx{To: t.To, Data: t.Data, Value: t.Value})
		}
	}
	if fp == "" {
		return []string{hash}
	}
	var out []string
	for _, t := range sameCall(txs, fp) {
		out = append(out, t.Hash)
	}
	if len(out) == 0 {
		return []string{hash}
	}
	return out
}

// finalOfCall is the current signing of hash's call among txs, or hash.
func finalOfCall(a *models.KeyLaneNonce, txs []models.KeyLaneTx, hash string) string {
	var fp string
	for _, t := range txs {
		if strings.EqualFold(t.Hash, hash) {
			fp = callFingerprint(blockchain.UnsignedTx{To: t.To, Data: t.Data, Value: t.Value})
		}
	}
	if fp == "" {
		return hash
	}
	if cur := currentSigning(a, sameCall(txs, fp)); cur != nil {
		return cur.Hash
	}
	return hash
}

// committedSpend sums each allocation's current signing (txs grouped by
// allocation, oldest first).
func committedSpend(allocs []models.KeyLaneNonce, txs []models.KeyLaneTx) *big.Int {
	by := map[uuid.UUID][]models.KeyLaneTx{}
	for _, t := range txs {
		by[t.AllocationID] = append(by[t.AllocationID], t)
	}
	sum := new(big.Int)
	for i := range allocs {
		addSpend(sum, currentSigning(&allocs[i], by[allocs[i].ID]))
	}
	return sum
}

// addSpend adds a signing's value + gas limit × price to sum.
func addSpend(sum *big.Int, tx *models.KeyLaneTx) {
	if tx == nil {
		return
	}
	if p, ok := new(big.Int).SetString(tx.GasPrice, 10); ok {
		sum.Add(sum, new(big.Int).Mul(p, big.NewInt(tx.GasLimit)))
	}
	if v, ok := new(big.Int).SetString(tx.Value, 10); ok {
		sum.Add(sum, v)
	}
}

// GormKeyLaneStore is the KeyLaneStore on the API database. On Postgres the
// lane lock is pg_advisory_xact_lock, held for the transaction.
type GormKeyLaneStore struct{ DB *gorm.DB }

func (s *GormKeyLaneStore) Durable() bool { return true }

func (s *GormKeyLaneStore) WithLock(ctx context.Context, chainID int64, address string, fn func(q KeyLaneTxn) error) error {
	key := laneLockKey(chainID, address)
	if s.DB.Dialector.Name() != "postgres" {
		m := processLaneLock(key)
		m.Lock()
		defer m.Unlock()
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", key).Error; err != nil {
				return fmt.Errorf("key lane lock: %w", err)
			}
		}
		return fn(&gormLaneTxn{db: tx, chainID: chainID, address: strings.ToLower(address)})
	})
}

func (s *GormKeyLaneStore) txn(ctx context.Context, chainID int64, address string) *gormLaneTxn {
	return &gormLaneTxn{db: s.DB.WithContext(ctx), chainID: chainID, address: strings.ToLower(address)}
}

func (s *GormKeyLaneStore) Get(ctx context.Context, chainID int64, address string, nonce uint64) (*models.KeyLaneNonce, error) {
	return s.txn(ctx, chainID, address).Get(nonce)
}

func (s *GormKeyLaneStore) ByOp(ctx context.Context, chainID int64, address, opKey string) (*models.KeyLaneNonce, error) {
	return s.txn(ctx, chainID, address).ByOp(opKey)
}

func (s *GormKeyLaneStore) HasUnminedAbove(ctx context.Context, chainID int64, address string, nonce uint64) (bool, error) {
	var n int64
	err := s.txn(ctx, chainID, address).lane().Model(&models.KeyLaneNonce{}).
		Where("nonce > ? AND status IN ?", int64(nonce), liveAllocationStatuses).Count(&n).Error
	return n > 0, err
}

func (s *GormKeyLaneStore) MaxNonce(ctx context.Context, chainID int64, address string) (int64, bool, error) {
	return s.txn(ctx, chainID, address).MaxNonce()
}

func (s *GormKeyLaneStore) Signings(ctx context.Context, allocID uuid.UUID) ([]models.KeyLaneTx, error) {
	return (&gormLaneTxn{db: s.DB.WithContext(ctx)}).Signings(allocID)
}

func (s *GormKeyLaneStore) allocationOf(ctx context.Context, chainID int64, address, hash string) (*models.KeyLaneNonce, []models.KeyLaneTx, error) {
	var row models.KeyLaneTx
	err := s.DB.WithContext(ctx).Where("hash = ? AND chain_id = ? AND address = ?", strings.ToLower(hash), chainID, strings.ToLower(address)).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var a models.KeyLaneNonce
	if err := s.DB.WithContext(ctx).Where("id = ?", row.AllocationID).First(&a).Error; err != nil {
		return nil, nil, err
	}
	txs, err := s.Signings(ctx, a.ID)
	return &a, txs, err
}

func (s *GormKeyLaneStore) SigningsOf(ctx context.Context, chainID int64, address, hash string) ([]string, error) {
	a, txs, err := s.allocationOf(ctx, chainID, address, hash)
	if err != nil || a == nil {
		return []string{hash}, err
	}
	return signingsOfCall(txs, hash), nil
}

func (s *GormKeyLaneStore) FinalHash(ctx context.Context, chainID int64, address, hash string) (string, error) {
	a, txs, err := s.allocationOf(ctx, chainID, address, hash)
	if err != nil || a == nil {
		return hash, err
	}
	return finalOfCall(a, txs, hash), nil
}

func (s *GormKeyLaneStore) Committed(ctx context.Context, chainID int64, address string, fromNonce uint64) (*big.Int, error) {
	return s.txn(ctx, chainID, address).Committed(fromNonce)
}

func (s *GormKeyLaneStore) Allocation(ctx context.Context, chainID int64, address, hash string) (*models.KeyLaneNonce, error) {
	a, _, err := s.allocationOf(ctx, chainID, address, hash)
	return a, err
}

func (s *GormKeyLaneStore) Cancelling(ctx context.Context, chainID int64, address string, below uint64) ([]models.KeyLaneNonce, error) {
	var out []models.KeyLaneNonce
	err := s.txn(ctx, chainID, address).lane().Where("nonce < ? AND cancel = ? AND status IN ?", int64(below), true, liveAllocationStatuses).
		Order("nonce").Find(&out).Error
	return out, err
}

func (s *GormKeyLaneStore) Hold(ctx context.Context, chainID int64, address string) (*models.KeyLaneState, error) {
	return s.txn(ctx, chainID, address).Hold()
}

type gormLaneTxn struct {
	db      *gorm.DB
	chainID int64
	address string
}

func (q *gormLaneTxn) lane() *gorm.DB {
	return q.db.Where("chain_id = ? AND address = ?", q.chainID, q.address)
}

// firstOrNil is the one allocation q matches, or nil (never a logged
// "record not found": an absent nonce is the common case).
func firstOrNil(q *gorm.DB) (*models.KeyLaneNonce, error) {
	var rows []models.KeyLaneNonce
	if err := q.Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (q *gormLaneTxn) Get(nonce uint64) (*models.KeyLaneNonce, error) {
	return firstOrNil(q.lane().Where("nonce = ?", int64(nonce)))
}

func (q *gormLaneTxn) ByID(id uuid.UUID) (*models.KeyLaneNonce, error) {
	return firstOrNil(q.db.Where("id = ?", id))
}

func (q *gormLaneTxn) ByOp(opKey string) (*models.KeyLaneNonce, error) {
	if opKey == "" {
		return nil, nil
	}
	return firstOrNil(q.lane().Where("op_key = ?", opKey))
}

func (q *gormLaneTxn) MaxNonce() (int64, bool, error) {
	var max *int64
	if err := q.lane().Model(&models.KeyLaneNonce{}).Select("MAX(nonce)").Scan(&max).Error; err != nil {
		return 0, false, err
	}
	if max == nil {
		return 0, false, nil
	}
	return *max, true, nil
}

// Committed reads the live allocations and all their signings in two
// statements (never one per allocation): it runs inside the lane lock.
func (q *gormLaneTxn) Committed(fromNonce uint64) (*big.Int, error) {
	var allocs []models.KeyLaneNonce
	if err := q.lane().Where("nonce >= ? AND status IN ?", int64(fromNonce), liveAllocationStatuses).Find(&allocs).Error; err != nil {
		return nil, err
	}
	if len(allocs) == 0 {
		return new(big.Int), nil
	}
	ids := make([]uuid.UUID, len(allocs))
	for i := range allocs {
		ids[i] = allocs[i].ID
	}
	var txs []models.KeyLaneTx
	if err := q.db.Where("allocation_id IN ?", ids).Order("created_at, id").Find(&txs).Error; err != nil {
		return nil, err
	}
	return committedSpend(allocs, txs), nil
}

func (q *gormLaneTxn) Create(a *models.KeyLaneNonce) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	a.ChainID, a.Address = q.chainID, q.address
	if err := q.db.Create(a).Error; err != nil {
		return fmt.Errorf("allocate nonce %d: %w", a.Nonce, err)
	}
	return nil
}

func (q *gormLaneTxn) Save(a *models.KeyLaneNonce) error {
	now := time.Now()
	res := q.db.Model(&models.KeyLaneNonce{}).
		Where("id = ? AND version = ? AND status <> ?", a.ID, a.Version, models.KeyLaneNonceMined).
		Updates(map[string]any{
			"status": a.Status, "op_key": a.OpKey, "cancel": a.Cancel, "accepted_hash": a.AcceptedHash, "accepted_price": a.AcceptedPrice,
			"bumps": a.Bumps, "accepted_bumps": a.AcceptedBumps, "bump_limit": a.BumpLimit, "price_cap": a.PriceCap,
			"rebroadcasts": a.Rebroadcasts, "acted_at": a.ActedAt, "version": a.Version + 1, "updated_at": now,
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return errStaleAllocation
	}
	a.Version, a.UpdatedAt = a.Version+1, now
	return nil
}

func (q *gormLaneTxn) MarkMined(mined uint64) error {
	if err := q.lane().Model(&models.KeyLaneNonce{}).
		// A cancelling allocation is resolved by the watch (cancelled or
		// mined: which of its two calls mined), never assumed mined.
		Where("nonce < ? AND status IN ? AND cancel = ?", int64(mined), liveAllocationStatuses, false).
		// A mined operation is final on chain: its key is freed, so a
		// caller's later, deliberate retry (after reading the outcome) is a
		// new operation.
		Updates(map[string]any{"status": models.KeyLaneNonceMined, "op_key": "", "version": gorm.Expr("version + 1"), "updated_at": time.Now()}).Error; err != nil {
		return err
	}
	if err := q.lane().Model(&models.KeyLaneTx{}).
		Where("nonce < ? AND status IN ?", int64(mined), liveTxStatuses).
		Update("status", models.KeyLaneTxMined).Error; err != nil {
		return err
	}
	return q.lane().Model(&models.KeyLaneTx{}).
		Where("nonce < ? AND raw_tx IS NOT NULL", int64(mined)).
		Update("raw_tx", nil).Error
}

func (q *gormLaneTxn) DB() *gorm.DB { return q.db }

func (q *gormLaneTxn) RetireOp(opKey string) (bool, error) {
	if a, err := q.ByOp(opKey); err != nil || a != nil {
		return a != nil, err
	}
	// No live allocation (a MINED one freed its key: the caller's own record,
	// written by its hook, proves that case): tombstone the key so no later
	// allocation for it is ever made.
	return false, q.db.Clauses(clause.OnConflict{DoNothing: true}).
		Create(&models.KeyLaneRetiredOp{ChainID: q.chainID, Address: q.address, OpKey: opKey, CreatedAt: time.Now()}).Error
}

func (q *gormLaneTxn) Retired(opKey string) (bool, error) {
	if opKey == "" {
		return false, nil
	}
	var n int64
	err := q.lane().Model(&models.KeyLaneRetiredOp{}).Where("op_key = ?", opKey).Count(&n).Error
	return n > 0, err
}

func (q *gormLaneTxn) Hold() (*models.KeyLaneState, error) {
	var rows []models.KeyLaneState
	if err := q.lane().Limit(1).Find(&rows).Error; err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func (q *gormLaneTxn) SetHold(nonce *uint64, reason string, at time.Time) error {
	st := map[string]any{"held_nonce": nil, "held_reason": "", "held_since": nil, "updated_at": time.Now()}
	if nonce != nil {
		n := int64(*nonce)
		st["held_nonce"], st["held_reason"], st["held_since"] = n, truncateRunes(reason, 256), at
	}
	cur, err := q.Hold()
	if err != nil {
		return err
	}
	if cur == nil {
		row := models.KeyLaneState{ChainID: q.chainID, Address: q.address, HeldReason: st["held_reason"].(string), Version: 1}
		if nonce != nil {
			n := int64(*nonce)
			row.HeldNonce, row.HeldSince = &n, &at
		}
		return q.db.Create(&row).Error
	}
	st["version"] = cur.Version + 1
	return q.db.Model(&models.KeyLaneState{}).Where("chain_id = ? AND address = ? AND version = ?", q.chainID, q.address, cur.Version).
		Updates(st).Error
}

func (q *gormLaneTxn) RecordTx(row *models.KeyLaneTx) error {
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	row.ChainID, row.Address, row.Hash = q.chainID, q.address, strings.ToLower(row.Hash)
	return q.db.Create(row).Error
}

func (q *gormLaneTxn) SetTxStatus(id uuid.UUID, status string) error {
	return q.db.Model(&models.KeyLaneTx{}).Where("id = ?", id).Update("status", status).Error
}

func (q *gormLaneTxn) Signings(allocID uuid.UUID) ([]models.KeyLaneTx, error) {
	var txs []models.KeyLaneTx
	err := q.db.Where("allocation_id = ?", allocID).Order("created_at, id").Find(&txs).Error
	return txs, err
}

// memKeyLaneStore is the KeyLaneStore in memory: a registry without the
// database (tests, local tooling). Not durable.
type memKeyLaneStore struct {
	mu     sync.Mutex
	allocs []*models.KeyLaneNonce
	txs    []*models.KeyLaneTx
	states map[string]models.KeyLaneState
}

func newMemKeyLaneStore() *memKeyLaneStore { return &memKeyLaneStore{} }

func (m *memKeyLaneStore) Durable() bool { return false }

func (m *memKeyLaneStore) WithLock(_ context.Context, chainID int64, address string, fn func(q KeyLaneTxn) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// A failed body must leave nothing behind: work on copies, commit on success.
	work := &memKeyLaneStore{states: map[string]models.KeyLaneState{}}
	for k, v := range m.states {
		work.states[k] = v
	}
	for _, a := range m.allocs {
		c := *a
		work.allocs = append(work.allocs, &c)
	}
	for _, t := range m.txs {
		c := *t
		work.txs = append(work.txs, &c)
	}
	if err := fn(&memLaneTxn{m: work, chainID: chainID, address: strings.ToLower(address)}); err != nil {
		return err
	}
	m.allocs, m.txs, m.states = work.allocs, work.txs, work.states
	return nil
}

func (m *memKeyLaneStore) read(chainID int64, address string) *memLaneTxn {
	return &memLaneTxn{m: m, chainID: chainID, address: strings.ToLower(address)}
}

func (m *memKeyLaneStore) Get(_ context.Context, chainID int64, address string, nonce uint64) (*models.KeyLaneNonce, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.read(chainID, address).Get(nonce)
}

func (m *memKeyLaneStore) ByOp(_ context.Context, chainID int64, address, opKey string) (*models.KeyLaneNonce, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.read(chainID, address).ByOp(opKey)
}

func (m *memKeyLaneStore) HasUnminedAbove(_ context.Context, chainID int64, address string, nonce uint64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.read(chainID, address)
	for _, a := range m.allocs {
		if q.mine(a) && uint64(a.Nonce) > nonce && containsString(liveAllocationStatuses, a.Status) {
			return true, nil
		}
	}
	return false, nil
}

func (m *memKeyLaneStore) MaxNonce(_ context.Context, chainID int64, address string) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.read(chainID, address).MaxNonce()
}

func (m *memKeyLaneStore) Signings(_ context.Context, allocID uuid.UUID) ([]models.KeyLaneTx, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return (&memLaneTxn{m: m}).Signings(allocID)
}

func (m *memKeyLaneStore) allocationOf(chainID int64, address, hash string) (*models.KeyLaneNonce, []models.KeyLaneTx) {
	q := m.read(chainID, address)
	for _, t := range m.txs {
		if strings.EqualFold(t.Hash, hash) && t.ChainID == chainID {
			for _, a := range m.allocs {
				if a.ID == t.AllocationID {
					c := *a
					txs, _ := q.Signings(a.ID)
					return &c, txs
				}
			}
		}
	}
	return nil, nil
}

func (m *memKeyLaneStore) SigningsOf(_ context.Context, chainID int64, address, hash string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, txs := m.allocationOf(chainID, address, hash)
	if a == nil {
		return []string{hash}, nil
	}
	return signingsOfCall(txs, hash), nil
}

func (m *memKeyLaneStore) FinalHash(_ context.Context, chainID int64, address, hash string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, txs := m.allocationOf(chainID, address, hash)
	if a == nil {
		return hash, nil
	}
	return finalOfCall(a, txs, hash), nil
}

func (m *memKeyLaneStore) Allocation(_ context.Context, chainID int64, address, hash string) (*models.KeyLaneNonce, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, _ := m.allocationOf(chainID, address, hash)
	return a, nil
}

func (m *memKeyLaneStore) Cancelling(_ context.Context, chainID int64, address string, below uint64) ([]models.KeyLaneNonce, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.read(chainID, address)
	var out []models.KeyLaneNonce
	for _, a := range m.allocs {
		if q.mine(a) && uint64(a.Nonce) < below && a.Cancel && containsString(liveAllocationStatuses, a.Status) {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (m *memKeyLaneStore) Hold(_ context.Context, chainID int64, address string) (*models.KeyLaneState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.read(chainID, address).Hold()
}

func (m *memKeyLaneStore) Committed(_ context.Context, chainID int64, address string, fromNonce uint64) (*big.Int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.read(chainID, address).Committed(fromNonce)
}

type memLaneTxn struct {
	m       *memKeyLaneStore
	chainID int64
	address string
}

func (q *memLaneTxn) mine(a *models.KeyLaneNonce) bool {
	return a.ChainID == q.chainID && a.Address == q.address
}

func (q *memLaneTxn) Get(nonce uint64) (*models.KeyLaneNonce, error) {
	for _, a := range q.m.allocs {
		if q.mine(a) && uint64(a.Nonce) == nonce {
			c := *a
			return &c, nil
		}
	}
	return nil, nil
}

func (q *memLaneTxn) ByID(id uuid.UUID) (*models.KeyLaneNonce, error) {
	for _, a := range q.m.allocs {
		if a.ID == id {
			c := *a
			return &c, nil
		}
	}
	return nil, nil
}

func (q *memLaneTxn) ByOp(opKey string) (*models.KeyLaneNonce, error) {
	if opKey == "" {
		return nil, nil
	}
	for _, a := range q.m.allocs {
		if q.mine(a) && a.OpKey == opKey {
			c := *a
			return &c, nil
		}
	}
	return nil, nil
}

func (q *memLaneTxn) MaxNonce() (int64, bool, error) {
	var max int64 = -1
	for _, a := range q.m.allocs {
		if q.mine(a) && a.Nonce > max {
			max = a.Nonce
		}
	}
	return max, max >= 0, nil
}

func (q *memLaneTxn) Committed(fromNonce uint64) (*big.Int, error) {
	var allocs []models.KeyLaneNonce
	var txs []models.KeyLaneTx
	for _, a := range q.m.allocs {
		if q.mine(a) && uint64(a.Nonce) >= fromNonce && containsString(liveAllocationStatuses, a.Status) {
			allocs = append(allocs, *a)
			sigs, _ := q.Signings(a.ID)
			txs = append(txs, sigs...)
		}
	}
	return committedSpend(allocs, txs), nil
}

func (q *memLaneTxn) Create(a *models.KeyLaneNonce) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	a.ChainID, a.Address = q.chainID, q.address
	for _, x := range q.m.allocs {
		if q.mine(x) && (x.Nonce == a.Nonce || (a.OpKey != "" && x.OpKey == a.OpKey)) {
			return fmt.Errorf("allocate nonce %d: unique violation", a.Nonce)
		}
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now()
	}
	c := *a
	q.m.allocs = append(q.m.allocs, &c)
	return nil
}

func (q *memLaneTxn) Save(a *models.KeyLaneNonce) error {
	for i, x := range q.m.allocs {
		if x.ID == a.ID {
			if x.Version != a.Version || x.Status == models.KeyLaneNonceMined {
				return errStaleAllocation
			}
			a.Version++
			c := *a
			q.m.allocs[i] = &c
			return nil
		}
	}
	return fmt.Errorf("save allocation %s: not found", a.ID)
}

func (q *memLaneTxn) MarkMined(mined uint64) error {
	for _, a := range q.m.allocs {
		if q.mine(a) && uint64(a.Nonce) < mined && !a.Cancel && containsString(liveAllocationStatuses, a.Status) {
			a.Status, a.OpKey, a.Version = models.KeyLaneNonceMined, "", a.Version+1
		}
	}
	for _, t := range q.m.txs {
		if t.ChainID == q.chainID && t.Address == q.address && uint64(t.Nonce) < mined {
			t.RawTx = nil
			if containsString(liveTxStatuses, t.Status) {
				t.Status = models.KeyLaneTxMined
			}
		}
	}
	return nil
}

func (q *memLaneTxn) DB() *gorm.DB { return nil }

func (q *memLaneTxn) RetireOp(opKey string) (bool, error) {
	if a, _ := q.ByOp(opKey); a != nil {
		return true, nil
	}
	if q.m.states == nil {
		q.m.states = map[string]models.KeyLaneState{}
	}
	q.m.states["retired:"+q.stateKey()+":"+opKey] = models.KeyLaneState{}
	return false, nil
}

func (q *memLaneTxn) Retired(opKey string) (bool, error) {
	_, ok := q.m.states["retired:"+q.stateKey()+":"+opKey]
	return ok && opKey != "", nil
}

func (q *memLaneTxn) stateKey() string { return fmt.Sprintf("%d:%s", q.chainID, q.address) }

func (q *memLaneTxn) Hold() (*models.KeyLaneState, error) {
	st, ok := q.m.states[q.stateKey()]
	if !ok {
		return nil, nil
	}
	return &st, nil
}

func (q *memLaneTxn) SetHold(nonce *uint64, reason string, at time.Time) error {
	if q.m.states == nil {
		q.m.states = map[string]models.KeyLaneState{}
	}
	st := q.m.states[q.stateKey()]
	st.ChainID, st.Address, st.Version = q.chainID, q.address, st.Version+1
	st.HeldNonce, st.HeldReason, st.HeldSince = nil, "", nil
	if nonce != nil {
		n := int64(*nonce)
		st.HeldNonce, st.HeldReason, st.HeldSince = &n, reason, &at
	}
	q.m.states[q.stateKey()] = st
	return nil
}

func (q *memLaneTxn) RecordTx(row *models.KeyLaneTx) error {
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	row.ChainID, row.Address, row.Hash = q.chainID, q.address, strings.ToLower(row.Hash)
	for _, t := range q.m.txs {
		if t.ChainID == row.ChainID && t.Address == row.Address && t.Nonce == row.Nonce && t.Hash == row.Hash {
			return fmt.Errorf("record signing %s: unique violation", row.Hash)
		}
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now()
	}
	c := *row
	q.m.txs = append(q.m.txs, &c)
	return nil
}

func (q *memLaneTxn) SetTxStatus(id uuid.UUID, status string) error {
	for _, t := range q.m.txs {
		if t.ID == id {
			t.Status = status
		}
	}
	return nil
}

func (q *memLaneTxn) Signings(allocID uuid.UUID) ([]models.KeyLaneTx, error) {
	var out []models.KeyLaneTx
	for _, t := range q.m.txs {
		if t.AllocationID == allocID {
			out = append(out, *t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
