package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

const (
	poolTestChain    int64 = 97
	poolTestUSDT           = "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7" // 18 dp
	poolTestTreasury       = "0x7E5a000000000000000000000000000000000001"
)

// usdt18 is whole × 10^18 (the pool token's base units).
func usdt18(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

// usdt18Units is canonical units in 18-dp chain units, as a string.
func usdt18Units(t *testing.T, canonical string) string {
	t.Helper()
	u, _ := new(big.Int).SetString(canonical, 10)
	c, err := CanonicalToChainUnits(u, 18)
	if err != nil {
		t.Fatal(err)
	}
	return c.String()
}

// fakePoolGate is the pool's gates: open, and the operator able to pay.
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
func (g *fakePoolGate) ReleaseGasOK(context.Context, int, bool) bool          { return g.gasOK }
func (g *fakePoolGate) ReprobeReceipts(context.Context)                       {}
func (g *fakePoolGate) Enabled() bool                                         { return true }
func (g *fakePoolGate) FloatBalances(context.Context) ([]SponsorFloat, error) { return nil, nil }

// newPoolSaleWallet is the pool's sale wallet (the conversion treasury's BSC
// lane in production) over its own fake lane: 18-dp USDT, a recording
// verifier and a probe whose sale receipts are 11 deep.
func newPoolSaleWallet(t *testing.T, stock *big.Int) (*CompanyWallet, *Treasury, *recordingSaleVerifier) {
	t.Helper()
	rig, _ := newSponsorRig("bsc", "USDT")
	tr := NewTreasury(map[string]*TreasuryLane{"bsc": {SponsorLane: rig.lane, ChainID: poolTestChain}}, TreasuryCaps{}, nil)
	rig.native.decimals = 18
	rig.native.tokenBalances = map[string]*big.Int{strings.ToLower(rig.lane.Tokens["USDT"]): stock}
	v := &recordingSaleVerifier{err: errors.New("verify: not found")}
	p := &fakeTxProbe{head: 100, receiptBlock: 90, background: []types.Log{{BlockNumber: 100}}}
	w := NewCompanyWallet(CompanyWalletConfig{Treasury: tr, Decimals: map[string]map[string]int{"bsc": {"USDT": 18}},
		Verifiers: map[string]ERC20SourcedTransferVerifier{"bsc": v}, Probes: map[string]TreasuryTxProbe{"bsc": p},
		ConversionTreasury: tr.Address()})
	if w == nil {
		t.Fatal("no pool sale wallet")
	}
	return w, tr, v
}

// vaultBase is a central-mode ITService over SQLite with a verified buyer.
type vaultBase struct {
	db     *gorm.DB
	it     *ITService
	desk   *PoolDesk
	gates  *fakePoolGate
	userID uuid.UUID
}

func newVaultBase(t *testing.T) *vaultBase {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	if err := db.AutoMigrate(&models.ITIntent{}, &models.PoolOrder{}, &models.PaycrestOrderRecord{}, &models.FiatVirtualAccount{},
		&models.ITIntentAuditLog{}, &models.WalletAddress{}, &models.RampLeg{}, &models.JobRunMarker{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(models.RampLegsOneLiveSaleIndexSQL).Error; err != nil {
		t.Fatal(err)
	}
	createInboundCreditsTable(t, db)
	it := NewITService(db, nil, stubFx{ngn: 1500})
	it.SetInboundCentral(true, testCentralAccount())
	return &vaultBase{db: db, it: it, gates: &fakePoolGate{open: true, gasOK: true}, userID: seedVerifiedUser(t, db)}
}

func (r *vaultBase) price(t *testing.T, naira string) *PoolPrice {
	t.Helper()
	p, err := r.it.QuotePoolBuy(context.Background(), "USDT", naira, "NGN", poolMid, specPoolSettings())
	if err != nil {
		t.Fatalf("price ₦%s: %v", naira, err)
	}
	return p
}

func (r *vaultBase) intent(t *testing.T, id uuid.UUID) models.ITIntent {
	t.Helper()
	var i models.ITIntent
	if err := r.db.First(&i, "id = ?", id).Error; err != nil {
		t.Fatalf("intent: %v", err)
	}
	return i
}

var _ = time.Second
