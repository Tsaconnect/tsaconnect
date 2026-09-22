package services

import (
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// lifiCalldataFixture is a real /quote transactionRequest captured from LiFi
// (testdata/lifi_calldata). Decoding what LiFi actually sends — not calldata
// this package built itself — is what proves the layouts are right.
type lifiCalldataFixture struct {
	Tool        string `json:"tool"`
	FromChainID int64  `json:"fromChainId"`
	ToChainID   int64  `json:"toChainId"`
	Receiver    string `json:"receiver"`
	Data        string `json:"data"`
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
			if err := checkLiFiDeliveryTo(f.Data, f.FromChainID, f.ToChainID, f.Receiver, "solana"); err != nil {
				t.Fatalf("%s: %v", f.Tool, err)
			}
			other := "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"
			if err := checkLiFiDeliveryTo(f.Data, f.FromChainID, f.ToChainID, other, "solana"); !errors.Is(err, ErrTreasuryInvalidRoute) {
				t.Fatalf("a route paying someone else was accepted: %v", err)
			}
		})
	}
	if seen == 0 {
		t.Fatal("no Solana fixtures captured")
	}
}

// The receiver's position differs per facet, so a tool whose layout was never
// captured cannot be vouched for — it is refused, never guessed at.
func TestCheckLiFiDeliveryTo_UnknownSolanaToolIsRefused(t *testing.T) {
	data := testBridgeCalldata(t, lifiNonEVMAddress, solanaLiFiChainID, false) // unknown selector 0x1794958f layout
	if err := checkLiFiDeliveryTo(data, 8453, solanaLiFiChainID, "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin", "solana"); !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("err = %v", err)
	}
}

// An address family with no decoder is refused rather than silently checked
// with the EVM one, which would read a placeholder as the recipient.
func TestCheckLiFiDeliveryTo_UnknownFamilyIsRefused(t *testing.T) {
	f := loadLiFiFixtures(t)["polymer_base_solana_usdc"]
	if err := checkLiFiDeliveryTo(f.Data, f.FromChainID, f.ToChainID, f.Receiver, "tron"); !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("err = %v", err)
	}
}

// A captured Solana tool must still be refused when it is asked to deliver
// somewhere other than Solana, or hand the funds to a destination call.
func TestCheckLiFiDeliveryTo_SolanaRefusesAnotherDestination(t *testing.T) {
	f := loadLiFiFixtures(t)["near_base_solana_sol"]
	if err := checkLiFiDeliveryTo(f.Data, f.FromChainID, 42161, f.Receiver, "solana"); !errors.Is(err, ErrTreasuryInvalidRoute) {
		t.Fatalf("want ErrTreasuryInvalidRoute for a route to another chain, got %v", err)
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
