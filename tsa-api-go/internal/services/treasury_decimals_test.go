package services

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTreasury_TokenDecimalsNeedsALaneAndAToken(t *testing.T) {
	tr := NewTreasury(map[string]*TreasuryLane{"bsc": {SponsorLane: &SponsorLane{ChainKey: "bsc",
		Signer: &fakeReleaseSigner{addr: "0x1"}, Tokens: map[string]string{}}, ChainID: 56}}, TreasuryCaps{}, nil)
	if _, err := tr.TokenDecimals("base", "USDC"); err == nil {
		t.Fatal("no lane")
	}
	if _, err := tr.TokenDecimals("bsc", "USDT"); err == nil {
		t.Fatal("no token on the lane")
	}
}

// BSC stables are 18 dp: the lane's chain client is the source of truth.
func TestTreasury_TokenDecimalsReadsTheLaneToken(t *testing.T) {
	const usdt = "0x55d398326f99059fF775485246999027B3197955"
	native := &fakeSponsorNative{decimals: 6, decimalsByToken: map[string]int{strings.ToLower(usdt): 18}}
	tr := NewTreasury(map[string]*TreasuryLane{"bsc": {SponsorLane: &SponsorLane{ChainKey: "bsc",
		Signer: &fakeReleaseSigner{addr: "0x1"}, Native: native, Tokens: map[string]string{"USDT": usdt}}, ChainID: 56}}, TreasuryCaps{}, nil)
	got, err := tr.TokenDecimals("BSC", "usdt")
	if err != nil || got != 18 {
		t.Fatalf("TokenDecimals = %d, %v; want 18", got, err)
	}
}

type countingDecimals struct {
	*fakeSponsorNative
	calls int
}

func (c *countingDecimals) GetTokenDecimals(token string) (int, error) {
	c.calls++
	return c.fakeSponsorNative.GetTokenDecimals(token)
}

// Review M2: an RPC outage is asked once per 30 s, not on every quote; a
// success is cached as before.
func TestTreasury_TokenDecimalsCachesAFailedReadBriefly(t *testing.T) {
	const usdc = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	native := &countingDecimals{fakeSponsorNative: &fakeSponsorNative{decimals: 6, decimalsErr: errors.New("rpc down")}}
	now := time.Unix(1_700_000_000, 0)
	tr := NewTreasury(map[string]*TreasuryLane{"base": {SponsorLane: &SponsorLane{ChainKey: "base",
		Signer: &fakeReleaseSigner{addr: "0x1"}, Native: native, Tokens: map[string]string{"USDC": usdc}}, ChainID: 8453}},
		TreasuryCaps{}, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if _, err := tr.TokenDecimals("base", "USDC"); err == nil {
			t.Fatal("a failed read must stay an error")
		}
	}
	if native.calls != 1 {
		t.Fatalf("chain asked %d times inside the window, want 1", native.calls)
	}
	native.decimalsErr = nil
	now = now.Add(31 * time.Second)
	for i := 0; i < 3; i++ {
		if d, err := tr.TokenDecimals("base", "USDC"); err != nil || d != 6 {
			t.Fatalf("after the window = %d, %v; want 6", d, err)
		}
	}
	if native.calls != 2 {
		t.Fatalf("chain asked %d times, want one retry then the cached success", native.calls)
	}
}
