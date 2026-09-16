package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// ReleaseSigner signs and broadcasts the unsigned adminRelease tx produced by
// P2PEscrowService.PrepareAdminRelease with the ONE platform admin EOA key. The
// key never lives in the DB. Prod = AWS KMS (release_signer_kms.go, later); dev
// = LocalKeyReleaseSigner; tests = fakeReleaseSigner.
type ReleaseSigner interface {
	// Address is the admin EOA address — the `from` passed to PrepareAdminRelease.
	Address() string
	// SignAndBroadcast signs the JSON-encoded blockchain.UnsignedTx and broadcasts
	// it, returning the tx hash.
	SignAndBroadcast(ctx context.Context, unsignedTx []byte) (txHash string, err error)
}

// txBroadcaster is the slice of *blockchain.EVMClient the signer needs.
type txBroadcaster interface {
	SendRawTransaction(signedTx []byte) (string, error)
	ChainID() *big.Int
}

// LocalKeyReleaseSigner holds the admin key in process. Dev/staging only.
type LocalKeyReleaseSigner struct {
	key *ecdsa.PrivateKey
	bc  txBroadcaster
}

func NewLocalKeyReleaseSigner(key *ecdsa.PrivateKey, bc txBroadcaster) *LocalKeyReleaseSigner {
	return &LocalKeyReleaseSigner{key: key, bc: bc}
}

func (s *LocalKeyReleaseSigner) Address() string {
	return crypto.PubkeyToAddress(s.key.PublicKey).Hex()
}

func (s *LocalKeyReleaseSigner) SignAndBroadcast(ctx context.Context, unsignedTx []byte) (string, error) {
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsignedTx, &u); err != nil {
		return "", notBroadcast(fmt.Errorf("decode unsigned tx: %w", err))
	}
	gasPrice, ok := new(big.Int).SetString(u.GasPrice, 10)
	if !ok {
		return "", notBroadcast(fmt.Errorf("bad gasPrice %q", u.GasPrice))
	}
	value, ok := new(big.Int).SetString(u.Value, 10)
	if !ok {
		value = big.NewInt(0)
	}
	chainID, ok := new(big.Int).SetString(u.ChainID, 10)
	if !ok {
		chainID = s.bc.ChainID()
	}
	data, err := hexutil.Decode("0x" + u.Data)
	if err != nil {
		return "", notBroadcast(fmt.Errorf("decode call data: %w", err))
	}
	to := common.HexToAddress(u.To)
	tx := types.NewTx(&types.LegacyTx{
		Nonce:    u.Nonce,
		GasPrice: gasPrice,
		Gas:      u.GasLimit,
		To:       &to,
		Value:    value,
		Data:     data,
	})
	signed, err := types.SignTx(tx, types.NewEIP155Signer(chainID), s.key)
	if err != nil {
		return "", notBroadcast(fmt.Errorf("sign tx: %w", err))
	}
	raw, err := rlp.EncodeToBytes(signed)
	if err != nil {
		return "", notBroadcast(fmt.Errorf("rlp encode: %w", err))
	}
	return s.bc.SendRawTransaction(raw)
}

// fakeReleaseSigner records calls for tests and returns a canned hash.
type fakeReleaseSigner struct {
	addr  string
	hash  string
	calls [][]byte
	err   error
}

func (f *fakeReleaseSigner) Address() string { return f.addr }
func (f *fakeReleaseSigner) SignAndBroadcast(_ context.Context, tx []byte) (string, error) {
	f.calls = append(f.calls, tx)
	if f.err != nil {
		return "", f.err
	}
	return f.hash, nil
}
