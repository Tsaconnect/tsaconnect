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
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// KeyLaneStore is the DB-authoritative record of a hot key's nonces
// (key_lane_nonces) and every signing of them (key_lane_txs). Every decision
// about a nonce — allocate, re-broadcast, speed up, fill — runs inside
// WithLock, which serialises it per (chain, address) across every process.
type KeyLaneStore interface {
	// Durable: survives a restart and is shared by processes (the database).
	Durable() bool
	WithLock(ctx context.Context, chainID int64, address string, fn func(q KeyLaneTxn) error) error
	// FinalHash follows a transaction to its allocation's accepted signing (a
	// speed-up's hash), or returns hash unchanged when it knows none.
	FinalHash(ctx context.Context, chainID int64, address, hash string) (string, error)
	// Committed is what this lane's allocations at or above fromNonce will
	// still spend: value + gas limit × price of each one's current signing.
	Committed(ctx context.Context, chainID int64, address string, fromNonce uint64) (*big.Int, error)
	// MarkMined marks allocations below mined as mined and drops the raw bytes
	// of their signings.
	MarkMined(ctx context.Context, chainID int64, address string, mined uint64) error
}

// KeyLaneTxn is the store inside one lane's lock.
type KeyLaneTxn interface {
	AllocationAt(nonce uint64) (*models.KeyLaneNonce, error)
	// HasAllocationAbove: any unmined allocation above nonce.
	HasAllocationAbove(nonce uint64) (bool, error)
	// Allocate reserves the smallest free nonce at or above from (and above
	// every mined allocation) for the call fp. Free = released, or above every
	// allocation ever made: a lower nonce with no allocation is a hole outside
	// this server's record and is never taken (reviewGapLocked holds on it).
	Allocate(from uint64, fp, role string, at time.Time) (*models.KeyLaneNonce, error)
	// AllocateExact reserves exactly nonce (a released one, or one with no
	// allocation), or fails.
	AllocateExact(nonce uint64, fp, role string, at time.Time) (*models.KeyLaneNonce, error)
	// Release marks an allocation released: its only signing never left.
	Release(id uuid.UUID) error
	RecordTx(row *models.KeyLaneTx) error
	SetTxStatus(id uuid.UUID, status string) error
	// CurrentTx is the allocation's accepted signing, else its latest pending one.
	CurrentTx(alloc *models.KeyLaneNonce) (*models.KeyLaneTx, error)
	// FirstTx is the allocation's first signing (its original price).
	FirstTx(alloc *models.KeyLaneNonce) (*models.KeyLaneTx, error)
	Accept(alloc *models.KeyLaneNonce, tx *models.KeyLaneTx) error
	Touch(alloc *models.KeyLaneNonce, bumps, rebroadcasts int, at time.Time) error
	Committed(fromNonce uint64) (*big.Int, error)
}

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

func (s *GormKeyLaneStore) FinalHash(ctx context.Context, chainID int64, address, hash string) (string, error) {
	var row models.KeyLaneTx
	err := s.DB.WithContext(ctx).Where("chain_id = ? AND address = ? AND lower(hash) = ?", chainID, strings.ToLower(address), strings.ToLower(hash)).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return hash, nil
	}
	if err != nil {
		return hash, err
	}
	q := &gormLaneTxn{db: s.DB.WithContext(ctx), chainID: chainID, address: strings.ToLower(address)}
	var alloc models.KeyLaneNonce
	if err := q.db.Where("id = ?", row.AllocationID).First(&alloc).Error; err != nil {
		return hash, nil
	}
	cur, err := q.CurrentTx(&alloc)
	if err != nil || cur == nil {
		return hash, err
	}
	return cur.Hash, nil
}

func (s *GormKeyLaneStore) Committed(ctx context.Context, chainID int64, address string, fromNonce uint64) (*big.Int, error) {
	q := &gormLaneTxn{db: s.DB.WithContext(ctx), chainID: chainID, address: strings.ToLower(address)}
	return q.Committed(fromNonce)
}

