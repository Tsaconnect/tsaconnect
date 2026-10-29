package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// ReleaseSigner signs and broadcasts the unsigned release tx produced by
// P2PEscrowService.PrepareRelease with the ONE platform release key. The
// key never lives in the DB. Prod = AWS KMS (release_signer_kms.go, later); dev
// = LocalKeyReleaseSigner; tests = fakeReleaseSigner.
type ReleaseSigner interface {
	// Address is the release key's address — the `from` passed to PrepareRelease.
	Address() string
	// SignAndBroadcast signs the JSON-encoded blockchain.UnsignedTx and broadcasts
	// it, returning the tx hash.
	SignAndBroadcast(ctx context.Context, unsignedTx []byte) (txHash string, err error)
}

// ReleaseBroadcaster is the slice of *blockchain.EVMClient a signer needs.
type ReleaseBroadcaster interface {
	SendRawTransaction(signedTx []byte) (string, error)
	ChainID() *big.Int
}

// LocalKeyReleaseSigner holds the admin key in process. Dev/staging only.
type LocalKeyReleaseSigner struct {
	key *ecdsa.PrivateKey
	bc  ReleaseBroadcaster
}

func NewLocalKeyReleaseSigner(key *ecdsa.PrivateKey, bc ReleaseBroadcaster) *LocalKeyReleaseSigner {
	return &LocalKeyReleaseSigner{key: key, bc: bc}
}

func (s *LocalKeyReleaseSigner) Address() string {
	return crypto.PubkeyToAddress(s.key.PublicKey).Hex()
}

func (s *LocalKeyReleaseSigner) SignAndBroadcast(ctx context.Context, unsignedTx []byte) (string, error) {
	raw, hash, err := s.signRaw(unsignedTx)
	if err != nil {
		return "", err
	}
	return s.broadcastRaw(raw, hash)
}

// signRaw signs the JSON-encoded blockchain.UnsignedTx and returns the signed
// RLP and its hash — what a KeyLane keeps so a dropped transaction can be
// re-broadcast byte for byte (never re-signed at the same nonce).
func (s *LocalKeyReleaseSigner) signRaw(unsignedTx []byte) ([]byte, string, error) {
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsignedTx, &u); err != nil {
		return nil, "", notBroadcast(fmt.Errorf("decode unsigned tx: %w", err))
	}
	gasPrice, ok := new(big.Int).SetString(u.GasPrice, 10)
	if !ok {
		return nil, "", notBroadcast(fmt.Errorf("bad gasPrice %q", u.GasPrice))
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
		return nil, "", notBroadcast(fmt.Errorf("decode call data: %w", err))
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
		return nil, "", notBroadcast(fmt.Errorf("sign tx: %w", err))
	}
	raw, err := rlp.EncodeToBytes(signed)
	if err != nil {
		return nil, "", notBroadcast(fmt.Errorf("rlp encode: %w", err))
	}
	return raw, signed.Hash().Hex(), nil
}

// broadcastRaw sends a signed transaction and classifies a failure: a
// definite refusal is ErrTxNotBroadcast, anything else is uncertain and
// carries the hash.
func (s *LocalKeyReleaseSigner) broadcastRaw(raw []byte, hash string) (string, error) {
	got, err := s.bc.SendRawTransaction(raw)
	if err != nil && rejectedDefinitely(err) {
		return "", notBroadcast(err)
	}
	if err != nil {
		return "", broadcastUncertainError{err: err, txHash: hash}
	}
	return got, nil
}

// rejectedDefinitely reports a node refusing a transaction before it could
// enter any mempool. That refusal is definite — unlike a timeout, which can
// hide one that did — so the signer reports it as ErrTxNotBroadcast and a
// treasury lane hands the nonce back (pre-merge review M-1: otherwise the
// next send skips it and queues behind a gap). Matched on text because the
// RPC error carries no code for most of these:
//   - the sender cannot pay (geth/erigon "insufficient funds", nethermind
//     "InsufficientFunds");
//   - the price is below the node's floor ("transaction underpriced") or the
//     block's base fee, or the fee is above the node's RPC cap;
//   - the gas limit is below the intrinsic cost;
//   - every RPC endpoint answered 429: the failover transport moves a
//     broadcast on only past a 429 (refused unprocessed), so a final 429
//     means none took it.
//
// Deliberately NOT definite, because the nonce is taken and stepping it back
// would rebuild the collision the lane's floor prevents: "replacement
// transaction underpriced" (our own tx holds the nonce), "nonce too low" (a
// mined tx holds it — possibly this very one), "already known". A 5xx stays
// uncertain too: the failover transport stops there because the node may
// hold the transaction.
func rejectedDefinitely(err error) bool {
	// The HTTP status decides first (re-review N-1): a 429 is definite; a 5xx
	// is uncertain whatever its body says, since the node or a proxy in front
	// of it may hold the transaction. Any other HTTP error (a 400 whose body
	// says "insufficient funds") and a JSON-RPC error fall to the text rules.
	var herr rpc.HTTPError
	if errors.As(err, &herr) {
		switch {
		case herr.StatusCode == http.StatusTooManyRequests:
			return true
		case herr.StatusCode >= http.StatusInternalServerError:
			return false
		}
	}
	if rejectedForFunds(err) {
		return true
	}
	msg := strings.ToLower(strings.ReplaceAll(err.Error(), " ", ""))
	if strings.Contains(msg, "replacementtransactionunderpriced") {
		return false
	}
	for _, definite := range []string{
		"transactionunderpriced",
		"exceedstheconfiguredcap",
		"lessthanblockbasefee",
		"intrinsicgastoolow",
		"exceedsblockgaslimit",
	} {
		if strings.Contains(msg, definite) {
			return true
		}
	}
	return false
}

// rejectedForFunds reports the node refusing a send its sender cannot pay
// for (geth/erigon "insufficient funds", nethermind "InsufficientFunds").
func rejectedForFunds(err error) bool {
	msg := strings.ToLower(strings.ReplaceAll(err.Error(), " ", ""))
	return strings.Contains(msg, "insufficientfunds")
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

func (f *fakeReleaseSigner) Send(ctx context.Context, _ int64, build func() ([]byte, error)) (string, error) {
	unsigned, err := build()
	if err != nil {
		return "", notBroadcast(err)
	}
	// Stands in for the key lane: the caller's pre-broadcast write first
	// (nothing is sent if it fails), unless the send is refused outright.
	if hash := f.hash; !errors.Is(f.err, ErrTxNotBroadcast) {
		if f.err != nil {
			hash = uncertainTxHash(f.err)
		}
		var u blockchain.UnsignedTx
		_ = json.Unmarshal(unsigned, &u)
		if err := emulatePreBroadcast(ctx, u.Nonce, hash); err != nil {
			return "", notBroadcast(err)
		}
	}
	return f.SignAndBroadcast(ctx, unsigned)
}
