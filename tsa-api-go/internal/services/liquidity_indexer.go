package services

import (
	"context"
	"errors"
	"fmt"
	"log"
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
	// Treasury is the conversion treasury, the only address our fills pay.
	// Empty (no order desk) skips the destination check; SetVaultLedger fills
	// it from the desk.
	Treasury string
}

type VaultIndexer struct {
	c        VaultIndexerConfig
	fiatRate FiatRateFunc
	now      func() time.Time // tests
	decimals int
}

// SetTreasury names the address every one of our fills must pay. Boot-time
// only (before the indexer job runs).
func (x *VaultIndexer) SetTreasury(addr string) {
	if x != nil {
		x.c.Treasury = addr
	}
}

// FiatRateFunc is the fiat per 1 USD of the pool token (USDT = $1).
type FiatRateFunc func(ctx context.Context, currency string) (*big.Rat, error)

// SetFiat lets the indexer lock a fiat LP's deposits at the rate of the day.
// Boot-time only. Without it fiat LPs' deposits are indexed unstamped.
func (x *VaultIndexer) SetFiat(rate FiatRateFunc, tokenDecimals int) {
	if x != nil {
		x.fiatRate, x.decimals = rate, tokenDecimals
	}
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
	rates := map[string]*big.Rat{}
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
		if ev.Kind == VaultEventDeposited && ev.Amount != nil {
			if err := x.stampFiat(ctx, &row, ev.Amount, rates); err != nil {
				return nil, err
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// fiatStampMaxLag bounds how stale a deposit may be and still be locked at
// the current rate. Past it the deposit is indexed unstamped (ALERT) rather
// than priced at a rate it never had; inside it, an unreadable rate fails the
// tick so the next one retries (a stall of at most this long).
const fiatStampMaxLag = 30 * time.Minute

// stampFiat locks a deposit by a fiat LP at the current rate; rates holds the
// rate read earlier in the same batch.
func (x *VaultIndexer) stampFiat(ctx context.Context, row *models.LiquidityLedgerEntry, amount *big.Int, rates map[string]*big.Rat) error {
	if x.fiatRate == nil || row.Account == "" {
		return nil
	}
	var cur string
	err := x.c.DB.WithContext(ctx).Model(&models.PaymentMerchant{}).
		Where("kind = ? AND LOWER(operator_wallet_address) = ? AND fiat_currency <> ''", models.PaymentMerchantKindLP, strings.ToLower(row.Account)).
		Limit(1).Pluck("fiat_currency", &cur).Error
	if err != nil {
		return fmt.Errorf("liquidity indexer: fiat mode of %s: %w", row.Account, err)
	}
	if cur == "" {
		return nil
	}
	now := time.Now
	if x.now != nil {
		now = x.now
	}
	if lag := now().Sub(row.BlockTime); lag > fiatStampMaxLag {
		log.Printf("[liquidity] ALERT liquidity_fiat_stamp_missed=1 account=%s tx=%s lag=%s — deposit indexed unstamped; set its rate by hand",
			row.Account, row.TxHash, lag.Round(time.Minute))
		return nil
	}
	rate := rates[cur]
	if rate == nil {
		r, err := x.fiatRate(ctx, cur)
		if err != nil || r == nil || r.Sign() <= 0 {
			return fmt.Errorf("liquidity indexer: %s rate for a fiat LP deposit: %v", cur, err)
		}
		rate, rates[cur] = r, r
	}
	row.FiatCurrency = cur
	rs, as := rate.FloatString(8), FiatOf(amount, x.decimals, rate)
	row.FiatRate, row.FiatAmount = &rs, &as
	return nil
}

// FiatOf is amount (token units) in fiat at rate, rounded to 2 dp.
func FiatOf(amount *big.Int, decimals int, rate *big.Rat) string {
	v := new(big.Rat).SetFrac(amount, pow10(decimals))
	return v.Mul(v, rate).FloatString(2)
}

// LedgerTotals are the vault's books as the ledger rebuilds them. Unclaimed
// is Σspread credited − Σclaimed (at least what LPs can still claim: the
// contract's per-share accrual floors in the pool's favour).
type LedgerTotals struct {
	Principal, Outstanding, PendingWithdrawals, Unclaimed *big.Int
	Fills                                                 map[string]bool // fill refs seen
	FillRows                                              []LedgerFill    // every Filled event, in ledger order
}

// LedgerFill is one Filled event as the ledger holds it.
type LedgerFill struct {
	Ref, To, TxHash string
	Amount, Spread  *big.Int
}

// Totals sums the ledger up to and including block.
func (x *VaultIndexer) Totals(ctx context.Context, block uint64) (*LedgerTotals, error) {
	var rows []models.LiquidityLedgerEntry
	if err := x.c.DB.WithContext(ctx).Where("chain_id = ? AND vault = ? AND block <= ?", x.c.ChainID, x.vault(), block).
		Order("block, log_index").Find(&rows).Error; err != nil {
		return nil, err
	}
	t := &LedgerTotals{Principal: new(big.Int), Outstanding: new(big.Int), PendingWithdrawals: new(big.Int), Unclaimed: new(big.Int),
		Fills: map[string]bool{}}
	for _, r := range rows {
		amt, spread := bigFromDecimal(r.Amount), bigFromDecimal(r.Spread)
		switch r.Kind {
		case VaultEventDeposited:
			t.Principal.Add(t.Principal, amt)
		case VaultEventWithdrawn: // paid at once
			t.Principal.Sub(t.Principal, amt)
		case VaultEventWithdrawalPending: // withdrawn, owed later: no longer principal
			t.Principal.Sub(t.Principal, amt)
			t.PendingWithdrawals.Add(t.PendingWithdrawals, amt)
		case VaultEventWithdrawalPaid:
			t.PendingWithdrawals.Sub(t.PendingWithdrawals, amt)
		case VaultEventClaimed:
			t.Unclaimed.Sub(t.Unclaimed, amt)
		case VaultEventFilled:
			t.Outstanding.Add(t.Outstanding, amt).Add(t.Outstanding, spread)
			t.Unclaimed.Add(t.Unclaimed, spread)
			t.Fills[strings.ToLower(r.Ref)] = true
			t.FillRows = append(t.FillRows, LedgerFill{Ref: strings.ToLower(r.Ref), To: r.To, TxHash: r.TxHash, Amount: amt, Spread: spread})
		case VaultEventRefilled, vaultEventLegacySettled:
			t.Outstanding.Sub(t.Outstanding, amt)
		}
	}
	return t, nil
}

// VaultReconcileReport is one reconcile's findings (empty Drift = clean).
type VaultReconcileReport struct {
	Block        uint64   `json:"block"`
	Drift        []string `json:"drift"`
	UnknownFills []string `json:"unknownFills"` // fill refs that match no order of ours
	// MismatchedFills: a fill under one of our refs whose amount, spread or
	// destination is not that order's, or whose order never sent a fill.
	MismatchedFills []string `json:"mismatchedFills"`
	Utilisation     float64  `json:"utilisation"` // outstanding / (principal + pending withdrawals)
}

// poolVaultHighUseBps: outstanding at this share of what the pool owes LPs
// (principal + pending withdrawals) alerts — the company should refill.
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
	}{{"outstanding", st.Outstanding, tot.Outstanding}, {"principal", st.TotalPrincipal, tot.Principal},
		{"pending_withdrawals", st.TotalPendingWithdrawals, tot.PendingWithdrawals}} {
		if c.chain.Cmp(c.ledger) != 0 {
			rep.Drift = append(rep.Drift, fmt.Sprintf("%s chain=%s ledger=%s", c.name, c.chain, c.ledger))
		}
	}
	// The contract's invariant: balance + outstanding = principal + unclaimed
	// + pending withdrawals (dust and stray transfers only ever in the pool's
	// favour). Short of it, tokens left the vault outside its books.
	held := new(big.Int).Add(st.Balance, st.Outstanding)
	owed := new(big.Int).Add(st.TotalPrincipal, tot.Unclaimed)
	owed.Add(owed, st.TotalPendingWithdrawals)
	if held.Cmp(owed) < 0 {
		rep.Drift = append(rep.Drift, fmt.Sprintf("invariant balance+outstanding=%s < principal+unclaimed+pending=%s", held, owed))
	}
	if toLPs := new(big.Int).Add(st.TotalPrincipal, st.TotalPendingWithdrawals); toLPs.Sign() > 0 {
		use := new(big.Int).Mul(st.Outstanding, big.NewInt(10_000))
		use.Quo(use, toLPs)
		rep.Utilisation = float64(use.Int64()) / 10_000
		if use.Int64() >= poolVaultHighUseBps {
			alertf("liquidity_vault_high_use", "[liquidity] ALERT liquidity_vault_high_use outstanding=%s principal=%s pending=%s use=%.1f%% — refill the pool before it stops selling",
				st.Outstanding, st.TotalPrincipal, st.TotalPendingWithdrawals, rep.Utilisation*100)
		}
	}
	// A pending withdrawal the pool can pay at least part of now: payPending
	// pays min(pending, balance), so any token balance is worth sending (liquid
	// stays 0 until every pending one is covered). An admin should pay it
	// (console: Pay pending).
	if st.TotalPendingWithdrawals.Sign() > 0 && st.Balance.Sign() > 0 {
		alertf("liquidity_pending_withdrawals", "[liquidity] ALERT liquidity_pending_withdrawals pending=%s balance=%s block=%d — pay pending withdrawals from the console",
			st.TotalPendingWithdrawals, st.Balance, block)
	}
	// Every fill must be one of our orders, exactly as that order sent it: an
	// unknown ref, or a fill of ours with another amount, spread or
	// destination, is the operator key spending LP liquidity outside the
	// order path. Only final blocks are in the ledger, so a reorg never reports.
	ours, err := x.ourFills(ctx)
	if err != nil {
		return nil, err
	}
	for _, f := range tot.FillRows {
		o, ok := ours[f.Ref]
		if !ok {
			rep.UnknownFills = append(rep.UnknownFills, f.Ref)
			continue
		}
		if why := x.fillMismatch(f, o); why != "" {
			rep.MismatchedFills = append(rep.MismatchedFills, f.Ref+" "+why)
		}
	}
	for _, d := range rep.Drift {
		alertf("liquidity_vault_drift:"+d, "[liquidity] ALERT liquidity_vault_ledger_drift block=%d %s", block, d)
	}
	for _, ref := range rep.UnknownFills {
		alertf("liquidity_vault_unknown_fill:"+ref, "[liquidity] ALERT liquidity_vault_unknown_fill ref=%s block<=%d — a fill matching no pool order: check the operator key", ref, block)
	}
	for _, m := range rep.MismatchedFills {
		alertf("liquidity_vault_fill_mismatch:"+m, "[liquidity] ALERT liquidity_vault_fill_mismatch %s block<=%d — a fill under our ref that is not the order's: check the operator key", m, block)
	}
	return rep, nil
}

// fillMismatch is why on-chain fill f is not order o's fill ("" when it is):
// the order must have sent a fill (hash recorded or proven), for its fill
// units, its spread (as sent, or the snapshot's), to the treasury.
func (x *VaultIndexer) fillMismatch(f LedgerFill, o models.PoolOrder) string {
	switch {
	case o.FillTxHash == "" && o.FilledAt == nil:
		return "order=" + o.IntentID.String() + " has no fill recorded"
	case f.Amount == nil || f.Amount.Cmp(bigFromDecimal(o.FillUnits)) != 0:
		return fmt.Sprintf("order=%s amount=%v want=%s", o.IntentID, f.Amount, bigFromDecimal(o.FillUnits))
	case f.Spread == nil || (f.Spread.Cmp(bigFromDecimal(o.FillSpreadUnits)) != 0 && f.Spread.Cmp(bigFromDecimal(o.SpreadUnits)) != 0):
		return fmt.Sprintf("order=%s spread=%v", o.IntentID, f.Spread)
	case x.c.Treasury != "" && !SameEVMAddress(f.To, x.c.Treasury):
		return fmt.Sprintf("order=%s to=%s", o.IntentID, f.To)
	}
	return ""
}

// ourFills are every vault order we ever recorded, by fill ref.
func (x *VaultIndexer) ourFills(ctx context.Context) (map[string]models.PoolOrder, error) {
	var rows []models.PoolOrder
	if err := x.c.DB.WithContext(ctx).Model(&models.PoolOrder{}).Where("fill_units IS NOT NULL AND generation = 1").
		Select("intent_id", "fill_units", "spread_units", "fill_spread_units", "fill_tx_hash", "filled_at").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]models.PoolOrder, len(rows))
	for _, r := range rows {
		out[strings.ToLower(HashIDHex(r.IntentID))] = r
	}
	return out, nil
}
