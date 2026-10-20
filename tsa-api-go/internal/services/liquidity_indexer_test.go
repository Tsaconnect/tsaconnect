package services

import (
	"context"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

type fakeLogReader struct {
	head    uint64
	logs    []types.Log
	queries int
}

func (f *fakeLogReader) BlockNumber(context.Context) (uint64, error) { return f.head, nil }
func (f *fakeLogReader) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	f.queries++
	var out []types.Log
	for _, l := range f.logs {
		if l.BlockNumber >= q.FromBlock.Uint64() && l.BlockNumber <= q.ToBlock.Uint64() {
			out = append(out, l)
		}
	}
	return out, nil
}
func (f *fakeLogReader) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	return &types.Header{Number: n, Time: 1_760_000_000 + n.Uint64()}, nil
}

type fakeBooks struct {
	*LiquidityVault
	state VaultState
}

func (b *fakeBooks) Address() string { return testVaultAddr.Hex() }
func (b *fakeBooks) StateAt(_ context.Context, block uint64) (*VaultState, error) {
	st := b.state
	st.Block = block
	return &st, nil
}

func ledgerLog(t *testing.T, block uint64, idx uint, name string, topics []common.Hash, data ...any) types.Log {
	l := vaultLog(t, name, topics, data...)
	l.BlockNumber, l.Index = block, idx
	l.TxHash = common.BigToHash(new(big.Int).SetUint64(block*1000 + uint64(idx)))
	return l
}

func newIndexerRig(t *testing.T, logs ...types.Log) (*VaultIndexer, *fakeLogReader, *fakeBooks) {
	t.Helper()
	return newIndexerRigOn(t, setupTradeDB(t), logs...)
}

// newIndexerRigOn is the indexer over an existing test database.
func newIndexerRigOn(t *testing.T, db *gorm.DB, logs ...types.Log) (*VaultIndexer, *fakeLogReader, *fakeBooks) {
	t.Helper()
	if err := db.AutoMigrate(&models.LiquidityLedgerEntry{}, &models.LiquidityIndexCursor{}, &models.PoolOrder{}); err != nil {
		t.Fatal(err)
	}
	reader := &fakeLogReader{head: 1_000, logs: logs}
	books := &fakeBooks{LiquidityVault: &LiquidityVault{addr: testVaultAddr},
		state: VaultState{Balance: new(big.Int), TotalPendingWithdrawals: new(big.Int), Outstanding: new(big.Int), TotalPrincipal: new(big.Int)}}
	x, err := NewVaultIndexer(VaultIndexerConfig{DB: db, ChainID: 97, Vault: books, Reader: reader, DeployBlock: 100, Chunk: 200})
	if err != nil {
		t.Fatal(err)
	}
	return x, reader, books
}

func lpTopic(a string) common.Hash { return common.BytesToHash(common.HexToAddress(a).Bytes()) }

const (
	ledgerLP       = "0x00000000000000000000000000000000000000cc"
	ledgerTreasury = "0x00000000000000000000000000000000000000bb"
)

// Chunked from the deploy block, only final blocks, idempotent across ticks.
func TestVaultIndexer_IndexesFinalBlocksOnceInChunks(t *testing.T) {
	x, reader, _ := newIndexerRig(t,
		ledgerLog(t, 150, 0, "Deposited", []common.Hash{lpTopic(ledgerLP)}, usdt18(25)),
		ledgerLog(t, 640, 1, "WithdrawalPending", []common.Hash{lpTopic(ledgerLP)}, usdt18(5)),
		ledgerLog(t, 997, 0, "WithdrawalPaid", []common.Hash{lpTopic(ledgerLP)}, usdt18(5)), // inside the 5-block finality window
	)
	n, err := x.IndexOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("indexed = %d %v, want the two final events", n, err)
	}
	if reader.queries != 5 { // 100..995 in 200-block chunks
		t.Fatalf("queries = %d, want 5 chunks", reader.queries)
	}
	if cur, _ := x.Cursor(context.Background()); cur != 996 {
		t.Fatalf("cursor = %d, want 996", cur)
	}
	reader.head = 1_010
	if n, err := x.IndexOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("second tick indexed = %d %v, want the now-final event only", n, err)
	}
	var rows int64
	x.c.DB.Model(&models.LiquidityLedgerEntry{}).Count(&rows)
	if rows != 3 {
		t.Fatalf("rows = %d, want 3", rows)
	}
}

