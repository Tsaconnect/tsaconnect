package services

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
)

// ChainSender is a ReleaseSigner that also owns the per-chain lock every use of
// its key must hold from nonce read to broadcast (spec 2026-10-08 S8, S39).
// build runs under the lock, so the nonce it reads is still the next one when
// the transaction goes out.
type ChainSender interface {
	ReleaseSigner
	Send(ctx context.Context, chainID int64, build func() ([]byte, error)) (string, error)
}

// ChainReleaseSigner is the one release key with a broadcaster per P2P chain.
// It never routes a transaction to a chain it was not built for.
type ChainReleaseSigner struct {
	address string
	byChain map[int64]*KeyLane
}

// NewChainReleaseSigner takes each chain's lane from reg, so the release key
// shares one lock and nonce floor with every other role signing with the same
// key on that chain. A nil reg gives the signer private lanes.
func NewChainReleaseSigner(reg *SignerRegistry, key *ecdsa.PrivateKey, broadcasters map[int64]ReleaseBroadcaster) *ChainReleaseSigner {
	c := &ChainReleaseSigner{address: crypto.PubkeyToAddress(key.PublicKey).Hex(), byChain: map[int64]*KeyLane{}}
	for id, bc := range broadcasters {
		if lane := reg.Lane("pm_release", key, bc); lane != nil {
			c.byChain[id] = lane
		}
	}
	return c
}

func (c *ChainReleaseSigner) Address() string { return c.address }

// Chains is every chain id the signer can send on, sorted.
func (c *ChainReleaseSigner) Chains() []int64 {
	out := make([]int64, 0, len(c.byChain))
	for id := range c.byChain {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Send builds (prepare + nonce read), signs and broadcasts on chainID under
// that chain's lane (lock + nonce floor).
func (c *ChainReleaseSigner) Send(ctx context.Context, chainID int64, build func() ([]byte, error)) (string, error) {
	lane := c.byChain[chainID]
	if lane == nil {
		return "", notBroadcast(fmt.Errorf("release signer: no broadcaster for chain %d", chainID))
	}
	hash, _, err := lane.Send(ctx, func() ([]byte, error) {
		unsigned, err := build()
		if err != nil {
			return nil, notBroadcast(err)
		}
		if err := ownTx(lane, unsigned); err != nil {
			return nil, notBroadcast(err)
		}
		return unsigned, nil
	})
	return hash, err
}

// SignAndBroadcast routes an already-built tx by its own chain id, under that
// chain's lock. A caller that reads a nonce must use Send instead.
func (c *ChainReleaseSigner) SignAndBroadcast(ctx context.Context, unsigned []byte) (string, error) {
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsigned, &u); err != nil {
		return "", notBroadcast(fmt.Errorf("decode unsigned tx: %w", err))
	}
	id, err := strconv.ParseInt(u.ChainID, 10, 64)
	if err != nil {
		return "", notBroadcast(fmt.Errorf("release signer: the transaction names no chain"))
	}
	for _, lane := range c.byChain {
		if lane.ChainID() == id {
			hash, _, err := lane.Send(ctx, func() ([]byte, error) { return unsigned, nil })
			return hash, err
		}
	}
	return "", notBroadcast(fmt.Errorf("release signer: no broadcaster for chain %d", id))
}

// ownTx refuses a tx that names no chain or another chain than the lane's.
func ownTx(l *KeyLane, unsigned []byte) error {
	var u blockchain.UnsignedTx
	if err := json.Unmarshal(unsigned, &u); err != nil {
		return fmt.Errorf("decode unsigned tx: %w", err)
	}
	if u.ChainID == "" {
		return fmt.Errorf("release signer: the transaction names no chain") // as SignAndBroadcast refuses
	}
	if u.ChainID != strconv.FormatInt(l.ChainID(), 10) {
		return fmt.Errorf("release signer: a tx built for chain %s was sent on chain %d", u.ChainID, l.ChainID())
	}
	return nil
}
