package services

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// PrepareAdminRelease (the pool chain fake): only a Pending or Paid trade is
// releasable; the id rides in the calldata so the sender can mine it.
func (f *poolOpenChain) PrepareAdminRelease(id [32]byte, _ string) ([]byte, error) {
	f.cmu.Lock()
	t := f.trades[id]
	f.cmu.Unlock()
	if t == nil || (t.Status != OnChainP2PStatusPending && t.Status != OnChainP2PStatusPaid) {
		return nil, errors.New("estimate gas (would revert): execution reverted: NotReleasable")
	}
	return json.Marshal(blockchain.UnsignedTx{To: poolTestEscrow, Value: "0", ChainID: "97", Data: "adminRelease:" + hex.EncodeToString(id[:])})
}

// VerifyTradeReleased reads the fake's own state: a mined release pays the
// whole slice to the buyer (fee 0, S2).
func (f *poolOpenChain) VerifyTradeReleased(_ string, id [32]byte) (*P2PTradeReleasedEvent, error) {
	f.cmu.Lock()
	defer f.cmu.Unlock()
	t := f.trades[id]
	if t == nil || t.Status != OnChainP2PStatusReleased {
		return nil, errors.New("receipt not found")
	}
	return &P2PTradeReleasedEvent{TradeID: id, Buyer: t.Buyer, AmountToBuyer: new(big.Int).Set(t.Amount), FeeToSystem: new(big.Int)}, nil
}

// releaseHashOf is a release's deterministic tx hash in the fake.
func releaseHashOf(id [32]byte) common.Hash {
	return common.BytesToHash(append([]byte("rel:"), id[:28]...))
}

// settleVerifier answers VerifyTransferFrom per hash: what the release paid
// the treasury, as the sender recorded it (escrow → treasury only).
type settleVerifier struct {
	mu       sync.Mutex
	credited map[string]*big.Int
	calls    int
}

func (v *settleVerifier) VerifyTransfer(context.Context, string, string, string) (*big.Int, error) {
	return nil, errors.New("unused: the proof reads a sourced transfer")
}
func (v *settleVerifier) VerifyTransferFrom(_ context.Context, hash, _, from, to string) (*big.Int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	if !SameEVMAddress(from, poolTestEscrow) || !SameEVMAddress(to, poolTestTreasury) {
		return nil, ErrTransferWrongRecipient
	}
	if got, ok := v.credited[strings.ToLower(hash)]; ok {
		return new(big.Int).Set(got), nil
	}
	return nil, errors.New("verify: not found")
}

// poolReleaseSender is the release key (ChainSender): it builds inside Send,
// and "mines" the adminRelease it is handed unless noMine — with a receipt
// carrying the escrow's TradeReleased log, so the proof's pure parser reads it.
type poolReleaseSender struct {
	chain    *poolOpenChain
	verifier *settleVerifier
	mu       sync.Mutex
	noMine   bool
	sends    []int64 // chain ids, in order
	receipts map[common.Hash]*types.Receipt
}

// settleProbe serves the sender's release receipts (with logs) and delegates
// everything else to the pool chain fake.
type settleProbe struct {
	TreasuryTxProbe
	s *poolReleaseSender
}

func (p *settleProbe) TransactionReceipt(ctx context.Context, h common.Hash) (*types.Receipt, error) {
	p.s.mu.Lock()
	r, ok := p.s.receipts[h]
	p.s.mu.Unlock()
	if ok {
		return r, nil
	}
	return p.TreasuryTxProbe.TransactionReceipt(ctx, h)
}

// releaseReceipt is a mined adminRelease's receipt: TradeReleased(tradeId,
// buyer, amount, 0) from the pool escrow.
func releaseReceipt(id [32]byte, buyer common.Address, amount *big.Int, block uint64) *types.Receipt {
	data := append(common.LeftPadBytes(amount.Bytes(), 32), make([]byte, 32)...)
	return &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: new(big.Int).SetUint64(block),
		Logs: []*types.Log{{Address: common.HexToAddress(poolTestEscrow), Data: data,
			Topics: []common.Hash{topicTradeReleased, common.BytesToHash(id[:]), common.BytesToHash(buyer.Bytes())}}}}
}

func (p *poolReleaseSender) Address() string { return "0x00000000000000000000000000000000000000a1" }
func (p *poolReleaseSender) SignAndBroadcast(context.Context, []byte) (string, error) {
	return "", errors.New("the release key only signs through Send")
}
func (p *poolReleaseSender) Send(_ context.Context, chainID int64, build func() ([]byte, error)) (string, error) {
	unsigned, err := build()
	if err != nil {
		return "", notBroadcast(err)
	}
	p.mu.Lock()
	p.sends = append(p.sends, chainID)
	noMine := p.noMine
	p.mu.Unlock()
	var u blockchain.UnsignedTx
	_ = json.Unmarshal(unsigned, &u)
	raw, _ := hex.DecodeString(strings.TrimPrefix(u.Data, "adminRelease:"))
	var id [32]byte
	copy(id[:], raw)
	hash := releaseHashOf(id)
	if strings.HasPrefix(u.Data, "adminRelease:") && !noMine {
		p.chain.cmu.Lock()
		t := p.chain.trades[id]
		t.Status = OnChainP2PStatusReleased
		p.chain.receipts[hash] = p.chain.head
		amount, buyer, head := new(big.Int).Set(t.Amount), t.Buyer, p.chain.head
		p.chain.cmu.Unlock()
		p.mu.Lock()
		p.receipts[hash] = releaseReceipt(id, buyer, amount, head)
		p.mu.Unlock()
		p.verifier.mu.Lock()
		p.verifier.credited[strings.ToLower(hash.Hex())] = amount
		p.verifier.mu.Unlock()
	}
	return hash.Hex(), nil
}

