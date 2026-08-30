package services

import (
	"errors"
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

func TestValidateBuyDelivery(t *testing.T) {
	const evm = "0x1111111111111111111111111111111111111111"
	const sol = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"

	for _, tc := range []struct {
		name          string
		chain, asset  string
		address       string
		wantErr       error
	}{
		{"polygon usdc", "POLYGON", "USDC", evm, nil},
		{"polygon usdt", "POLYGON", "USDT", evm, nil},
		{"ethereum usdt", "ETHEREUM", "USDT", evm, nil},
		{"solana usdc", "SOLANA", "USDC", sol, nil},
		{"base usdc", "BASE", "USDC", evm, nil},
		// Flutterwave rejects Base+USDT. A rejected transfer to a live
		// address is not recoverable, so this must fail before an intent.
		{"base usdt rejected", "BASE", "USDT", evm, ErrUnsupportedChainAsset},
		{"bsc rejected", "BSC", "USDC", evm, ErrUnsupportedChainAsset},
		{"tron rejected", "TRON", "USDT", evm, ErrUnsupportedChainAsset},
		{"evm address on solana", "SOLANA", "USDC", evm, ErrBadDeliveryAddress},
		{"solana address on polygon", "POLYGON", "USDC", sol, ErrBadDeliveryAddress},
		{"empty address", "POLYGON", "USDC", "", ErrBadDeliveryAddress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBuyDelivery(tc.chain, tc.asset, tc.address)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestDeliverableChains(t *testing.T) {
	const evm = "0x1111111111111111111111111111111111111111"
	const sol = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"

	t.Run("only chains the user can actually receive on", func(t *testing.T) {
		// No solana address → solana must not be offered, or funds land
		// somewhere the user cannot see.
		got := DeliverableChains("USDC", map[string]string{"POLYGON": evm, "BASE": evm})
		want := []string{"BASE", "POLYGON"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("got %v, want %v", got, want)
			}
		}
	})

	t.Run("usdt hides base", func(t *testing.T) {
		got := DeliverableChains("USDT", map[string]string{"POLYGON": evm, "BASE": evm, "SOLANA": sol})
		for _, c := range got {
			if c == "BASE" {
				t.Fatal("BASE offered for USDT; Flutterwave rejects that pair")
			}
		}
	})

	t.Run("unsupported chains are dropped", func(t *testing.T) {
		got := DeliverableChains("USDC", map[string]string{"BSC": evm, "SONIC": evm})
		if len(got) != 0 {
			t.Fatalf("got %v, want none", got)
		}
	})
}
