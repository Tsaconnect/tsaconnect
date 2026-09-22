package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	solanago "github.com/gagliardetto/solana-go"
)

// lifiCalldataFixture is a real /quote transactionRequest captured from LiFi
// (testdata/lifi_calldata). Decoding what LiFi actually sends — not calldata
// this package built itself — is what proves the layouts are right.
type lifiCalldataFixture struct {
	Tool        string `json:"tool"`
	FromChainID int64  `json:"fromChainId"`
	ToChainID   int64  `json:"toChainId"`
	// ToToken is the destination token the quote was asked for. Only the
	// non-EVM captures record it: on Solana it decides whether the delivery
	// lands at the owner (native SOL) or in a token account (an SPL mint).
	ToToken  string `json:"toToken"`
	Receiver string `json:"receiver"`
	Data     string `json:"data"`
}

func loadLiFiFixtures(t *testing.T) map[string]lifiCalldataFixture {
	t.Helper()
	paths, err := filepath.Glob("testdata/lifi_calldata/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no LiFi calldata fixtures found: %v", err)
	}
	out := map[string]lifiCalldataFixture{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var f lifiCalldataFixture
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out[strings.TrimSuffix(filepath.Base(p), ".json")] = f
	}
	return out
}

// Every captured tool — cross-chain bridges and same-chain swaps alike —
// decodes to the wallet the quote was asked to deliver to, on the chain it
// was asked to deliver on.
func TestDecodeLiFiDelivery_LiveFixtures(t *testing.T) {
	for name, f := range loadLiFiFixtures(t) {
		if f.ToChainID == solanaLiFiChainID {
			// A non-EVM destination puts a placeholder in BridgeData.receiver;
			// TestCheckLiFiDeliveryTo_SolanaFixtures covers those.
			continue
		}
		t.Run(name, func(t *testing.T) {
			got, err := decodeLiFiDelivery(hexutil.MustDecode(f.Data), f.FromChainID)
			if err != nil {
				t.Fatalf("%s: %v", f.Tool, err)
			}
			if got.Receiver != common.HexToAddress(f.Receiver) {
				t.Errorf("receiver = %s, want %s", got.Receiver.Hex(), f.Receiver)
			}
			if got.DestinationChainID != f.ToChainID {
				t.Errorf("destination chain = %d, want %d", got.DestinationChainID, f.ToChainID)
			}
			if got.HasDestinationCall {
				t.Error("a plain delivery must not report a destination call")
			}
		})
	}
}

func TestCheckLiFiDelivery_AcceptsTheQuotedDelivery(t *testing.T) {
	f := loadLiFiFixtures(t)["across_polygon_optimism"]
	// Checksum casing of the stored wallet must not matter.
	if err := checkLiFiDelivery(f.Data, f.FromChainID, f.ToChainID, common.HexToAddress(f.Receiver).Hex()); err != nil {
		t.Fatalf("the quoted delivery was refused: %v", err)
	}
}

func TestCheckLiFiDelivery_RefusesAnotherReceiver(t *testing.T) {
	for name, f := range loadLiFiFixtures(t) {
		t.Run(name, func(t *testing.T) {
			err := checkLiFiDelivery(f.Data, f.FromChainID, f.ToChainID, sponsorTestUser)
			if !errors.Is(err, ErrTreasuryInvalidRoute) {
				t.Fatalf("want ErrTreasuryInvalidRoute for a route paying someone else, got %v", err)
			}
		})
	}
}

func TestCheckLiFiDelivery_RefusesAnotherDestinationChain(t *testing.T) {
	f := loadLiFiFixtures(t)["polymer_base_arbitrum"]
	err := checkLiFiDelivery(f.Data, f.FromChainID, 10, f.Receiver)
	if !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("want ErrTreasuryInvalidRoute for a route to the wrong chain, got %v", err)
	}
}

// A destination call hands the bridged funds to a contract on arrival, which
// can forward them anywhere — the receiver field no longer says who ends up
// with the money.
func TestCheckLiFiDelivery_RefusesADestinationCall(t *testing.T) {
	data := testBridgeCalldata(t, sponsorTestUser, 137, true)
	err := checkLiFiDelivery(data, 8453, 137, sponsorTestUser)
	if !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("want ErrTreasuryInvalidRoute for a destination call, got %v", err)
	}
}

