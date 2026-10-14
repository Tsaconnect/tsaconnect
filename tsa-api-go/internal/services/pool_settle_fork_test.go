//go:build bscfork && bsctestnet

package services

// Plan C2 Task 15 (§24.4.2, R3): the settle side on a LOCAL fork of BSC
// mainnet (chain 56), against real USDT 0x55d3…7955 moved to fresh LP wallets
// from an impersonated holder. No real funds move and nothing is broadcast to
// a real network: the rig refuses any RPC that is not a local fork of 56.
//
// It reuses Task 14's c1Rehearsal/c2Rehearsal helpers (pay, advanceUntil,
// logSettled, send, …), built here for chain 56 instead of 97 — hence the
// second tag. Run (fork up first: .liq-sdd/c2/bscfork-up.sh):
//
//	go test -tags bscfork,bsctestnet ./internal/services/ -count=1 -timeout 15m -run '^TestPoolSettleFork_' -v

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	forkChainID = 56
	forkDSNHost = "@127.0.0.1:55493/" // the throwaway Postgres only
	// forkUSDTHolder is a Binance hot wallet holding BSC-USD: impersonated on
	// the fork only, to move real USDT to the rehearsal LPs.
	forkUSDTHolder = "0x8894E0a0c962CB723c1976a4421c95949bE2D4E3"
)

// forkKey is a rehearsal key derived from a public label: worthless anywhere
// but the fork (bscfork-up.sh derives the same with `cast keccak`).
func forkKey(t *testing.T, label string) string {
	t.Helper()
	return hex.EncodeToString(crypto.Keccak256([]byte("liq-c2-bscfork:" + label)))
}

func forkAddr(t *testing.T, label string) string {
	return crypto.PubkeyToAddress(mustKey(t, forkKey(t, label)).PublicKey).Hex()
}

// syncLog tees the standard logger so a drill can assert its ALERT lines.
type syncLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncLog) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(l.buf.String(), sub)
}

type forkRig struct {
	*c2Rehearsal
	escrowAddr string
	usdt       string
	treasury   string
	release    string
	dest       string // her wallet for this run: an address no key holds, fresh per run
	logs       *syncLog
}

