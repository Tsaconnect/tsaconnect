//go:build vaultnet

package services

// End-to-end rehearsal of the shared liquidity pool against the REAL
// TsaLiquidityPool on a public testnet (BSC testnet, chain 97), driving the
// real services the way cmd/server wires them: the LiquidityVault adapter on
// an EVMClient, the operator as a ChainReleaseSigner over a durable KeyLane
// (Postgres store), the treasury as its own KeyLane-backed Treasury lane, the
// pool gates, PoolDesk/ITService, VaultLPService and VaultIndexer. Only the
// naira side (the claim a central-account credit would make), KYC/user rows,
// the FX/mid price and anything Flutterwave/Paycrest are faked. Every chain
// call is real and every tx must mine with status 1.
//
// Steps: provider posts collateral → LP activates, is allow-listed by the
// operator and deposits (locked 24 h) → a buyer's pool order is claimed,
// filled by the operator, proven at depth and delivered by the treasury →
// gas-free claimFor (and its 8 h rate limit) → a second small order (leaves
// the LP something to claim) → ledger index + reconcile → provider refill and
// settle-from-collateral (idempotent per operationId) → console summary →
// pause (opens and deposits refused; withdraw/claim still build) → unpause.
//
// Run (a throwaway Postgres; keys only ever in env, never on a command line):
//
//	pg_ctl -D <dir> -o "-p 55432 -k '' -c listen_addresses=127.0.0.1" -l <log> start
//	export DATABASE_URL='postgres://postgres@127.0.0.1:55432/tsatest?sslmode=disable'
//	export VAULT_NET='{"rpc":"<bsc testnet rpc>","vault":"0x…","token":"0x…","deployBlock":136773203,
//	  "logsRpc":"https://bsc-testnet-rpc.publicnode.com"}'   # logsRpc optional: the indexer's eth_getLogs
//	                                                          # (data-seed-prebsc refuses every eth_getLogs)
//	export LP_PK=… OPERATOR_PK=… TREASURY_PK=… PROVIDER_PK=…   # read from the keys file, never echoed
//	go test -tags vaultnet ./internal/services/ -run '^TestVaultNet_Rehearsal$' -count=1 -v -timeout 40m
//
// Roles: PROVIDER = owner = collateral provider = guardian (funds the others'
// gas from its own tBNB when they run low); OPERATOR is a vault operator;
// TREASURY is an approved fill recipient; LP is any wallet. Re-runnable: each
// run posts fresh collateral and uses a fresh schema, so fills from earlier
// runs show as unknown to this run's ledger and are only logged.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm"
)

const (
	vaultNetChainID  int64 = 97
	vaultNetChainKey       = "bsc"
	vaultNetExplorer       = "https://testnet.bscscan.com/tx/"
)

// vaultNetDirectABI is what the test itself signs: the PM's console actions
// and the mock token's open mint.
const vaultNetDirectABI = `[
{"type":"function","name":"mint","inputs":[{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[],"stateMutability":"nonpayable"},
{"type":"function","name":"approve","inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"name":"","type":"bool"}],"stateMutability":"nonpayable"},
{"type":"function","name":"postCollateral","inputs":[{"name":"amount","type":"uint256"}],"outputs":[],"stateMutability":"nonpayable"},
{"type":"function","name":"refill","inputs":[{"name":"amount","type":"uint256"},{"name":"ref","type":"bytes32"}],"outputs":[],"stateMutability":"nonpayable"},
{"type":"function","name":"pause","inputs":[],"outputs":[],"stateMutability":"nonpayable"},
{"type":"function","name":"unpause","inputs":[],"outputs":[],"stateMutability":"nonpayable"}
]`

type vaultNetFixture struct {
	RPC         string `json:"rpc"`
	Vault       string `json:"vault"`
	Token       string `json:"token"`
	DeployBlock uint64 `json:"deployBlock"`
	// LogsRPC, when set, serves the indexer's eth_getLogs (the Binance
	// data-seed nodes refuse every eth_getLogs with "limit exceeded").
	LogsRPC string `json:"logsRpc"`
}

type vaultNetKey struct {
	key  *ecdsa.PrivateKey
	addr string
}

type vaultNet struct {
	t       *testing.T
	ctx     context.Context
	f       vaultNetFixture
	client  *blockchain.EVMClient
	vault   *LiquidityVault
	direct  abi.ABI
	db      *gorm.DB
	reg     *SignerRegistry
	op      *ChainReleaseSigner
	gates   *PoolChainGates
	desk    *PoolDesk
	it      *ITService
	lpSvc   *VaultLPService
	ledger  *VaultIndexer
	token   PoolToken
	lp      vaultNetKey
	opKey   vaultNetKey
	tr      vaultNetKey
	prov    vaultNetKey
	lastBlk uint64 // the latest block a step's tx mined in
	ourRefs map[string]bool
}

