package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// The vault ledger (spec 2026-10-14 §4.4): every vault event, indexed once it
// is final, then reconciled against the vault's own books at the same block.

// VaultLogReader is the pool chain's read surface the indexer needs;
// *ethclient.Client is one.
type VaultLogReader interface {
	BlockNumber(ctx context.Context) (uint64, error)
	FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error)
	HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error)
}

// VaultBooks is what the reconciler reads of the vault.
type VaultBooks interface {
	Address() string
	StateAt(ctx context.Context, block uint64) (*VaultState, error)
	DecodeVaultLog(l types.Log) (VaultEvent, bool)
}

var _ VaultBooks = (*LiquidityVault)(nil)

type VaultIndexerConfig struct {
	DB          *gorm.DB
	ChainID     int64
	Vault       VaultBooks
	Reader      VaultLogReader
	DeployBlock uint64
	Chunk       uint64 // blocks per eth_getLogs (the preset's LogChunkBlocks)
	Finality    uint64 // blocks behind head indexed (never a reorg to undo)
	// MaxChunks bounds one tick's catch-up; the next tick resumes.
	MaxChunks int
}

type VaultIndexer struct {
	c VaultIndexerConfig
}

func NewVaultIndexer(c VaultIndexerConfig) (*VaultIndexer, error) {
	if c.DB == nil || c.Vault == nil || c.Reader == nil || c.DeployBlock == 0 {
		return nil, errors.New("liquidity indexer: missing a part")
	}
	if c.Chunk == 0 {
		c.Chunk = 2000
	}
	if c.Finality == 0 {
		c.Finality = 5
	}
	if c.MaxChunks <= 0 {
		c.MaxChunks = 50
	}
	return &VaultIndexer{c: c}, nil
}

func (x *VaultIndexer) vault() string { return strings.ToLower(x.c.Vault.Address()) }

// Cursor is the next block to index (every block below it is in the ledger).
func (x *VaultIndexer) Cursor(ctx context.Context) (uint64, error) {
	var cur models.LiquidityIndexCursor
	err := x.c.DB.WithContext(ctx).Where("chain_id = ? AND vault = ?", x.c.ChainID, x.vault()).Limit(1).Find(&cur).Error
	if err != nil {
		return 0, err
	}
	if cur.NextBlock == 0 {
		return x.c.DeployBlock, nil
	}
	return cur.NextBlock, nil
}

// IndexOnce indexes final blocks from the cursor, a chunk at a time, each
// chunk's rows and the cursor in one transaction. Returns the rows written.
func (x *VaultIndexer) IndexOnce(ctx context.Context) (int, error) {
	head, err := x.c.Reader.BlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("liquidity indexer: head: %w", err)
	}
	if head < x.c.Finality {
		return 0, nil
	}
	final := head - x.c.Finality
	from, err := x.Cursor(ctx)
	if err != nil {
		return 0, err
	}
	written := 0
	addr := common.HexToAddress(x.c.Vault.Address())
	for i := 0; i < x.c.MaxChunks && from <= final; i++ {
		to := min(from+x.c.Chunk-1, final)
		logs, err := x.c.Reader.FilterLogs(ctx, ethereum.FilterQuery{
			FromBlock: new(big.Int).SetUint64(from), ToBlock: new(big.Int).SetUint64(to),
			Addresses: []common.Address{addr}, Topics: [][]common.Hash{VaultEventTopics()},
		})
		if err != nil {
			return written, fmt.Errorf("liquidity indexer: logs %d-%d: %w", from, to, err)
		}
		rows, err := x.rowsFor(ctx, logs)
		if err != nil {
			return written, err
		}
		next := to + 1
		err = x.c.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if len(rows) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error; err != nil {
					return err
				}
			}
			return tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "chain_id"}, {Name: "vault"}},
				DoUpdates: clause.Assignments(map[string]any{"next_block": next, "updated_at": time.Now()}),
			}).Create(&models.LiquidityIndexCursor{ChainID: x.c.ChainID, Vault: x.vault(), NextBlock: next}).Error
		})
		if err != nil {
			return written, err
		}
		written += len(rows)
		from = next
	}
	return written, nil
}