// Rebuilt books equal the chain's: clean (the invariant balance + outstanding
// = principal + unclaimed + pending holds). A fill of ours is known; a fill
// matching no order is reported (the operator key spent outside the path).
// Amounts are small integers: SQLite keeps numerics above int64 as REAL
// (production Postgres is exact), and the ledger is unit-agnostic.
func TestVaultIndexer_ReconcileCleanAndUnknownFill(t *testing.T) {
	ours := uuid.New()
	ourRef, strayRef := HashID(ours), HashID(uuid.New())
	tr := lpTopic(ledgerTreasury)
	x, _, books := newIndexerRig(t,
		ledgerLog(t, 150, 0, "Deposited", []common.Hash{lpTopic(ledgerLP)}, big.NewInt(225)),
		ledgerLog(t, 170, 0, "Filled", []common.Hash{common.Hash(ourRef), tr}, big.NewInt(100), big.NewInt(1)),
		ledgerLog(t, 180, 0, "Filled", []common.Hash{common.Hash(strayRef), tr}, big.NewInt(10), big.NewInt(0)),
		ledgerLog(t, 190, 0, "Refilled", []common.Hash{common.Hash(ourRef)}, big.NewInt(50)),
	)
	fill, spread := "100", "1"
	x.c.DB.Create(&models.PoolOrder{IntentID: ours, Generation: 1, ChainID: 97, Token: "USDT", TokenDecimals: 18,
		MidMinor: "0", LPRateMinor: "0", DisplayRateMinor: "0", OpenedUnits: "0", SurplusUnits: "0", FillUnits: &fill,
		SpreadUnits: &spread, FillSpreadUnits: &spread, FillTxHash: "0xfill"})
	x.SetTreasury(ledgerTreasury)
	if _, err := x.IndexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 101 + 10 − 50 = 61 outstanding; balance 225 − 110 + 50 = 165; 225
	// principal, 1 unclaimed: 165 + 61 = 225 + 1.
	books.state = VaultState{Balance: big.NewInt(165), TotalPendingWithdrawals: new(big.Int), Outstanding: big.NewInt(61), TotalPrincipal: big.NewInt(225)}
	rep, err := x.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Drift) != 0 {
		t.Fatalf("drift = %v, want none", rep.Drift)
	}
	if len(rep.UnknownFills) != 1 || rep.UnknownFills[0] != "0x"+common.Bytes2Hex(strayRef[:]) {
		t.Fatalf("unknown fills = %v, want only the stray ref", rep.UnknownFills)
	}
	if len(rep.MismatchedFills) != 0 {
		t.Fatalf("mismatched fills = %v, want none", rep.MismatchedFills)
	}
}