func vaultNetEnvKey(t *testing.T, name string) vaultNetKey {
	t.Helper()
	raw := strings.TrimPrefix(strings.TrimSpace(os.Getenv(name)), "0x")
	if raw == "" {
		t.Skipf("%s not set", name)
	}
	k, err := crypto.HexToECDSA(raw)
	if err != nil {
		t.Fatalf("%s is not a private key", name) // never the value
	}
	return vaultNetKey{key: k, addr: crypto.PubkeyToAddress(k.PublicKey).Hex()}
}

func whole(n int64) *big.Int { return usdt18(n) }

func fmtUnits(n *big.Int) string {
	if n == nil {
		return "<nil>"
	}
	return BaseUnitsToDecimal(n, 18)
}

// ─── Chain helpers ──────────────────────────────────────────────────────────

func (n *vaultNet) receipt(hash string) *types.Receipt {
	n.t.Helper()
	r, err := n.client.WaitForReceipt(hash, 3*time.Minute)
	if err != nil {
		n.t.Fatalf("receipt %s: %v (%s%s)", hash, err, vaultNetExplorer, hash)
	}
	if r.Status != 1 {
		n.t.Fatalf("tx %s REVERTED (%s%s)", hash, vaultNetExplorer, hash)
	}
	if b := r.BlockNumber.Uint64(); b > n.lastBlk {
		n.lastBlk = b
	}
	return r
}

// signSend signs an UnsignedTx (as the app or the PM's wallet would) and
// waits for it to mine with status 1.
func (n *vaultNet) signSend(k vaultNetKey, unsigned []byte, label string) *types.Receipt {
	n.t.Helper()
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsigned, &u); err != nil {
		n.t.Fatal(err)
	}
	price, _ := new(big.Int).SetString(u.GasPrice, 10)
	value, ok := new(big.Int).SetString(u.Value, 10)
	if !ok {
		value = new(big.Int)
	}
	to := common.HexToAddress(u.To)
	tx := types.NewTx(&types.LegacyTx{Nonce: u.Nonce, GasPrice: price, Gas: u.GasLimit * 12 / 10, To: &to, Value: value, Data: common.FromHex(u.Data)})
	signed, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(vaultNetChainID)), k.key)
	if err != nil {
		n.t.Fatal(err)
	}
	if err := n.client.Client.SendTransaction(n.ctx, signed); err != nil {
		n.t.Fatalf("%s: send: %v", label, err)
	}
	r := n.receipt(signed.Hash().Hex())
	n.t.Logf("  tx %-22s %s%s (block %d, gas %d)", label, vaultNetExplorer, signed.Hash().Hex(), r.BlockNumber.Uint64(), r.GasUsed)
	return r
}

// call is a direct tx from k (the PM's console, the token's mint).
func (n *vaultNet) call(k vaultNetKey, to, label, method string, args ...any) *types.Receipt {
	n.t.Helper()
	data, err := n.direct.Pack(method, args...)
	if err != nil {
		n.t.Fatal(err)
	}
	unsigned, err := n.client.PrepareContractCall(k.addr, to, data, nil, 0)
	if err != nil {
		n.t.Fatalf("%s: prepare: %v", label, err)
	}
	return n.signSend(k, unsigned, label)
}

// topUp gives addr tBNB from the provider when it holds less than min.
func (n *vaultNet) topUp(addr string, min, amount *big.Int, label string) {
	n.t.Helper()
	bal, err := n.client.GetBalance(addr)
	if err != nil {
		n.t.Fatal(err)
	}
	if bal.Cmp(min) >= 0 {
		n.t.Logf("  %s holds %s wei tBNB — no top-up", label, bal)
		return
	}
	unsigned, err := n.client.PrepareNativeTransfer(n.prov.addr, addr, amount)
	if err != nil {
		n.t.Fatal(err)
	}
	n.signSend(n.prov, unsigned, "fund "+label)
}

func (n *vaultNet) usdt(addr string) *big.Int {
	n.t.Helper()
	b, err := n.client.GetTokenBalance(n.f.Token, addr)
	if err != nil {
		n.t.Fatal(err)
	}
	return b
}

func (n *vaultNet) state() *VaultState {
	n.t.Helper()
	st, err := n.vault.State(n.ctx)
	if err != nil {
		n.t.Fatal(err)
	}
	return st
}

func (n *vaultNet) position(addr string) *VaultPosition {
	n.t.Helper()
	p, err := n.vault.Position(n.ctx, addr)
	if err != nil {
		n.t.Fatal(err)
	}
	return p
}

// tick is what the scheduler would run between steps: the key lanes' watch.
func (n *vaultNet) tick() {
	if err := n.reg.Watch(n.ctx); err != nil {
		n.t.Logf("  lane watch: %v", err)
	}
	time.Sleep(3 * time.Second)
}

