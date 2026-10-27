package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// fakeFeeConfigChain answers FeeConfig calls by selector with ABI-encoded outputs.
type fakeFeeConfigChain struct {
	answers map[string][]byte
	lastTo  common.Address
}

func (f *fakeFeeConfigChain) CallContract(_ context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	f.lastTo = *call.To
	for name, out := range f.answers {
		if string(call.Data[:4]) == string(parsedFeeConfigABI.Methods[name].ID) {
			return out, nil
		}
	}
	return nil, errors.New("execution reverted")
}

func (f *fakeFeeConfigChain) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return &types.Receipt{Status: types.ReceiptStatusSuccessful}, nil
}

func mustPackOutputs(t *testing.T, method string, vals ...interface{}) []byte {
	t.Helper()
	out, err := parsedFeeConfigABI.Methods[method].Outputs.Pack(vals...)
	if err != nil {
		t.Fatalf("pack %s outputs: %v", method, err)
	}
	return out
}

func TestFeeConfigFeatureKey(t *testing.T) {
	key, err := FeeConfigFeatureKey(FeeConfigFeatureOrder)
	if err != nil {
		t.Fatal(err)
	}
	// bytes32("order"): the ASCII bytes, right-padded (ethers encodeBytes32String).
	want := common.HexToHash("0x6f72646572000000000000000000000000000000000000000000000000000000")
	if common.Hash(key) != want {
		t.Fatalf("key = %x, want %x", key, want)
	}
	if _, err := FeeConfigFeatureKey(""); err == nil {
		t.Fatal("empty feature must be refused")
	}
	if _, err := FeeConfigFeatureKey("p2p_platfrom"); err == nil {
		t.Fatal("an unknown feature must be refused")
	}
}

func TestFeeConfigReader_ReadsRateLimitsAndRecipient(t *testing.T) {
	recipient := common.HexToAddress("0xaF326D5D242C9A55590540f14658adDDd3586A8d")
	usdc := common.HexToAddress("0x29219dd400f2Bf60E5a23d13Be72B486D4038894")
	fake := &fakeFeeConfigChain{answers: map[string][]byte{
		"rawRate":      mustPackOutputs(t, "rawRate", feeConfigRateTuple{Bps: 200, FlatFee: big.NewInt(100000), Active: true}),
		"limitsFor":    mustPackOutputs(t, "limitsFor", feeConfigLimitsTuple{MaxPerOrder: big.NewInt(1_000_000_000), MaxOwed: big.NewInt(20_000_000_000)}),
		"feeRecipient": mustPackOutputs(t, "feeRecipient", recipient),
	}}
	r := NewFeeConfigReader(map[int64]FeeManagerRPCClient{146: fake})
	ctx := context.Background()

	rate, err := r.RawRate(ctx, 146, "0x4826533B4897376654Bb4d4AD88B7faFD0C98528", FeeConfigFeatureOrder, usdc)
	if err != nil {
		t.Fatal(err)
	}
	if rate.BPS != 200 || rate.FlatFee.Int64() != 100000 || !rate.Active {
		t.Fatalf("rate = %+v", rate)
	}
	if fake.lastTo != common.HexToAddress("0x4826533B4897376654Bb4d4AD88B7faFD0C98528") {
		t.Fatalf("called %s, not the FeeConfig", fake.lastTo.Hex())
	}
	l, err := r.LimitsFor(ctx, 146, "0x4826533B4897376654Bb4d4AD88B7faFD0C98528", usdc)
	if err != nil || l.MaxPerOrder.Int64() != 1_000_000_000 || l.MaxOwed.Int64() != 20_000_000_000 {
		t.Fatalf("limits = %+v err %v", l, err)
	}
	got, err := r.FeeRecipient(ctx, 146, "0x4826533B4897376654Bb4d4AD88B7faFD0C98528")
	if err != nil || got != recipient {
		t.Fatalf("recipient = %s err %v", got.Hex(), err)
	}
}

func TestFeeConfigReader_UnconfiguredLimitsIsARevert(t *testing.T) {
	r := NewFeeConfigReader(map[int64]FeeManagerRPCClient{146: &fakeFeeConfigChain{answers: map[string][]byte{}}})
	_, err := r.LimitsFor(context.Background(), 146, "0x1", common.HexToAddress("0x2"))
	if !errors.Is(err, ErrFeeConfigReverted) {
		t.Fatalf("a reverting limitsFor must surface as ErrFeeConfigReverted, never as zero limits; got %v", err)
	}
}

// An RPC failure is "could not check", not a revert.
func TestFeeConfigReader_RPCErrorIsNotARevert(t *testing.T) {
	r := NewFeeConfigReader(map[int64]FeeManagerRPCClient{146: rpcDownChain{}})
	_, err := r.FeeRecipient(context.Background(), 146, "0x1")
	if err == nil || errors.Is(err, ErrFeeConfigReverted) {
		t.Fatalf("want a plain RPC error, got %v", err)
	}
}

type rpcDownChain struct{}

