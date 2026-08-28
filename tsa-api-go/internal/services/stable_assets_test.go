package services

import (
	"math/big"
	"testing"
)

func TestSupportsDelivery_Matrix(t *testing.T) {
	cases := []struct {
		chain, asset string
		want         bool
	}{
		{"POLYGON", "USDC", true},
		{"POLYGON", "USDT", true},
		{"ETHEREUM", "USDC", true},
		{"ETHEREUM", "USDT", true},
		{"SOLANA", "USDC", true},
		{"SOLANA", "USDT", true},
		{"BASE", "USDC", true},
		{"BASE", "USDT", false}, // Flutterwave supports USDC only on Base
		{"BSC", "USDT", false},  // transfers to BSC fail outright
		{"TRON", "USDT", false},
		{"ARBITRUM", "USDC", false},
		{"polygon", "usdc", true}, // case-insensitive
	}
	for _, c := range cases {
		if got := SupportsDelivery(c.chain, c.asset); got != c.want {
			t.Errorf("SupportsDelivery(%q,%q) = %v, want %v", c.chain, c.asset, got, c.want)
		}
	}
}

func TestValidateDeliveryAddress_EVMChains(t *testing.T) {
	const good = "0x742d35Cc6634C0532925a3b844Bc454e4438f44e"
	for _, chain := range []string{"POLYGON", "ETHEREUM", "BASE"} {
		if err := ValidateDeliveryAddress(chain, good); err != nil {
			t.Errorf("%s: valid EVM address rejected: %v", chain, err)
		}
		for _, bad := range []string{
			"",
			"742d35Cc6634C0532925a3b844Bc454e4438f44e",    // no 0x
			"0x742d35Cc6634C0532925a3b844Bc454e4438f44",   // too short
			"0x742d35Cc6634C0532925a3b844Bc454e4438f44ee", // too long
			"0xZZZd35Cc6634C0532925a3b844Bc454e4438f44e",  // non-hex
		} {
			if err := ValidateDeliveryAddress(chain, bad); err == nil {
				t.Errorf("%s: bad address %q accepted", chain, bad)
			}
		}
	}
}

func TestValidateDeliveryAddress_Solana(t *testing.T) {
	const good = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	if err := ValidateDeliveryAddress("SOLANA", good); err != nil {
		t.Fatalf("valid base58 address rejected: %v", err)
	}
	for _, bad := range []string{
		"",
		"0x742d35Cc6634C0532925a3b844Bc454e4438f44e", // EVM address on Solana
		"short",
		"9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWW0", // '0' is not in base58
		"9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWl", // 'l' is not in base58
	} {
		if err := ValidateDeliveryAddress("SOLANA", bad); err == nil {
			t.Errorf("bad Solana address %q accepted", bad)
		}
	}
}

func TestValidateDeliveryAddress_UnknownChain(t *testing.T) {
	if err := ValidateDeliveryAddress("TRON", "TXYZ"); err == nil {
		t.Fatal("unknown chain accepted")
	}
}

func TestStableBaseUnitRoundTrip(t *testing.T) {
	cases := []struct {
		major string
		units string
	}{
		{"1", "1000000"},
		{"1.5", "1500000"},
		{"0.000001", "1"},
		{"100.123456", "100123456"},
	}
	for _, c := range cases {
		got, err := StableToBaseUnits(c.major)
		if err != nil {
			t.Fatalf("StableToBaseUnits(%q): %v", c.major, err)
		}
		if got.String() != c.units {
			t.Errorf("StableToBaseUnits(%q) = %s, want %s", c.major, got, c.units)
		}
	}
}

func TestStableToBaseUnits_RejectsExcessPrecision(t *testing.T) {
	// Seven decimals cannot be represented; silently truncating would lose
	// user funds, so this must be an error.
	if _, err := StableToBaseUnits("1.1234567"); err == nil {
		t.Fatal("over-precise amount accepted")
	}
}

func TestStableFromBaseUnits(t *testing.T) {
	if got := StableFromBaseUnits(big.NewInt(1500000)); got != 1.5 {
		t.Fatalf("StableFromBaseUnits(1500000) = %v, want 1.5", got)
	}
}