// eventually polls cond (after a lane watch each round) until it holds.
func (n *vaultNet) eventually(what string, limit time.Duration, cond func() (bool, error)) {
	n.t.Helper()
	deadline := time.Now().Add(limit)
	for {
		ok, err := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			n.t.Fatalf("%s: not reached in %s (last err %v)", what, limit, err)
		}
		n.tick()
	}
}

// ─── Wiring (cmd/server/pool_orders.go, liquidity.go) ───────────────────────

func newVaultNet(t *testing.T) *vaultNet {
	t.Helper()
	raw := os.Getenv("VAULT_NET")
	if raw == "" {
		t.Skip("VAULT_NET not set")
	}
	n := &vaultNet{t: t, ctx: context.Background(), ourRefs: map[string]bool{}}
	if err := json.Unmarshal([]byte(raw), &n.f); err != nil {
		t.Fatal(err)
	}
	n.lp, n.opKey, n.tr, n.prov = vaultNetEnvKey(t, "LP_PK"), vaultNetEnvKey(t, "OPERATOR_PK"), vaultNetEnvKey(t, "TREASURY_PK"), vaultNetEnvKey(t, "PROVIDER_PK")
	var err error
	if n.direct, err = abi.JSON(strings.NewReader(vaultNetDirectABI)); err != nil {
		t.Fatal(err)
	}
	if n.client, err = blockchain.NewEVMClient(n.f.RPC, vaultNetChainID); err != nil {
		t.Fatal(err)
	}
	if id, err := n.client.Client.ChainID(n.ctx); err != nil || id.Int64() != vaultNetChainID {
		t.Fatalf("chain id = %v %v, want %d (testnet only)", id, err, vaultNetChainID)
	}
	n.vault = NewLiquidityVault(n.client, n.f.Vault)

	// The production schema, in a throwaway Postgres schema.
	n.db = testutil.TempPostgres(t, "vaultnet_")
	prev := config.DB
	config.DB = n.db
	t.Cleanup(func() { config.DB = prev })
	if err := config.AutoMigrate(); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}

	// Every hot key's lane on the durable store (main.go), finality 5.
	n.reg = NewSignerRegistry()
	n.reg.SetStore(&GormKeyLaneStore{DB: n.db})
	n.reg.SetFinalityDepth(5)
	n.op = NewChainReleaseSigner(n.reg, n.opKey.key, map[int64]ReleaseBroadcaster{vaultNetChainID: n.client})

	dec, err := n.client.GetTokenDecimals(n.f.Token)
	if err != nil || dec != 18 {
		t.Fatalf("token decimals = %d %v", dec, err)
	}
	n.token = PoolToken{ChainID: vaultNetChainID, ChainKey: vaultNetChainKey, Symbol: "USDT", Address: n.f.Token, Decimals: dec}

	// The pool-only treasury (poolTreasuryFor, conversion off): a router-less
	// lane on the treasury key (buildSponsorLaneOn).
	trLane := n.reg.Lane("treasury", n.tr.key, n.client)
	sponsor := &SponsorLane{ChainKey: vaultNetChainKey, Chain: EVMRelayChain{Client: n.client}, Builder: EVMRelayTxBuilder{Client: n.client},
		Native: n.client, Signer: trLane.Signer(), KeyLane: trLane,
		Guard:  NewSponsorGuard("ramp_treasury_"+vaultNetChainKey, SponsorLimits{BalanceFloor: big.NewInt(1e14)}, nil),
		Tokens: map[string]string{"USDT": n.f.Token}}
	treasury := NewTreasury(map[string]*TreasuryLane{vaultNetChainKey: {SponsorLane: sponsor, ChainID: vaultNetChainID}},
		TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 20000}, nil)
	if !SameEVMAddress(treasury.Address(), n.tr.addr) {
		t.Fatalf("treasury address %s, want %s", treasury.Address(), n.tr.addr)
	}

	// The gates (buildPoolChain).
	n.gates = NewPoolChainGates(PoolChainGatesConfig{ChainID: vaultNetChainID, ChainKey: vaultNetChainKey, Network: "testnet",
		Vault: n.f.Vault, Tokens: map[string]string{"USDT": n.f.Token}, Decimals: map[string]int{"USDT": dec},
		ReleaseAdmin: n.op.Address(), ReleaseGasFloorWei: big.NewInt(1e15), Chain: n.client.Client, Receipts: n.client.Client.Client(),
		TreasuryLane: treasury.Lane(vaultNetChainKey)})

	// The desk (buildPoolDesk).
	sale := NewCompanyWallet(CompanyWalletConfig{Treasury: treasury, Decimals: map[string]map[string]int{vaultNetChainKey: {"USDT": dec}},
		Verifiers:          map[string]ERC20SourcedTransferVerifier{vaultNetChainKey: NewLogTransferVerifier(n.client.Client)},
		Probes:             map[string]TreasuryTxProbe{vaultNetChainKey: n.client.Probe()},
		ConversionTreasury: treasury.Address()})
	if sale == nil {
		t.Fatal("no sale wallet")
	}
	nonces := NewTreasuryNonceWatch(vaultNetChainKey, n.client.Probe(), treasury.Address(), 30*time.Second, nil)
	if n.desk, err = NewPoolDesk(PoolDeskConfig{DB: n.db, Token: n.token, Vault: n.vault, Treasury: treasury, Gates: n.gates,
		Probe: n.client.Probe(), Nonces: nonces, ChainName: "BNB Smart Chain Testnet", Sender: n.op, Sale: sale,
		Generation: n.client.EndpointGeneration}); err != nil {
		t.Fatalf("desk: %v", err)
	}

	// ITService in central mode (the naira side is the fake), with the desk
	// and the ledger (wirePoolOrders).
	n.it = NewITService(n.db, nil, stubFx{ngn: 1500})
	n.it.SetInboundCentral(true, testCentralAccount())
	n.it.SetPoolDesk(n.desk)
	logs := n.client
	if n.f.LogsRPC != "" {
		if logs, err = blockchain.NewEVMClient(n.f.LogsRPC, vaultNetChainID); err != nil {
			t.Fatal(err)
		}
	}
	if n.ledger, err = NewVaultIndexer(VaultIndexerConfig{DB: n.db, ChainID: vaultNetChainID, Vault: n.vault, Reader: logs.Client,
		DeployBlock: n.f.DeployBlock, Chunk: 2000}); err != nil {
		t.Fatal(err)
	}
	n.it.SetVaultLedger(n.ledger)

	// The LP side (wireVaultLiquidity).
	if n.lpSvc, err = NewVaultLPService(VaultLPConfig{DB: n.db, PM: NewPMService(n.db), Vault: n.vault, Tokens: n.client,
		Sender: n.op, Token: n.token, ChainName: "BNB Smart Chain Testnet", Gate: n.gates}); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedUser is a KYC-verified user whose linked (proven) wallet is wallet.