func (rpcDownChain) CallContract(context.Context, ethereum.CallMsg, *big.Int) ([]byte, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func (rpcDownChain) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func TestFeeConfigReader_Dormant(t *testing.T) {
	if NewFeeConfigReader(nil) != nil || NewFeeConfigReader(map[int64]FeeManagerRPCClient{146: nil}) != nil {
		t.Fatal("no RPC client must leave the reader dormant (nil)")
	}
	var r *FeeConfigReader
	if _, err := r.FeeRecipient(context.Background(), 146, "0x1"); !errors.Is(err, ErrFeeConfigDormant) {
		t.Fatalf("a nil reader must answer ErrFeeConfigDormant, got %v", err)
	}
	live := NewFeeConfigReader(map[int64]FeeManagerRPCClient{146: &fakeFeeConfigChain{}})
	if _, err := live.TxSucceeded(context.Background(), 56, "0xabc"); !errors.Is(err, ErrFeeConfigDormant) {
		t.Fatalf("a chain without an RPC client must answer ErrFeeConfigDormant, got %v", err)
	}
}

// 18-decimal amounts must reach JS as strings: a JSON number above 2^53 loses precision.
func TestFeeConfigJSON_AmountsAreStrings(t *testing.T) {
	big18, _ := new(big.Int).SetString("20000000000000000000000", 10)
	out, err := json.Marshal(struct {
		R FeeConfigRate   `json:"r"`
		L FeeConfigLimits `json:"l"`
	}{FeeConfigRate{BPS: 200, FlatFee: big18, Active: true}, FeeConfigLimits{MaxPerOrder: big18, MaxOwed: nil}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"r":{"bps":200,"flatFee":"20000000000000000000000","active":true},"l":{"maxPerOrder":"20000000000000000000000","maxOwed":"0"}}`
	if string(out) != want {
		t.Fatalf("json = %s\nwant   %s", out, want)
	}
}

func TestPackFeeConfigWrites(t *testing.T) {
	usdt := common.HexToAddress("0x55d398326f99059fF775485246999027B3197955")
	data, err := PackFeeConfigSetRates([]FeeConfigRateRow{
		{Feature: FeeConfigFeatureP2PPlatform, Token: usdt, Rate: FeeConfigRate{BPS: 190, Active: true}},
		{Feature: FeeConfigFeatureP2PGas, Token: usdt, Rate: FeeConfigRate{BPS: 0, Active: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	args, err := parsedFeeConfigABI.Methods["setRates"].Inputs.Unpack(data[4:])
	if err != nil {
		t.Fatalf("unpack setRates: %v", err)
	}
	features := args[0].([][32]byte)
	if len(features) != 2 || string(features[0][:12]) != "p2p_platform" {
		t.Fatalf("features = %q", features)
	}
	if _, err := PackFeeConfigSetRates(nil); err == nil {
		t.Fatal("an empty batch must be refused")
	}
	if _, err := PackFeeConfigSetRates([]FeeConfigRateRow{{Feature: "order", Rate: FeeConfigRate{BPS: 1}}}); err == nil {
		t.Fatal("a row without a token must be refused")
	}
	if _, err := PackFeeConfigSetRates([]FeeConfigRateRow{{Feature: "order", Token: usdt, Rate: FeeConfigRate{BPS: FeeConfigMaxBPS + 1}}}); err == nil {
		t.Fatal("a rate above MAX_BPS must be refused")
	}
	tooBig := new(big.Int).Lsh(big.NewInt(1), 128)
	if _, err := PackFeeConfigSetRates([]FeeConfigRateRow{{Feature: "order", Token: usdt, Rate: FeeConfigRate{FlatFee: tooBig}}}); err == nil {
		t.Fatal("a flat fee above uint128 must be refused")
	}
	if _, err := PackFeeConfigSetRates([]FeeConfigRateRow{
		{Feature: "order", Token: usdt, Rate: FeeConfigRate{BPS: 200}},
		{Feature: "order", Token: usdt, Rate: FeeConfigRate{BPS: 100}},
	}); err == nil {
		t.Fatal("a repeated (feature, token) row must be refused")
	}
	if _, err := PackFeeConfigSetLimits(usdt, FeeConfigLimits{MaxPerOrder: big.NewInt(1), MaxOwed: big.NewInt(0)}); err == nil {
		t.Fatal("zero limits must be refused")
	}
	if _, err := PackFeeConfigSetLimits(common.Address{}, FeeConfigLimits{MaxPerOrder: big.NewInt(1), MaxOwed: big.NewInt(1)}); err == nil {
		t.Fatal("limits without a token must be refused")
	}
	if _, err := PackFeeConfigSetLimits(usdt, FeeConfigLimits{MaxPerOrder: big.NewInt(1), MaxOwed: tooBig}); err == nil {
		t.Fatal("a limit above uint128 must be refused")
	}
	if _, err := PackFeeConfigSetFeeRecipient(common.Address{}); err == nil {
		t.Fatal("a zero fee recipient must be refused")
	}
}

// The Go ABI must match smart-contracts/contracts/core/FeeConfig.sol exactly; a selector built from
// the Solidity signature catches any drift in names, types or tuple layout.
func TestFeeConfigABI_MatchesContractSelectors(t *testing.T) {
	for method, sig := range map[string]string{
		"rawRate":         "rawRate(bytes32,address)",
		"limitsFor":       "limitsFor(address)",
		"feeRecipient":    "feeRecipient()",
		"setRates":        "setRates(bytes32[],address[],(uint16,uint128,bool)[])",
		"setLimits":       "setLimits(address,(uint128,uint128))",
		"setFeeRecipient": "setFeeRecipient(address)",
	} {
		want := crypto.Keccak256([]byte(sig))[:4]
		if got := parsedFeeConfigABI.Methods[method].ID; string(got) != string(want) {
			t.Errorf("%s selector %x, want %x (%s)", method, got, want, sig)
		}
	}
}
