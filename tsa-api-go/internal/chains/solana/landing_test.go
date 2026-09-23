package solana

import (
	"encoding/json"
	"os"
	"testing"

	solanago "github.com/gagliardetto/solana-go"
	solanarpc "github.com/gagliardetto/solana-go/rpc"
)

// The fixtures in testdata are REAL getTransaction captures from
// https://api.mainnet-beta.solana.com, taken with exactly the options
// RPC.Transaction asks for (base64 encoding, confirmed commitment,
// maxSupportedTransactionVersion 0). Nothing here is hand-written: every
// owner/mint/amount constant below was read out of the capture itself, which
// is the whole point — a landing proof invented by this package would prove
// nothing about what the chain actually says.
const (
	// delivery_sol_tx.json — signature
	// 2dnr2h4NJBC63iQjE7aMNi9KNkjoAGP3AyGesmCsXifvRiqKde8G6VzXaWkeGfyDEjgTNe2rx9uQK5TH5EKSf1zd
	// (slot 449666838). The owner is credited 0.02 SOL and is NOT the fee
	// payer, so the credit is the whole lamport delta.
	solFixtureOwner  = "2JiiQhWnDJdemj8vBoKPKYDqAcr93UsUTF4xVoRj7bPm"
	solFixtureAmount = "20000000"

	// delivery_spl_tx.json — signature
	// HgeEquDWjNxW4wmW8uZLFDRxgskL4GDyfu3Q92SzZ88ARcL8WXBCMF2ZRmrBPK9jPaajbznLWfjhxXVaeLppFU4
	// (slot 449666806): 10163.829864 USDC credited to an owner whose token
	// account only ever appears in meta's token balances.
	splFixtureOwner  = "DqKcF2TcXjSirHPTzoHph5WJBFzV3mFMBoz5eywi9iym"
	splFixtureMint   = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	splFixtureAmount = "10163829864"

	// fixtureStranger is in neither capture. A landing proof that pays out on
	// an address the transaction never touched is the failure this exists to
	// make impossible.
	fixtureStranger = "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"
)

func loadDeliveryFixture(t *testing.T, name string) *solanarpc.GetTransactionResult {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var tx solanarpc.GetTransactionResult
	if err := json.Unmarshal(raw, &tx); err != nil {
		t.Fatal(err)
	}
	return &tx
}

func TestReceivedFrom_ReadsTheDeliveredAmount(t *testing.T) {
	cases := []struct {
		name         string
		fixture      string
		owner, mint  string
		want         string
		strangerMint string
	}{
		{
			name: "native SOL", fixture: "delivery_sol_tx.json",
			owner: solFixtureOwner, mint: "", want: solFixtureAmount,
		},
		{
			// The System Program id is how LiFi (and our own order rows) spell
			// "native SOL", so it has to mean the same thing as an empty mint.
			name: "native SOL named by the system program id", fixture: "delivery_sol_tx.json",
			owner: solFixtureOwner, mint: "11111111111111111111111111111111", want: solFixtureAmount,
		},
		{
			name: "an SPL mint", fixture: "delivery_spl_tx.json",
			owner: splFixtureOwner, mint: splFixtureMint, want: splFixtureAmount,
			strangerMint: splFixtureMint,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := loadDeliveryFixture(t, tc.fixture)
			owner := solanago.MustPublicKeyFromBase58(tc.owner)
			if got := receivedFrom(tx, owner, tc.mint); got.String() != tc.want {
				t.Fatalf("received = %s, want %s", got, tc.want)
			}
			stranger := solanago.MustPublicKeyFromBase58(fixtureStranger)
			if got := receivedFrom(tx, stranger, tc.strangerMint); got.Sign() != 0 {
				t.Fatalf("someone else received %s; want nothing", got)
			}
		})
	}
}

// A mint the transaction never moved is not a delivery, even in a transaction
// that did credit the owner something else.
func TestReceivedFrom_IsZeroForAnotherMint(t *testing.T) {
	tx := loadDeliveryFixture(t, "delivery_spl_tx.json")
	owner := solanago.MustPublicKeyFromBase58(splFixtureOwner)
	if got := receivedFrom(tx, owner, "So11111111111111111111111111111111111111112"); got.Sign() != 0 {
		t.Fatalf("received = %s for a mint this transaction never moved", got)
	}
}

// The SPL fixture's owner is paid in tokens, not lamports: reading it as a
// native delivery must not turn a token credit into SOL the user never got.
func TestReceivedFrom_DoesNotReadTokensAsLamports(t *testing.T) {
	tx := loadDeliveryFixture(t, "delivery_spl_tx.json")
	owner := solanago.MustPublicKeyFromBase58(splFixtureOwner)
	if got := receivedFrom(tx, owner, ""); got.String() == splFixtureAmount {
		t.Fatalf("an SPL credit was read as %s lamports", got)
	}
}
