package services

import (
	"errors"
	"reflect"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/config"
)

type fakeDecimals map[string]struct {
	d   int
	err error
}

func (f fakeDecimals) GetTokenDecimals(addr string) (int, error) {
	r, ok := f[addr]
	if !ok {
		return 0, errors.New("no such token")
	}
	return r.d, r.err
}

func addrOf(m map[string]string) func(string) string { return func(s string) string { return m[s] } }

func TestRegistry_DecimalsReadOnChain_18dp(t *testing.T) {
	kept, dec := resolveAssetDecimals(56, "bsc", config.P2PPurposePool, []string{"USDT"},
		addrOf(map[string]string{"USDT": "0xusdt"}), fakeDecimals{"0xusdt": {d: 18}})
	if !reflect.DeepEqual(kept, []string{"USDT"}) || dec["USDT"] != 18 {
		t.Fatalf("kept = %v dec = %v", kept, dec)
	}
}

func TestRegistry_DropsAssetWithUnreadableDecimals(t *testing.T) {
	kept, dec := resolveAssetDecimals(56, "bsc", config.P2PPurposePool, []string{"USDT"},
		addrOf(map[string]string{"USDT": "0xusdt"}), fakeDecimals{"0xusdt": {err: errors.New("403 Forbidden")}})
	if len(kept) != 0 || len(dec) != 0 {
		t.Fatalf("a pool asset with unreadable decimals must be dropped: kept = %v dec = %v", kept, dec)
	}
}

// Review Focus 2 / P7.
func TestRegistry_OrdinaryChainKeepsAssetOnUnreadableDecimals(t *testing.T) {
	kept, dec := resolveAssetDecimals(146, "sonic", config.P2PPurposeP2P, []string{"USDT", "USDC"},
		addrOf(map[string]string{"USDT": "0xa", "USDC": "0xb"}), fakeDecimals{"0xa": {err: errors.New("timeout")}, "0xb": {d: 6}})
	if !reflect.DeepEqual(kept, []string{"USDT", "USDC"}) {
		t.Fatalf("kept = %v, want both (USDT on the table fallback)", kept)
	}
	if _, ok := dec["USDT"]; ok || dec["USDC"] != 6 {
		t.Fatalf("dec = %v, want only USDC read on-chain", dec)
	}
}

func poolRegistry() *P2PEscrowRegistry {
	return &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{
		146: {service: &mockEscrow{}, eip712Version: "2", domainChainID: 146, chainKey: "sonic", assets: []string{"USDT", "USDC"},
			purpose: config.P2PPurposeP2P, decimals: map[string]int{"USDT": 6, "USDC": 6}},
		56: {service: &mockEscrow{}, eip712Version: "2", domainChainID: 56, chainKey: "bsc", assets: []string{"USDT"},
			purpose: config.P2PPurposePool, decimals: map[string]int{"USDT": 18}},
	}}
}

// Review Focus 5 / P8 / S5.
func TestRegistry_PoolChainRefusesOrdinaryActivity(t *testing.T) {
	r := poolRegistry()
	if r.AssetAllowed(56, "USDT") {
		t.Fatal("ordinary P2P must not open new activity on the pool chain")
	}
	if !r.AssetDelisted(56, "USDT") {
		t.Fatal("the pool chain reads as delisted for ordinary flows (exits still work)")
	}
	if !r.AssetAllowed(146, "USDT") {
		t.Fatal("Sonic must stay allowed")
	}
	for _, c := range r.Chains() {
		if c.ChainID == 56 {
			t.Fatalf("Chains() lists the pool chain: %+v", r.Chains())
		}
	}
	if r.Purpose(56) != config.P2PPurposePool || r.Purpose(146) != config.P2PPurposeP2P || r.Purpose(1) != "" {
		t.Fatal("purpose")
	}
	if d, ok := r.TokenDecimals(56, "usdt"); !ok || d != 18 {
		t.Fatalf("decimals = %d %v", d, ok)
	}
}

func TestP2PTradeService_TokenDecimalsPrefersTheRegistry(t *testing.T) {
	s := &P2PTradeService{escrows: poolRegistry()}
	if s.TokenDecimals(56, "USDT") != 18 || s.TokenDecimals(146, "USDT") != 6 {
		t.Fatal("registry values")
	}
	// Not read on-chain: the table, unchanged.
	if s.TokenDecimals(97, "USDT") != 18 || s.TokenDecimals(14601, "USDC") != 6 {
		t.Fatal("table fallback")
	}
}
