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

// solanaChainKey is what a Solana destination is called everywhere a chain key
// is written — the order's requested_chain, the leg ledger, the logs. It is
// NOT looked up from the EVM chain registry (Solana is not in it), so it is
// stated once here rather than spelled out at each site.
const solanaChainKey = "solana"

// addressFamily is the address family a chain's recipients are written in —
// the one thing that decides both how a route's calldata names its recipient
// and how a landing is proven. It is derived from the chain id alone, never
// from a chain name, which is client-supplied at every edge.
func addressFamily(chainID int64) string {
	if chainID == solanaLiFiChainID {
		return "solana"
	}
	return "evm"
}

// validateAddressForChain checks that addr is written in the family chainID's
// addresses belong to. It shares addressFamily's single switch rather than
// re-deciding what a chain id means: a buy's destination and a sell's source
// are the same question asked from opposite ends.
//
// validateSolana is the Solana chain client's own check, injected the same
// way resolveRequestedAsset takes it. Unset, a Solana address cannot be
// checked and is refused rather than taken on the client's word.
func validateAddressForChain(chainID int64, addr string, validateSolana solanaAddressValidator) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return errors.New("address is required")
	}
	switch addressFamily(chainID) {
	case "solana":
		if validateSolana == nil {
			return errors.New("no Solana chain is configured")
		}
		return validateSolana(addr)
	default:
		if !common.IsHexAddress(addr) {
			return fmt.Errorf("%q is not an address on chain %d", addr, chainID)
		}
		return nil
	}
}

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
	// Destination is where the requested token is actually delivered when
	// that is NOT the user's EVM wallet — a Solana address today. Empty on
	// every EVM destination, where the wallet is the recipient.
	Destination string
}

// solanaAddressValidator validates a Solana address. It is injected rather
// than imported so this package keeps no dependency on a chain client: the
// wiring passes the Solana chain client's own ValidateAddress, which is the
// same check the wallet validates a send address with. Nil means no Solana
// chain is configured, and then a Solana destination is refused rather than
// taken on the client's word.
type solanaAddressValidator func(addr string) error

// resolveRequestedAsset derives everything server-side: the chain key from
// the chain id (the client's chain name is ignored) and stable-ness from
// config.
func resolveRequestedAsset(chains *BlockchainService, validateSolana solanaAddressValidator, req RequestedAsset) (resolvedAsset, error) {
	if chains == nil {
		return resolvedAsset{}, ErrBadRequestedAsset
	}
	if req.ChainID == solanaLiFiChainID {
		return resolveSolanaAsset(validateSolana, req)
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

// resolveSolanaAsset is the Solana half of resolveRequestedAsset. Solana is
// not in the EVM chain registry — there is no chain id to look a key up by and
// no treasury lane there — so everything about it is decided here:
//
//   - The token is a base58 mint, or the System Program id for native SOL.
//     An EVM-looking address is not a Solana token however plausible it looks.
//   - The recipient is required, and it is NOT the user's EVM wallet: nothing
//     on Solana can be delivered to a 20-byte address, and a route quoted to
//     the wrong recipient is money sent to a stranger. It is part of naming
//     the asset here rather than an afterthought at execution time, so a
//     request that cannot say where the tokens go never reaches LiFi.
//   - Stable is always false. A Solana stablecoin is still a destination the
//     treasury holds no float in, so there is nothing to promise against and
//     nothing to top up with; LiFi's own minimum is the floor (see the
//     design's §4.4).
func resolveSolanaAsset(validate solanaAddressValidator, req RequestedAsset) (resolvedAsset, error) {
	if validate == nil {
		return resolvedAsset{}, fmt.Errorf("%w: no Solana chain is configured", ErrBadRequestedAsset)
	}
	address := strings.TrimSpace(req.Address)
	if err := validate(address); err != nil {
		return resolvedAsset{}, fmt.Errorf("%w: token %q: %v", ErrBadRequestedAsset, address, err)
	}
	destination := strings.TrimSpace(req.Destination)
	if err := validate(destination); err != nil {
		return resolvedAsset{}, fmt.Errorf("%w: solana recipient %q: %v", ErrBadRequestedAsset, destination, err)
	}
	return resolvedAsset{
		ChainKey: solanaChainKey, ChainID: solanaLiFiChainID,
		Address: address, Destination: destination,
	}, nil
}
