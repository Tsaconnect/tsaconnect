package sui

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/chains"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// Ground-truth interop vectors from @mysten/sui v1.45.2 for the mnemonic
// "test test test test test test test test test test test junk" at path
// m/44'/784'/0'/0'/0'. Cross-checked against @noble primitives. These pin the
// four correctness invariants (address, digest, signature serialization) to the
// official SDK.
const (
	vecPubHex = "1e6b2604ae257d45be4fb5bb3e9179936c6840552886895fe4434182228d610b"
	vecAddr   = "0xc88ef07b9b8b2fc3b7daad9478f4e1337f01792e2eab9c3794494e610636026e"
	vecTxHex  = "deadbeef00112233445566778899aabbccddeeffdeadbeef00112233445566778899aabbccddeeffdeadbeef00112233445566778899aabbccddeeff"
	vecDigest = "74202195b64a7fec6f90c304ec5ea2996641d0eb7877d3a98defefa47229a9f9"
	vecSig    = "46762fd90ded89381877837159493c411e8f7ff902b7d40a291ead95001474a0fcbc4cbdaae58f148d5de01536ef88894485c5044ab8dc2a62dc5faa68956306"
	vecSerB64 = "AEZ2L9kN7Yk4GHeDcVlJPEEej3/5ArfUCikerZUAFHSg/LxMvarljxSNXeAVNu+IiUSFxQRKuNwqYtxfqmiVYwYeayYEriV9Rb5Ptbs+kXmTbGhAVSiGiV/kQ0GCIo1hCw=="
	usdcType  = "0xdba34672e30cb065b1f93e3ab55318768fd6fef66c15942c9f7cb846e2f900e7::usdc::USDC"
)

func TestValidateAddress(t *testing.T) {
	valid := []string{
		vecAddr,
		"0x0000000000000000000000000000000000000000000000000000000000000002",
		"0xC88EF07B9B8B2FC3B7DAAD9478F4E1337F01792E2EAB9C3794494E610636026E", // upper-case accepted
	}
	for _, a := range valid {
		if err := ValidateAddress(a); err != nil {
			t.Errorf("ValidateAddress(%s) = %v, want nil", a, err)
		}
	}
	invalid := []string{
		"",
		"0x2", // short form deliberately rejected for a payment target
		"c88ef07b9b8b2fc3b7daad9478f4e1337f01792e2eab9c3794494e610636026e", // missing 0x
		"0x" + "f",                                     // too short
		"0x" + hexRepeat("f", 63),                      // 63 hex
		"0x" + hexRepeat("f", 65),                      // 65 hex
		"0x14dC79964da2C08b23698B3D3cc7Ca32193d9955",   // EVM
		"HAgk14JpMQLgt6rVgv7cBQFJWFto5Dqxi472uT3DKpqk", // Solana base58
	}
	for _, a := range invalid {
		if err := ValidateAddress(a); err == nil {
			t.Errorf("ValidateAddress(%s) = nil, want error", a)
		}
	}
}

func TestAddressFromPubkey(t *testing.T) {
	pub, _ := hex.DecodeString(vecPubHex)
	got, err := AddressFromPubkey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if got != vecAddr {
		t.Errorf("AddressFromPubkey = %s, want %s", got, vecAddr)
	}
	if _, err := AddressFromPubkey(pub[:31]); err == nil {
		t.Error("AddressFromPubkey(31 bytes) = nil error, want error")
	}
}

func TestSigningDigest(t *testing.T) {
	tx, _ := hex.DecodeString(vecTxHex)
	got := signingDigest(tx)
	if hex.EncodeToString(got[:]) != vecDigest {
		t.Errorf("signingDigest = %s, want %s", hex.EncodeToString(got[:]), vecDigest)
	}
}

func TestParseAndSerializeSignature(t *testing.T) {
	// The app packs pubkey32 || sig64; we split it and rebuild Sui's on-chain
	// base64(flag||sig||pubkey) — it must match the SDK's serialized signature.
	pub, sig, err := parsePackedSig(vecPubHex + vecSig)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(pub) != vecPubHex {
		t.Errorf("pub = %s, want %s", hex.EncodeToString(pub), vecPubHex)
	}
	if hex.EncodeToString(sig) != vecSig {
		t.Errorf("sig = %s, want %s", hex.EncodeToString(sig), vecSig)
	}
	if got := serializeSignature(pub, sig); got != vecSerB64 {
		t.Errorf("serializeSignature = %s, want %s", got, vecSerB64)
	}
	// Wrong length is rejected.
	if _, _, err := parsePackedSig(vecSig); err == nil {
		t.Error("parsePackedSig(64 bytes) = nil error, want error")
	}
}

func TestVectorSignatureVerifiesAgainstDigest(t *testing.T) {
	// The exact check AssembleAndBroadcast performs (review C2): the vector's
	// signature verifies against the intent digest under the vector's pubkey, and
	// does NOT verify against the raw (un-hashed, un-prefixed) tx bytes.
	pub, _ := hex.DecodeString(vecPubHex)
	sig, _ := hex.DecodeString(vecSig)
	digest, _ := hex.DecodeString(vecDigest)
	if !ed25519.Verify(pub, digest, sig) {
		t.Error("vector signature failed to verify against the signing digest")
	}
	tx, _ := hex.DecodeString(vecTxHex)
	if ed25519.Verify(pub, tx, sig) {
		t.Error("signature unexpectedly verified against raw tx bytes (must sign the intent digest)")
	}
}