// forkDB is a fresh schema per drill on the throwaway Postgres.
func forkDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if !strings.Contains(dsn, forkDSNHost) {
		t.Fatalf("refusing: DATABASE_URL must be the throwaway Postgres on %s", strings.Trim(forkDSNHost, "@/"))
	}
	schema := "liq_c2_bscfork_" + strings.ToLower(strings.TrimPrefix(t.Name(), "TestPoolSettleFork_"))
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	if err := admin.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE; CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("schema: %v", err)
	}
	scoped, err := testutil.DSNWithSchema(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(postgres.Open(scoped), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	config.DB = db
	if err := config.AutoMigrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// newForkRig is newC1Rehearsal + newC2Rehearsal for chain 56 on the fork.
func newForkRig(t *testing.T) *forkRig {
	t.Helper()
	escAddr, fmAddr := os.Getenv("LIQ_FORK_POOL_ESCROW"), os.Getenv("LIQ_FORK_POOL_FEEMANAGER")
	if !common.IsHexAddress(escAddr) || !common.IsHexAddress(fmAddr) {
		t.Skip("needs LIQ_FORK_POOL_ESCROW and LIQ_FORK_POOL_FEEMANAGER (bscfork-pool.env)")
	}
	rpcURL := os.Getenv("LIQ_FORK_RPC")
	if rpcURL == "" {
		rpcURL = "http://127.0.0.1:8545"
	}
	if !strings.HasPrefix(rpcURL, "http://127.0.0.1:") {
		t.Fatalf("refusing: the fork RPC must be local, got %s", rpcURL)
	}
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.MaxIdleConnsPerHost = 32
	}
	t.Setenv("BSC_NETWORK", "mainnet")
	t.Setenv("SONIC_NETWORK", "testnet")
	t.Setenv("BSC_MAINNET_RPC_URL", rpcURL)
	logs := &syncLog{}
	prevOut := log.Writer()
	log.SetOutput(io.MultiWriter(prevOut, logs))
	t.Cleanup(func() { log.SetOutput(prevOut) })

	db := forkDB(t)
	cfg := config.Load()
	// Every chain but BSC mainnet points at a dead local port: nothing in the
	// rig can reach a real network.
	for network, nc := range cfg.Networks {
		for name, ch := range nc.Chains {
			if network == "mainnet" && name == "bsc" {
				ch.RPCURL = rpcURL
			} else {
				ch.RPCURL = "http://127.0.0.1:9"
			}
			nc.Chains[name] = ch
		}
	}
	// The bscPresets mainnet row (p2p_presets.go) with the fork's escrow and
	// FeeManager filled in; the preset stays empty until the mainnet rollout.
	for id, cc := range cfg.P2PChains {
		if cc.Purpose == config.P2PPurposePool {
			delete(cfg.P2PChains, id)
		}
	}
	cfg.P2PChains[forkChainID] = config.P2PChainConfig{ChainID: forkChainID, ChainKey: "bsc", Network: "mainnet",
		EscrowAddress: common.HexToAddress(escAddr).Hex(), EIP712Version: "2", Assets: []string{"USDT"}, Purpose: config.P2PPurposePool,
		Pool: &config.PoolChainConfig{FeeManager: common.HexToAddress(fmAddr).Hex(), Tokens: []string{"USDT"},
			ReleaseGasFloorWei: "10000000000000000", LogChunkBlocks: 2000}}

	bs := NewBlockchainService(cfg)
	reg := NewP2PEscrowRegistry(bs, cfg)
	pool, ok := reg.PoolChain()
	if !ok || pool.ChainID != forkChainID {
		t.Fatalf("pool chain = %+v %v: refusing anything but chain 56", pool, ok)
	}
	client, _ := bs.ClientForChainID(forkChainID)
	escrow, err := reg.For(forkChainID)
	if client == nil || err != nil {
		t.Fatalf("client/escrow: %v", err)
	}
	ctx := context.Background()
	if id, err := client.Client.ChainID(ctx); err != nil || id.Int64() != forkChainID {
		t.Fatalf("RPC chain id = %v %v: refusing anything but 56", id, err)
	}
	var ver string
	if err := client.Client.Client().CallContext(ctx, &ver, "web3_clientVersion"); err != nil ||
		!(strings.HasPrefix(ver, "HardhatNetwork") || strings.HasPrefix(ver, "anvil/")) {
		t.Fatalf("RPC client %q %v: refusing anything but a local fork", ver, err)
	}
	if err := CheckPoolPaymentWindow(escrow); err != nil {
		t.Fatalf("fork escrow window: %v", err)
	}
	prev := Fees
	t.Cleanup(func() { Fees = prev })
	Fees = nil

	trades := NewP2PTradeService(db, reg, &mockRates{}, &mockBus{}, 146)
	trades.SetTokenResolver(bs.TokenAddressForChainID)
	pm := NewPMService(db)
	trades.SetPMService(pm)
	trades.SetPoolGate(fakePoolOpen(true))
	stock := NewPoolStock(reg, bs.TokenAddressForChainID)
	tok, ok := stock.Token()
	if !ok || !SameEVMAddress(tok.Address, "0x55d398326f99059fF775485246999027B3197955") {
		t.Fatalf("pool token = %+v, want BSC-USD", tok)
	}
	treasuryPK := forkKey(t, "treasury")
	lane := &SponsorLane{
		ChainKey: pool.ChainKey, Chain: EVMRelayChain{Client: client}, Builder: EVMRelayTxBuilder{Client: client}, Native: client,
		Signer: NewLocalKeyReleaseSigner(mustKey(t, treasuryPK), client), Guard: NewSponsorGuard("liq_c2_bscfork", SponsorLimits{}, nil),
		Tokens: map[string]string{tok.Symbol: tok.Address},
	}
	tr := NewTreasury(map[string]*TreasuryLane{pool.ChainKey: {
		SponsorLane: lane, ChainID: forkChainID, EscrowCalls: []common.Address{common.HexToAddress(escrow.ContractAddress())},
	}}, TreasuryCaps{}, nil)
	nonces := NewTreasuryNonceWatch(pool.ChainKey, client.Probe(), tr.Address(), 30*time.Second, nil)
	buyer := uuid.NewSHA1(uuid.NameSpaceOID, []byte("liq-c2-bscfork-buyer"))
	desk, err := NewPoolDesk(PoolDeskConfig{DB: db, PM: pm, Trades: trades, Stock: stock, Token: tok, Escrow: escrow,
		EscrowAddr: escrow.ContractAddress(), Treasury: tr, Gates: &fakePoolGate{open: true, gasOK: true}, Probe: client.Probe(),
		Generation: client.EndpointGeneration, Nonces: nonces, BuyerUserID: buyer, ChainName: "BNB Smart Chain"})
	if err != nil {
		t.Fatalf("desk: %v", err)
	}
	it := NewITService(db, nil, nil)
	it.SetInboundCentral(true, testCentralAccount())
	it.SetPoolDesk(desk)

	c1 := &c1Rehearsal{db: db, trades: trades, it: it, desk: desk, client: client}
	c1.signer = func(pk string) *LocalKeyReleaseSigner { return NewLocalKeyReleaseSigner(mustKey(t, pk), client) }
	r := &forkRig{c2Rehearsal: &c2Rehearsal{c1Rehearsal: c1}, escrowAddr: escrow.ContractAddress(), usdt: tok.Address,
		treasury: tr.Address(), release: forkAddr(t, "release"), logs: logs,
		dest: common.BytesToAddress(crypto.Keccak256([]byte(fmt.Sprintf("liq-c2-bscfork-dest:%s:%d", t.Name(), time.Now().UnixNano())))[12:]).Hex()}
	if !SameEVMAddress(tr.Address(), forkAddr(t, "treasury")) {
		t.Fatal("treasury address mismatch")
	}
	// Task 14's settle wiring (newC2Rehearsal), on chain 56.
	r.sender = NewChainReleaseSigner(nil, mustKey(t, forkKey(t, "release")), map[int64]ReleaseBroadcaster{forkChainID: client})
	verifier := NewLogTransferVerifier(client.Client)
	sale := NewCompanyWallet(CompanyWalletConfig{Treasury: tr,
		Decimals:  map[string]map[string]int{"bsc": {"USDT": 18}},
		Verifiers: map[string]ERC20SourcedTransferVerifier{"bsc": verifier},
		Probes:    map[string]TreasuryTxProbe{"bsc": client.Probe()}, ConversionTreasury: tr.Address()})
	if sale == nil {
		t.Fatal("no sale wallet on the treasury's bsc lane")
	}
	desk.c.Sender, desk.c.Verifier, desk.c.Sale, desk.c.LogChunk = r.sender, verifier, sale, cfg.P2PChains[forkChainID].Pool.LogChunkBlocks
	desk.proveWindow = 20 * time.Second // P16 rehearsal budget, as Task 14
	trades.SetPoolSlices(it, r.sender)
	it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
	r.gw = &fakeNombaGateway{payoutID: "trf_fork"}
	r.fwd = NewP2PSettlementForwarder(db, NewPMService(db), r.gw, &stubPayoutBalances{ngn: 1_000_000}, "TSA Connect")
	src := NewP2PTradeClaimSource(db, NewPMService(db), trades, r.sender)
	src.SetForwarder(r.fwd)
	it.SetPoolForwards(src)
	return r
}

// rpc calls one fork JSON-RPC method (hardhat_* / evm_* / anvil aliases).
func (r *forkRig) rpc(t *testing.T, out any, method string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := r.client.Client.Client().CallContext(ctx, out, method, args...); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

// impersonatedSend sends data from an impersonated account and waits for a
// successful receipt.
func (r *forkRig) impersonatedSend(t *testing.T, label, from, to string, data []byte) string {
	t.Helper()
	r.rpc(t, nil, "hardhat_impersonateAccount", from)
	defer r.rpc(t, nil, "hardhat_stopImpersonatingAccount", from)
	var hash common.Hash
	r.rpc(t, &hash, "eth_sendTransaction", map[string]any{"from": from, "to": to, "data": hexutil.Bytes(data), "gas": hexutil.Uint64(300_000)})
	rc, err := r.client.WaitForReceipt(hash.Hex(), 90*time.Second)
	if err != nil || rc.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("%s %s: receipt %v %v", label, hash.Hex(), rc, err)
	}
	t.Logf("%s (impersonated %s) %s %s", label, from, hash.Hex(), r.txInfo(hash.Hex()))
	return hash.Hex()
}

// escrowCallAs sends an escrow call prepared by the adapter (PrepareRelease,
// PrepareRaiseDispute) from the impersonated LP.
func (r *forkRig) escrowCallAs(t *testing.T, label, from string, raw []byte, err error) string {
	t.Helper()
	if err != nil {
		t.Fatalf("%s prepare: %v", label, err)
	}
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(raw, &u); err != nil {
		t.Fatal(err)
	}
	return r.impersonatedSend(t, label, from, u.To, common.FromHex(u.Data))
}

func (r *forkRig) usdtBalance(t *testing.T, addr string) *big.Int {
	t.Helper()
	b, err := r.client.GetTokenBalance(r.usdt, addr)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	return b
}

func (r *forkRig) nonceOf(t *testing.T, addr string, pending bool) uint64 {
	t.Helper()
	ctx := context.Background()
	var n uint64
	var err error
	if pending {
		n, err = r.client.Client.PendingNonceAt(ctx, common.HexToAddress(addr))
	} else {
		n, err = r.client.Client.NonceAt(ctx, common.HexToAddress(addr), nil)
	}
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	return n
}

// newLP is a fresh LP: gas, real USDT from the impersonated holder, a pool
// deposit of `stock` USDT and a signed stock ad (Plan B's flow, on 56).
func (r *forkRig) newLP(t *testing.T, label string, stock int64) c1LP {
	t.Helper()
	ctx := context.Background()
	pk := forkKey(t, "lp:"+t.Name()+":"+label)
	addr := crypto.PubkeyToAddress(mustKey(t, pk).PublicKey).Hex()
	r.rpc(t, nil, "hardhat_setBalance", addr, "0xDE0B6B3A7640000") // 1 BNB
	amount := usdt18(stock)
	data := append(crypto.Keccak256([]byte("transfer(address,uint256)"))[:4],
		append(common.LeftPadBytes(common.HexToAddress(addr).Bytes(), 32), common.LeftPadBytes(amount.Bytes(), 32)...)...)
	r.impersonatedSend(t, "USDT to "+label, forkUSDTHolder, r.usdt, data)
	pm := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, addr)
	if err := r.db.Model(pm).Update("pool_chain_id", forkChainID).Error; err != nil {
		t.Fatal(err)
	}
	pm.PoolChainID = ptrInt64(forkChainID)
	lp := c1LP{pm: pm, addr: addr, pk: pk}
	raw, err := r.trades.PrepareApprove(ctx, pm.UserID, addr, "USDT", amount.String(), forkChainID)
	r.send(t, ctx, pk, "approve", raw, err)
	raw, err = r.trades.PrepareDeposit(ctx, pm.UserID, addr, "USDT", amount.String(), forkChainID)
	h := r.send(t, ctx, pk, "deposit", raw, err)
	if _, err := r.trades.ConfirmDeposit(ctx, pm.UserID, addr, "USDT", amount.String(), h, forkChainID); err != nil {
		t.Fatalf("confirm deposit: %v", err)
	}
	res, err := r.trades.CreateAd(ctx, pm.UserID, addr, CreateAdParams{Token: "USDT", TokenAmount: "1", FiatCurrency: "NGN", ChainID: forkChainID})
	if err != nil {
		t.Fatalf("stock ad: %v", err)
	}
	digest, _ := decodeHex32(res.Ad.VoucherHash)
	sig, _ := crypto.Sign(digest[:], mustKey(t, pk))
	sig[64] += 27
	if ad, err := r.trades.SignAd(ctx, res.Ad.ID, pm.UserID, addr, sig); err != nil || ad.Status != models.P2PAdStatusActive {
		t.Fatalf("sign: %v", err)
	}
	t.Logf("LP %s %s: %d USDT in the pool, stock ad %s", label, addr, stock, res.Ad.ID)
	return lp
}

