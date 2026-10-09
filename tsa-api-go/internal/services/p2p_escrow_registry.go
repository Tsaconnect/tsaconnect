package services

import (
	"context"
	"errors"
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
	// purpose is config.P2PPurposeP2P or P2PPurposePool (spec S5): a pool
	// chain never takes new ordinary P2P activity.
	purpose string
	// decimals holds each asset's decimals() as read on-chain at boot (S6);
	// a symbol missing here falls back to tokenDecimalsFor.
	decimals map[string]int
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
	ChainID  int64          `json:"chainId"`
	Assets   []string       `json:"assets"`
	Purpose  string         `json:"purpose"`
	Decimals map[string]int `json:"decimals,omitempty"`
}

// Chains lists the chains open to ordinary P2P (chainID + tradeable assets),
// sorted by chainID for stable output. The pool chain is omitted (spec S5 /
// plan P8): ordinary P2P never opens on it.
func (r *P2PEscrowRegistry) Chains() []P2PChainInfo {
	if r == nil {
		return nil
	}
	out := make([]P2PChainInfo, 0, len(r.byChain))
	for id, e := range r.byChain {
		if e.purpose == config.P2PPurposePool {
			continue
		}
		var decimals map[string]int
		if len(e.decimals) > 0 {
			decimals = make(map[string]int, len(e.decimals))
			for sym, d := range e.decimals {
				decimals[sym] = d
			}
		}
		out = append(out, P2PChainInfo{ChainID: id, Assets: e.assets, Purpose: r.Purpose(id), Decimals: decimals})
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
	if e.purpose == config.P2PPurposePool {
		return false // spec S5 / plan P8: ordinary P2P never opens on the pool chain (Plan B adds the LP route)
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
//
// The delist is OFF-CHAIN only: the API stops preparing new MCGP activity,
// but the escrow contract still accepts MCGP from anyone who calls it
// directly (deposit, and openTrade against a voucher a seller already
// signed) until the owner calls setTokenAcceptance(MCGP, false) on each
// P2PEscrow proxy. That call is the real switch; it does not block the exit
// paths (release, cancel, withdraw), so existing positions still finish.
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
		assets, decimals := resolveAssetDecimals(chainID, cc.ChainKey, cc.Purpose, cc.Assets,
			func(sym string) string { return bs.TokenAddressForChainID(chainID, sym) }, client)
		reg.byChain[chainID] = &chainEscrow{
			service:       NewP2PEscrowServiceForChain(client, cfg, cc.EscrowAddress),
			chainKey:      cc.ChainKey,
			eip712Version: cc.EIP712Version,
			domainChainID: verifyP2PChainAtBoot(client, chainID, cc),
			assets:        assets,
			purpose:       cc.Purpose,
			decimals:      decimals,
		}
	}
	log.Printf("[config] P2P escrow registry: %d chain(s) enabled: %v", len(reg.byChain), reg.ChainIDs())
	return reg
}

// tokenDecimalsReader is the one ERC-20 decimals() read surface: the registry's
// chain client, and part of every sponsor/treasury lane's sponsorNativeSender.
type tokenDecimalsReader interface {
	GetTokenDecimals(tokenAddress string) (int, error)
}

// resolveAssetDecimals reads each asset's decimals on-chain (spec S6, plan P7).
// A pool asset that cannot be read is dropped (the pool's decimals gate then
// keeps it closed). An ordinary chain keeps the asset on the table value, so a
// flaky RPC at boot cannot take that chain's P2P down.
func resolveAssetDecimals(chainID int64, chainKey, purpose string, assets []string, addressOf func(string) string, r tokenDecimalsReader) ([]string, map[string]int) {
	var kept []string
	decimals := map[string]int{}
	for _, sym := range assets {
		var d int
		var err error
		if addr := addressOf(sym); addr == "" {
			err = errors.New("no token address configured")
		} else {
			d, err = r.GetTokenDecimals(addr)
		}
		if err != nil {
			if purpose == config.P2PPurposePool {
				log.Printf("[config] ALERT p2p_asset_decimals_unreadable chain=%s(%d) asset=%s err=%s — dropped from the pool",
					chainKey, chainID, sym, RedactAlertText(err.Error()))
				continue
			}
			log.Printf("[config] ALERT p2p_asset_decimals_unreadable chain=%s(%d) asset=%s err=%s — keeping the table value %d",
				chainKey, chainID, sym, RedactAlertText(err.Error()), tokenDecimalsFor(chainID, sym))
			kept = append(kept, sym)
			continue
		}
		if table := tokenDecimalsFor(chainID, sym); table != d {
			log.Printf("[config] ALERT p2p_asset_decimals_mismatch chain=%s(%d) asset=%s chain_decimals=%d table=%d — using the chain",
				chainKey, chainID, sym, d, table)
		}
		decimals[strings.ToUpper(sym)] = d
		kept = append(kept, sym)
	}
	return kept, decimals
}

// TokenDecimals is symbol's decimals on chainID as read on-chain at boot.
// ok=false: not read (unconfigured chain, a test registry, or an ordinary
// asset whose read failed) — the caller uses tokenDecimalsFor.
func (r *P2PEscrowRegistry) TokenDecimals(chainID int64, symbol string) (int, bool) {
	if r == nil || r.byChain == nil {
		return 0, false
	}
	e, ok := r.byChain[chainID]
	if !ok || e.decimals == nil {
		return 0, false
	}
	d, ok := e.decimals[strings.ToUpper(symbol)]
	return d, ok
}

// Purpose is the chain's preset purpose ("" for an unconfigured chain).
func (r *P2PEscrowRegistry) Purpose(chainID int64) string {
	if r == nil || r.byChain == nil {
		return ""
	}
	if e, ok := r.byChain[chainID]; ok {
		if e.purpose == "" {
			return config.P2PPurposeP2P
		}
		return e.purpose
	}
	return ""
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
