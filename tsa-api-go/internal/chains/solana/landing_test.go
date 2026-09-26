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


// ── a versioned (v0) transaction that loads its accounts from a lookup table ──

// delivery_sol_v0_tx.json — signature
// 4m6W5aKM5Vw15szsNAJotgtQtTWDYTRo4df1pj5froJeriRtRADDqH4hyUKMZjnKKwBeDcobn78b51a63dLVWxEz
// (slot 449678913), a REAL mainnet capture taken with the same options
// RPC.Transaction sends. It is a version-0 transaction: only 10 of its 31
// account keys are written into the message, the other 21 arrive in
// meta.loadedAddresses. The owner below is loaded writable #1, i.e. account
// index 11 — past the end of the static keys — and the transaction credits it
// 0.042 SOL for the SPL token it sold in the same transaction.
const (
	v0FixtureOwner   = "BwWK17cbHxwWBKZkUYvzxLcNQ1YVyaFezduWbtm2de6s"
	v0FixtureLamport = "42097172"
	// The same owner's token account is DEBITED in this transaction, which is
	// what makes it the honest test of "lamports and token units are
	// different questions asked of the same owner".
	v0FixtureMint = "41iqE6DLEwK7GyTwy6eZLuu5nVKbuLnYgyMFQ2qqpump"
)

// A v0 transaction's recipient is routinely an address the message never
// spells out: it comes back in meta.loadedAddresses instead. Reading only the
// static keys finds no owner, credits zero, and parks an order that was in
// fact paid.
func TestReceivedFrom_ReadsALookupTableDelivery(t *testing.T) {
	tx := loadDeliveryFixture(t, "delivery_sol_v0_tx.json")
	owner := solanago.MustPublicKeyFromBase58(v0FixtureOwner)
	if got := receivedFrom(tx, owner, ""); got.String() != v0FixtureLamport {
		t.Fatalf("received = %s lamports, want %s", got, v0FixtureLamport)
	}
	stranger := solanago.MustPublicKeyFromBase58(fixtureStranger)
	if got := receivedFrom(tx, stranger, ""); got.Sign() != 0 {
		t.Fatalf("someone else received %s; want nothing", got)
	}
}

// The owner of the v0 capture is BOTH an account key (credited lamports) and
// an SPL token-account owner (debited tokens) in the same transaction, so
// neither number can be reached by accident: asking for the mint must not
// return the lamports, and asking for lamports must not return the token
// delta. The earlier SPL fixture could not pin this — its owner is not an
// account key at all, so the lamport read stopped at a guard long before any
// arithmetic.
func TestReceivedFrom_DoesNotConfuseLamportsWithTokens(t *testing.T) {
	tx := loadDeliveryFixture(t, "delivery_sol_v0_tx.json")
	owner := solanago.MustPublicKeyFromBase58(v0FixtureOwner)
	if got := receivedFrom(tx, owner, ""); got.String() != v0FixtureLamport {
		t.Fatalf("lamports = %s, want %s", got, v0FixtureLamport)
	}
	// The mint moved the other way: the owner SOLD it, so it received none.
	if got := receivedFrom(tx, owner, v0FixtureMint); got.Sign() != 0 {
		t.Fatalf("a token DEBIT was read as a %s credit", got)
	}
}

// ── several token accounts of one mint ─────────────────────────────────────

// splitBalances builds a SYNTHETIC GetTransactionResult — not a capture, and
// labelled as such — holding one owner's token balances across two accounts of
// the same mint. A real capture of this shape would prove the same thing, but
// the two shapes that matter (a delivery beside an untouched account, and a
// self-transfer between two of her own accounts) have to be constructed to be
// tested together.
func splitBalances(t *testing.T, owner, mint string, pre, post map[uint16]string) *solanarpc.GetTransactionResult {
	t.Helper()
	pk := solanago.MustPublicKeyFromBase58(owner)
	m := solanago.MustPublicKeyFromBase58(mint)
	build := func(amounts map[uint16]string) []solanarpc.TokenBalance {
		out := make([]solanarpc.TokenBalance, 0, len(amounts))
		for _, idx := range []uint16{3, 7} { // deterministic order: low index first
			amt, ok := amounts[idx]
			if !ok {
				continue
			}
			out = append(out, solanarpc.TokenBalance{
				AccountIndex: idx, Owner: &pk, Mint: m,
				UiTokenAmount: &solanarpc.UiTokenAmount{Amount: amt, Decimals: 6},
			})
		}
		return out
	}
	return &solanarpc.GetTransactionResult{Meta: &solanarpc.TransactionMeta{
		PreTokenBalances: build(pre), PostTokenBalances: build(post),
	}}
}

// An owner can hold more than one token account of the same mint, and the
// transaction that pays her names them all. Reading only the last one is wrong
// in both directions, and both directions move money:
//
//   - understated: the delivery lands in her associated account while another
//     account of hers sits untouched later in the list. Read last-wins, the
//     credit is zero and a paid order parks for recovery.
//   - overstated: the transaction only shuffles her own tokens between two of
//     her accounts. Read last-wins, the second account's rise looks like a
//     delivery and an order settles on tokens she already had.
func TestReceivedFrom_SumsEveryTokenAccountOfTheMint(t *testing.T) {
	const owner, mint = splFixtureOwner, splFixtureMint
	cases := []struct {
		name      string
		pre, post map[uint16]string
		want      string
	}{
		{
			name: "a delivery beside an untouched account",
			pre:  map[uint16]string{3: "0", 7: "500"},
			post: map[uint16]string{3: "1000", 7: "500"},
			want: "1000",
		},
		{
			name: "her own tokens moved between her own accounts",
			pre:  map[uint16]string{3: "1000", 7: "0"},
			post: map[uint16]string{3: "0", 7: "1000"},
			want: "0",
		},
		{
			name: "a delivery into an account that did not exist before",
			pre:  map[uint16]string{7: "500"},
			post: map[uint16]string{3: "1000", 7: "500"},
			want: "1000",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := splitBalances(t, owner, mint, tc.pre, tc.post)
			if got := receivedFrom(tx, solanago.MustPublicKeyFromBase58(owner), mint); got.String() != tc.want {
				t.Fatalf("received = %s, want %s", got, tc.want)
			}
		})
	}
}