func ptrInt64(v int64) *int64 { return &v }

// openOrder opens a pool order for naira through OpenPoolBuy (30 s P16 budget).
func (r *forkRig) openOrder(t *testing.T, naira string) (*models.ITIntent, error) {
	t.Helper()
	user := seedRehearsalBuyer(t, r.db)
	p, err := r.it.QuotePoolBuy(context.Background(), "USDT", naira, "NGN", poolMid, CurrentPoolSettings())
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	intent, pay, err := r.it.OpenPoolBuy(ctx, user, p, r.dest)
	if err != nil {
		return nil, err
	}
	if pay == nil {
		t.Fatal("open returned no pay-in")
	}
	t.Logf("OPEN latency %s: intent %s units=%s", time.Since(start), intent.ID, intent.AssetAmount)
	for _, sl := range r.liveSlices(t, intent.ID) {
		t.Logf("  slice %s units=%s trade=%s nonce=%d open tx=%s %s", sl.ID, sl.TokenAmount, sl.OnChainTradeID,
			derefNonce(sl.OpenNonce), sl.OpenTxHash, r.txInfo(sl.OpenTxHash))
	}
	return intent, nil
}

func (r *forkRig) mustOpenOneSlice(t *testing.T, naira string) (*models.ITIntent, models.P2PTrade) {
	t.Helper()
	intent, err := r.openOrder(t, naira)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	slices := r.liveSlices(t, intent.ID)
	if len(slices) != 1 {
		t.Fatalf("%d slices, want 1", len(slices))
	}
	return intent, slices[0]
}

