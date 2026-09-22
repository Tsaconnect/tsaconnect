package services

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// ramp_destination.go — what the user asked to be delivered, resolved
// server-side.
//
// A converted buy used to be addressed by (chain name, token symbol), which
// only ever worked because both sides were looked up in the same config map.
// Buying an arbitrary token means the app names it the only way it can — a
// LiFi chain id and a token address — and neither of those may be trusted:
// the chain key is derived FROM the id, and whether the destination is a
// stablecoin (and with what decimals and symbol) comes from our own config,
// never from the request.

const evmNativeAddress = "0x0000000000000000000000000000000000000000"

// evmNativeAliasAddress is the OTHER spelling of "this chain's native coin".
// LiFi, 1inch and most aggregators use it interchangeably with the zero
// address, and which one comes back varies by chain and by tool.
const evmNativeAliasAddress = "0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE"

// isNativeTokenAddress reports whether addr names the chain's native coin in
// either accepted spelling. Both mean the same asset everywhere in this
// ecosystem, so accepting either cannot admit a token we did not ask for —
// whereas accepting only the one we happened to store would park every native
// delivery a provider reports with the other.
func isNativeTokenAddress(addr string) bool {
	addr = strings.TrimSpace(addr)
	return strings.EqualFold(addr, evmNativeAddress) || strings.EqualFold(addr, evmNativeAliasAddress)
}

// ErrBadRequestedAsset: the requested chain id or token can't be resolved
// server-side. Every field of a requested asset is untrusted input.
var ErrBadRequestedAsset = errors.New("that token or network can't be bought here")

var rampStableSymbols = []string{"USDC", "USDT"}

// isStableDestination reports whether address on chainID is a configured
// stablecoin the treasury can top up in, with its decimals and symbol. The
// config map is the source of truth, compared case-insensitively.
func isStableDestination(chains *BlockchainService, chainID int64, address string) (bool, int, string) {
	if chains == nil || address == "" {
		return false, 0, ""
	}
	for _, sym := range rampStableSymbols {
		if a := chains.TokenAddressForChainID(chainID, sym); a != "" && strings.EqualFold(a, address) {
			return true, tokenDecimalsFor(chainID, sym), sym
		}
	}
	return false, 0, ""
}

type resolvedAsset struct {
	ChainKey       string
	ChainID        int64
	Address        string
	Stable         bool
	StableDecimals int
	StableSymbol   string
}

// resolveRequestedAsset derives everything server-side: the chain key from
// the chain id (the client's chain name is ignored) and stable-ness from
// config. EVM destinations only; Solana arrives in Part 2b.
func resolveRequestedAsset(chains *BlockchainService, req RequestedAsset) (resolvedAsset, error) {
	if chains == nil {
		return resolvedAsset{}, ErrBadRequestedAsset
	}
	chainID, address := req.ChainID, strings.TrimSpace(req.Address)
	if chainID == 0 && req.Chain != "" && req.Token != "" { // legacy symbol request
		chainID, _ = chains.ChainIDForName(req.Chain)
		address = chains.TokenAddressForChainID(chainID, strings.ToUpper(strings.TrimSpace(req.Token)))
	}
	key, ok := chains.ChainNameForID(chainID)
	if !ok {
		return resolvedAsset{}, fmt.Errorf("%w: chain %d", ErrBadRequestedAsset, chainID)
	}
	stable, decimals, sym := isStableDestination(chains, chainID, address)
	// A configured stablecoin passes on its config entry alone: the test
	// registries (and any config written before addresses were validated)
	// carry placeholder addresses that are not hex, and refusing those would
	// refuse the one destination we know most about.
	if address == "" || (!stable && !common.IsHexAddress(address)) {
		return resolvedAsset{}, fmt.Errorf("%w: token %q", ErrBadRequestedAsset, address)
	}
	return resolvedAsset{ChainKey: key, ChainID: chainID, Address: address, Stable: stable, StableDecimals: decimals, StableSymbol: sym}, nil
}