// The spoof a layout guess would fall for: a generic-swap call whose first
// word (transactionId, attacker-chosen) is an offset to a fake BridgeData
// naming the user, while the swap's real receiver is the attacker. The
// selector decides the layout, so the real receiver is the one checked.
func TestCheckLiFiDelivery_GenericSwapIsDecodedByItsOwnLayout(t *testing.T) {
	f := loadLiFiFixtures(t)["nordstern_bsc_usdt_to_bnb"]
	data := hexutil.MustDecode(f.Data)
	attacker := common.HexToAddress(sponsorTestUser)
	copy(data[4+3*32+12:4+4*32], attacker.Bytes()) // the swap's real receiver

	// Append a BridgeData naming the user and aim transactionId at it.
	fake := hexutil.MustDecode(testBridgeCalldata(t, f.Receiver, f.ToChainID, false))[4+32:]
	offset := new(big.Int).SetInt64(int64(len(data) - 4))
	copy(data[4:4+32], common.LeftPadBytes(offset.Bytes(), 32))
	data = append(data, fake...)

	// Precondition: read as BridgeData, the spoof really does name the user,
	// so this test fails if the decoder ever falls back to guessing a layout.
	vals, err := abi.Arguments{{Type: lifiBridgeDataType}}.Unpack(data[4:])
	if err != nil {
		t.Fatalf("spoof is not a valid BridgeData: %v", err)
	}
	if bd := abi.ConvertType(vals[0], new(lifiBridgeData)).(*lifiBridgeData); bd.Receiver != common.HexToAddress(f.Receiver) {
		t.Fatalf("spoof BridgeData names %s, want the user", bd.Receiver.Hex())
	}

	err = checkLiFiDelivery(hexutil.Encode(data), f.FromChainID, f.ToChainID, f.Receiver)
	if !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("a swap paying the attacker was accepted: %v", err)
	}
}

// A same-chain swap cannot deliver to another chain, whatever it claims.
func TestCheckLiFiDelivery_GenericSwapIsSameChainOnly(t *testing.T) {
	f := loadLiFiFixtures(t)["nordstern_base_usdc_to_weth"]
	err := checkLiFiDelivery(f.Data, f.FromChainID, 137, f.Receiver)
	if !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("want ErrTreasuryInvalidRoute for a swap asked to cross chains, got %v", err)
	}
}

// wantRefused asserts both that a route was refused and WHICH guard refused
// it: every refusal carries the same sentinel, so the sentinel alone cannot
// tell "the receiver is wrong" from "the calldata did not even parse".
func wantRefused(t *testing.T, err error, because string) {
	t.Helper()
	if !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("want ErrTreasuryInvalidRoute, got %v", err)
	}
	if !strings.Contains(err.Error(), because) {
		t.Fatalf("refused for the wrong reason: want %q in %v", because, err)
	}
}

// A Solana destination is the case BridgeData alone cannot answer: LiFi fills
// its receiver with the non-EVM placeholder and puts the real 32-byte address
// in the tool's own facet data. Every captured tool must decode back to the
// address the quote was asked to deliver to — and to nothing else.
func TestCheckLiFiDeliveryTo_SolanaFixtures(t *testing.T) {
	seen := 0
	for name, f := range loadLiFiFixtures(t) {
		if f.ToChainID != solanaLiFiChainID {
			continue
		}
		seen++
		t.Run(name, func(t *testing.T) {
			if err := checkLiFiDeliveryTo(f.Data, f.FromChainID, f.ToChainID, f.Receiver, "solana", f.ToToken); err != nil {
				t.Fatalf("%s: %v", f.Tool, err)
			}
			other := "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"
			err := checkLiFiDeliveryTo(f.Data, f.FromChainID, f.ToChainID, other, "solana", f.ToToken)
			wantRefused(t, err, "not the order's wallet")
		})
	}
	if seen == 0 {
		t.Fatal("no Solana fixtures captured")
	}
}