func (s *GormKeyLaneStore) MarkMined(ctx context.Context, chainID int64, address string, mined uint64) error {
	addr := strings.ToLower(address)
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.KeyLaneNonce{}).
			Where("chain_id = ? AND address = ? AND nonce < ? AND status = ?", chainID, addr, int64(mined), models.KeyLaneNonceAllocated).
			Update("status", models.KeyLaneNonceMined).Error; err != nil {
			return err
		}
		return tx.Model(&models.KeyLaneTx{}).
			Where("chain_id = ? AND address = ? AND nonce < ? AND raw_tx IS NOT NULL", chainID, addr, int64(mined)).
			Updates(map[string]any{"raw_tx": nil, "status": gorm.Expr("CASE WHEN status IN (?, ?) THEN ? ELSE status END",
				models.KeyLaneTxPending, models.KeyLaneTxAccepted, models.KeyLaneTxMined)}).Error
	})
}

type gormLaneTxn struct {
	db      *gorm.DB
	chainID int64
	address string
}

func (q *gormLaneTxn) lane() *gorm.DB {
	return q.db.Where("chain_id = ? AND address = ?", q.chainID, q.address)
}

func (q *gormLaneTxn) AllocationAt(nonce uint64) (*models.KeyLaneNonce, error) {
	var a models.KeyLaneNonce
	err := q.lane().Where("nonce = ?", int64(nonce)).First(&a).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &a, err
}

func (q *gormLaneTxn) HasAllocationAbove(nonce uint64) (bool, error) {
	var n int64
	err := q.lane().Model(&models.KeyLaneNonce{}).Where("nonce > ? AND status = ?", int64(nonce), models.KeyLaneNonceAllocated).Count(&n).Error
	return n > 0, err
}

func (q *gormLaneTxn) Allocate(from uint64, fp, role string, at time.Time) (*models.KeyLaneNonce, error) {
	var rows []models.KeyLaneNonce
	if err := q.lane().Select("nonce", "status").Order("nonce").Find(&rows).Error; err != nil {
		return nil, err
	}
	return q.AllocateExact(nextFreeNonce(from, rows), fp, role, at)
}

func (q *gormLaneTxn) AllocateExact(nonce uint64, fp, role string, at time.Time) (*models.KeyLaneNonce, error) {
	var existing models.KeyLaneNonce
	err := q.lane().Where("nonce = ?", int64(nonce)).First(&existing).Error
	switch {
	case err == nil && existing.Status == models.KeyLaneNonceReleased:
		existing.Fingerprint, existing.Status, existing.Role, existing.ActedAt = fp, models.KeyLaneNonceAllocated, role, at
		existing.AcceptedHash, existing.AcceptedPrice, existing.Bumps, existing.Rebroadcasts, existing.AcceptedBumps = "", "", 0, 0, 0
		if err := q.db.Save(&existing).Error; err != nil {
			return nil, fmt.Errorf("allocate nonce %d: %w", nonce, err)
		}
		return &existing, nil
	case err == nil:
		return nil, fmt.Errorf("allocate nonce %d: already allocated", nonce)
	case !errors.Is(err, gorm.ErrRecordNotFound):
		return nil, err
	}
	a := &models.KeyLaneNonce{ID: uuid.New(), ChainID: q.chainID, Address: q.address, Nonce: int64(nonce), Fingerprint: fp,
		Status: models.KeyLaneNonceAllocated, Role: role, ActedAt: at}
	if err := q.db.Create(a).Error; err != nil {
		return nil, fmt.Errorf("allocate nonce %d: %w", nonce, err)
	}
	return a, nil
}

func (q *gormLaneTxn) Release(id uuid.UUID) error {
	return q.db.Model(&models.KeyLaneNonce{}).Where("id = ?", id).Update("status", models.KeyLaneNonceReleased).Error
}

