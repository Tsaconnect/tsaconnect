package services

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// With a nil client in non-production, buildUnsignedTx returns a minimal
// {to,data} tx — which still runs abi.Pack for the method, so these assert the
// ABI + arg types line up for every prepare path.
func newTestSwap() *MCGPSwapService {
	return NewMCGPSwapService(nil, &config.Config{
		Env:              "development",
		MCGPSwapBAddress: "0x000000000000000000000000000000000000bEEF",
	})
}

func decodeData(t *testing.T, b []byte) string {
	t.Helper()
	var tx blockchain.UnsignedTx
	if err := json.Unmarshal(b, &tx); err != nil {
		t.Fatalf("unmarshal unsigned tx: %v", err)
	}
	if tx.Data == "" {
		t.Fatalf("empty call data")
	}
	return tx.Data
}

func TestPrepareMethods_PackEveryCall(t *testing.T) {
	s := newTestSwap()
	from := "0x1111111111111111111111111111111111111111"
	to := "0x2222222222222222222222222222222222222222"
	amt := big.NewInt(1_000_000)
	var ref [32]byte
	copy(ref[:], []byte("ref-abc"))

	cases := []struct {
		name string
		fn   func() ([]byte, error)
	}{
		{"buyPremium", func() ([]byte, error) { return s.PrepareBuyPremium(from, amt, amt) }},
		{"spend", func() ([]byte, error) { return s.PrepareSpend(from, to, amt, ref) }},
		{"sellPremium", func() ([]byte, error) { return s.PrepareSellPremium(from, amt, amt) }},
		{"sellLegacy", func() ([]byte, error) { return s.PrepareSellLegacy(from, amt, amt) }},
		{"withdraw", func() ([]byte, error) { return s.PrepareWithdrawPremium(from, amt) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := tc.fn()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			data := decodeData(t, b)
			// Each method has a distinct 4-byte selector (8 hex chars) prefix.
			if len(data) < 8 {
				t.Fatalf("%s: data too short: %q", tc.name, data)
			}
		})
	}
}

func TestSelectorsAreDistinct(t *testing.T) {
	s := newTestSwap()
	from := "0x1111111111111111111111111111111111111111"
	amt := big.NewInt(1)
	var ref [32]byte
	seen := map[string]string{}
	add := func(name string, b []byte) {
		sel := decodeData(t, b)[:8]
		if prev, ok := seen[sel]; ok {
			t.Fatalf("selector collision %s: %s and %s", sel, prev, name)
		}
		seen[sel] = name
	}
	b, _ := s.PrepareBuyPremium(from, amt, amt)
	add("buyPremium", b)
	b, _ = s.PrepareSpend(from, from, amt, ref)
	add("spend", b)
	b, _ = s.PrepareSellPremium(from, amt, amt)
	add("sellPremium", b)
	b, _ = s.PrepareSellLegacy(from, amt, amt)
	add("sellLegacy", b)
	b, _ = s.PrepareWithdrawPremium(from, amt)
	add("withdrawPremiumToWallet", b)
}

func TestIsConfigured(t *testing.T) {
	if newTestSwap().IsConfigured() != true {
		t.Fatal("expected configured")
	}
	if NewMCGPSwapService(nil, &config.Config{}).IsConfigured() != false {
		t.Fatal("expected dormant when address empty")
	}
}
