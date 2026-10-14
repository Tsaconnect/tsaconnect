//go:build vaultnode

package services

// The vault adapter and ledger against the REAL TsaLiquidityPool on a local
// hardhat node — ABI, event decoding, StateAt and the indexer's log scan, none
// of which a fake can prove. Run (node + fixture first):
//
//	cd smart-contracts && npx hardhat node &
//	npx hardhat run scripts/rehearse-liquidity-pool-local.js --network localhost   # prints the JSON below
//	VAULT_NODE='<that JSON>' go test -tags vaultnode ./internal/services/ -run '^TestVaultNode_' -count=1 -v
//
// Hardhat's public dev keys only; the fixture refuses any chain but 31337.

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// hardhat account #2 (the fixture's operator): a public test key.
const vaultNodeOperatorKey = "5de4111afa1a4b94908f83103eb1f1706367c2e68ca870fc3fb9a804cdab365a"

type vaultNodeFixture struct {
	Vault, Token, Operator, Treasury, LP string
	DeployBlock                          uint64
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

func TestVaultNode_FillDecodeAndLedger(t *testing.T) {
	client, vault, f := vaultNode(t)
	ctx := context.Background()
	u := func(n int64) *big.Int { return new(big.Int).Mul(big.NewInt(n), big.NewInt(1e18)) }

	st, err := vault.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := new(big.Int).Set(st.Outstanding)
	if st.Collateral.Sign() == 0 || st.TotalPrincipal.Cmp(u(25)) < 0 || st.Paused {
		t.Fatalf("fixture state = %+v", st)
	}
	w, err := vault.Wiring(ctx, f.Treasury)
	if err != nil || !strings.EqualFold(w.Operator.Hex(), f.Operator) || !strings.EqualFold(w.Token.Hex(), f.Token) || !w.Recipient {
		t.Fatalf("wiring = %+v %v", w, err)
	}

	// A refused fill is refused at the estimate, with the contract's reason.
	ref := HashID(uuid.New())
	if _, err := vault.PrepareFill(f.Operator, f.LP, u(1), big.NewInt(0), ref); err == nil || decodeRevertReason(err) == "" {
		t.Fatalf("fill to a non-recipient: err = %v, want a decoded revert", err)
	}

	unsigned, err := vault.PrepareFill(f.Operator, f.Treasury, u(10), u(1), ref)
	if err != nil {
		t.Fatalf("prepare fill: %v", err)
	}
	r := sendAs(t, client, vaultNodeOperatorKey, unsigned)
	ev := vault.FilledIn(r, ref)
	if r.Status != 1 || ev == nil || ev.Amount.Cmp(u(10)) != 0 || ev.Spread.Cmp(u(1)) != 0 || !strings.EqualFold(ev.To, f.Treasury) {
		t.Fatalf("receipt status=%d event=%+v", r.Status, ev)
	}
	if used, err := vault.FillUsed(ctx, ref); err != nil || !used {
		t.Fatalf("FillUsed = %v %v", used, err)
	}
	st, _ = vault.State(ctx)
	if new(big.Int).Sub(st.Outstanding, before).Cmp(u(11)) != 0 {
		t.Fatalf("outstanding moved by %s, want 11", new(big.Int).Sub(st.Outstanding, before))
	}
	pos, err := vault.Position(ctx, f.LP)
	if err != nil || !pos.Allowed || pos.Claimable.Sign() == 0 {
		t.Fatalf("lp position = %+v %v", pos, err)
	}

	// The indexer reads the real logs, and the rebuilt books match StateAt.
	db := setupTradeDB(t)
	if err := db.AutoMigrate(&models.LiquidityLedgerEntry{}, &models.LiquidityIndexCursor{}, &models.PoolOrder{}); err != nil {
		t.Fatal(err)
	}
	x, err := NewVaultIndexer(VaultIndexerConfig{DB: db, ChainID: 31337, Vault: vault, Reader: client.Client,
		DeployBlock: f.DeployBlock, Chunk: 5, Finality: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Client.Client().Call(nil, "hardhat_mine", "0x2"); err != nil { // make the fill final
		t.Fatal(err)
	}
	n, err := x.IndexOnce(ctx)
	if err != nil || n < 4 { // collateral posted, depositor set, deposited, filled (+ earlier runs' fills)
		t.Fatalf("indexed %d %v, want the fixture's events and the fill", n, err)
	}
	var kinds []string
	db.Model(&models.LiquidityLedgerEntry{}).Distinct("kind").Order("kind").Pluck("kind", &kinds)
	if strings.Join(kinds, ",") != "collateral_posted,deposited,depositor_set,filled" {
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