// For an SPL destination the owner is not where the money lands: the tokens
// are minted into a token account. Only the owner's own associated account
// will do — anything else pays the right owner on paper and someone else in
// practice.
func TestCheckLiFiDeliveryTo_SolanaChecksTheTokenAccount(t *testing.T) {
	f := loadLiFiFixtures(t)["polymer_base_solana_usdc"]
	data := hexutil.MustDecode(f.Data)

	// Precondition: the captured word really is the canonical ATA, so this
	// test fails if the fixture or the derivation ever stops agreeing.
	owner := solanago.MustPublicKeyFromBase58(f.Receiver)
	mint := solanago.MustPublicKeyFromBase58(f.ToToken)
	ata, _, err := solanago.FindAssociatedTokenAddress(owner, mint)
	if err != nil {
		t.Fatal(err)
	}
	const ataWord = 4 + (33+3)*32 // argument 2 starts at word 33; ATA is its word 3
	if got := solanago.PublicKeyFromBytes(data[ataWord : ataWord+32]); got != ata {
		t.Fatalf("fixture's token account is %s, want %s", got, ata)
	}

	// An attacker's token account under the user's own owner key.
	copy(data[ataWord:ataWord+32], solanago.MustPublicKeyFromBase58("9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin").Bytes())
	err = checkLiFiDeliveryTo(hexutil.Encode(data), f.FromChainID, f.ToChainID, f.Receiver, "solana", f.ToToken)
	wantRefused(t, err, "mints into token account")
}

// Only a native-SOL near route was captured; nothing in NEARIntentsData says
// where an SPL delivery would land, so such a route is refused rather than
// waved through on the owner alone.
func TestCheckLiFiDeliveryTo_SolanaRefusesSPLWithNoCapturedTokenAccount(t *testing.T) {
	f := loadLiFiFixtures(t)["near_base_solana_sol"]
	usdc := "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	err := checkLiFiDeliveryTo(f.Data, f.FromChainID, f.ToChainID, f.Receiver, "solana", usdc)
	wantRefused(t, err, "no captured token-account word")
}

// The destination token is the caller's knowledge, not the route's; without
// it an SPL route cannot be checked at all.
func TestCheckLiFiDeliveryTo_SolanaNeedsTheDestinationToken(t *testing.T) {
	f := loadLiFiFixtures(t)["polymer_base_solana_usdc"]
	err := checkLiFiDeliveryTo(f.Data, f.FromChainID, f.ToChainID, f.Receiver, "solana", "")
	wantRefused(t, err, "without its destination token")
}

// The receiver's position differs per facet, so a tool whose layout was never
// captured cannot be vouched for — it is refused, never guessed at.
func TestCheckLiFiDeliveryTo_UnknownSolanaToolIsRefused(t *testing.T) {
	data := testBridgeCalldata(t, lifiNonEVMAddress, solanaLiFiChainID, false) // unknown selector 0x1794958f layout
	err := checkLiFiDeliveryTo(data, 8453, solanaLiFiChainID, "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin", "solana", solanaNativeToken)
	wantRefused(t, err, "no captured Solana receiver layout")
}

// An address family with no decoder is refused rather than silently checked
// with the EVM one, which would read a placeholder as the recipient.
func TestCheckLiFiDeliveryTo_UnknownFamilyIsRefused(t *testing.T) {
	f := loadLiFiFixtures(t)["polymer_base_solana_usdc"]
	err := checkLiFiDeliveryTo(f.Data, f.FromChainID, f.ToChainID, f.Receiver, "tron", f.ToToken)
	wantRefused(t, err, "no receiver decoder")
}

// A captured Solana tool must still be refused when it is asked to deliver
// somewhere other than Solana, or hand the funds to a destination call.
func TestCheckLiFiDeliveryTo_SolanaRefusesAnotherDestination(t *testing.T) {
	f := loadLiFiFixtures(t)["near_base_solana_sol"]
	err := checkLiFiDeliveryTo(f.Data, f.FromChainID, 42161, f.Receiver, "solana", f.ToToken)
	wantRefused(t, err, "delivers on chain")
}

// The facet struct's position is an attacker-controlled offset — nothing
// upstream validates any argument but the first — so every bound has to hold
// against calldata built to break it. A 2^63-1 offset used to wrap the bounds
// check and panic the decoder.
func TestCheckLiFiDeliveryTo_RefusesMalformedSolanaCalldata(t *testing.T) {
	f := loadLiFiFixtures(t)["polymer_base_solana_usdc"]
	sound := hexutil.MustDecode(f.Data)
	const arg2Head = 4 + 2*32

	withOffset := func(offset []byte) string {
		data := append([]byte(nil), sound...)
		copy(data[arg2Head:arg2Head+32], common.LeftPadBytes(offset, 32))
		return hexutil.Encode(data)
	}
	for name, data := range map[string]string{
		"offset overflows int64":   withOffset(hexutil.MustDecode("0x7fffffffffffffff")),
		"offset is the max word":   withOffset(bytes.Repeat([]byte{0xff}, 32)),
		"offset past the calldata": withOffset(big.NewInt(int64(len(sound))).Bytes()),
		"offset in the last word":  withOffset(big.NewInt(int64(len(sound) - 4 - 32)).Bytes()),
		"truncated after arg 2":    hexutil.Encode(sound[:4+34*32]),
	} {
		t.Run(name, func(t *testing.T) {
			// A panic here is the bug this test exists for, so it is left to
			// fail the test rather than being recovered.
			err := checkLiFiDeliveryTo(data, f.FromChainID, f.ToChainID, f.Receiver, "solana", f.ToToken)
			if !errors.Is(err, ErrTreasuryInvalidRoute) {
				t.Fatalf("want ErrTreasuryInvalidRoute, got %v", err)
			}
		})
	}
}