func (n *vaultNet) seedUser(wallet, name string) uuid.UUID {
	n.t.Helper()
	id := uuid.New()
	if err := n.db.Exec(`INSERT INTO users (id, name, username, password, email, wallet_address, verification_status, verified_legal_name, created_at, updated_at)
		VALUES (?, ?, ?, 'x', ?, ?, ?, ?, NOW(), NOW())`, id, name, "vn_"+id.String()[:8], id.String()+"@vaultnet.test", wallet, models.VerificationStatusVerified, name).Error; err != nil {
		n.t.Fatalf("seed user: %v", err)
	}
	return id
}

func (n *vaultNet) intent(id uuid.UUID) models.ITIntent {
	n.t.Helper()
	var i models.ITIntent
	if err := n.db.First(&i, "id = ?", id).Error; err != nil {
		n.t.Fatal(err)
	}
	return i
}

func (n *vaultNet) order(id uuid.UUID) models.PoolOrder {
	n.t.Helper()
	var o models.PoolOrder
	if err := n.db.First(&o, "intent_id = ? AND generation = 1", id).Error; err != nil {
		n.t.Fatal(err)
	}
	return o
}

// buy quotes, opens, claims (the faked naira credit) and drives the order to
// settled through AdvancePoolBuy, as the settle job would.
func (n *vaultNet) buy(buyerUser uuid.UUID, buyer, naira string) (models.ITIntent, models.PoolOrder) {
	n.t.Helper()
	p, err := n.it.QuotePoolBuy(n.ctx, "USDT", naira, "NGN", poolMid, specPoolSettings())
	if err != nil {
		n.t.Fatalf("quote ₦%s: %v", naira, err)
	}
	intent, _, err := n.it.OpenPoolBuy(n.ctx, buyerUser, p, buyer)
	if err != nil {
		n.t.Fatalf("open ₦%s: %v", naira, err)
	}
	ref := vaultFillRef(intent.ID)
	n.ourRefs["0x"+common.Bytes2Hex(ref[:])] = true
	ok, err := n.it.claimAndPriceStableBuy(n.ctx, intent, "sess_"+intent.ID.String()[:8], intent.AmountMinor, time.Now(), false, PayInCentral)
	if !ok || err != nil {
		n.t.Fatalf("claim: %v %v", ok, err)
	}
	got := n.intent(intent.ID)
	n.t.Logf("  intent %s ₦%s → %s USDT (canonical %s), status %s", intent.ID, naira, BaseUnitsToDecimal(bigFromDecimal(&got.AssetAmount), StableAssetDecimals), got.AssetAmount, got.Status)
	n.eventually("order settled", 10*time.Minute, func() (bool, error) {
		err := n.it.AdvancePoolBuy(n.ctx, intent.ID)
		i := n.intent(intent.ID)
		if i.Status == models.ITStatusBuyAwaitingRate || i.Status == models.ITStatusFailed || i.Status == models.ITStatusNeedsReview {
			n.t.Fatalf("order parked: %s %q (advance err %v)", i.Status, i.FailureReason, err)
		}
		return i.Status == models.ITStatusSettled, err
	})
	o := n.order(intent.ID)
	n.receipt(o.FillTxHash)
	var legs []models.RampLeg
	n.db.Where("order_id = ?", intent.ID).Find(&legs)
	for _, l := range legs {
		if l.TxHash != "" {
			n.receipt(l.TxHash)
			n.t.Logf("  sale leg %s amount %s tx %s%s", l.ID, fmtUnits(bigFromDecimal(&l.Amount)), vaultNetExplorer, l.TxHash)
		}
	}
	n.t.Logf("  fill tx %s%s", vaultNetExplorer, o.FillTxHash)
	return n.intent(intent.ID), o
}

