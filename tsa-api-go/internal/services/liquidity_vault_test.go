package services

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
)

var testVaultAddr = common.HexToAddress("0x00000000000000000000000000000000000000aa")

func vaultLog(t *testing.T, name string, topics []common.Hash, data ...any) types.Log {
	t.Helper()
	ev := parsedLiquidityVaultABI.Events[name]
	packed, err := ev.Inputs.NonIndexed().Pack(data...)
	if err != nil {
		t.Fatalf("pack %s: %v", name, err)
	}
	return types.Log{Address: testVaultAddr, Topics: append([]common.Hash{ev.ID}, topics...), Data: packed,
		TxHash: common.HexToHash("0x01"), Index: 3, BlockNumber: 42}
}

func TestDecodeVaultLog_Filled(t *testing.T) {
	v := &LiquidityVault{addr: testVaultAddr}
	ref := HashID(uuid.New())
	treasury := common.HexToAddress("0x00000000000000000000000000000000000000bb")
	l := vaultLog(t, "Filled", []common.Hash{common.Hash(ref), common.BytesToHash(treasury.Bytes())}, big.NewInt(100), big.NewInt(1))

	ev, ok := v.DecodeVaultLog(l)
	if !ok || ev.Kind != VaultEventFilled {
		t.Fatalf("decode = %+v, %v", ev, ok)
	}
	if ev.To != treasury.Hex() || ev.Amount.Int64() != 100 || ev.Spread.Int64() != 1 || ev.Ref != "0x"+common.Bytes2Hex(ref[:]) {
		t.Fatalf("fields = %+v", ev)
	}
	if ev.Block != 42 || ev.LogIndex != 3 {
		t.Fatalf("position = %d/%d", ev.Block, ev.LogIndex)
	}

	r := &types.Receipt{Logs: []*types.Log{&l}}
	if got := v.FilledIn(r, ref); got == nil || got.Amount.Int64() != 100 {
		t.Fatalf("FilledIn = %+v", got)
	}
	if got := v.FilledIn(r, HashID(uuid.New())); got != nil {
		t.Fatalf("FilledIn matched another ref: %+v", got)
	}
}

func TestDecodeVaultLog_LPEvents(t *testing.T) {
	v := &LiquidityVault{addr: testVaultAddr}
	lp := common.HexToAddress("0x00000000000000000000000000000000000000cc")
	off := common.HexToAddress("0x00000000000000000000000000000000000000dd")
	lpTopic := common.BytesToHash(lp.Bytes())

	dep, ok := v.DecodeVaultLog(vaultLog(t, "Deposited", []common.Hash{lpTopic}, big.NewInt(5)))
	if !ok || dep.Kind != VaultEventDeposited || dep.Account != lp.Hex() || dep.Amount.Int64() != 5 {
		t.Fatalf("Deposited = %+v, %v", dep, ok)
	}
	cl, ok := v.DecodeVaultLog(vaultLog(t, "Claimed", []common.Hash{lpTopic, common.BytesToHash(off.Bytes())}, big.NewInt(7)))
	if !ok || cl.Kind != VaultEventClaimed || cl.Account != lp.Hex() || cl.To != off.Hex() || cl.Amount.Int64() != 7 {
		t.Fatalf("Claimed = %+v, %v", cl, ok)
	}
	ds, ok := v.DecodeVaultLog(vaultLog(t, "DepositorSet", []common.Hash{lpTopic}, true))
	if !ok || ds.Kind != VaultEventDepositorSet || ds.Account != lp.Hex() || !ds.Allowed {
		t.Fatalf("DepositorSet = %+v, %v", ds, ok)
	}
	wp, ok := v.DecodeVaultLog(vaultLog(t, "WithdrawalPending", []common.Hash{lpTopic}, big.NewInt(9)))
	if !ok || wp.Kind != VaultEventWithdrawalPending || wp.Account != lp.Hex() || wp.Amount.Int64() != 9 {
		t.Fatalf("WithdrawalPending = %+v, %v", wp, ok)
	}
	pd, ok := v.DecodeVaultLog(vaultLog(t, "WithdrawalPaid", []common.Hash{lpTopic}, big.NewInt(4)))
	if !ok || pd.Kind != VaultEventWithdrawalPaid || pd.Account != lp.Hex() || pd.Amount.Int64() != 4 {
		t.Fatalf("WithdrawalPaid = %+v, %v", pd, ok)
	}
}

