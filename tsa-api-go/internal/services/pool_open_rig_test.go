package services

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const poolTestTreasury = "0x7E5a000000000000000000000000000000000001"

// poolOpenChain (not fakePoolChain: Plan A's gates test already declares that
// name in this package, pool_chain_gates_test.go:22) is the pool chain end to end: the escrow (balances from the
// embedded poolEscrowFake), the treasury's signer, and the probe. A broadcast
// open "lands" (trade Pending, stock moved available → locked) unless told not
// to, so every open-path decision is driven by chain truth, as in production.
type poolOpenChain struct {
	*poolEscrowFake
	cmu           sync.Mutex
	trades        map[[32]byte]*OnChainP2PTrade
	snaps         map[[32]byte][2]uint16
	receipts      map[common.Hash]uint64
	opens         map[[32]byte]pendingOpen // by tradeId (the open's calldata)
	head          uint64
	depth         uint64 // blocks the head runs ahead of a receipt (1 → 2 deep)
	nextNonce     uint64
	latest        uint64
	gen           atomic.Uint64
	bumpAfter     int // bump the endpoint generation after this many broadcasts
	broadcasts    int
	prepareErr    map[string]error // lowercase seller wallet → prepare revert
	neverLand     map[string]bool  // lowercase seller wallet → broadcast but never mined
	failBroadcast map[string]bool  // lowercase seller wallet → broadcast errors (attempted, hash unknown)
	onBroadcast   func(nonce uint64, tradeID [32]byte)
	onPrepare     func()
	// Release-key calls (Tasks 12, 13). The release sender in those tests
	// always broadcasts, so the fake mines a cancel or a resolve when it is
	// prepared.
	resolves     []bool     // adminResolve's releaseToBuyer, in order (F21)
	cancels      [][32]byte // cancelByTimeout tradeIds (the expiry canceller)
	cancelNoMine bool       // a cancel is sent but not mined (Task 13's back-off, review M10)
}

type pendingOpen struct {
	tradeID [32]byte
	seller  common.Address
	buyer   string
	token   common.Address
	amount  *big.Int
}

func newPoolOpenChain() *poolOpenChain {
	return &poolOpenChain{trades: map[[32]byte]*OnChainP2PTrade{}, snaps: map[[32]byte][2]uint16{},
		receipts: map[common.Hash]uint64{}, opens: map[[32]byte]pendingOpen{}, head: 1000, depth: 1, nextNonce: 10, latest: 10,
		prepareErr: map[string]error{}, neverLand: map[string]bool{}, failBroadcast: map[string]bool{}}
}

// ── escrow ──
func (f *poolOpenChain) PrepareOpenTradeWithVoucher(tradeID [32]byte, v AdVoucher, _ []byte, buyer string, amount *big.Int) ([]byte, error) {
	if hook := f.onPrepare; hook != nil {
		hook()
	}
	f.cmu.Lock()
	defer f.cmu.Unlock()
	seller := strings.ToLower(v.Seller.Hex())
	if err := f.prepareErr[seller]; err != nil {
		return nil, err
	}
	if _, exists := f.trades[tradeID]; exists {
		return nil, errors.New("estimate gas (would revert): execution reverted: TradeExists")
	}
	// The RPC's pending view: a prepare reads it, a broadcast moves it.
	n := f.nextNonce
	f.opens[tradeID] = pendingOpen{tradeID: tradeID, seller: v.Seller, buyer: buyer, token: v.Token, amount: new(big.Int).Set(amount)}
	return json.Marshal(blockchain.UnsignedTx{Nonce: n, To: poolTestEscrow, Value: "0", GasPrice: "1", GasLimit: 200_000,
		ChainID: "97", Data: hex.EncodeToString(tradeID[:])})
}

func (f *poolOpenChain) ReadTrade(id [32]byte) (*OnChainP2PTrade, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	if t, ok := f.trades[id]; ok {
		cp := *t
		return &cp, nil
	}
	return &OnChainP2PTrade{Status: OnChainP2PStatusNone}, nil
}

func (f *poolOpenChain) PrepareAdminResolve(id [32]byte, releaseToBuyer bool, _ string) ([]byte, error) {
	f.cmu.Lock()
	f.resolves = append(f.resolves, releaseToBuyer)
	t := f.trades[id]
	f.cmu.Unlock()
	if t != nil && t.Status == OnChainP2PStatusDisputed {
		f.cmu.Lock()
		t.Status = OnChainP2PStatusResolved
		f.cmu.Unlock()
		if !releaseToBuyer {
			b, _ := f.ReadBalance(t.Seller.Hex(), "")
			f.setBalance(t.Seller.Hex(), new(big.Int).Add(b.Available, t.Amount), new(big.Int).Sub(b.Locked, t.Amount))
		}
	}
	return json.Marshal(blockchain.UnsignedTx{To: poolTestEscrow, Value: "0", ChainID: "97", Data: "adminResolve"})
}

