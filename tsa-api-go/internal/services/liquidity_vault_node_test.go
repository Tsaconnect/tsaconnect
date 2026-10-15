//go:build vaultnode

package services

// The vault adapter and ledger against the REAL TsaLiquidityPool on a local
// hardhat node — ABI, event decoding, StateAt and the indexer's log scan, none
// of which a fake can prove. No collateral: a fill takes LP liquidity, a
// withdraw the pool cannot pay in full goes partly pending, the refill
// wallet refills and the operator pays the pending part (payPending). Run (node + fixture first):
//
//	cd smart-contracts && npx hardhat node &
//	npx hardhat run scripts/rehearse-liquidity-pool-local.js --network localhost   # prints the JSON below
// Run against a FRESH fixture (the amounts below assume its 25 principal).
//
//	VAULT_NODE='<that JSON>' go test -tags vaultnode ./internal/services/ -run '^TestVaultNode_' -count=1 -v
//
// Hardhat's public dev keys only; the fixture refuses any chain but 31337.

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// Hardhat public dev keys: #2 the fixture's operator, #1 its refill wallet
// (collateralProvider), #5 its LP.
const (
	vaultNodeOperatorKey = "5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"
	vaultNodeRefillKey   = "59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"
	vaultNodeLPKey       = "8b3a350cf5c34c9194ca85829a2df0ec3153be0318b5e2d3348e872092edffba"
)

type vaultNodeFixture struct {
	Vault, Token, Operator, Treasury, LP, RefillWallet string
	DeployBlock                                        uint64
}

func vaultNode(t *testing.T) (*blockchain.EVMClient, *LiquidityVault, vaultNodeFixture) {
	t.Helper()
	raw := os.Getenv("VAULT_NODE")
	if raw == "" {
		t.Skip("VAULT_NODE not set")
	}
	var f vaultNodeFixture
	if err := json.Unmarshal([]byte(raw), &f); err != nil {
		t.Fatal(err)
	}
	client, err := blockchain.NewEVMClient("http://127.0.0.1:8545", 31337)
	if err != nil {
		t.Fatal(err)
	}
	return client, NewLiquidityVault(client, f.Vault), f
}

