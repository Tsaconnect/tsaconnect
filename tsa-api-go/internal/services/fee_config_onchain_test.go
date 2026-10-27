package services

import (
	"context"
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
	if _, err := FeeConfigFeatureKey("a_feature_name_longer_than_31_bytes"); err == nil {
		t.Fatal("a feature over 31 bytes must be refused")
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
	r := NewFeeConfigReader(fake)
	ctx := context.Background()

	rate, err := r.RawRate(ctx, "0x4826533B4897376654Bb4d4AD88B7faFD0C98528", FeeConfigFeatureOrder, usdc)
	if err != nil {
		t.Fatal(err)
	}
	if rate.BPS != 200 || rate.FlatFee.Int64() != 100000 || !rate.Active {
		t.Fatalf("rate = %+v", rate)
	}
	if fake.lastTo != common.HexToAddress("0x4826533B4897376654Bb4d4AD88B7faFD0C98528") {
		t.Fatalf("called %s, not the FeeConfig", fake.lastTo.Hex())
	}
	l, err := r.LimitsFor(ctx, "0x4826533B4897376654Bb4d4AD88B7faFD0C98528", usdc)
	if err != nil || l.MaxPerOrder.Int64() != 1_000_000_000 || l.MaxOwed.Int64() != 20_000_000_000 {
		t.Fatalf("limits = %+v err %v", l, err)
	}
	got, err := r.FeeRecipient(ctx, "0x4826533B4897376654Bb4d4AD88B7faFD0C98528")
	if err != nil || got != recipient {
		t.Fatalf("recipient = %s err %v", got.Hex(), err)
	}
}

func TestFeeConfigReader_UnconfiguredLimitsIsAnError(t *testing.T) {
	r := NewFeeConfigReader(&fakeFeeConfigChain{answers: map[string][]byte{}})
	if _, err := r.LimitsFor(context.Background(), "0x1", common.HexToAddress("0x2")); err == nil {
		t.Fatal("a reverting limitsFor must surface as an error, never as zero limits")
	}
	if NewFeeConfigReader(nil) != nil {
		t.Fatal("a nil RPC client must leave the reader dormant (nil)")
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
	if _, err := PackFeeConfigSetLimits(usdt, FeeConfigLimits{MaxPerOrder: big.NewInt(1), MaxOwed: big.NewInt(0)}); err == nil {
		t.Fatal("zero limits must be refused")
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