func (f *poolOpenChain) PrepareCancelByTimeout(id [32]byte, _ string) ([]byte, error) {
	f.cmu.Lock()
	f.cancels = append(f.cancels, id)
	t := f.trades[id]
	noMine := f.cancelNoMine
	f.cmu.Unlock()
	if t == nil || t.Status != OnChainP2PStatusPending {
		return nil, errors.New("estimate gas (would revert): execution reverted: NotPending")
	}
	if noMine {
		return json.Marshal(blockchain.UnsignedTx{To: poolTestEscrow, Value: "0", ChainID: "97", Data: "cancelByTimeout"})
	}
	f.cmu.Lock()
	t.Status = OnChainP2PStatusCancelled
	f.cmu.Unlock()
	b, _ := f.ReadBalance(t.Seller.Hex(), "")
	f.setBalance(t.Seller.Hex(), new(big.Int).Add(b.Available, t.Amount), new(big.Int).Sub(b.Locked, t.Amount))
	return json.Marshal(blockchain.UnsignedTx{To: poolTestEscrow, Value: "0", ChainID: "97", Data: "cancelByTimeout"})
}

// setTrade rewrites a landed trade's on-chain status (an LP's own action).
func (f *poolOpenChain) setTrade(id [32]byte, st OnChainP2PTradeStatus) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	if t := f.trades[id]; t != nil {
		t.Status = st
	}
}

func (f *poolOpenChain) ReadTradeFeeSnapshot(id [32]byte) (uint16, uint16, bool) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	s := f.snaps[id]
	return s[0], s[1], true
}

// ── the treasury's signer ──
func (f *poolOpenChain) Address() string { return poolTestTreasury }
func (f *poolOpenChain) SignAndBroadcast(_ context.Context, unsigned []byte) (string, error) {
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsigned, &u); err != nil {
		return "", err
	}
	var id [32]byte
	if raw, err := hex.DecodeString(u.Data); err == nil && len(raw) == 32 {
		copy(id[:], raw)
	}
	f.cmu.Lock()
	po := f.opens[id]
	hook := f.onBroadcast
	f.broadcasts++
	if u.Nonce+1 > f.nextNonce {
		f.nextNonce = u.Nonce + 1
	}
	if f.bumpAfter > 0 && f.broadcasts == f.bumpAfter {
		f.gen.Add(1)
	}
	seller := strings.ToLower(po.seller.Hex())
	fail, land := f.failBroadcast[seller], !f.neverLand[seller]
	f.cmu.Unlock()
	if hook != nil {
		hook(u.Nonce, po.tradeID)
	}
	if fail {
		return "", errors.New("write tcp: connection reset by peer")
	}
	hash := common.BytesToHash(po.tradeID[:])
	if land {
		f.cmu.Lock()
		f.trades[po.tradeID] = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending, Seller: po.seller,
			Buyer: common.HexToAddress(po.buyer), Token: po.token, Amount: po.amount, CreatedAt: big.NewInt(time.Now().Unix())}
		f.receipts[hash] = f.head
		f.latest = u.Nonce + 1
		f.cmu.Unlock()
		b, _ := f.ReadBalance(po.seller.Hex(), "")
		f.setBalance(po.seller.Hex(), new(big.Int).Sub(b.Available, po.amount), new(big.Int).Add(b.Locked, po.amount))
	}
	return hash.Hex(), nil
}

// ── the probe (TreasuryTxProbe) ──
func (f *poolOpenChain) TransactionReceipt(_ context.Context, h common.Hash) (*types.Receipt, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	b, ok := f.receipts[h]
	if !ok {
		return nil, ethereum.NotFound
	}
	return &types.Receipt{Status: 1, BlockNumber: new(big.Int).SetUint64(b)}, nil
}
func (f *poolOpenChain) BlockNumber(context.Context) (uint64, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	return f.head + f.depth, nil
}
func (f *poolOpenChain) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	return f.nextNonce, nil
}
func (f *poolOpenChain) NonceAt(context.Context, common.Address, *big.Int) (uint64, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	return f.latest, nil
}
func (f *poolOpenChain) TransactionByHash(context.Context, common.Hash) (*types.Transaction, bool, error) {
	return nil, false, ethereum.NotFound
}
func (f *poolOpenChain) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return &types.Header{Time: uint64(time.Now().Unix())}, nil
}
func (f *poolOpenChain) FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error) {
	return nil, nil
}
func (f *poolOpenChain) EndpointGeneration() uint64 { return f.gen.Load() }

// fakePoolGate is the pool's gates (PoolGate) with switches.
type fakePoolGate struct {
	mu     sync.Mutex
	open   bool
	gasOK  bool
	closed string
}

func (g *fakePoolGate) Open() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.open && g.closed == ""
}
func (g *fakePoolGate) Status() PoolGateStatus                                { return PoolGateStatus{Open: g.Open()} }
func (g *fakePoolGate) Check(context.Context) PoolGateStatus                  { return g.Status() }
func (g *fakePoolGate) Close(reason string)                                   { g.mu.Lock(); g.closed = reason; g.mu.Unlock() }
func (g *fakePoolGate) ReleaseGasOK(context.Context) bool                     { return g.gasOK }
func (g *fakePoolGate) ReprobeReceipts(context.Context)                       {}
func (g *fakePoolGate) Enabled() bool                                         { return true }
func (g *fakePoolGate) FloatBalances(context.Context) ([]SponsorFloat, error) { return nil, nil }