// logProbe overrides FilterLogs on the pool chain probe (the F22 scan).
type logProbe struct {
	TreasuryTxProbe
	logs []types.Log
}

func (l *logProbe) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	var out []types.Log
	for _, lg := range l.logs {
		if lg.BlockNumber >= q.FromBlock.Uint64() && lg.BlockNumber <= q.ToBlock.Uint64() {
			out = append(out, lg)
		}
	}
	return out, nil
}

// newPoolSaleWallet is the pool's sale wallet (the conversion treasury's BSC
// lane in production) over its own fake lane: 18-dp USDT, a recording
// verifier and a probe whose sale receipts are 11 deep.
func newPoolSaleWallet(t *testing.T, stock *big.Int) (*CompanyWallet, *fakeRelaySigner, *recordingSaleVerifier, *fakeTxProbe) {
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
	return w, rig.signer, v, p
}

// poolSettleRig is a wired pool desk with the settle side: the release key,
// the release verifier, the sale wallet, the LPs' settlement banks, the
// forward table and the sale-leg index.
type poolSettleRig struct {
	*poolOpenRig
	sender       *poolReleaseSender
	verifier     *settleVerifier
	sale         *CompanyWallet
	saleSigner   *fakeRelaySigner
	saleVerifier *recordingSaleVerifier
	saleProbe    *fakeTxProbe
	lps          []*models.PaymentMerchant
}

func newPoolSettleRig(t *testing.T, freeWhole ...int64) *poolSettleRig {
	t.Helper()
	return poolSettleRigOn(t, newPoolOpenRig(t), freeWhole...)
}

// poolSettleRigOn adds the settle side to an open rig (SQLite above, or a
// temp Postgres in the concurrency tests).
func poolSettleRigOn(t *testing.T, base *poolOpenRig, freeWhole ...int64) *poolSettleRig {
	t.Helper()
	r := &poolSettleRig{poolOpenRig: base}
	for _, free := range freeWhole {
		lp, _ := r.addLP(t, free)
		r.db.Model(&models.PaymentMerchant{}).Where("id = ?", lp.ID).Updates(map[string]any{
			"settlement_bank_code": "058", "settlement_account_number": "0123456789", "settlement_account_name": "LP " + lp.ID.String()[:4]})
		r.lps = append(r.lps, lp)
	}
	if err := r.db.AutoMigrate(&models.RampLeg{}); err != nil {
		t.Fatal(err)
	}
	if err := r.db.Exec(models.RampLegsOneLiveSaleIndexSQL).Error; err != nil {
		t.Fatal(err)
	}
	if r.db.Dialector.Name() == "sqlite" {
		createSettlementForwardTable(t, r.db)
	} else if err := r.db.AutoMigrate(&models.P2PSettlementForward{}); err != nil {
		t.Fatal(err)
	}
	r.verifier = &settleVerifier{credited: map[string]*big.Int{}}
	r.sender = &poolReleaseSender{chain: r.chain, verifier: r.verifier, receipts: map[common.Hash]*types.Receipt{}}
	r.trades.SetPoolSlices(r.it, r.sender)
	r.chain.depth = 4 // a receipt at head is 5 deep: RAMP_TREASURY_CONFIRMATIONS
	r.sale, r.saleSigner, r.saleVerifier, r.saleProbe = newPoolSaleWallet(t, usdt18(1_000))
	r.desk.c.Sender, r.desk.c.Verifier, r.desk.c.Sale, r.desk.c.LogChunk = r.sender, r.verifier, r.sale, 2000
	r.desk.c.Probe = &settleProbe{TreasuryTxProbe: r.chain, s: r.sender}
	return r
}

// openPaid opens a pool order for naira and pays it exactly, on time.
func (r *poolSettleRig) openPaid(t *testing.T, naira string) *models.ITIntent {
	t.Helper()
	intent, _, err := r.open(t, naira)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ok, err := r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_"+intent.ID.String()[:8], intent.AmountMinor, time.Now(), false, PayInCentral)
	if !ok || err != nil {
		t.Fatalf("claim: %v %v", ok, err)
	}
	got := r.intent(t, intent.ID)
	return &got
}

func (r *poolSettleRig) advance(t *testing.T, id uuid.UUID) error {
	t.Helper()
	return r.it.AdvancePoolBuy(context.Background(), id)
}

// broadcasts is how many sends the sale wallet's signer made.
func (f *fakeRelaySigner) broadcasts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.broadcastCalls
}
