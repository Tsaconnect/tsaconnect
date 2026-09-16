package services

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// fakeTokenCaller answers name()/version()/DOMAIN_SEPARATOR() by selector.
// A selector absent from the map reverts, like USDT0's missing version().
type fakeTokenCaller map[string][]byte

func (f fakeTokenCaller) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	if out, ok := f[hex.EncodeToString(msg.Data[:4])]; ok {
		return out, nil
	}
	return nil, errors.New("execution reverted")
}

func abiString(t *testing.T, s string) []byte {
	t.Helper()
	typ, _ := abi.NewType("string", "", nil)
	out, err := abi.Arguments{{Type: typ}}.Pack(s)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReadAuthorizationDomain_BaseUSDCMatchesChain(t *testing.T) {
	caller := fakeTokenCaller{
		"06fdde03": abiString(t, "USD Coin"),
		"54fd4d50": abiString(t, "2"),
		"3644e515": common.FromHex("0x02fa7265e7c5d81118673727957699e4d68f74cd74b7db77da710fe8a2c7834f"),
	}
	d, err := ReadAuthorizationDomain(context.Background(), caller, big.NewInt(8453), "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913")
	if err != nil {
		t.Fatalf("ReadAuthorizationDomain: %v", err)
	}
	if d.Name != "USD Coin" || d.Version != "2" || d.ChainID != "8453" ||
		d.VerifyingContract != "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" {
		t.Fatalf("domain = %+v", d)
	}
}

func TestReadAuthorizationDomain_USDT0DefaultsVersionToOne(t *testing.T) {
	caller := fakeTokenCaller{
		"06fdde03": abiString(t, "USD₮0"),
		"3644e515": common.FromHex("0x566af68fb471b22d6421762f84aa7bd761c670a2e4d5c8a47d4085d5957b127c"),
	}
	d, err := ReadAuthorizationDomain(context.Background(), caller, big.NewInt(42161), "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9")
	if err != nil {
		t.Fatalf("ReadAuthorizationDomain: %v", err)
	}
	if d.Version != "1" {
		t.Fatalf("version = %q, want 1", d.Version)
	}
}

func TestReadAuthorizationDomain_RefusesMismatch(t *testing.T) {
	caller := fakeTokenCaller{
		"06fdde03": abiString(t, "USD Coin"),
		"54fd4d50": abiString(t, "2"),
		"3644e515": common.FromHex("0x02fa7265e7c5d81118673727957699e4d68f74cd74b7db77da710fe8a2c7834f"),
	}
	// Right token, wrong chain: the separator cannot match.
	_, err := ReadAuthorizationDomain(context.Background(), caller, big.NewInt(42161), "0x833589fcd6edb6e08f4c7c32d4f71b54bda02913")
	if !errors.Is(err, ErrSponsorDomainMismatch) {
		t.Fatalf("want ErrSponsorDomainMismatch, got %v", err)
	}
}

func TestRampSponsorMode_Table(t *testing.T) {
	cases := map[[2]string]SponsorMode{
		{"base", "USDC"}:     SponsorModeAuthorization,
		{"BASE", "usdt"}:     SponsorModeNone,
		{"arbitrum", "USDT"}: SponsorModeAuthorization,
		{"polygon", "USDT"}:  SponsorModeTopUp,
		{"bsc", "USDC"}:      SponsorModeTopUp,
		{"ethereum", "USDC"}: SponsorModeNone,
		{"base", "DAI"}:      SponsorModeNone,
	}
	for in, want := range cases {
		if got := RampSponsorMode(in[0], in[1]); got != want {
			t.Errorf("RampSponsorMode(%s, %s) = %s, want %s", in[0], in[1], got, want)
		}
	}
}

func TestDecimalToBaseUnits(t *testing.T) {
	ok := []struct {
		in   string
		dec  int
		want string
	}{
		{"36.421800", 6, "36421800"},
		{"1", 18, "1000000000000000000"},
		{"0.000001", 6, "1"},
		{"14.708586", 18, "14708586000000000000"},
	}
	for _, c := range ok {
		got, err := DecimalToBaseUnits(c.in, c.dec)
		if err != nil || got.String() != c.want {
			t.Errorf("DecimalToBaseUnits(%q, %d) = %v, %v; want %s", c.in, c.dec, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "-1", "1.0000001", "1e6", "abc", "1.2.3"} {
		if _, err := DecimalToBaseUnits(bad, 6); err == nil {
			t.Errorf("DecimalToBaseUnits(%q, 6) accepted", bad)
		}
	}
}