// poolOpenRig is a central-mode ITService with a wired pool desk over the
// fake chain, a verified buyer, and LPs added with addLP.
type poolOpenRig struct {
	*deskRig
	chain    *poolOpenChain
	it       *ITService
	gates    *fakePoolGate
	nonceNow time.Time
	userID   uuid.UUID
	buyer    uuid.UUID
}

func newPoolOpenRig(t *testing.T) *poolOpenRig {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	if err := db.AutoMigrate(&models.ITIntent{}, &models.PoolOrder{}, &models.PaycrestOrderRecord{}, &models.FiatVirtualAccount{},
		&models.ITIntentAuditLog{}, &models.WalletAddress{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	createInboundCreditsTable(t, db)
	return buildPoolOpenRig(t, db)
}

// buildPoolOpenRig is the rig over an existing database (SQLite above, or a
// temp Postgres in the concurrency tests). SQLite limit (review I6): addLP's
// voucher is 500,000 USDT = 5e23 base units, and SQLite stores
// CAST(… AS NUMERIC) arithmetic above int64 as REAL. A SQLite test therefore
// opens at most once per ad; any test that opens twice on one ad is a
// _Postgres test. Production Postgres is exact; no float parsing is added.
func buildPoolOpenRig(t *testing.T, db *gorm.DB) *poolOpenRig {
	t.Helper()
	chain := newPoolOpenChain()
	esc := newPoolEscrowFake()
	chain.poolEscrowFake = esc
	dr := buildDeskRig(t, db, esc, chain)
	rig, _ := newSponsorRig("bsc", "USDT")
	rig.lane.Signer = chain
	rig.lane.Tokens["USDT"] = poolTestUSDT
	lane := &TreasuryLane{SponsorLane: rig.lane, ChainID: poolTestChain, EscrowCalls: []common.Address{common.HexToAddress(poolTestEscrow)}}
	tr := NewTreasury(map[string]*TreasuryLane{"bsc": lane}, TreasuryCaps{}, nil)
	r := &poolOpenRig{deskRig: dr, chain: chain, gates: &fakePoolGate{open: true, gasOK: true}, nonceNow: time.Now(), buyer: uuid.New()}
	nonces := NewTreasuryNonceWatch("bsc", chain, poolTestTreasury, 30*time.Second, func() time.Time { return r.nonceNow })
	desk, err := NewPoolDesk(PoolDeskConfig{DB: db, PM: NewPMService(db), Trades: dr.trades, Stock: dr.desk.c.Stock,
		Token: dr.desk.c.Token, Escrow: chain, EscrowAddr: poolTestEscrow, Treasury: tr, Gates: r.gates, Probe: chain,
		Generation: chain.EndpointGeneration, Nonces: nonces, BuyerUserID: r.buyer})
	if err != nil {
		t.Fatalf("desk: %v", err)
	}
	desk.proveWindow = 500 * time.Millisecond // keeps an unproven slice's test fast; production is 6 s
	r.desk = desk
	if db.Dialector.Name() == "sqlite" {
		r.userID = seedVerifiedUser(t, db)
	} else {
		r.userID = uuid.New() // Postgres tests drive insertOrder directly (no KYC read)
	}
	r.it = NewITService(db, nil, stubFx{ngn: 1500})
	r.it.SetInboundCentral(true, testCentralAccount())
	r.it.SetPoolDesk(desk)
	return r
}

func (r *poolOpenRig) price(t *testing.T, naira string) *PoolPrice {
	t.Helper()
	p, err := r.it.QuotePoolBuy("USDT", naira, "NGN", poolMid, specPoolSettings())
	if err != nil {
		t.Fatalf("price ₦%s: %v", naira, err)
	}
	return p
}

func (r *poolOpenRig) open(t *testing.T, naira string) (*models.ITIntent, *StableBuyInstructions, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), PoolOpenCap)
	defer cancel()
	return r.it.OpenPoolBuy(ctx, r.userID, r.price(t, naira), testRoutedEVM)
}

func (r *poolOpenRig) slicesOf(t *testing.T, intentID uuid.UUID) []models.P2PTrade {
	t.Helper()
	var out []models.P2PTrade
	r.db.Where("it_intent_id = ?", intentID).Order("created_at").Find(&out)
	return out
}

func (r *poolOpenRig) intent(t *testing.T, id uuid.UUID) models.ITIntent {
	t.Helper()
	var i models.ITIntent
	if err := r.db.First(&i, "id = ?", id).Error; err != nil {
		t.Fatalf("intent: %v", err)
	}
	return i
}

func walletOf(lp *models.PaymentMerchant) string { return strings.ToLower(lp.OperatorWalletAddress) }
