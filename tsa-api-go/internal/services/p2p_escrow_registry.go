package services

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// chainEscrow bundles a chain's escrow adapter with its EIP-712 domain version
// (they move in lockstep — see spec §8.2).
type chainEscrow struct {
	service       *P2PEscrowService
	eip712Version string
}

// P2PEscrowRegistry holds one escrow adapter per P2P-enabled chain, keyed by
// chainID. Resolution is fail-loud: an unconfigured chain returns an error and
// NEVER falls back to another chain — a BSC trade read against the Sonic escrow
// would orphan funds (see spec §8.5).
type P2PEscrowRegistry struct {
	byChain map[int64]*chainEscrow
}

// For returns the escrow adapter for chainID, or an error if that chain is not
// P2P-enabled. Callers MUST treat the error as "P2P unavailable on this chain",
// not as a reason to use a default chain.
func (r *P2PEscrowRegistry) For(chainID int64) (*P2PEscrowService, error) {
	if r == nil || r.byChain == nil {
		return nil, fmt.Errorf("p2p escrow registry not initialized")
	}
	e, ok := r.byChain[chainID]
	if !ok {
		return nil, fmt.Errorf("p2p: no escrow configured for chain %d", chainID)
	}
	return e.service, nil
}

// EIP712VersionFor returns the EIP-712 domain version for chainID, or an error
// if the chain is not P2P-enabled. Fail-loud, like For.
func (r *P2PEscrowRegistry) EIP712VersionFor(chainID int64) (string, error) {
	if r == nil || r.byChain == nil {
		return "", fmt.Errorf("p2p escrow registry not initialized")
	}
	e, ok := r.byChain[chainID]
	if !ok {
		return "", fmt.Errorf("p2p: no escrow configured for chain %d", chainID)
	}
	return e.eip712Version, nil
}

// ChainIDs returns the configured P2P chain IDs (for diagnostics/iteration).
func (r *P2PEscrowRegistry) ChainIDs() []int64 {
	if r == nil {
		return nil
	}
	out := make([]int64, 0, len(r.byChain))
	for id := range r.byChain {
		out = append(out, id)
	}
	return out
}

// NewP2PEscrowRegistry builds the per-chain escrow registry from cfg.P2PChains,
// resolving each chain's client via bs.ClientForChainID. It also runs a boot
// verification per chain (eth_chainId probe + escrow bytecode check) and logs
// loudly on any mismatch so an operator can fix env before the first trade. A
// chain whose client can't be resolved is skipped with a warning (For() will
// then fail loud for it).
func NewP2PEscrowRegistry(bs *BlockchainService, cfg *config.Config) *P2PEscrowRegistry {
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{}}
	if cfg == nil || bs == nil {
		return reg
	}
	for chainID, cc := range cfg.P2PChains {
		client, _ := bs.ClientForChainID(chainID)
		if client == nil {
			log.Printf("[config] WARNING: P2P chain %d (%s) has no RPC client — skipping (P2P unavailable on this chain)", chainID, cc.ChainKey)
			continue
		}
		reg.byChain[chainID] = &chainEscrow{
			service:       NewP2PEscrowServiceForChain(client, cfg, cc.EscrowAddress),
			eip712Version: cc.EIP712Version,
		}
		verifyP2PChainAtBoot(client, chainID, cc)
	}
	log.Printf("[config] P2P escrow registry: %d chain(s) enabled: %v", len(reg.byChain), reg.ChainIDs())
	return reg
}

// verifyP2PChainAtBoot warns (does not abort) when the RPC's eth_chainId
// disagrees with the configured chainID, or when the escrow address has no
// bytecode — either makes signatures/txs fail later with opaque errors.
func verifyP2PChainAtBoot(client *blockchain.EVMClient, chainID int64, cc config.P2PChainConfig) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if rpcID, err := client.Client.ChainID(ctx); err != nil {
		log.Printf("[config] WARNING: eth_chainId probe failed for P2P chain %d (%s): %v", chainID, cc.ChainKey, err)
	} else if rpcID.Int64() != chainID {
		log.Printf("[config] WARNING: P2P chain %s configured chainID=%d but RPC reports %d (EIP712Version=%s) — voucher signatures will be rejected until this matches", cc.ChainKey, chainID, rpcID.Int64(), cc.EIP712Version)
	}
	if cc.EscrowAddress == "" {
		return
	}
	if code, err := client.GetCode(cc.EscrowAddress); err != nil {
		log.Printf("[config] WARNING: could not check bytecode at %s escrow %s: %v", cc.ChainKey, cc.EscrowAddress, err)
	} else if len(code) == 0 {
		log.Printf("[config] WARNING: %s escrow %s has NO bytecode on chain %d — P2P will fail with recovery mismatches", cc.ChainKey, cc.EscrowAddress, chainID)
	} else {
		log.Printf("[config] P2P escrow verified: %s %s on chain %d (%d bytes)", cc.ChainKey, cc.EscrowAddress, chainID, len(code))
	}
}
