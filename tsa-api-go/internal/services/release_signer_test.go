package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

type captureBroadcaster struct {
	chainID  *big.Int
	lastSent []byte
	hash     string
	err      error
}

func (c *captureBroadcaster) ChainID() *big.Int { return c.chainID }
func (c *captureBroadcaster) SendRawTransaction(signed []byte) (string, error) {
	c.lastSent = signed
	return c.hash, c.err
}

func TestLocalKeyReleaseSigner_SignsAndBroadcasts(t *testing.T) {
	key, _ := crypto.GenerateKey()
	wantAddr := crypto.PubkeyToAddress(key.PublicKey)
	bc := &captureBroadcaster{chainID: big.NewInt(146), hash: "0xabc"}
	signer := NewLocalKeyReleaseSigner(key, bc)

	if signer.Address() != wantAddr.Hex() {
		t.Fatalf("Address() = %s, want %s", signer.Address(), wantAddr.Hex())
	}

	unsigned, _ := json.Marshal(blockchain.UnsignedTx{
		Nonce: 7, GasPrice: "1000000000", GasLimit: 90000,
		To: "0x000000000000000000000000000000000000dEaD", Value: "0",
		Data: "abcdef", ChainID: "146",
	})
	hash, err := signer.SignAndBroadcast(context.Background(), unsigned)
	if err != nil {
		t.Fatalf("SignAndBroadcast: %v", err)
	}
	if hash != "0xabc" {
		t.Fatalf("hash = %s, want 0xabc", hash)
	}
	tx := new(types.Transaction)
	if err := rlp.DecodeBytes(bc.lastSent, tx); err != nil {
		t.Fatalf("decode broadcast tx: %v", err)
	}
	from, err := types.Sender(types.NewEIP155Signer(big.NewInt(146)), tx)
	if err != nil {
		t.Fatalf("recover sender: %v", err)
	}
	if from != wantAddr {
		t.Fatalf("recovered sender %s, want admin %s", from.Hex(), wantAddr.Hex())
	}
	if tx.Nonce() != 7 || tx.To().Hex() != common.HexToAddress("0x000000000000000000000000000000000000dEaD").Hex() {
		t.Fatalf("tx fields mismatch: nonce=%d to=%s", tx.Nonce(), tx.To())
	}
}

// A node refusing a transaction because the sender cannot pay for it is a
// definite answer: the transaction never entered any mempool. Callers treat
// ErrTxNotBroadcast as safe to retry; anything else stays "may have landed".
func TestLocalKeyReleaseSigner_InsufficientFundsIsNotBroadcast(t *testing.T) {
	key, _ := crypto.GenerateKey()
	unsigned, _ := json.Marshal(blockchain.UnsignedTx{
		Nonce: 1, GasPrice: "1000000000", GasLimit: 90000,
		To: "0x000000000000000000000000000000000000dEaD", Value: "0", ChainID: "56",
	})
	for msg, wantNotBroadcast := range map[string]bool{
		"insufficient funds for gas * price + value: balance 1, tx cost 2": true,
		"INSUFFICIENT FUNDS for transfer":                                  true,
		"InsufficientFunds":                                                true,
		"context deadline exceeded":                                        false,
		"nonce too low":                                                    false,
		// Pre-merge review M-1: the other definite refusals.
		"transaction underpriced":                                     true,
		"tx fee (1.20 ether) exceeds the configured cap (1.00 ether)": true,
		"max fee per gas less than block base fee":                    true,
		"intrinsic gas too low":                                       true,
		// The nonce is taken (our own pending tx, or a mined one): stepping
		// it back would rebuild the collision, and the floor is already right.
		"replacement transaction underpriced": false,
		"already known":                       false,
		"connection reset by peer":            false,
	} {
		bc := &captureBroadcaster{chainID: big.NewInt(56), err: fmt.Errorf("failed to send transaction: %w", errors.New(msg))}
		_, err := NewLocalKeyReleaseSigner(key, bc).SignAndBroadcast(context.Background(), unsigned)
		if err == nil {
			t.Fatalf("%q: want an error", msg)
		}
		if got := errors.Is(err, ErrTxNotBroadcast); got != wantNotBroadcast {
			t.Errorf("%q: not-broadcast = %v, want %v", msg, got, wantNotBroadcast)
		}
	}
}