// saleLegs are the order's sale legs (her delivery), every attempt.
func (r *forkRig) saleLegs(t *testing.T, id uuid.UUID) []models.RampLeg {
	t.Helper()
	var legs []models.RampLeg
	r.db.Where("order_id = ? AND kind = ?", id, models.RampLegKindSale).Order("attempt").Find(&legs)
	return legs
}

// assertDeliveredOnce: one confirmed sale leg, and her wallet holds exactly it.
func (r *forkRig) assertDeliveredOnce(t *testing.T, id uuid.UUID) {
	t.Helper()
	legs := r.saleLegs(t, id)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusConfirmed {
		t.Fatalf("sale legs = %+v, want one confirmed", legs)
	}
	got := r.usdtBalance(t, r.dest)
	if got.String() != legs[0].Amount {
		t.Fatalf("her wallet holds %s, the leg sent %s: not delivered exactly once", got, legs[0].Amount)
	}
	t.Logf("delivered once: %s USDT units, tx %s %s", legs[0].Amount, legs[0].TxHash, r.txInfo(legs[0].TxHash))
}

func (r *forkRig) assertForwardedOnce(t *testing.T, sliceID uuid.UUID) *models.P2PSettlementForward {
	t.Helper()
	f := r.forwardOf(sliceID)
	if f == nil || f.Status != models.P2PForwardSubmitted || r.callsFor(f.Reference) != 1 || len(r.gw.calls) != 1 {
		t.Fatalf("slice %s forward = %+v calls=%d total=%d, want one submitted forward", sliceID, f, r.callsFor(refOf(f)), len(r.gw.calls))
	}
	t.Logf("LP forwarded once: %s ₦%s ref=%s", f.ID, f.Amount, f.Reference)
	return f
}