// The bounds of lifiArgumentWord pinned directly, including the positions no
// captured facet can reach but hostile calldata can.
func TestLiFiArgumentWord_Bounds(t *testing.T) {
	word := func(v string) []byte { return common.LeftPadBytes(hexutil.MustDecode(v), 32) }
	args := append(word("0x00"), word("0x40")...) // arg 1 points at the word after itself
	args = append(args, bytes.Repeat([]byte{0xaa}, 32)...)

	if got, err := lifiArgumentWord(args, 1, 0); err != nil || !bytes.Equal(got, bytes.Repeat([]byte{0xaa}, 32)) {
		t.Fatalf("the in-bounds read failed: %x %v", got, err)
	}
	for name, tc := range map[string]struct {
		args                 []byte
		argIndex, wordOffset int
	}{
		"empty args":                 {nil, 0, 0},
		"args shorter than one word": {args[:16], 0, 0},
		"argument past the head":     {args, 3, 0},
		"negative argument":          {args, -1, 0},
		"negative word":              {args, 1, -1},
		"offset past the end":        {append(word("0x1000"), args[32:]...), 0, 0},
		"offset overflows int64":     {append(word("0x7fffffffffffffff"), args[32:]...), 0, 0},
		"offset is the max word":     {append(bytes.Repeat([]byte{0xff}, 32), args[32:]...), 0, 0},
		"word past the end":          {args, 1, 1},
		"word offset overflows":      {args, 1, math.MaxInt64 / 16},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := lifiArgumentWord(tc.args, tc.argIndex, tc.wordOffset); err == nil {
				t.Fatal("out-of-bounds read was allowed")
			}
		})
	}
}

func TestLiFiTransactionID_FromLiveFixtures(t *testing.T) {
	for name, f := range loadLiFiFixtures(t) {
		id, ok := lifiTransactionID(hexutil.MustDecode(f.Data))
		if !ok || len(id) != 66 {
			t.Errorf("%s: id = %q ok = %v", name, id, ok)
		}
	}
}

func TestCheckLiFiDelivery_RefusesUndecodableCalldata(t *testing.T) {
	transfer := "0xa9059cbb" + strings.Repeat("0", 24) + sponsorTestUser[2:] + strings.Repeat("f", 64)
	for name, data := range map[string]string{
		"empty":          "0x",
		"selector only":  "0x6d21c5df",
		"not hex":        "0xzz",
		"erc20 transfer": transfer,
	} {
		t.Run(name, func(t *testing.T) {
			err := checkLiFiDelivery(data, 8453, 137, sponsorTestUser)
			if !errors.Is(err, ErrTreasuryInvalidRoute) {
				t.Fatalf("want ErrTreasuryInvalidRoute, got %v", err)
			}
		})
	}
}

// testBridgeCalldata builds the smallest calldata the decoder accepts for a
// cross-chain route: an arbitrary facet selector followed by a BridgeData
// tuple as the first argument, the shape every LiFi bridge facet shares.
func testBridgeCalldata(t *testing.T, receiver string, destChainID int64, hasDestinationCall bool) string {
	t.Helper()
	packed, err := abi.Arguments{{Type: lifiBridgeDataType}}.Pack(lifiBridgeData{
		Bridge:             "across",
		Integrator:         "tsa",
		Receiver:           common.HexToAddress(receiver),
		MinAmount:          big.NewInt(1),
		DestinationChainId: big.NewInt(destChainID),
		HasDestinationCall: hasDestinationCall,
	})
	if err != nil {
		t.Fatal(err)
	}
	return hexutil.Encode(append([]byte{0x17, 0x94, 0x95, 0x8f}, packed...))
}
