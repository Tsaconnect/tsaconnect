package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

const (
	coreUSDT = "0x6047828dc181963ba44974801FF68e538dA5eaF9"
	coreUSDC = "0x29219dd400f2Bf60E5a23d13Be72B486D4038894"
)

// readyCoreFeeAdmin is a CoreFeeAdmin over a fake Sonic set whose gates have passed.
func readyCoreFeeAdmin(t *testing.T, chain *fakeCoreChain) *CoreFeeAdmin {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	r := testCoreRegistry(chain, &now)
	if err := r.Gate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return NewCoreFeeAdmin(r, func(_ int64, sym string) string {
		return map[string]string{"USDT": coreUSDT, "USDC": coreUSDC}[sym]
	})
}

func TestCoreFeeAdmin_State(t *testing.T) {
	chain := healthyCoreChain()
	usdt := common.HexToAddress(coreUSDT)
	chain.rates[rateKey{FeeConfigFeatureOrder, usdt}] = feeConfigRateTuple{Bps: 200, FlatFee: big.NewInt(100_000), Active: true}
	chain.limits[usdt] = feeConfigLimitsTuple{MaxPerOrder: big.NewInt(5_000_000_000), MaxOwed: big.NewInt(50_000_000_000)}
	st, err := readyCoreFeeAdmin(t, chain).State(context.Background(), 146)
	if err != nil {
		t.Fatal(err)
	}
	if st.FeeRecipient != common.HexToAddress(coreFees).Hex() || len(st.Tokens) != 2 || len(st.Rates) != 6 || len(st.Limits) != 2 {
		t.Fatalf("state = %+v", st)
	}
	var order CoreFeeRate
	for _, r := range st.Rates {
		if r.Feature == FeeConfigFeatureOrder && r.Token == "USDT" {
			order = r
		}
	}
	if order.BPS != 200 || order.Flat != "0.1" || !order.Active {
		t.Fatalf("order USDT = %+v, want 200 bps + 0.1", order)
	}
	for _, l := range st.Limits {
		if l.Token == "USDT" && (!l.Configured || l.MaxPerOrder != "5000" || l.MaxOwed != "50000") {
			t.Fatalf("USDT limits = %+v", l)
		}
		if l.Token == "USDC" && l.Configured {
			t.Fatalf("USDC limits = %+v, want not configured", l)
		}
	}
}

// Only rows that differ from the chain are planned; each tx is a CONFIG_ROLE call on FeeConfig.
func TestCoreFeeAdmin_PlanOnlyChanges(t *testing.T) {
	chain := healthyCoreChain()
	usdt := common.HexToAddress(coreUSDT)
	chain.rates[rateKey{FeeConfigFeatureOrder, usdt}] = feeConfigRateTuple{Bps: 200, FlatFee: big.NewInt(100_000), Active: true}
	a := readyCoreFeeAdmin(t, chain)
	change := CoreFeeChange{
		Rates: []CoreFeeRate{
			{Feature: FeeConfigFeatureOrder, Token: "USDT", BPS: 200, Flat: "0.10", Active: true}, // unchanged
			{Feature: FeeConfigFeatureP2PPlatform, Token: "usdt", BPS: 50, Active: true},
		},
		Limits:       []CoreFeeLimits{{Token: "USDC", MaxPerOrder: "5000", MaxOwed: "50000"}},
		FeeRecipient: "0x00000000000000000000000000000000005E5799",
	}
	plan, err := a.Plan(context.Background(), 146, change)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Txs) != 3 || len(plan.Changes) != 3 {
		t.Fatalf("plan = %+v, want setRates (1 row), setLimits USDC, setFeeRecipient", plan)
	}
	m := parsedFeeConfigABI.Methods
	want := []string{"setRates", "setLimits", "setFeeRecipient"}
	for i, tx := range plan.Txs {
		data := hexutil.MustDecode(tx.Data)
		if tx.To != coreFC || tx.ChainID != 146 || string(data[:4]) != string(m[want[i]].ID) {
			t.Fatalf("tx %d = %+v, want %s on FeeConfig", i, tx, want[i])
		}
	}
	args, _ := m["setRates"].Inputs.Unpack(hexutil.MustDecode(plan.Txs[0].Data)[4:])
	if n := len(args[1].([]common.Address)); n != 1 {
		t.Fatalf("setRates carries %d rows, want only the changed one", n)
	}

	// Once the chain holds the change, nothing is planned and Verify reports it applied.
	chain.rates[rateKey{FeeConfigFeatureP2PPlatform, usdt}] = feeConfigRateTuple{Bps: 50, FlatFee: new(big.Int), Active: true}
	chain.limits[common.HexToAddress(coreUSDC)] = feeConfigLimitsTuple{MaxPerOrder: big.NewInt(5_000_000_000), MaxOwed: big.NewInt(50_000_000_000)}
	chain.recipient = common.HexToAddress("0x00000000000000000000000000000000005E5799")
	pending, err := a.Verify(context.Background(), 146, change)
	if err != nil || len(pending) != 0 {
		t.Fatalf("verify after apply: pending=%v err=%v", pending, err)
	}
}

func TestCoreFeeAdmin_PlanRefusals(t *testing.T) {
	a := readyCoreFeeAdmin(t, healthyCoreChain())
	for name, change := range map[string]CoreFeeChange{
		"unknown token":     {Rates: []CoreFeeRate{{Feature: FeeConfigFeatureOrder, Token: "DAI", BPS: 10, Active: true}}},
		"unknown feature":   {Rates: []CoreFeeRate{{Feature: "swap", Token: "USDT", BPS: 10, Active: true}}},
		"over the cap":      {Rates: []CoreFeeRate{{Feature: FeeConfigFeatureOrder, Token: "USDT", BPS: 1001, Active: true}}},
		"flat on p2p":       {Rates: []CoreFeeRate{{Feature: FeeConfigFeatureP2PGas, Token: "USDT", BPS: 10, Flat: "0.1", Active: true}}},
		"too many decimals": {Rates: []CoreFeeRate{{Feature: FeeConfigFeatureOrder, Token: "USDT", Flat: "0.0000001", Active: true}}},
		"zero limit":        {Limits: []CoreFeeLimits{{Token: "USDT", MaxPerOrder: "0", MaxOwed: "10"}}},
		"limits twice": {Limits: []CoreFeeLimits{{Token: "USDT", MaxPerOrder: "1", MaxOwed: "10"},
			{Token: "usdt", MaxPerOrder: "2", MaxOwed: "10"}}},
		"bad recipient":     {FeeRecipient: "0x1234"},
		"hot-key recipient": {FeeRecipient: directOperator},
	} {
		if _, err := a.Plan(context.Background(), 146, change); !errors.Is(err, ErrCoreFeeInvalid) {
			t.Errorf("%s: err = %v, want ErrCoreFeeInvalid", name, err)
		}
	}
}

// Fees are authored only on a set whose gates passed.
func TestCoreFeeAdmin_RefusesASetNotReady(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := NewCoreFeeAdmin(testCoreRegistry(healthyCoreChain(), &now), func(int64, string) string { return coreUSDT })
	if _, err := a.State(context.Background(), 146); !errors.Is(err, ErrCoreSetNotReady) {
		t.Fatalf("err = %v, want ErrCoreSetNotReady", err)
	}
	if got := a.Chains(); len(got) != 1 || got[0].Ready || !strings.EqualFold(got[0].ChainKey, "sonic") {
		t.Fatalf("chains = %+v", got)
	}
	if NewCoreFeeAdmin(nil, nil) != nil {
		t.Fatal("a dormant registry has no fee admin")
	}
}