// releasedLogs counts TradeReleased logs for the slice on the escrow.
func (r *forkRig) releasedLogs(t *testing.T, sl models.P2PTrade) int {
	t.Helper()
	id := HashID(sl.ID)
	rc, err := r.client.Client.TransactionReceipt(context.Background(), common.HexToHash(sl.OpenTxHash))
	if err != nil {
		t.Fatalf("open receipt: %v", err)
	}
	logs, err := r.client.Client.FilterLogs(context.Background(), ethereum.FilterQuery{FromBlock: rc.BlockNumber,
		Addresses: []common.Address{common.HexToAddress(r.escrowAddr)},
		Topics:    [][]common.Hash{{topicTradeReleased}, {common.BytesToHash(id[:])}}})
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	return len(logs)
}

// F21: the LP disputes a slice of a paid order. The reconciler pauses the LP
// and sends adminResolve(…, true) through the release key's Send; the resolve
// proves to the treasury, the LP is forwarded once and she is delivered.
func TestPoolSettleFork_F21DisputePaid(t *testing.T) {
	r := newForkRig(t)
	ctx := context.Background()
	lp := r.newLP(t, "lp1", 20)
	intent, sl := r.mustOpenOneSlice(t, "10000")
	r.pay(t, intent)
	raw, err := r.desk.c.Escrow.PrepareRaiseDispute(HashID(sl.ID), lp.addr)
	r.escrowCallAs(t, "raiseDispute (LP)", lp.addr, raw, err)
	if on, err := r.desk.c.Escrow.ReadTrade(HashID(sl.ID)); err != nil || on.Status != OnChainP2PStatusDisputed {
		t.Fatalf("on-chain = %+v %v, want Disputed", on, err)
	}
	relNonce := r.nonceOf(t, r.release, false)
	got := r.advanceUntil(t, intent.ID, models.ITStatusSettled, 5*time.Minute)
	r.logSettled(t, got)

	var pm models.PaymentMerchant
	r.db.First(&pm, "id = ?", lp.pm.ID)
	if pm.Status != models.PaymentMerchantStatusPaused || !r.logs.has("ALERT liquidity_pool_slice_disputed slice="+sl.ID.String()) {
		t.Fatalf("LP status %s, dispute alert %v: want paused and alerted", pm.Status, r.logs.has("liquidity_pool_slice_disputed"))
	}
	var cur models.P2PTrade
	r.db.First(&cur, "id = ?", sl.ID)
	ev, err := r.desk.c.Escrow.VerifyTradeResolved(cur.ReleaseTxHash, HashID(sl.ID))
	if err != nil || !ev.ReleasedToBuyer {
		t.Fatalf("resolve %s = %+v %v, want TradeResolved to the treasury", cur.ReleaseTxHash, ev, err)
	}
	tx, _, err := r.client.Client.TransactionByHash(ctx, common.HexToHash(cur.ReleaseTxHash))
	if err != nil {
		t.Fatal(err)
	}
	from, _ := types.Sender(types.LatestSignerForChainID(big.NewInt(forkChainID)), tx)
	if !SameEVMAddress(from.Hex(), r.release) || !SameEVMAddress(tx.To().Hex(), r.escrowAddr) {
		t.Fatalf("resolve from %s to %s, want the release key to the escrow", from.Hex(), tx.To().Hex())
	}
	if n := r.nonceOf(t, r.release, false); n != relNonce+1 {
		t.Fatalf("release key sent %d txs, want exactly the one resolve", n-relNonce)
	}
	if ok, err := r.desk.proveSliceRelease(ctx, &cur, r.it.saleDepth()); err != nil || !ok {
		t.Fatalf("slice not proven at the treasury: %v", err)
	}
	t.Logf("adminResolve(true) via Send: %s %s", cur.ReleaseTxHash, r.txInfo(cur.ReleaseTxHash))
	r.assertForwardedOnce(t, sl.ID)
	r.assertDeliveredOnce(t, intent.ID)
	t.Log("F21 PASS")
}