// M5: a fill under one of our refs must be that order's fill: its amount, its
// spread, to the treasury, from an order that sent one. Anything else is the
// operator key spending outside the order path.
func TestVaultIndexer_ReconcileReportsMismatchedFills(t *testing.T) {
	tr, stranger := lpTopic(ledgerTreasury), lpTopic(ledgerLP)
	ids := map[string]uuid.UUID{"amount": uuid.New(), "spread": uuid.New(), "to": uuid.New(), "unsent": uuid.New(), "good": uuid.New()}
	ref := func(k string) common.Hash { return common.Hash(HashID(ids[k])) }
	x, _, books := newIndexerRig(t,
		ledgerLog(t, 160, 0, "Deposited", []common.Hash{lpTopic(ledgerLP)}, big.NewInt(1000)),
		ledgerLog(t, 170, 0, "Filled", []common.Hash{ref("amount"), tr}, big.NewInt(99), big.NewInt(1)),
		ledgerLog(t, 171, 0, "Filled", []common.Hash{ref("spread"), tr}, big.NewInt(100), big.NewInt(7)),
		ledgerLog(t, 172, 0, "Filled", []common.Hash{ref("to"), stranger}, big.NewInt(100), big.NewInt(1)),
		ledgerLog(t, 173, 0, "Filled", []common.Hash{ref("unsent"), tr}, big.NewInt(100), big.NewInt(1)),
		ledgerLog(t, 174, 0, "Filled", []common.Hash{ref("good"), tr}, big.NewInt(100), big.NewInt(0)),
	)
	for k, id := range ids {
		fill, spread, sent := "100", "1", "0"
		o := models.PoolOrder{IntentID: id, Generation: 1, ChainID: 97, Token: "USDT", TokenDecimals: 18,
			MidMinor: "0", LPRateMinor: "0", DisplayRateMinor: "0", OpenedUnits: "0", SurplusUnits: "0",
			FillUnits: &fill, SpreadUnits: &spread, FillSpreadUnits: &sent, FillTxHash: "0x" + k}
		if k == "unsent" {
			o.FillTxHash, o.FillSpreadUnits = "", nil
		}
		x.c.DB.Create(&o)
	}
	x.SetTreasury(ledgerTreasury)
	if _, err := x.IndexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	// balance 1000 − 499 = 501; 501 + 509 = 1000 principal + 10 unclaimed.
	books.state = VaultState{Balance: big.NewInt(501), TotalPendingWithdrawals: new(big.Int), Outstanding: big.NewInt(509), TotalPrincipal: big.NewInt(1000)}
	rep, err := x.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Drift) != 0 || len(rep.UnknownFills) != 0 {
		t.Fatalf("drift=%v unknown=%v, want none", rep.Drift, rep.UnknownFills)
	}
	got := strings.Join(rep.MismatchedFills, "\n")
	for _, k := range []string{"amount", "spread", "to", "unsent"} {
		if !strings.Contains(got, ids[k].String()) {
			t.Fatalf("mismatched = %v, want the %s fill reported", rep.MismatchedFills, k)
		}
	}
	if strings.Contains(got, ids["good"].String()) || len(rep.MismatchedFills) != 4 {
		t.Fatalf("mismatched = %v, want exactly the four bad fills", rep.MismatchedFills)
	}
}

func TestVaultIndexer_ReconcileReportsDrift(t *testing.T) {
	x, _, books := newIndexerRig(t, ledgerLog(t, 150, 0, "Deposited", []common.Hash{lpTopic(ledgerLP)}, usdt18(25)))
	if _, err := x.IndexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	books.state = VaultState{Balance: usdt18(30), TotalPendingWithdrawals: new(big.Int), Outstanding: new(big.Int), TotalPrincipal: usdt18(30)}
	rep, err := x.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Drift) != 1 {
		t.Fatalf("drift = %v, want principal drift", rep.Drift)
	}
}

func TestVaultIndexer_NoUtilisationWithNothingOutstanding(t *testing.T) {
	x, _, books := newIndexerRig(t, ledgerLog(t, 150, 0, "Deposited", []common.Hash{lpTopic(ledgerLP)}, big.NewInt(100)))
	if _, err := x.IndexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	books.state = VaultState{Balance: big.NewInt(100), TotalPendingWithdrawals: new(big.Int), Outstanding: new(big.Int), TotalPrincipal: big.NewInt(100)}
	rep, _ := x.Reconcile(context.Background())
	if rep.Utilisation != 0 || len(rep.Drift) != 0 {
		t.Fatalf("use = %v drift = %v", rep.Utilisation, rep.Drift)
	}
}

