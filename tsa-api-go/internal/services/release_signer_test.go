package services

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

type captureBroadcaster struct {
	chainID  *big.Int
	lastSent []byte
	hash     string
}

func (c *captureBroadcaster) ChainID() *big.Int { return c.chainID }
func (c *captureBroadcaster) SendRawTransaction(signed []byte) (string, error) {
	c.lastSent = signed
	return c.hash, nil
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