// A ledger rebuilt from the deploy block must still read the settles emitted
// before the no-collateral upgrade (testnet history), or outstanding drifts.
func TestDecodeVaultLog_LegacySettledFromCollateral(t *testing.T) {
	v := &LiquidityVault{addr: testVaultAddr}
	ref := HashID(uuid.New())
	ev, ok := v.DecodeVaultLog(vaultLog(t, "SettledFromCollateral", []common.Hash{common.Hash(ref)}, big.NewInt(65)))
	if !ok || ev.Kind != vaultEventLegacySettled || ev.Amount.Int64() != 65 || ev.Ref != "0x"+common.Bytes2Hex(ref[:]) {
		t.Fatalf("legacy settle = %+v, %v", ev, ok)
	}
}

func TestDecodeVaultLog_RefusesForeignLogs(t *testing.T) {
	v := &LiquidityVault{addr: testVaultAddr}
	l := vaultLog(t, "Refilled", []common.Hash{{}}, big.NewInt(9))
	l.Address = common.HexToAddress("0x00000000000000000000000000000000000000ee")
	if _, ok := v.DecodeVaultLog(l); ok {
		t.Fatal("decoded a log from another contract")
	}
	transfer := types.Log{Address: testVaultAddr, Topics: []common.Hash{crypto.Keccak256Hash([]byte("Transfer(address,address,uint256)"))}}
	if _, ok := v.DecodeVaultLog(transfer); ok {
		t.Fatal("decoded a non-vault event")
	}
}

func TestVaultEventTopics_CoverEveryKind(t *testing.T) {
	if got := len(VaultEventTopics()); got != len(vaultEventKinds) {
		t.Fatalf("topics = %d, kinds = %d", got, len(vaultEventKinds))
	}
}

func TestVaultState_LiquidNeverNegative(t *testing.T) {
	s := &VaultState{Balance: big.NewInt(5), TotalPendingWithdrawals: big.NewInt(7)}
	if s.Liquid().Sign() != 0 {
		t.Fatalf("liquid = %s", s.Liquid())
	}
	s.TotalPendingWithdrawals = big.NewInt(2)
	if s.Liquid().Int64() != 3 {
		t.Fatalf("liquid = %s", s.Liquid())
	}
	if (&VaultState{}).Liquid().Sign() != 0 {
		t.Fatal("an unread state is not liquid")
	}
}

type nestedDataErr struct{ data map[string]interface{} }

func (e nestedDataErr) Error() string  { return "execution reverted" }
func (e nestedDataErr) ErrorCode() int { return -32603 }
func (e nestedDataErr) ErrorData() any { return e.data }

// Hardhat nests revert data ({"message", "data"}); geth sends the hex string.
// Both must decode, or a vault refusal would read as a transient forever.
func TestDecodeRevertReason_NestedRevertData(t *testing.T) {
	str := vaultRevert("Insufficient liquidity")
	if got := decodeRevertReason(str); got != "Insufficient liquidity" {
		t.Fatalf("hex string data: %q", got)
	}
	var de revertDataErr
	if !errors.As(str, &de) {
		t.Fatal("no data error")
	}
	nested := nestedDataErr{data: map[string]interface{}{"message": "reverted", "data": de.data}}
	if got := decodeRevertReason(nested); got != "Insufficient liquidity" {
		t.Fatalf("nested data: %q", got)
	}
}