func TestBuildFeeQuote(t *testing.T) {
	oneSui := new(big.Int).SetUint64(1_000_000_000)

	// Token send: only needs the SUI budget for gas.
	q := buildFeeQuote(oneSui, false, 0, "USDC", "SUI")
	if !q.HasEnoughGas || q.GasSymbol != "SUI" || q.EstFeeRaw != "10000000" {
		t.Errorf("token fee quote = %+v", q)
	}
	// Native send within balance: amount + budget <= balance.
	q = buildFeeQuote(oneSui, true, 500_000_000, "SUI", "SUI")
	if !q.HasEnoughGas {
		t.Errorf("native fee quote should have enough gas: %+v", q)
	}
	// Native send-max: amount == full balance leaves nothing for the budget → blocked.
	q = buildFeeQuote(oneSui, true, 1_000_000_000, "SUI", "SUI")
	if q.HasEnoughGas || q.Note == "" {
		t.Errorf("native send-max should be blocked with a note: %+v", q)
	}
	// No SUI at all → token send blocked with a helpful note.
	q = buildFeeQuote(big.NewInt(0), false, 0, "USDC", "SUI")
	if q.HasEnoughGas || q.Note == "" {
		t.Errorf("no-SUI token send should be blocked: %+v", q)
	}
}

func TestSymbolFor(t *testing.T) {
	c := NewClient(config.NonEVMChainConfig{
		NativeSymbol: "SUI",
		Tokens:       map[string]string{"USDC": usdcType},
	}, nil)

	if got := c.symbolFor(usdcType, "spoofed"); got != "USDC" {
		t.Errorf("symbolFor(configured) = %q, want USDC (config wins over metadata)", got)
	}
	// Unregistered type falls back to the (untrusted) metadata symbol.
	other := "0xabc123abc123abc123abc123abc123abc123abc123abc123abc123abc123abcd::coin::COIN"
	if got := c.symbolFor(other, "WBTC"); got != "WBTC" {
		t.Errorf("symbolFor(unregistered, meta) = %q, want WBTC", got)
	}
	// No metadata symbol → short abbreviation, not the raw 70-char type.
	got := c.symbolFor(other, "")
	if len(got) >= len(other) {
		t.Errorf("symbolFor(unregistered, no meta) = %q, want abbreviation", got)
	}
}

func TestPickCoins(t *testing.T) {
	coin := func(id string, bal uint64) SuiCoin {
		return SuiCoin{CoinObjectID: id, Balance: strconv.FormatUint(bal, 10)}
	}
	target := big.NewInt(1500)

	// Covers with the fewest coins, largest-first.
	ids, err := pickCoins([]SuiCoin{coin("a", 500), coin("b", 2000), coin("c", 100)}, false, target)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(ids) != 1 || ids[0] != "b" {
		t.Errorf("pickCoins picked %v, want [b] (the single 2000 coin covers 1500)", ids)
	}

	// Needs several coins summed.
	ids, err = pickCoins([]SuiCoin{coin("a", 800), coin("b", 800)}, false, target)
	if err != nil || len(ids) != 2 {
		t.Errorf("pickCoins = %v, %v; want 2 coins", ids, err)
	}

	// Genuine shortfall (all funds visible, not enough).
	if _, err := pickCoins([]SuiCoin{coin("a", 100)}, false, target); err == nil ||
		strings.Contains(err.Error(), "consolidate") {
		t.Errorf("want plain insufficient-balance error, got %v", err)
	}

	// Fragmentation: more coins exist than were returned → actionable message.
	if _, err := pickCoins([]SuiCoin{coin("a", 100)}, true, target); err == nil ||
		!strings.Contains(err.Error(), "consolidate") {
		t.Errorf("want consolidate hint on truncated shortfall, got %v", err)
	}
}

func TestNativeCoinTypeNormalization(t *testing.T) {
	if !isNativeCoinType(nativeCoinTypeShort) || !isNativeCoinType(nativeCoinType) {
		t.Error("both native coin-type forms should normalize to native")
	}
	if isNativeCoinType(usdcType) {
		t.Error("USDC should not be native")
	}
}

func hexRepeat(s string, n int) string {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, s[0])
	}
	return string(out)
}

// Sui transfers carry no bridge memo through this path. Silently dropping one
// would strand a bridge deposit — real funds sent with nothing tying them to
// the user's swap — so the guard rejects the request as caller-fixable rather
// than building a tx that loses the tag.
func TestPrepareRejectsBridgeMemo(t *testing.T) {
	c := NewClient(config.NonEVMChainConfig{NativeSymbol: "SUI"}, nil)
	req := chains.TransferRequest{
		From:    vecAddr,
		To:      vecAddr,
		Amount:  "1000000",
		Symbol:  "SUI",
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

// Tron's aggregator transaction and its token approval have no meaning here.
// Ignoring either would build a different transaction than the app asked for,
// so each is refused as caller-fixable.
func TestPrepareRejectsTronOnlyFields(t *testing.T) {
	c := NewClient(config.NonEVMChainConfig{NativeSymbol: "SUI"}, nil)
	for name, req := range map[string]chains.TransferRequest{
		"tronRawHex": {From: vecAddr, To: vecAddr, Amount: "1000", Symbol: "SUI", TronRawHex: "0a020f37"},
		"approve":    {From: vecAddr, To: vecAddr, Amount: "1000", Symbol: "SUI", Approve: true},
	} {
		_, err := c.PrepareTransfer(context.Background(), req)
		var invalid *chains.InvalidRequestError
		if !errors.As(err, &invalid) {
			t.Fatalf("%s: want chains.InvalidRequestError, got %T (%v)", name, err, err)
		}
	}
}
