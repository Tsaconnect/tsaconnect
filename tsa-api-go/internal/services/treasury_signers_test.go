package services

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// orderSigner records broadcasts in one ordered log shared with the hook.
type orderSigner struct {
	mu   sync.Mutex
	log  *[]string
	addr string
	err  error
}

func (o *orderSigner) Address() string { return o.addr }
func (o *orderSigner) SignAndBroadcast(_ context.Context, _ []byte) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	*o.log = append(*o.log, "broadcast")
	if o.err != nil {
		return "", o.err
	}
	return "0x" + strings.Repeat("ab", 32), nil
}

// signRaw / broadcastRaw make orderSigner a raw signer: the key lane runs its
// real outbox (hook inside the allocation transaction, then the broadcast).
func (o *orderSigner) signRaw(unsigned []byte) ([]byte, string, error) {
	return unsigned, crypto.Keccak256Hash(unsigned).Hex(), nil
}
func (o *orderSigner) broadcastRaw(raw []byte, hash string) (string, error) {
	if _, err := o.SignAndBroadcast(context.Background(), raw); err != nil {
		return "", err
	}
	return hash, nil
}

// nonceBroadcaster records the nonce of every raw transaction a real local
// signer hands it, and answers with err.
type nonceBroadcaster struct {
	nonces []uint64
	err    error
}

func (b *nonceBroadcaster) ChainID() *big.Int { return big.NewInt(8453) }
func (b *nonceBroadcaster) SendRawTransaction(raw []byte) (string, error) {
	var tx types.Transaction
	if err := rlp.DecodeBytes(raw, &tx); err != nil {
		return "", err
	}
	b.nonces = append(b.nonces, tx.Nonce())
	if b.err != nil {
		return "", fmt.Errorf("failed to send transaction: %w", b.err)
	}
	return tx.Hash().Hex(), nil
}

// treasuryLaneOnNode puts a real local signer on the lane, over a node that
// answers with bc.err, and an RPC whose pending view stays at 7.
func treasuryLaneOnNode(t *testing.T) (*Treasury, *TreasuryLane, *nonceBroadcaster) {
	t.Helper()
	tr, lane, native, _ := newTestTreasuryWithFakes(t)
	key, _ := crypto.GenerateKey()
	bc := &nonceBroadcaster{}
	lane.Signer = NewLocalKeyReleaseSigner(key, bc)
	lane.Native = signableTransfers{native}
	return tr, lane, bc
}

// signableTransfers prepares a transfer a real signer accepts, at the RPC's
// pending nonce 7.
type signableTransfers struct{ *fakeSponsorNative }

func (signableTransfers) PrepareERC20Transfer(token, _, _ string, _ *big.Int) ([]byte, error) {
	return json.Marshal(blockchain.UnsignedTx{Nonce: 7, To: token, Value: "0", GasPrice: "1", GasLimit: 60_000, ChainID: "8453", Data: "a9059cbb"})
}

func (b *nonceBroadcaster) last(t *testing.T) uint64 {
	t.Helper()
	if len(b.nonces) == 0 {
		t.Fatal("nothing broadcast")
	}
	return b.nonces[len(b.nonces)-1]
}

// rawRelaySigner is fakeRelaySigner as a raw signer (outbox ordering tests).
type rawRelaySigner struct{ *fakeRelaySigner }

func (r rawRelaySigner) signRaw(unsigned []byte) ([]byte, string, error) {
	return unsigned, crypto.Keccak256Hash(unsigned).Hex(), nil
}
func (r rawRelaySigner) broadcastRaw(raw []byte, hash string) (string, error) {
	if _, err := r.SignAndBroadcast(context.Background(), raw); err != nil {
		return "", err
	}
	return hash, nil
}
