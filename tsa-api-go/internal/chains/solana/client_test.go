package solana

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	bin "github.com/gagliardetto/binary"
	solanago "github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/programs/system"
	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// A well-known valid Solana address (the USDC mainnet mint) and the Phantom
// address derived from the canonical test mnemonic (see the app derivation test).
const (
	usdcMint     = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	phantomVec   = "HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk"
	usdtMint     = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
	systemAddr   = "11111111111111111111111111111111"
	someBlockash = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
)

func TestValidateAddress(t *testing.T) {
	valid := []string{usdcMint, phantomVec, usdtMint, systemAddr}
	for _, a := range valid {
		if err := ValidateAddress(a); err != nil {
			t.Errorf("ValidateAddress(%s) = %v, want nil", a, err)
		}
	}
	invalid := []string{
		"",
		"0x14dC79964da2C08b23698B3D3cc7Ca32193d9955", // EVM
		"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",         // Tron (not 32 bytes)
		"bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu", // BTC bech32
		"notbase58!!!",
	}
	for _, a := range invalid {
		if err := ValidateAddress(a); err == nil {
			t.Errorf("ValidateAddress(%s) = nil, want error", a)
		}
	}
}

func TestSymbolFor(t *testing.T) {
	c := NewClient(config.NonEVMChainConfig{
		NativeSymbol: "SOL",
		Tokens:       map[string]string{"USDC": usdcMint},
	}, nil)

	if got := c.symbolFor(usdcMint); got != "USDC" {
		t.Errorf("symbolFor(configured mint) = %q, want USDC", got)
	}
	// An unregistered mint gets a short abbreviation, not the raw 44-char string.
	got := c.symbolFor(usdtMint)
	want := usdtMint[:4] + "…" + usdtMint[len(usdtMint)-4:]
	if got != want {
		t.Errorf("symbolFor(unknown mint) = %q, want %q", got, want)
	}
}

func TestBuildFeeQuote(t *testing.T) {
	tests := []struct {
		name         string
		bal          uint64
		isToken      bool
		needsATA     bool
		nativeAmount uint64
		wantEnough   bool
		wantFee      string
		wantNoteSub  string // substring the note must contain ("" = no note)
	}{
		{"native, covers amount+fee", 1_000_000, false, false, 500_000, true, "5000", ""},
		{"native, covers fee but not amount+fee", 6000, false, false, 5000, false, "5000", "amount plus the network fee"},
		{"native, short even for fee", 4000, false, false, 0, false, "5000", "amount plus the network fee"},
		{"token, ata exists, enough", 1_000_000, true, false, 0, true, "5000", ""},
		{"token, needs ata, enough", 3_000_000, true, true, 0, true, "2044280", ""},
		{"token, needs ata, short", 1000, true, true, 0, false, "2044280", "account rent"},
		{"token, no ata, short", 1000, true, false, 0, false, "5000", "network fee)."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := buildFeeQuote(tt.bal, tt.isToken, tt.needsATA, tt.nativeAmount, "USDC", "SOL")
			if q.HasEnoughGas != tt.wantEnough {
				t.Errorf("HasEnoughGas = %v, want %v", q.HasEnoughGas, tt.wantEnough)
			}
			if q.EstFeeRaw != tt.wantFee {
				t.Errorf("EstFeeRaw = %q, want %q", q.EstFeeRaw, tt.wantFee)
			}
			if q.GasSymbol != "SOL" || q.GasDecimals != 9 {
				t.Errorf("gas = %s/%d, want SOL/9", q.GasSymbol, q.GasDecimals)
			}
			if q.NeedsActivation != tt.needsATA {
				t.Errorf("NeedsActivation = %v, want %v", q.NeedsActivation, tt.needsATA)
			}
			if tt.wantNoteSub == "" && q.Note != "" {
				t.Errorf("Note = %q, want empty", q.Note)
			}
			if tt.wantNoteSub != "" && !strings.Contains(q.Note, tt.wantNoteSub) {
				t.Errorf("Note = %q, want to contain %q", q.Note, tt.wantNoteSub)
			}
		})
	}
}

// TestMessageRoundTrip guards the AssembleAndBroadcast decode path: a legacy
// message built by solana-go must survive MarshalBinary → hex → BinDecoder →
// UnmarshalWithDecoder with its account keys and blockhash intact. This is the
// exact transform PrepareTransfer/AssembleAndBroadcast rely on.
func TestMessageRoundTrip(t *testing.T) {
	from := solanago.MustPublicKeyFromBase58(phantomVec)
	to := solanago.MustPublicKeyFromBase58(usdcMint) // any valid 32-byte key
	blockhash, err := solanago.HashFromBase58(someBlockash)
	if err != nil {
		t.Fatalf("blockhash: %v", err)
	}
	tx, err := solanago.NewTransaction(
		[]solanago.Instruction{system.NewTransferInstruction(1_000_000, from, to).Build()},
		blockhash,
		solanago.TransactionPayer(from),
	)
	if err != nil {
		t.Fatalf("NewTransaction: %v", err)
	}
	msgBytes, err := tx.Message.MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	// Round-trip through the hex + decoder path used by AssembleAndBroadcast.
	raw, _ := hex.DecodeString(hex.EncodeToString(msgBytes))
	var msg solanago.Message
	if err := msg.UnmarshalWithDecoder(bin.NewBinDecoder(raw)); err != nil {
		t.Fatalf("UnmarshalWithDecoder: %v", err)
	}
	if msg.RecentBlockhash != blockhash {
		t.Errorf("blockhash not preserved: got %s want %s", msg.RecentBlockhash, blockhash)
	}
	if len(msg.AccountKeys) < 2 || !msg.AccountKeys[0].Equals(from) {
		t.Errorf("fee payer not first account: %v", msg.AccountKeys)
	}
	// The re-marshaled message must be byte-identical (deterministic serialization).
	reMarshaled, err := msg.MarshalBinary()
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if hex.EncodeToString(reMarshaled) != hex.EncodeToString(msgBytes) {
		t.Errorf("round-trip changed the message bytes")
	}
}

func TestFamily(t *testing.T) {
	c := NewClient(config.NonEVMChainConfig{NativeSymbol: "SOL"}, nil)
	if c.Family() != "solana" {
		t.Errorf("Family() = %q, want solana", c.Family())
	}
	var _ chains.ChainClient = c // compile-time: Client satisfies the interface
}

// Solana transfers carry no bridge memo through this path. Silently dropping
// one would strand a bridge deposit — real funds sent with nothing tying them
// to the user's swap — so the guard rejects the request as caller-fixable
// rather than building a tx that loses the tag.
func TestPrepareRejectsBridgeMemo(t *testing.T) {
	c := NewClient(config.NonEVMChainConfig{NativeSymbol: "SOL"}, nil)
	req := chains.TransferRequest{
		From:    usdcMint,
		To:      usdcMint,
		Amount:  "1000000",
		Symbol:  "SOL",
		MemoHex: "3d3a4554482e455448",
	}
	_, err := c.PrepareTransfer(context.Background(), req)
	if err == nil {
		t.Fatal("want an error for a memo this path cannot carry")
	}
	var invalid *chains.InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("want chains.InvalidRequestError, got %T (%v)", err, err)
	}
}
