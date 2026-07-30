// Tests for the MCGP swap handler's ref derivation.
//
// The `ref` is the only thing standing between one payment intent and two
// on-chain spends: `usedRef` rejects a duplicate ref, but two independently
// prepared refs look like two different payments to both the chain and the
// ledger's ref_hash index. Under the gasless relay path a prepare can be
// retried after an ambiguous response, so a ref that is random per call is a
// double-spend waiting to happen.

package handlers

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestMCGPSwapHandler_PrepareSpend_IsIdempotentPerIntent is the core guarantee:
// the same payer retrying the same intent must land on the same ref.
func TestMCGPSwapHandler_PrepareSpend_IsIdempotentPerIntent(t *testing.T) {
	const payer = "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"
	const key = "intent-9f3a1c"

	first := payRef(payer, key)
	second := payRef(payer, key)

	if first != second {
		t.Fatalf("same payer + same idempotency key must derive one ref: got %s then %s",
			first.Hex(), second.Hex())
	}
	if first == (common.Hash{}) {
		t.Fatal("derived ref must not be the zero hash — the contract rejects it")
	}
}

func TestPayRef_DiffersPerPayer(t *testing.T) {
	const key = "intent-9f3a1c"
	a := payRef("0x87c2422be9A11b71A5c530ecd156134D3b04E3cE", key)
	b := payRef("0x09124a9E3c1B5AcF4207E33828195D00846E5EA8", key)

	if a == b {
		t.Fatal("two payers using the same key must not collide — otherwise one user can burn another's ref")
	}
}

func TestPayRef_DiffersPerKey(t *testing.T) {
	const payer = "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"
	if payRef(payer, "intent-one") == payRef(payer, "intent-two") {
		t.Fatal("distinct intents must derive distinct refs")
	}
}

// A payer's wallet address arrives checksummed from some call sites and
// lowercased from others; the ref must not depend on which.
func TestPayRef_PayerCaseAndWhitespaceInsensitive(t *testing.T) {
	const key = "intent-9f3a1c"
	checksummed := payRef("0x87c2422be9A11b71A5c530ecd156134D3b04E3cE", key)
	lowered := payRef(strings.ToLower("0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"), key)
	padded := payRef("  0x87c2422be9A11b71A5c530ecd156134D3b04E3cE  ", key)

	if checksummed != lowered || checksummed != padded {
		t.Fatalf("ref must be stable across address casing/whitespace: %s / %s / %s",
			checksummed.Hex(), lowered.Hex(), padded.Hex())
	}
}

// Whitespace-only keys are treated as absent rather than as a shared key —
// otherwise every caller sending "" -> " " would derive one ref and permanently
// block each other's payments.
func TestPayRef_BlankKeyFallsBackToRandom(t *testing.T) {
	const payer = "0x87c2422be9A11b71A5c530ecd156134D3b04E3cE"

	for _, blank := range []string{"", "   ", "\t"} {
		a := payRef(payer, blank)
		b := payRef(payer, blank)
		if a == b {
			t.Fatalf("blank key %q must mint a fresh random ref per call, got %s twice", blank, a.Hex())
		}
	}
}