// Pre-merge review M-1: the failover transport moves a broadcast on only past
// a 429 (refused unprocessed), so a final 429 means every endpoint refused it.
// A 5xx stops the failover because the node may hold the transaction: that
// one stays uncertain.
func TestLocalKeyReleaseSigner_All429IsNotBroadcastA5xxIsNot(t *testing.T) {
	key, _ := crypto.GenerateKey()
	unsigned, _ := json.Marshal(blockchain.UnsignedTx{
		Nonce: 1, GasPrice: "1000000000", GasLimit: 90000,
		To: "0x000000000000000000000000000000000000dEaD", Value: "0", ChainID: "56",
	})
	for status, wantNotBroadcast := range map[int]bool{429: true, 502: false, 503: false} {
		herr := rpc.HTTPError{StatusCode: status, Status: fmt.Sprintf("%d", status)}
		bc := &captureBroadcaster{chainID: big.NewInt(56), err: fmt.Errorf("failed to send transaction: %w", herr)}
		_, err := NewLocalKeyReleaseSigner(key, bc).SignAndBroadcast(context.Background(), unsigned)
		if got := errors.Is(err, ErrTxNotBroadcast); got != wantNotBroadcast {
			t.Errorf("HTTP %d: not-broadcast = %v, want %v", status, got, wantNotBroadcast)
		}
	}
}

// A broadcast that fails after signing may still be on the network: the error
// carries the signed transaction's hash, so the chain can later be asked about
// that very transaction — while the signer itself still returns no hash.
func TestLocalKeyReleaseSigner_UncertainBroadcastCarriesItsHash(t *testing.T) {
	key, _ := crypto.GenerateKey()
	unsigned, _ := json.Marshal(blockchain.UnsignedTx{
		Nonce: 7, GasPrice: "1000000000", GasLimit: 90000,
		To: "0x000000000000000000000000000000000000dEaD", Value: "0", ChainID: "146",
	})
	bc := &captureBroadcaster{chainID: big.NewInt(146), err: errors.New("context deadline exceeded")}
	hash, err := NewLocalKeyReleaseSigner(key, bc).SignAndBroadcast(context.Background(), unsigned)
	if err == nil || hash != "" || errors.Is(err, ErrTxNotBroadcast) || err.Error() != "context deadline exceeded" {
		t.Fatalf("hash=%q err=%v", hash, err)
	}
	var sent types.Transaction
	if derr := rlp.DecodeBytes(bc.lastSent, &sent); derr != nil {
		t.Fatal(derr)
	}
	if got := uncertainTxHash(err); got != sent.Hash().Hex() {
		t.Fatalf("uncertain hash %q, want the signed tx's %s", got, sent.Hash().Hex())
	}
	if got := uncertainTxHash(fmt.Errorf("wrapped: %w", err)); got != sent.Hash().Hex() {
		t.Fatalf("wrapped: uncertain hash %q", got)
	}
	if uncertainTxHash(notBroadcast(errors.New("insufficient funds"))) != "" {
		t.Fatal("a refusal that never left carries no hash")
	}
}

// Re-review N-1: a 429 is definite and a 5xx uncertain on the status alone;
// any other HTTP error falls through to the text rules, so a node that answers
// 400 with "insufficient funds" in its body is a definite refusal, as before M-1.
func TestLocalKeyReleaseSigner_HTTPErrorBodyIsStillRead(t *testing.T) {
	key, _ := crypto.GenerateKey()
	unsigned, _ := json.Marshal(blockchain.UnsignedTx{
		Nonce: 1, GasPrice: "1000000000", GasLimit: 90000,
		To: "0x000000000000000000000000000000000000dEaD", Value: "0", ChainID: "56",
	})
	for _, c := range []struct {
		status int
		body   string
		want   bool
	}{
		{400, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"insufficient funds for gas * price + value"}}`, true},
		{400, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"nonce too low"}}`, false},
		{502, "", false},
		// A 5xx is uncertain whatever its body says: the node (or a proxy in
		// front of it) may hold the transaction.
		{502, `{"jsonrpc":"2.0","error":{"code":-32000,"message":"insufficient funds for gas * price + value"}}`, false},
		{503, "transaction underpriced", false},
	} {
		herr := rpc.HTTPError{StatusCode: c.status, Status: fmt.Sprintf("%d", c.status), Body: []byte(c.body)}
		bc := &captureBroadcaster{chainID: big.NewInt(56), err: fmt.Errorf("failed to send transaction: %w", herr)}
		_, err := NewLocalKeyReleaseSigner(key, bc).SignAndBroadcast(context.Background(), unsigned)
		if got := errors.Is(err, ErrTxNotBroadcast); got != c.want {
			t.Errorf("HTTP %d %q: not-broadcast = %v, want %v", c.status, c.body, got, c.want)
		}
	}
}