// index runs the indexer until it has caught up past the last tx mined.
func (n *vaultNet) index() {
	n.t.Helper()
	n.eventually("ledger caught up", 5*time.Minute, func() (bool, error) {
		if _, err := n.ledger.IndexOnce(n.ctx); err != nil {
			return false, err
		}
		c, err := n.ledger.Cursor(n.ctx)
		return err == nil && c >= n.lastBlk, err
	})
}

func (n *vaultNet) reconcileClean(label string) {
	n.t.Helper()
	n.index()
	rep, err := n.ledger.Reconcile(n.ctx)
	if err != nil {
		n.t.Fatal(err)
	}
	var ours []string
	for _, r := range rep.UnknownFills {
		if n.ourRefs[strings.ToLower(r)] {
			ours = append(ours, r)
		} else {
			n.t.Logf("  (unknown fill %s from an earlier run — not this run's)", r)
		}
	}
	n.t.Logf("  %s reconcile @%d: drift=%v unknown=%v mismatched=%v utilisation=%.4f", label, rep.Block, rep.Drift, rep.UnknownFills, rep.MismatchedFills, rep.Utilisation)
	if len(rep.Drift) != 0 || len(ours) != 0 || len(rep.MismatchedFills) != 0 {
		n.t.Fatalf("%s: reconcile not clean: %+v", label, rep)
	}
}

// ─── The rehearsal ──────────────────────────────────────────────────────────