// sendAs signs an UnsignedTx with key and waits for its receipt.
func sendAs(t *testing.T, client *blockchain.EVMClient, keyHex string, unsigned []byte) *types.Receipt {
	t.Helper()
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsigned, &u); err != nil {
		t.Fatal(err)
	}
	key, _ := crypto.HexToECDSA(keyHex)
	price, _ := new(big.Int).SetString(u.GasPrice, 10)
	to := common.HexToAddress(u.To)
	tx := types.NewTx(&types.LegacyTx{Nonce: u.Nonce, GasPrice: price, Gas: u.GasLimit, To: &to, Value: big.NewInt(0), Data: common.FromHex(u.Data)})
	signed, err := types.SignTx(tx, types.NewEIP155Signer(big.NewInt(31337)), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Client.SendTransaction(context.Background(), signed); err != nil {
		t.Fatal(err)
	}
	r, err := client.WaitForReceipt(signed.Hash().Hex(), 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// eventsIn decodes the vault's events in a receipt.
func eventsIn(vault *LiquidityVault, r *types.Receipt) map[string]*big.Int {
	out := map[string]*big.Int{}
	for _, l := range r.Logs {
		if ev, ok := vault.DecodeVaultLog(*l); ok && ev.Amount != nil {
			out[ev.Kind] = ev.Amount
		}
	}
	return out
}

func TestVaultNode_FillPendingRefillPayPendingAndLedger(t *testing.T) {
	client, vault, f := vaultNode(t)
	ctx := context.Background()
	u := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e18)) }

	st, err := vault.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalPrincipal.Cmp(u(25)) != 0 || st.Balance.Cmp(u(25)) != 0 || st.TotalPendingWithdrawals.Sign() != 0 ||
		st.Outstanding.Sign() != 0 || st.Paused {
		t.Fatalf("fixture state = %+v, want a fresh fixture (25 principal, all liquid)", st)
	}
	w, err := vault.Wiring(ctx, f.Operator, f.Treasury)
	if err != nil || !w.Operator || !strings.EqualFold(w.Token.Hex(), f.Token) || !w.Recipient {
		t.Fatalf("wiring = %+v %v", w, err)
	}

	// A refused fill is refused at the estimate, with the contract's reason.
	ref := HashID(uuid.New())
	if _, err := vault.PrepareFill(f.Operator, f.LP, u(1), big.NewInt(0), ref); err == nil || decodeRevertReason(err) == "" {
		t.Fatalf("fill to a non-recipient: err = %v, want a decoded revert", err)
	}
	if _, err := vault.PrepareFill(f.Operator, f.Treasury, u(26), big.NewInt(0), ref); decodeRevertReason(err) != "Insufficient liquidity" {
		t.Fatalf("fill over liquid: err = %v, want Insufficient liquidity", err)
	}

	// 1. Fill 20 (+0.2 spread): the pool pays 20, the company owes 20.2.
	unsigned, err := vault.PrepareFill(f.Operator, f.Treasury, u(20), new(big.Int).Div(u(1), big.NewInt(5)), ref)
	if err != nil {
		t.Fatalf("prepare fill: %v", err)
	}
	r := sendAs(t, client, vaultNodeOperatorKey, unsigned)
	ev := vault.FilledIn(r, ref)
	if r.Status != 1 || ev == nil || ev.Amount.Cmp(u(20)) != 0 || !strings.EqualFold(ev.To, f.Treasury) {
		t.Fatalf("receipt status=%d event=%+v", r.Status, ev)
	}
	if used, err := vault.FillUsed(ctx, ref); err != nil || !used {
		t.Fatalf("FillUsed = %v %v", used, err)
	}
	owed := new(big.Int).Add(u(20), new(big.Int).Div(u(1), big.NewInt(5)))
	st, _ = vault.State(ctx)
	if st.Outstanding.Cmp(owed) != 0 || st.Liquid().Cmp(u(5)) != 0 {
		t.Fatalf("after fill: outstanding %s liquid %s, want 20.2 and 5", st.Outstanding, st.Liquid())
	}

	// 2. The LP withdraws all 25: 5 now, 20 pending (it stops earning at once).
	unsigned, err = vault.PrepareWithdraw(f.LP, u(25))
	if err != nil {
		t.Fatalf("prepare withdraw: %v", err)
	}
	r = sendAs(t, client, vaultNodeLPKey, unsigned)
	evs := eventsIn(vault, r)
	if r.Status != 1 || evs[VaultEventWithdrawn].Cmp(u(5)) != 0 || evs[VaultEventWithdrawalPending].Cmp(u(20)) != 0 {
		t.Fatalf("withdraw status=%d events=%v, want 5 paid and 20 pending", r.Status, evs)
	}
	pos, err := vault.Position(ctx, f.LP)
	if err != nil || pos.Principal.Sign() != 0 || pos.PendingWithdrawal.Cmp(u(20)) != 0 || pos.Claimable.Sign() == 0 {
		t.Fatalf("lp position = %+v %v, want no principal, 20 pending, the spread claimable", pos, err)
	}
	if p, err := vault.PendingWithdrawal(ctx, f.LP); err != nil || p.Cmp(u(20)) != 0 {
		t.Fatalf("pendingWithdrawal = %v %v", p, err)
	}

	// 3. Nothing is liquid: a claim is refused at estimate → claim_unavailable.
	if _, err := vault.PrepareClaim(f.LP); !errors.Is(lpRevertError(err), ErrLPClaimUnavailable) {
		t.Fatalf("claim with nothing liquid: %v, want ErrLPClaimUnavailable", err)
	}

	// 4. The refill wallet refills what is owed (approved by the fixture).
	refillABI, _ := abi.JSON(strings.NewReader(`[{"type":"function","name":"refill","inputs":[{"name":"amount","type":"uint256"},{"name":"ref","type":"bytes32"}],"outputs":[],"stateMutability":"nonpayable"}]`))
	data, _ := refillABI.Pack("refill", owed, HashID(uuid.New()))
	unsigned, err = client.PrepareContractCall(f.RefillWallet, f.Vault, data, nil, 0)
	if err != nil {
		t.Fatalf("prepare refill: %v", err)
	}
	if r = sendAs(t, client, vaultNodeRefillKey, unsigned); r.Status != 1 {
		t.Fatal("refill reverted")
	}

	// 5. The operator pays the pending withdrawal, to the LP only.
	unsigned, err = vault.PreparePayPending(f.Operator, f.LP)
	if err != nil {
		t.Fatalf("prepare payPending: %v", err)
	}
	r = sendAs(t, client, vaultNodeOperatorKey, unsigned)
	if evs = eventsIn(vault, r); r.Status != 1 || evs[VaultEventWithdrawalPaid].Cmp(u(20)) != 0 {
		t.Fatalf("payPending status=%d events=%v, want 20 paid", r.Status, evs)
	}
	st, _ = vault.State(ctx)
	if st.Outstanding.Sign() != 0 || st.TotalPendingWithdrawals.Sign() != 0 || st.TotalPrincipal.Sign() != 0 {
		t.Fatalf("after refill + payPending: %+v, want nothing owed", st)
	}

	// 6. The indexer reads the real logs, and the rebuilt books match StateAt.
	db := setupTradeDB(t)
	if err := db.AutoMigrate(&models.LiquidityLedgerEntry{}, &models.LiquidityIndexCursor{}, &models.PoolOrder{}); err != nil {
		t.Fatal(err)
	}
	x, err := NewVaultIndexer(VaultIndexerConfig{DB: db, ChainID: 31337, Vault: vault, Reader: client.Client,
		DeployBlock: f.DeployBlock, Chunk: 5, Finality: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Client.Client().Call(nil, "hardhat_mine", "0x2"); err != nil { // make the last tx final
		t.Fatal(err)
	}
	if _, err := x.IndexOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	db.Model(&models.LiquidityLedgerEntry{}).Distinct("kind").Order("kind").Pluck("kind", &kinds)
	if strings.Join(kinds, ",") != "deposited,depositor_set,filled,operator_set,refilled,withdrawal_paid,withdrawal_pending,withdrawn" {
		t.Fatalf("ledger kinds = %v", kinds)
	}
	rep, err := x.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// SQLite keeps 18-dp amounts above int64 as REAL, so exact drift is a
	// Postgres check; the stray fill (no pool order here) must be reported.
	found := false
	for _, ref2 := range rep.UnknownFills {
		found = found || ref2 == "0x"+common.Bytes2Hex(ref[:])
	}
	if !found {
		t.Fatalf("unknown fills = %v, want this test's fill", rep.UnknownFills)
	}
}