// F22 paid: the LP releases the slice itself. The settle engine finds the
// hash by the TradeReleased log scan, proves it 5 deep, forwards the LP once
// and delivers; the release key sends nothing.
func TestPoolSettleFork_F22SelfReleasePaid(t *testing.T) {
	r := newForkRig(t)
	lp := r.newLP(t, "lp1", 20)
	intent, sl := r.mustOpenOneSlice(t, "10000")
	r.pay(t, intent)
	relNonce := r.nonceOf(t, r.release, false)
	raw, err := r.desk.c.Escrow.PrepareRelease(HashID(sl.ID), lp.addr)
	self := r.escrowCallAs(t, "release (LP self-release)", lp.addr, raw, err)
	got := r.advanceUntil(t, intent.ID, models.ITStatusSettled, 5*time.Minute)
	r.logSettled(t, got)

	var cur models.P2PTrade
	r.db.First(&cur, "id = ?", sl.ID)
	if !strings.EqualFold(cur.ReleaseTxHash, self) {
		t.Fatalf("slice hash %q, want the LP's own release %s (found by the log scan)", cur.ReleaseTxHash, self)
	}
	if r.desk.c.LogChunk != 2000 {
		t.Fatalf("LogChunk = %d, want 2000", r.desk.c.LogChunk)
	}
	if n := r.nonceOf(t, r.release, false); n != relNonce {
		t.Fatalf("release key sent %d txs: a second adminRelease", n-relNonce)
	}
	if n := r.releasedLogs(t, sl); n != 1 {
		t.Fatalf("%d TradeReleased logs, want 1", n)
	}
	if !r.logs.has("how=self_released") {
		t.Fatal("no self-release alert")
	}
	r.assertForwardedOnce(t, sl.ID)
	r.assertDeliveredOnce(t, intent.ID)
	t.Log("F22 paid PASS")
}

// F22 unpaid: the LP releases a slice of an order nobody paid. Alerted, never
// forwarded automatically; the console offers pay-lp/returned and
// PayPoolSliceLP pays once.
func TestPoolSettleFork_F22SelfReleaseUnpaid(t *testing.T) {
	r := newForkRig(t)
	ctx := context.Background()
	lp := r.newLP(t, "lp1", 20)
	intent, sl := r.mustOpenOneSlice(t, "10000")
	raw, err := r.desk.c.Escrow.PrepareRelease(HashID(sl.ID), lp.addr)
	self := r.escrowCallAs(t, "release (LP self-release, unpaid)", lp.addr, raw, err)
	alert := "ALERT liquidity_pool_slice_self_released slice=" + sl.ID.String()
	deadline := time.Now().Add(2 * time.Minute)
	for !r.logs.has(alert) {
		if time.Now().After(deadline) {
			t.Fatal("no liquidity_pool_slice_self_released alert")
		}
		r.reconcilePass(t, ctx)
		time.Sleep(3 * time.Second)
	}
	r.reconcilePass(t, ctx)
	if f := r.forwardOf(sl.ID); f != nil || len(r.gw.calls) != 0 {
		t.Fatalf("forwarded automatically: %+v calls=%d", f, len(r.gw.calls))
	}
	detail, err := r.it.PoolOrderDetail(ctx, intent.ID)
	if err != nil || len(detail.Slices) != 1 {
		t.Fatalf("detail: %+v %v", detail, err)
	}
	if acts := strings.Join(detail.Slices[0].SliceAction, ","); acts != "pay-lp,returned" {
		t.Fatalf("slice actions %q, want pay-lp,returned (slice status %s)", acts, detail.Slices[0].Status)
	}
	admin := uuid.New()
	var fwd *models.P2PSettlementForward
	deadline = time.Now().Add(2 * time.Minute)
	for {
		if fwd, err = r.it.PayPoolSliceLP(ctx, admin, intent.ID, sl.ID, "fork drill: F22 unpaid, pay the LP"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pay-lp: %v", err)
		}
		t.Logf("pay-lp: %s (waiting for the 5-deep proof)", RedactAlertText(err.Error()))
		time.Sleep(3 * time.Second)
	}
	if _, err := r.it.PayPoolSliceLP(ctx, admin, intent.ID, sl.ID, "fork drill: second click"); err == nil {
		t.Fatal("a second pay-lp was accepted")
	} else {
		t.Logf("second pay-lp refused: %s", RedactAlertText(err.Error()))
	}
	r.assertForwardedOnce(t, sl.ID)
	var cur models.P2PTrade
	r.db.First(&cur, "id = ?", sl.ID)
	if !strings.EqualFold(cur.ReleaseTxHash, self) || cur.CancelReason != poolF22Paid {
		t.Fatalf("slice hash %q reason %q, want the LP's release and %s", cur.ReleaseTxHash, cur.CancelReason, poolF22Paid)
	}
	if detail, _ = r.it.PoolOrderDetail(ctx, intent.ID); len(detail.Slices[0].SliceAction) != 0 {
		t.Fatalf("after pay-lp the slice still offers %v", detail.Slices[0].SliceAction)
	}
	t.Logf("F22 unpaid PASS: forward %s ₦%s, release %s %s", fwd.ID, fwd.Amount, self, r.txInfo(self))
}