func TestVaultNet_Rehearsal(t *testing.T) {
	n := newVaultNet(t)
	ctx := n.ctx
	provBNB0, _ := n.client.GetBalance(n.prov.addr)
	t.Logf("provider %s operator %s treasury %s lp %s; provider tBNB %s wei", n.prov.addr, n.opKey.addr, n.tr.addr, n.lp.addr, provBNB0)

	w, err := n.vault.Wiring(ctx, n.opKey.addr, n.tr.addr)
	if err != nil || !w.Operator || !w.Recipient || !strings.EqualFold(w.Token.Hex(), n.f.Token) {
		t.Fatalf("vault wiring = %+v %v", w, err)
	}
	gas := func(milli int64) *big.Int { return new(big.Int).Mul(big.NewInt(milli), big.NewInt(1e15)) } // milli-BNB
	n.topUp(n.opKey.addr, gas(3), gas(4), "operator")
	n.topUp(n.tr.addr, gas(2), gas(3), "treasury")
	n.topUp(n.lp.addr, big.NewInt(5e14), gas(2), "lp")

	// 1. Provider posts collateral (the PM's own wallet, in the console).
	t.Log("STEP 1: provider posts 300 USDT collateral")
	st0 := n.state()
	n.call(n.prov, n.f.Token, "mint(provider)", "mint", common.HexToAddress(n.prov.addr), whole(300))
	n.call(n.prov, n.f.Token, "approve(vault)", "approve", common.HexToAddress(n.f.Vault), whole(300))
	n.call(n.prov, n.f.Vault, "postCollateral", "postCollateral", whole(300))
	st1 := n.state()
	if new(big.Int).Sub(st1.Collateral, st0.Collateral).Cmp(whole(300)) != 0 {
		t.Fatalf("collateral %s → %s, want +300", fmtUnits(st0.Collateral), fmtUnits(st1.Collateral))
	}
	if gs := n.gates.Check(ctx); !gs.Open {
		t.Fatalf("pool gates closed: %v", gs.Failures)
	}
	t.Logf("STEP 1 PASS: collateral %s outstanding %s principal %s; gates open", fmtUnits(st1.Collateral), fmtUnits(st1.Outstanding), fmtUnits(st1.TotalPrincipal))

	// 2. LP: activate, allow-listed by the operator, deposit 100 (locked).
	t.Log("STEP 2: LP activates, is allow-listed, deposits 100 USDT")
	lpUser := n.seedUser(n.lp.addr, "Lara Provider")
	allowedBefore := n.position(n.lp.addr).Allowed
	if _, err := n.lpSvc.Activate(ctx, lpUser, n.lp.addr); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if allowedBefore {
		t.Log("  (wallet already allow-listed on-chain from an earlier run)")
	}
	n.eventually("depositor allowed", 5*time.Minute, func() (bool, error) {
		sent, err := n.lpSvc.SyncDepositors(ctx)
		if sent > 0 {
			t.Logf("  SyncDepositors sent %d setDepositor call(s)", sent)
		}
		return n.position(n.lp.addr).Allowed, err
	})
	var depRows []models.KeyLaneNonce
	n.db.Where("LOWER(address) = LOWER(?) AND op_key LIKE ?", n.opKey.addr, "pool_depositor:%").Find(&depRows)
	for _, r := range depRows {
		t.Logf("  setDepositor op %s nonce %d tx %s%s", r.OpKey, r.Nonce, vaultNetExplorer, r.AcceptedHash)
	}
	n.call(n.prov, n.f.Token, "mint(lp)", "mint", common.HexToAddress(n.lp.addr), whole(100))
	lpBal0 := n.usdt(n.lp.addr)
	pos0 := n.position(n.lp.addr)
	for step := 0; step < 3; step++ {
		plan, err := n.lpSvc.PrepareDeposit(ctx, lpUser, "100")
		if err != nil {
			t.Fatalf("prepare deposit: %v", err)
		}
		n.signSend(n.lp, plan.Txs[0], "lp "+plan.Step)
		if plan.Step == "deposit" {
			break
		}
	}
	pos1 := n.position(n.lp.addr)
	if new(big.Int).Sub(pos1.Principal, pos0.Principal).Cmp(whole(100)) != 0 || new(big.Int).Sub(lpBal0, n.usdt(n.lp.addr)).Cmp(whole(100)) != 0 {
		t.Fatalf("principal %s → %s, want +100", fmtUnits(pos0.Principal), fmtUnits(pos1.Principal))
	}
	me, err := n.lpSvc.Me(ctx, lpUser)
	if err != nil {
		t.Fatal(err)
	}
	lockGap := time.Duration(0)
	if me.LockedUntil != nil {
		lockGap = time.Until(*me.LockedUntil)
	}
	t.Logf("  me: principal %s withdrawable %s lockedUntil %v (in %s) allowed %v", me.Principal, me.Withdrawable, me.LockedUntil, lockGap.Round(time.Second), me.Allowed)
	if me.LockedUntil == nil || lockGap < 23*time.Hour+50*time.Minute || lockGap > 24*time.Hour+5*time.Minute {
		t.Fatalf("lockedUntil = %v, want ≈ now+24h", me.LockedUntil)
	}
	if pos0.Principal.Sign() == 0 && (me.Principal != "100" || me.Withdrawable != "0") {
		t.Fatalf("me principal %s withdrawable %s, want 100 / 0", me.Principal, me.Withdrawable)
	}
	if _, err := n.lpSvc.PrepareWithdraw(ctx, lpUser, "1"); !errors.Is(err, ErrLPDepositLocked) {
		t.Fatalf("withdraw 1 err = %v, want ErrLPDepositLocked", err)
	}
	t.Log("STEP 2 PASS: allowed, deposited 100, locked ≈24h, withdraw refused deposit_locked")

	// 3. A buyer's pool order: claimed, filled, proven, delivered, settled.
	t.Log("STEP 3: buyer pool order ₦170,000")
	buyerKey, _ := crypto.GenerateKey()
	buyer := crypto.PubkeyToAddress(buyerKey.PublicKey).Hex()
	buyerUser := n.seedUser("0x"+common.Bytes2Hex(crypto.Keccak256([]byte(buyer))[:20]), "Bola Buyer")
	stA := n.state()
	claimA := n.position(n.lp.addr).Claimable
	intent, o := n.buy(buyerUser, buyer, "170000")
	stB := n.state()
	claimB := n.position(n.lp.addr).Claimable
	owed, _ := CanonicalToChainUnits(bigFromDecimal(&intent.AssetAmount), 18)
	got := n.usdt(buyer)
	fill, spread := bigFromDecimal(o.FillUnits), bigFromDecimal(o.FillSpreadUnits)
	if got.Cmp(owed) != 0 || fill.Cmp(owed) != 0 {
		t.Fatalf("buyer received %s, intent owes %s, fill %s", fmtUnits(got), fmtUnits(owed), fmtUnits(fill))
	}
	if d := new(big.Int).Sub(stB.Outstanding, stA.Outstanding); d.Cmp(new(big.Int).Add(fill, spread)) != 0 {
		t.Fatalf("outstanding moved %s, want fill+spread %s", fmtUnits(d), fmtUnits(new(big.Int).Add(fill, spread)))
	}
	// The LP's share of the spread (pro-rata on principal), floor-rounded by
	// the contract's accumulator.
	share := new(big.Int).Quo(new(big.Int).Mul(spread, pos1.Principal), stA.TotalPrincipal)
	gotShare := new(big.Int).Sub(claimB, claimA)
	if diff := new(big.Int).Sub(share, gotShare); diff.Sign() < 0 || diff.Cmp(big.NewInt(1e9)) > 0 {
		t.Fatalf("LP claimable +%s, want ≈ %s", fmtUnits(gotShare), fmtUnits(share))
	}
	if o.FilledAt == nil || o.SettledAt == nil || o.FillTxHash == "" || *o.FillSpreadUnits != *o.SpreadUnits {
		t.Fatalf("pool order = filled %v settled %v hash %q spread %v/%v", o.FilledAt, o.SettledAt, o.FillTxHash, o.FillSpreadUnits, o.SpreadUnits)
	}
	t.Logf("STEP 3 PASS: intent %s settled; buyer %s got %s USDT; fill %s spread %s; outstanding %s → %s; LP claimable +%s (expected %s)",
		intent.ID, buyer, fmtUnits(got), fmtUnits(fill), fmtUnits(spread), fmtUnits(stA.Outstanding), fmtUnits(stB.Outstanding), fmtUnits(gotShare), fmtUnits(share))

	// 4. Gas-free claim by the operator, then its rate limit.
	t.Log("STEP 4: gas-free claimFor")
	lpBalC := n.usdt(n.lp.addr)
	claimable := n.position(n.lp.addr).Claimable
	hash, err := n.lpSvc.ClaimForMe(ctx, lpUser)
	if err != nil {
		t.Fatalf("claim for me: %v", err)
	}
	n.receipt(hash)
	t.Logf("  claimFor tx %s%s", vaultNetExplorer, hash)
	if d := new(big.Int).Sub(n.usdt(n.lp.addr), lpBalC); d.Cmp(claimable) != 0 {
		t.Fatalf("LP received %s, claimable was %s", fmtUnits(d), fmtUnits(claimable))
	}
	if _, err := n.lpSvc.ClaimForMe(ctx, lpUser); !errors.Is(err, ErrLPClaimRateLimit) {
		t.Fatalf("second claim err = %v, want ErrLPClaimRateLimit", err)
	}
	t.Logf("STEP 4 PASS: LP received %s USDT gas-free; second claim rate-limited", fmtUnits(claimable))

	// 4b. A second small order: the LP has something to claim at the pause.
	t.Log("STEP 4b: second pool order ₦10,000")
	intent2, o2 := n.buy(buyerUser, buyer, "10000")
	t.Logf("STEP 4b PASS: intent %s settled; fill %s spread %s; LP claimable %s", intent2.ID, fmtUnits(bigFromDecimal(o2.FillUnits)),
		fmtUnits(bigFromDecimal(o2.FillSpreadUnits)), fmtUnits(n.position(n.lp.addr).Claimable))

	// 5. Ledger: index from the deploy block, reconcile.
	t.Log("STEP 5: ledger index + reconcile")
	n.reconcileClean("after fills")
	var kinds []string
	n.db.Model(&models.LiquidityLedgerEntry{}).Distinct("kind").Order("kind").Pluck("kind", &kinds)
	t.Logf("STEP 5 PASS: ledger kinds %v", kinds)

	// 6. Close outstanding: part refilled by the PM, the rest settled from
	// collateral by the operator (idempotent per operationId).
	t.Log("STEP 6: refill + settle-from-collateral")
	out := n.state().Outstanding
	refill := new(big.Int).Quo(out, big.NewInt(2))
	rest := new(big.Int).Sub(out, refill)
	n.call(n.prov, n.f.Token, "mint(provider)", "mint", common.HexToAddress(n.prov.addr), refill)
	n.call(n.prov, n.f.Token, "approve(vault)", "approve", common.HexToAddress(n.f.Vault), refill)
	n.call(n.prov, n.f.Vault, "refill", "refill", refill, HashID(uuid.New()))
	opID, admin := uuid.New(), uuid.New()
	h1, err := n.it.SettlePoolFromCollateral(ctx, admin, opID, BaseUnitsToDecimal(rest, 18), "vaultnet rehearsal: company keeps the naira")
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	h2, err := n.it.SettlePoolFromCollateral(ctx, admin, opID, BaseUnitsToDecimal(rest, 18), "retry")
	if err != nil || h2 != h1 {
		t.Fatalf("retry before mining = %q %v, want %q", h2, err, h1)
	}
	n.receipt(h1)
	n.tick()
	n.index()
	h3, err := n.it.SettlePoolFromCollateral(ctx, admin, opID, BaseUnitsToDecimal(rest, 18), "retry after mining")
	if err != nil || h3 != h1 {
		t.Fatalf("retry after mining = %q %v, want %q", h3, err, h1)
	}
	var settles int64
	n.db.Model(&models.LiquidityLedgerEntry{}).Where("kind = ? AND LOWER(ref) = ?", VaultEventSettled, strings.ToLower(HashIDHex(opID))).Count(&settles)
	if st := n.state(); st.Outstanding.Sign() != 0 || settles != 1 {
		t.Fatalf("outstanding %s settles %d, want 0 and one settle", fmtUnits(st.Outstanding), settles)
	}
	n.reconcileClean("after settle")
	t.Logf("STEP 6 PASS: outstanding %s → refill %s + settle %s (tx %s%s, retries answered the same hash) → 0",
		fmtUnits(out), fmtUnits(refill), fmtUnits(rest), vaultNetExplorer, h1)

	// 7. The console's summary.
	t.Log("STEP 7: VaultPoolSummary")
	sum, err := n.it.VaultPoolSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sj, _ := json.Marshal(sum)
	t.Logf("  summary %s", sj)
	spread30 := bigFromDecimal(&sum.Spread30d)
	if sum.Outstanding != "0" || sum.Reserved != "0" || sum.Free != sum.Collateral || !sum.ServerOperatorActive ||
		spread30 == nil || sum.Spread30d == "" || sum.Spread30d == "0" {
		t.Fatalf("summary = %s", sj)
	}
	t.Log("STEP 7 PASS")

	// 8. Pause by the guardian: opens and deposits refused, withdraw and
	// claim still build; unpause by the owner.
	t.Log("STEP 8: pause / unpause")
	prePause, err := n.it.QuotePoolBuy(ctx, "USDT", "10000", "NGN", poolMid, specPoolSettings()) // a quote she holds as the pause lands
	if err != nil {
		t.Fatalf("quote before pause: %v", err)
	}
	n.call(n.prov, n.f.Vault, "pause", "pause")
	t.Cleanup(func() { // never leave the vault paused
		if p, _ := n.vault.State(context.Background()); p != nil && p.Paused {
			n.call(n.prov, n.f.Vault, "unpause(cleanup)", "unpause")
		}
	})
	if gs := n.gates.Check(ctx); gs.Open {
		t.Fatal("gates open while paused")
	} else {
		t.Logf("  gates closed: %v", gs.Failures)
	}
	if _, err := n.it.QuotePoolBuy(ctx, "USDT", "10000", "NGN", poolMid, specPoolSettings()); err == nil {
		t.Fatal("quote served while paused")
	} else {
		t.Logf("  quote refused: %v", err)
	}
	if _, _, err := n.it.OpenPoolBuy(ctx, buyerUser, prePause, buyer); !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("open (pre-pause quote) while paused err = %v, want ErrPoolClosed", err)
	} else {
		t.Logf("  open refused: %v", err)
	}
	n.call(n.prov, n.f.Token, "mint(lp)", "mint", common.HexToAddress(n.lp.addr), whole(5))
	n.call(n.lp, n.f.Token, "lp approve(vault)", "approve", common.HexToAddress(n.f.Vault), whole(5))
	if plan, err := n.lpSvc.PrepareDeposit(ctx, lpUser, "5"); err == nil {
		t.Fatalf("deposit prepared while paused: %+v", plan)
	} else {
		t.Logf("  deposit refused at estimate: %v (reason %q)", err, decodeRevertReason(err))
	}
	if _, err := n.lpSvc.PrepareWithdraw(ctx, lpUser, "1"); !errors.Is(err, ErrLPDepositLocked) {
		t.Fatalf("withdraw while paused err = %v, want only the deposit lock (never the pause)", err)
	}
	if plan, err := n.lpSvc.PrepareClaim(ctx, lpUser, ""); err != nil || plan.Step != "claim" {
		t.Fatalf("claim while paused = %+v %v, want it built", plan, err)
	}
	n.call(n.prov, n.f.Vault, "unpause", "unpause")
	// The receipts gate needs a tx in the last 5 blocks: a quiet testnet can
	// fail it for a moment, so re-check as the 5-minute gate job would.
	n.eventually("gates open after unpause", 3*time.Minute, func() (bool, error) {
		gs := n.gates.Check(ctx)
		return gs.Open, fmt.Errorf("%v", gs.Failures)
	})
	t.Log("STEP 8 PASS: paused → open/deposit refused, withdraw refused only by its lock, claim built; unpaused → gates open")

	provBNB1, _ := n.client.GetBalance(n.prov.addr)
	t.Logf("provider tBNB %s → %s wei (spent %s)", provBNB0, provBNB1, new(big.Int).Sub(provBNB0, provBNB1))
	_ = fmt.Sprint
}