func (x *VaultIndexer) rowsFor(ctx context.Context, logs []types.Log) ([]models.LiquidityLedgerEntry, error) {
	times := map[uint64]time.Time{}
	var rows []models.LiquidityLedgerEntry
	for _, l := range logs {
		if l.Removed {
			continue
		}
		ev, ok := x.c.Vault.DecodeVaultLog(l)
		if !ok {
			continue
		}
		at, ok := times[l.BlockNumber]
		if !ok {
			h, err := x.c.Reader.HeaderByNumber(ctx, new(big.Int).SetUint64(l.BlockNumber))
			if err != nil || h == nil {
				return nil, fmt.Errorf("liquidity indexer: header %d: %v", l.BlockNumber, err)
			}
			at = time.Unix(int64(h.Time), 0).UTC()
			times[l.BlockNumber] = at
		}
		row := models.LiquidityLedgerEntry{ID: uuid.New(), ChainID: x.c.ChainID, Vault: x.vault(), TxHash: strings.ToLower(ev.TxHash),
			LogIndex: ev.LogIndex, Block: ev.Block, Kind: ev.Kind, Account: ev.Account, To: ev.To, Ref: ev.Ref, BlockTime: at}
		if ev.Amount != nil {
			a := ev.Amount.String()
			row.Amount = &a
		}
		if ev.Spread != nil {
			sp := ev.Spread.String()
			row.Spread = &sp
		}
		if ev.Kind == VaultEventDepositorSet || ev.Kind == VaultEventOperatorSet {
			allowed := ev.Allowed
			row.Allowed = &allowed
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// LedgerTotals are the vault's books as the ledger rebuilds them.
type LedgerTotals struct {
	Principal, Collateral, Outstanding *big.Int
	Fills                              map[string]bool // fill refs seen
}

// Totals sums the ledger up to and including block.
func (x *VaultIndexer) Totals(ctx context.Context, block uint64) (*LedgerTotals, error) {
	var rows []models.LiquidityLedgerEntry
	if err := x.c.DB.WithContext(ctx).Where("chain_id = ? AND vault = ? AND block <= ?", x.c.ChainID, x.vault(), block).
		Select("kind", "amount", "spread", "ref").Find(&rows).Error; err != nil {
		return nil, err
	}
	t := &LedgerTotals{Principal: new(big.Int), Collateral: new(big.Int), Outstanding: new(big.Int), Fills: map[string]bool{}}
	for _, r := range rows {
		amt, spread := bigFromDecimal(r.Amount), bigFromDecimal(r.Spread)
		switch r.Kind {
		case VaultEventDeposited:
			t.Principal.Add(t.Principal, amt)
		case VaultEventWithdrawn:
			t.Principal.Sub(t.Principal, amt)
		case VaultEventCollateralPosted:
			t.Collateral.Add(t.Collateral, amt)
		case VaultEventCollateralWithdrawn:
			t.Collateral.Sub(t.Collateral, amt)
		case VaultEventFilled:
			t.Outstanding.Add(t.Outstanding, amt).Add(t.Outstanding, spread)
			t.Fills[strings.ToLower(r.Ref)] = true
		case VaultEventRefilled:
			t.Outstanding.Sub(t.Outstanding, amt)
		case VaultEventSettled:
			t.Outstanding.Sub(t.Outstanding, amt)
			t.Collateral.Sub(t.Collateral, amt)
		}
	}
	return t, nil
}

// VaultReconcileReport is one reconcile's findings (empty Drift = clean).
type VaultReconcileReport struct {
	Block        uint64
	Drift        []string
	UnknownFills []string // fill refs that match no order of ours
	Utilisation  float64  // outstanding / collateral
}

// poolVaultHighUseBps: outstanding above this share of collateral alerts —
// the PM should refill or post more before the pool stops selling (spec O5).
const poolVaultHighUseBps = 8_000

// Reconcile compares the ledger with the vault's books at the last indexed
// block, and every on-chain fill with our orders. Every finding is an ALERT.
func (x *VaultIndexer) Reconcile(ctx context.Context) (*VaultReconcileReport, error) {
	cursor, err := x.Cursor(ctx)
	if err != nil {
		return nil, err
	}
	if cursor <= x.c.DeployBlock {
		return &VaultReconcileReport{}, nil // nothing indexed yet
	}
	block := cursor - 1
	st, err := x.c.Vault.StateAt(ctx, block)
	if err != nil {
		return nil, err
	}
	tot, err := x.Totals(ctx, block)
	if err != nil {
		return nil, err
	}
	rep := &VaultReconcileReport{Block: block}
	for _, c := range []struct {
		name          string
		chain, ledger *big.Int
	}{{"outstanding", st.Outstanding, tot.Outstanding}, {"collateral", st.Collateral, tot.Collateral}, {"principal", st.TotalPrincipal, tot.Principal}} {
		if c.chain.Cmp(c.ledger) != 0 {
			rep.Drift = append(rep.Drift, fmt.Sprintf("%s chain=%s ledger=%s", c.name, c.chain, c.ledger))
		}
	}
	if st.Collateral.Sign() > 0 {
		use := new(big.Int).Mul(st.Outstanding, big.NewInt(10_000))
		use.Quo(use, st.Collateral)
		rep.Utilisation = float64(use.Int64()) / 10_000
		if use.Int64() >= poolVaultHighUseBps {
			alertf("liquidity_vault_high_use", "[liquidity] ALERT liquidity_vault_high_use outstanding=%s collateral=%s use=%.1f%% — refill or post collateral before the pool stops selling",
				st.Outstanding, st.Collateral, rep.Utilisation*100)
		}
	}
	// Every fill must be one of our orders: an unknown ref is the operator key
	// spending collateral outside the order path.
	ours, err := x.ourFillRefs(ctx)
	if err != nil {
		return nil, err
	}
	for ref := range tot.Fills {
		if !ours[ref] {
			rep.UnknownFills = append(rep.UnknownFills, ref)
		}
	}
	for _, d := range rep.Drift {
		alertf("liquidity_vault_drift:"+d, "[liquidity] ALERT liquidity_vault_ledger_drift block=%d %s", block, d)
	}
	for _, ref := range rep.UnknownFills {
		alertf("liquidity_vault_unknown_fill:"+ref, "[liquidity] ALERT liquidity_vault_unknown_fill ref=%s block<=%d — a fill matching no pool order: check the operator key", ref, block)
	}
	return rep, nil
}

// ourFillRefs are the fill refs of every vault order we ever recorded.
func (x *VaultIndexer) ourFillRefs(ctx context.Context) (map[string]bool, error) {
	var ids []uuid.UUID
	if err := x.c.DB.WithContext(ctx).Model(&models.PoolOrder{}).Where("fill_units IS NOT NULL").
		Distinct("intent_id").Pluck("intent_id", &ids).Error; err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[strings.ToLower(HashIDHex(id))] = true
	}
	return out, nil
}
