package services

import (
	"strings"
	"testing"
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