// A withdraw the pool could not pay in full: Withdrawn (paid now) and
// WithdrawalPending both leave principal; WithdrawalPaid clears pending. The
// books balance with pending outstanding, and utilisation counts it.
func TestVaultIndexer_ReconcileWithPendingWithdrawals(t *testing.T) {
	ours := uuid.New()
	ref := HashID(ours)
	lp := []common.Hash{lpTopic(ledgerLP)}
	x, _, books := newIndexerRig(t,
		ledgerLog(t, 150, 0, "Deposited", lp, big.NewInt(100)),
		ledgerLog(t, 160, 0, "Filled", []common.Hash{common.Hash(ref), lpTopic(ledgerTreasury)}, big.NewInt(80), big.NewInt(2)),
		ledgerLog(t, 170, 0, "WithdrawalPending", lp, big.NewInt(50)), // withdraw 70: 20 paid now, 50 pending
		ledgerLog(t, 170, 1, "Withdrawn", lp, big.NewInt(20)),
		ledgerLog(t, 180, 0, "Refilled", []common.Hash{common.Hash(ref)}, big.NewInt(40)),
		ledgerLog(t, 190, 0, "WithdrawalPaid", lp, big.NewInt(30)),
	)
	fill, spread := "80", "2"
	x.c.DB.Create(&models.PoolOrder{IntentID: ours, Generation: 1, ChainID: 97, Token: "USDT", TokenDecimals: 18,
		MidMinor: "0", LPRateMinor: "0", DisplayRateMinor: "0", OpenedUnits: "0", SurplusUnits: "0", FillUnits: &fill,
		SpreadUnits: &spread, FillSpreadUnits: &spread, FillTxHash: "0xfill"})
	x.SetTreasury(ledgerTreasury)
	if _, err := x.IndexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	tot, err := x.Totals(context.Background(), 995)
	if err != nil {
		t.Fatal(err)
	}
	if tot.Principal.Int64() != 30 || tot.PendingWithdrawals.Int64() != 20 || tot.Outstanding.Int64() != 42 || tot.Unclaimed.Int64() != 2 {
		t.Fatalf("totals = principal %s pending %s outstanding %s unclaimed %s, want 30/20/42/2",
			tot.Principal, tot.PendingWithdrawals, tot.Outstanding, tot.Unclaimed)
	}
	// balance 100 − 80 − 20 + 40 − 30 = 10; 10 + 42 = 30 + 2 + 20.
	books.state = VaultState{Balance: big.NewInt(10), TotalPendingWithdrawals: big.NewInt(20), Outstanding: big.NewInt(42), TotalPrincipal: big.NewInt(30)}
	rep, err := x.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Drift) != 0 || len(rep.UnknownFills) != 0 || len(rep.MismatchedFills) != 0 {
		t.Fatalf("report = %+v, want clean", rep)
	}
	if rep.Utilisation != 0.84 { // 42 ÷ (30 + 20)
		t.Fatalf("utilisation = %v, want 0.84", rep.Utilisation)
	}
	// The chain's pending disagrees with the ledger, and tokens are missing:
	// both are drift.
	books.state.TotalPendingWithdrawals = big.NewInt(25)
	books.state.Balance = big.NewInt(9)
	rep, _ = x.Reconcile(context.Background())
	got := strings.Join(rep.Drift, "\n")
	if len(rep.Drift) != 2 || !strings.Contains(got, "pending_withdrawals") || !strings.Contains(got, "invariant") {
		t.Fatalf("drift = %v, want pending and invariant drift", rep.Drift)
	}
}

// The pool holds less than is pending (liquid 0) but payPending pays
// min(pending, balance): the admin is told as soon as any balance can go out,
// and not while the pool holds nothing.
func TestVaultIndexer_AlertsPendingWithdrawalsPayableInPart(t *testing.T) {
	prev := reconcileAlerts
	t.Cleanup(func() { reconcileAlerts = prev })
	x, _, books := newIndexerRig(t)
	if _, err := x.IndexOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		balance int64
		want    bool
	}{{0, false}, {5, true}} {
		reconcileAlerts = &alertLimiter{last: map[string]time.Time{}}
		logs := captureLog(t)
		books.state = VaultState{Balance: big.NewInt(c.balance), TotalPendingWithdrawals: big.NewInt(20), Outstanding: new(big.Int), TotalPrincipal: new(big.Int)}
		if _, err := x.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(logs.String(), "ALERT liquidity_pending_withdrawals"); got != c.want {
			t.Fatalf("balance %d: alerted = %v, want %v\n%s", c.balance, got, c.want, logs.String())
		}
	}
}