// nextFreeNonce is the smallest nonce at or above from, and above every mined
// allocation, that is released or above every allocation (rows sorted by
// nonce). A lower nonce with no row is a hole outside the record: skipped.
func nextFreeNonce(from uint64, rows []models.KeyLaneNonce) uint64 {
	n := from
	var maxRow int64 = -1
	status := map[uint64]string{}
	for _, r := range rows {
		if r.Status == models.KeyLaneNonceMined && uint64(r.Nonce)+1 > n {
			n = uint64(r.Nonce) + 1
		}
		if r.Nonce > maxRow {
			maxRow = r.Nonce
		}
		status[uint64(r.Nonce)] = r.Status
	}
	for ; ; n++ {
		st, ok := status[n]
		if ok && st == models.KeyLaneNonceReleased {
			return n
		}
		if !ok && int64(n) > maxRow {
			return n
		}
	}
}

func (q *gormLaneTxn) RecordTx(row *models.KeyLaneTx) error {
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	row.ChainID, row.Address = q.chainID, q.address
	return q.db.Create(row).Error
}

func (q *gormLaneTxn) SetTxStatus(id uuid.UUID, status string) error {
	return q.db.Model(&models.KeyLaneTx{}).Where("id = ?", id).Update("status", status).Error
}

func (q *gormLaneTxn) CurrentTx(alloc *models.KeyLaneNonce) (*models.KeyLaneTx, error) {
	var row models.KeyLaneTx
	var err error
	if alloc.AcceptedHash != "" {
		err = q.db.Where("allocation_id = ? AND hash = ?", alloc.ID, alloc.AcceptedHash).First(&row).Error
	} else {
		err = q.db.Where("allocation_id = ? AND status = ?", alloc.ID, models.KeyLaneTxPending).Order("created_at DESC").First(&row).Error
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}

func (q *gormLaneTxn) FirstTx(alloc *models.KeyLaneNonce) (*models.KeyLaneTx, error) {
	var row models.KeyLaneTx
	err := q.db.Where("allocation_id = ?", alloc.ID).Order("created_at").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &row, err
}

func (q *gormLaneTxn) Accept(alloc *models.KeyLaneNonce, tx *models.KeyLaneTx) error {
	if err := q.db.Model(&models.KeyLaneTx{}).
		Where("allocation_id = ? AND id <> ? AND status IN ?", alloc.ID, tx.ID, []string{models.KeyLaneTxPending, models.KeyLaneTxAccepted}).
		Update("status", models.KeyLaneTxReplaced).Error; err != nil {
		return err
	}
	if err := q.SetTxStatus(tx.ID, models.KeyLaneTxAccepted); err != nil {
		return err
	}
	alloc.AcceptedHash, alloc.AcceptedPrice, alloc.AcceptedBumps = tx.Hash, tx.GasPrice, alloc.Bumps
	return q.db.Model(&models.KeyLaneNonce{}).Where("id = ?", alloc.ID).
		Updates(map[string]any{"accepted_hash": tx.Hash, "accepted_price": tx.GasPrice, "accepted_bumps": alloc.Bumps}).Error
}

func (q *gormLaneTxn) Touch(alloc *models.KeyLaneNonce, bumps, rebroadcasts int, at time.Time) error {
	alloc.Bumps, alloc.Rebroadcasts, alloc.ActedAt = bumps, rebroadcasts, at
	return q.db.Model(&models.KeyLaneNonce{}).Where("id = ?", alloc.ID).
		Updates(map[string]any{"bumps": bumps, "rebroadcasts": rebroadcasts, "acted_at": at}).Error
}

func (q *gormLaneTxn) Committed(fromNonce uint64) (*big.Int, error) {
	var allocs []models.KeyLaneNonce
	if err := q.lane().Where("nonce >= ? AND status = ?", int64(fromNonce), models.KeyLaneNonceAllocated).Find(&allocs).Error; err != nil {
		return nil, err
	}
	sum := new(big.Int)
	for i := range allocs {
		tx, err := q.CurrentTx(&allocs[i])
		if err != nil {
			return nil, err
		}
		addSpend(sum, tx)
	}
	return sum, nil
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

// memKeyLaneStore is the KeyLaneStore in memory: a registry without the
// database (tests, local tooling). Not durable — a lane on it never signs with
// a signer that cannot hand over its bytes only when durable.
type memKeyLaneStore struct {
	mu     sync.Mutex
	allocs []*models.KeyLaneNonce
	txs    []*models.KeyLaneTx
}

func newMemKeyLaneStore() *memKeyLaneStore { return &memKeyLaneStore{} }

func (m *memKeyLaneStore) Durable() bool { return false }

func (m *memKeyLaneStore) WithLock(_ context.Context, chainID int64, address string, fn func(q KeyLaneTxn) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return fn(&memLaneTxn{m: m, chainID: chainID, address: strings.ToLower(address)})
}

func (m *memKeyLaneStore) FinalHash(_ context.Context, chainID int64, address, hash string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := &memLaneTxn{m: m, chainID: chainID, address: strings.ToLower(address)}
	for _, t := range m.txs {
		if strings.EqualFold(t.Hash, hash) && t.ChainID == chainID {
			for _, a := range m.allocs {
				if a.ID == t.AllocationID {
					if cur, _ := q.CurrentTx(a); cur != nil {
						return cur.Hash, nil
					}
				}
			}
		}
	}
	return hash, nil
}

func (m *memKeyLaneStore) Committed(_ context.Context, chainID int64, address string, fromNonce uint64) (*big.Int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return (&memLaneTxn{m: m, chainID: chainID, address: strings.ToLower(address)}).Committed(fromNonce)
}

func (m *memKeyLaneStore) MarkMined(_ context.Context, chainID int64, address string, mined uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	addr := strings.ToLower(address)
	for _, a := range m.allocs {
		if a.ChainID == chainID && a.Address == addr && uint64(a.Nonce) < mined && a.Status == models.KeyLaneNonceAllocated {
			a.Status = models.KeyLaneNonceMined
		}
	}
	for _, t := range m.txs {
		if t.ChainID == chainID && t.Address == addr && uint64(t.Nonce) < mined {
			t.RawTx = nil
			if t.Status == models.KeyLaneTxPending || t.Status == models.KeyLaneTxAccepted {
				t.Status = models.KeyLaneTxMined
			}
		}
	}
	return nil
}

type memLaneTxn struct {
	m       *memKeyLaneStore
	chainID int64
	address string
}

func (q *memLaneTxn) mine(a *models.KeyLaneNonce) bool {
	return a.ChainID == q.chainID && a.Address == q.address
}

func (q *memLaneTxn) AllocationAt(nonce uint64) (*models.KeyLaneNonce, error) {
	for _, a := range q.m.allocs {
		if q.mine(a) && uint64(a.Nonce) == nonce {
			return a, nil
		}
	}
	return nil, nil
}

func (q *memLaneTxn) HasAllocationAbove(nonce uint64) (bool, error) {
	for _, a := range q.m.allocs {
		if q.mine(a) && uint64(a.Nonce) > nonce && a.Status == models.KeyLaneNonceAllocated {
			return true, nil
		}
	}
	return false, nil
}

func (q *memLaneTxn) Allocate(from uint64, fp, role string, at time.Time) (*models.KeyLaneNonce, error) {
	var rows []models.KeyLaneNonce
	for _, a := range q.m.allocs {
		if q.mine(a) {
			rows = append(rows, *a)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Nonce < rows[j].Nonce })
	return q.AllocateExact(nextFreeNonce(from, rows), fp, role, at)
}

func (q *memLaneTxn) AllocateExact(nonce uint64, fp, role string, at time.Time) (*models.KeyLaneNonce, error) {
	if a, _ := q.AllocationAt(nonce); a != nil {
		if a.Status != models.KeyLaneNonceReleased {
			return nil, fmt.Errorf("allocate nonce %d: already allocated", nonce)
		}
		a.Fingerprint, a.Status, a.Role, a.ActedAt = fp, models.KeyLaneNonceAllocated, role, at
		a.AcceptedHash, a.AcceptedPrice, a.Bumps, a.Rebroadcasts, a.AcceptedBumps = "", "", 0, 0, 0
		return a, nil
	}
	a := &models.KeyLaneNonce{ID: uuid.New(), ChainID: q.chainID, Address: q.address, Nonce: int64(nonce), Fingerprint: fp,
		Status: models.KeyLaneNonceAllocated, Role: role, ActedAt: at, CreatedAt: at}
	q.m.allocs = append(q.m.allocs, a)
	return a, nil
}

func (q *memLaneTxn) Release(id uuid.UUID) error {
	for _, a := range q.m.allocs {
		if a.ID == id {
			a.Status = models.KeyLaneNonceReleased
		}
	}
	return nil
}

func (q *memLaneTxn) RecordTx(row *models.KeyLaneTx) error {
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	row.ChainID, row.Address = q.chainID, q.address
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now()
	}
	cp := *row
	q.m.txs = append(q.m.txs, &cp)
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

func (q *memLaneTxn) CurrentTx(alloc *models.KeyLaneNonce) (*models.KeyLaneTx, error) {
	var latest *models.KeyLaneTx
	for _, t := range q.m.txs {
		if t.AllocationID != alloc.ID {
			continue
		}
		if alloc.AcceptedHash != "" {
			if t.Hash == alloc.AcceptedHash {
				c := *t
				return &c, nil
			}
			continue
		}
		if t.Status == models.KeyLaneTxPending {
			latest = t
		}
	}
	if latest == nil {
		return nil, nil
	}
	c := *latest
	return &c, nil
}

func (q *memLaneTxn) FirstTx(alloc *models.KeyLaneNonce) (*models.KeyLaneTx, error) {
	for _, t := range q.m.txs {
		if t.AllocationID == alloc.ID {
			c := *t
			return &c, nil
		}
	}
	return nil, nil
}

func (q *memLaneTxn) Accept(alloc *models.KeyLaneNonce, tx *models.KeyLaneTx) error {
	for _, t := range q.m.txs {
		if t.AllocationID != alloc.ID {
			continue
		}
		switch {
		case t.ID == tx.ID:
			t.Status = models.KeyLaneTxAccepted
		case t.Status == models.KeyLaneTxPending || t.Status == models.KeyLaneTxAccepted:
			t.Status = models.KeyLaneTxReplaced
		}
	}
	alloc.AcceptedHash, alloc.AcceptedPrice, alloc.AcceptedBumps = tx.Hash, tx.GasPrice, alloc.Bumps
	if a, _ := q.AllocationAt(uint64(alloc.Nonce)); a != nil {
		a.AcceptedHash, a.AcceptedPrice, a.AcceptedBumps = tx.Hash, tx.GasPrice, alloc.Bumps
	}
	return nil
}

func (q *memLaneTxn) Touch(alloc *models.KeyLaneNonce, bumps, rebroadcasts int, at time.Time) error {
	alloc.Bumps, alloc.Rebroadcasts, alloc.ActedAt = bumps, rebroadcasts, at
	if a, _ := q.AllocationAt(uint64(alloc.Nonce)); a != nil {
		a.Bumps, a.Rebroadcasts, a.ActedAt = bumps, rebroadcasts, at
	}
	return nil
}

func (q *memLaneTxn) Committed(fromNonce uint64) (*big.Int, error) {
	sum := new(big.Int)
	for _, a := range q.m.allocs {
		if q.mine(a) && uint64(a.Nonce) >= fromNonce && a.Status == models.KeyLaneNonceAllocated {
			tx, _ := q.CurrentTx(a)
			addSpend(sum, tx)
		}
	}
	return sum, nil
}
