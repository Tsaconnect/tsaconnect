package services

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// chainEscrow bundles a chain's escrow adapter with its EIP-712 domain version
// (they move in lockstep — see spec §8.2) and the RPC-probed chainId used for
// the EIP-712 domain.
type chainEscrow struct {
	// service is held as the P2PEscrowAdapter interface (not the concrete
	// *P2PEscrowService) so tests can inject a mock via NewP2PEscrowRegistryFromMap.
	service       P2PEscrowAdapter
	eip712Version string
	// domainChainID is the chainId stamped into the EIP-712 domain. It is the
	// value the RPC actually reports (eth_chainId), falling back to the
	// configured chainID when the probe fails — a mismatch with what the
	// contract hashes makes every ecrecover resolve to a wrong address.
	domainChainID int64
	// chainKey names the chain for diagnostics ("sonic", "bsc"); empty for the
	// test/ops map constructor, which has no config to read it from.
	chainKey string
	// assets is the curated allow-list of token symbols tradeable on this chain.
	// An empty slice means "allow all" (used by NewP2PEscrowRegistryFromMap so
	// tests and ops maps stay permissive). An unconfigured chain returns false.
	assets []string
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
func (r *P2PEscrowRegistry) For(chainID int64) (P2PEscrowAdapter, error) {
	if r == nil || r.byChain == nil {
		return nil, fmt.Errorf("p2p escrow registry not initialized")
	}
	e, ok := r.byChain[chainID]
	if !ok {
		return nil, fmt.Errorf("p2p: no escrow configured for chain %d", chainID)
	}
	return e.service, nil
}

// Resolve returns everything the trade service needs to act on a chain: the
// escrow adapter, the EIP-712 domain chainId (RPC-probed), and the domain
// version. Fail-loud for an unconfigured chain — NEVER default to another
// chain (a BSC trade resolved against the Sonic escrow would orphan funds).
func (r *P2PEscrowRegistry) Resolve(chainID int64) (P2PEscrowAdapter, int64, string, error) {
	if r == nil || r.byChain == nil {
		return nil, 0, "", fmt.Errorf("p2p escrow registry not initialized")
	}
	e, ok := r.byChain[chainID]
	if !ok {
		return nil, 0, "", fmt.Errorf("p2p: no escrow configured for chain %d", chainID)
	}
	return e.service, e.domainChainID, e.eip712Version, nil
}

// NewP2PEscrowRegistryFromMap builds a registry directly from a chainID →
// adapter map, stamping domainChainID = chainID for every entry. Production
// code uses NewP2PEscrowRegistry (which RPC-probes the domain chainId); this
// constructor is the seam both the in-package and external (handlers) tests
// use to inject mock adapters, and is also handy for ops/diagnostics.
func NewP2PEscrowRegistryFromMap(m map[int64]P2PEscrowAdapter, version string) *P2PEscrowRegistry {
	reg := &P2PEscrowRegistry{byChain: map[int64]*chainEscrow{}}
	for chainID, esc := range m {
		reg.byChain[chainID] = &chainEscrow{
			service:       esc,
			eip712Version: version,
			domainChainID: chainID,
		}
	}
	return reg
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

// P2PChainInfo is the public shape of an enabled P2P chain.
type P2PChainInfo struct {
	ChainID int64    `json:"chainId"`
	Assets  []string `json:"assets"`
}

// Chains lists the enabled P2P chains (chainID + tradeable assets), sorted by
// chainID for stable output.
func (r *P2PEscrowRegistry) Chains() []P2PChainInfo {
	if r == nil {
		return nil
	}
	out := make([]P2PChainInfo, 0, len(r.byChain))
	for id, e := range r.byChain {
		out = append(out, P2PChainInfo{ChainID: id, Assets: e.assets})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChainID < out[j].ChainID })
	return out
}

// systemWalletReader is the read an escrow adapter must support to be included
// in SystemWallets. Adapters that don't (test mocks) are reported as unreadable
// rather than skipped, so a caller never mistakes "not checked" for "agrees".
type systemWalletReader interface {
	ReadSystemWallet() (string, error)
}

// FeeSinkStatus is one escrow's answer to "where do your fees go?".
// Reason is set when the read could not be made; SystemWallet is then empty.
type FeeSinkStatus struct {
	ChainID  int64  `json:"chainId"`
	ChainKey string `json:"chainKey"`
	Contract string `json:"contract"`
	// SystemWallet is the address this contract pays trade fees to.
	SystemWallet string `json:"systemWallet,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// SystemWallets reads systemWallet() from every enabled P2P escrow, so the
// admin fee-wallet panel can state per contract — not in aggregate — whether
// fees actually land at the assigned address. A failed read yields a Reason
// instead of dropping the row: an unread contract must stay visible, since
// "we couldn't check" and "it matches" are very different answers.
// Sorted by chainID for stable output.
func (r *P2PEscrowRegistry) SystemWallets() []FeeSinkStatus {
	if r == nil {
		return nil
	}
	out := make([]FeeSinkStatus, 0, len(r.byChain))
	for id, e := range r.byChain {
		row := FeeSinkStatus{ChainID: id, ChainKey: e.chainKey, Contract: e.service.ContractAddress()}
		reader, ok := e.service.(systemWalletReader)
		if !ok {
			row.Reason = "escrow adapter cannot read systemWallet"
			out = append(out, row)
			continue
		}
		addr, err := reader.ReadSystemWallet()
		switch {
		case err != nil:
			row.Reason = err.Error()
		case addr == "":
			row.Reason = "contract returned an empty systemWallet"
		default:
			row.SystemWallet = addr
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChainID < out[j].ChainID })
	return out
}

// AssetAllowed reports whether symbol may be used for NEW P2P activity on
// chainID (creating/publishing an ad, depositing into the pool, opening a
// trade) per the per-chain curation. An empty allow-list means "allow all"
// (used by the test/ops map constructor). An unconfigured chain returns false.
//
// It must never gate an exit path. Removing an asset from the list stops new
// activity in it; ads, pool balances and trades that already exist in that
// asset still release, cancel, dispute, resolve and withdraw.
func (r *P2PEscrowRegistry) AssetAllowed(chainID int64, symbol string) bool {
	if r == nil || r.byChain == nil {
		return false
	}
	e, ok := r.byChain[chainID]
	if !ok {
		return false
	}
	if len(e.assets) == 0 {
		return true
	}
	sym := strings.ToUpper(symbol)
	for _, a := range e.assets {
		if strings.ToUpper(a) == sym {
			return true
		}
	}
	return false
}

// AssetDelisted reports whether chainID is P2P-enabled but its curated
// allow-list excludes symbol — i.e. the asset was taken out of P2P while the
// chain itself still works. Existing positions in a delisted asset must be
// able to finish; only new activity is refused. An unconfigured chain is NOT
// "delisted": callers fail on it through their own chain resolution, with the
// error they always returned.
func (r *P2PEscrowRegistry) AssetDelisted(chainID int64, symbol string) bool {
	if r == nil || r.byChain == nil {
		return false
	}
	if _, ok := r.byChain[chainID]; !ok {
		return false
	}
	return !r.AssetAllowed(chainID, symbol)
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
			chainKey:      cc.ChainKey,
			eip712Version: cc.EIP712Version,
			domainChainID: verifyP2PChainAtBoot(client, chainID, cc),
			assets:        cc.Assets,
		}
	}
	log.Printf("[config] P2P escrow registry: %d chain(s) enabled: %v", len(reg.byChain), reg.ChainIDs())
	return reg
}

// verifyP2PChainAtBoot warns (does not abort) when the escrow address has no
// bytecode, and returns the effective EIP-712 domain chainId: the value the
// RPC actually reports (eth_chainId), falling back to the configured chainID
// when the probe fails. The EIP-712 domain MUST use whatever the chain itself
// reports — a mismatch with what the contract hashes makes every ecrecover
// resolve to a wrong address and rejects all signatures.
func verifyP2PChainAtBoot(client *blockchain.EVMClient, chainID int64, cc config.P2PChainConfig) int64 {
	domainChainID := chainID
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if rpcID, err := client.Client.ChainID(ctx); err != nil {
		log.Printf("[config] WARNING: eth_chainId probe failed for P2P chain %d (%s): %v — using configured %d for the EIP-712 domain", chainID, cc.ChainKey, err, chainID)
	} else {
		domainChainID = rpcID.Int64()
		if domainChainID != chainID {
			log.Printf("[config] WARNING: P2P chain %s configured chainID=%d but RPC reports %d (EIP712Version=%s) — using the RPC value for the domain so signatures stay valid", cc.ChainKey, chainID, domainChainID, cc.EIP712Version)
		}
	}
	if cc.EscrowAddress == "" {
		return domainChainID
	}
	if code, err := client.GetCode(cc.EscrowAddress); err != nil {
		log.Printf("[config] WARNING: could not check bytecode at %s escrow %s: %v", cc.ChainKey, cc.EscrowAddress, err)
	} else if len(code) == 0 {
		log.Printf("[config] WARNING: %s escrow %s has NO bytecode on chain %d — P2P will fail with recovery mismatches", cc.ChainKey, cc.EscrowAddress, chainID)
	} else {
		log.Printf("[config] P2P escrow verified: %s %s on chain %d (%d bytes)", cc.ChainKey, cc.EscrowAddress, chainID, len(code))
	}
	return domainChainID
}