// sendTreasuryTx signs a zero-value self-transfer from the treasury at nonce
// and gas price, logging its hash BEFORE the broadcast (the runbook's rule).
func (r *forkRig) sendTreasuryTx(t *testing.T, label string, nonce uint64, gasPrice *big.Int) string {
	t.Helper()
	to := common.HexToAddress(r.treasury)
	tx := types.NewTx(&types.LegacyTx{Nonce: nonce, GasPrice: gasPrice, Gas: 21_000, To: &to, Value: big.NewInt(0)})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(forkChainID)), mustKey(t, forkKey(t, "treasury")))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: nonce %d gas price %s wei, hash %s (recorded before broadcast)", label, nonce, gasPrice, signed.Hash().Hex())
	if err := r.client.Client.SendTransaction(context.Background(), signed); err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	return signed.Hash().Hex()
}

func (r *forkRig) mined(t *testing.T, hash string) *types.Receipt {
	t.Helper()
	rc, err := r.client.Client.TransactionReceipt(context.Background(), common.HexToHash(hash))
	if errors.Is(err, ethereum.NotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("receipt %s: %v", hash, err)
	}
	return rc
}

// F24: a treasury transaction stuck at nonce N blocks the lane. The watch
// reports it after 30 s, an open is refused, a delivery broadcast behind it
// stays pending and is never re-sent; the runbook's same-nonce replacement
// unblocks both, and nothing is paid twice.
func TestPoolSettleFork_F24SharedNonce(t *testing.T) {
	r := newForkRig(t)
	ctx := context.Background()
	r.newLP(t, "lp1", 20)
	resume := func() {
		var ok any
		r.rpc(t, &ok, "evm_setIntervalMining", 1)
	}
	t.Cleanup(resume) // never leave the fork halted

	// Order B: paid, its slice released and 5 deep, her delivery not yet sent.
	intent, sl := r.mustOpenOneSlice(t, "10000")
	r.pay(t, intent)
	if err := r.it.AdvancePoolBuy(ctx, intent.ID); err != nil {
		t.Logf("advance: %s", RedactAlertText(err.Error()))
	}
	r.db.First(&sl, "id = ?", sl.ID)
	if sl.ReleaseTxHash == "" || len(r.saleLegs(t, intent.ID)) != 0 {
		t.Fatalf("after one tick: release %q, %d sale legs; want released, not delivered", sl.ReleaseTxHash, len(r.saleLegs(t, intent.ID)))
	}
	rc, err := r.client.WaitForReceipt(sl.ReleaseTxHash, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for {
		head, err := r.client.Client.BlockNumber(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if head >= rc.BlockNumber.Uint64()+r.it.saleDepth()+1 {
			break
		}
		time.Sleep(time.Second)
	}

	// Halt the fork's mining and wedge nonce N.
	var ok any
	r.rpc(t, &ok, "evm_setAutomine", false)
	r.rpc(t, &ok, "evm_setIntervalMining", 0)
	n := r.nonceOf(t, r.treasury, true)
	if n != r.nonceOf(t, r.treasury, false) {
		t.Fatalf("treasury already has a pending tx before the drill")
	}
	stuck := r.sendTreasuryTx(t, "stuck tx", n, big.NewInt(1_000)) // far below any price a block would take
	if err := r.desk.c.Nonces.Check(ctx); err != nil {
		t.Fatalf("first check: %v", err)
	}
	gapSeen := time.Now()
	time.Sleep(31 * time.Second)
	if err := r.desk.c.Nonces.Check(ctx); !errors.Is(err, ErrTreasuryNonceStuck) {
		t.Fatalf("watch after %s = %v, want ErrTreasuryNonceStuck", time.Since(gapSeen).Round(time.Second), err)
	}
	t.Logf("TreasuryNonceWatch reported stuck after %s (latest=%d pending=%d)", time.Since(gapSeen).Round(time.Second), n, n+1)
	var before int64
	r.db.Model(&models.ITIntent{}).Count(&before)
	if _, err := r.openOrder(t, "10000"); !errors.Is(err, ErrTreasuryNonceStuck) {
		t.Fatalf("open while stuck = %v, want ErrTreasuryNonceStuck", err)
	}
	var after int64
	r.db.Model(&models.ITIntent{}).Count(&after)
	if after != before {
		t.Fatalf("a refused open wrote %d intents", after-before)
	}
	t.Log("OpenPoolBuy refused while the nonce is stuck")

	// Her delivery: proven releases, so the sale goes out behind nonce N and
	// waits; later ticks never re-send it.
	if err := r.it.AdvancePoolBuy(ctx, intent.ID); err != nil {
		t.Logf("advance: %s", RedactAlertText(err.Error()))
	}
	legs := r.saleLegs(t, intent.ID)
	if len(legs) != 1 || legs[0].TxHash == "" || legs[0].Status != models.RampLegStatusPending || legs[0].Nonce == nil || *legs[0].Nonce != n+1 {
		t.Fatalf("sale legs = %+v, want one pending at nonce %d", legs, n+1)
	}
	delivery := legs[0].TxHash
	for i := 0; i < 3; i++ {
		time.Sleep(2 * time.Second)
		if err := r.it.AdvancePoolBuy(ctx, intent.ID); err != nil {
			t.Logf("advance: %s", RedactAlertText(err.Error()))
		}
	}
	legs = r.saleLegs(t, intent.ID)
	if len(legs) != 1 || legs[0].TxHash != delivery || legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("sale legs after 3 more ticks = %+v, want the one pending leg", legs)
	}
	if p := r.nonceOf(t, r.treasury, true); p != n+2 || r.mined(t, delivery) != nil {
		t.Fatalf("treasury pending nonce %d (want %d), delivery mined=%v", p, n+2, r.mined(t, delivery) != nil)
	}
	t.Logf("delivery %s pending at nonce %d behind the stuck tx, never re-sent", delivery, n+1)

	// Runbook: same-nonce zero-value self-transfer at a higher price, hash
	// recorded first; one block mines it and the delivery behind it.
	gp, err := r.client.Client.SuggestGasPrice(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gp.Mul(gp, big.NewInt(3))
	if gp.Cmp(big.NewInt(1_000_000_000)) < 0 {
		gp = big.NewInt(1_000_000_000)
	}
	repl := r.sendTreasuryTx(t, "runbook replacement", n, gp)
	r.rpc(t, &ok, "evm_mine")
	if rc := r.mined(t, repl); rc == nil || rc.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("replacement not mined: %+v", rc)
	}
	if r.mined(t, stuck) != nil {
		t.Fatal("the stuck tx mined as well")
	}
	if rc := r.mined(t, delivery); rc == nil || rc.Status != types.ReceiptStatusSuccessful {
		t.Fatalf("delivery not mined behind the replacement: %+v", rc)
	}
	t.Logf("replacement %s %s; delivery %s %s", repl, r.txInfo(repl), delivery, r.txInfo(delivery))
	resume()
	if err := r.desk.c.Nonces.Check(ctx); err != nil {
		t.Fatalf("watch after the runbook: %v", err)
	}
	got := r.advanceUntil(t, intent.ID, models.ITStatusSettled, 3*time.Minute)
	r.logSettled(t, got)
	r.assertDeliveredOnce(t, intent.ID)
	r.assertForwardedOnce(t, sl.ID)
	c, err := r.openOrder(t, "10000")
	if err != nil {
		t.Fatalf("open after the runbook: %v", err)
	}
	if len(r.liveSlices(t, c.ID)) != 1 {
		t.Fatal("the open after the runbook has no slice")
	}
	t.Log("F24 PASS: " + fmt.Sprintf("stuck %s replaced by %s; one delivery, one forward", stuck, repl))
}
