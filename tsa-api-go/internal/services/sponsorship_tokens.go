package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

type SponsorMode string

const (
	SponsorModeNone          SponsorMode = "none"
	SponsorModeAuthorization SponsorMode = "authorization"
	SponsorModeTopUp         SponsorMode = "topup"
)

type SponsorRoute struct {
	Chain string      `json:"chain"`
	Token string      `json:"token"`
	Mode  SponsorMode `json:"mode"`
}

// rampSponsorModes is keyed on the contracts PAYCREST settles, not on whichever
// variant is most common on a chain — they differ on Base, where Paycrest's
// USDT is the bridged 0xfde4… (no EIP-3009), not USDT0. Verified on-chain
// 2026-09-14; see the Phase 1 spec.
//
// base:USDT is deliberately absent (self-pay). Base is OP-stack: a top-up there
// must also cover the L1 data fee, which the Phase 1 estimate does not, so it
// stays self-pay until an L1-fee-aware top-up is verified live.
var rampSponsorModes = map[string]SponsorMode{
	"base:USDC":     SponsorModeAuthorization,
	"arbitrum:USDC": SponsorModeAuthorization,
	"arbitrum:USDT": SponsorModeAuthorization, // USDT0
	"polygon:USDC":  SponsorModeAuthorization,
	"polygon:USDT":  SponsorModeTopUp, // permit only
	"bsc:USDC":      SponsorModeTopUp,
	"bsc:USDT":      SponsorModeTopUp,
}

func RampSponsorMode(chainKey, token string) SponsorMode {
	key := strings.ToLower(strings.TrimSpace(chainKey)) + ":" + strings.ToUpper(strings.TrimSpace(token))
	if m, ok := rampSponsorModes[key]; ok {
		return m
	}
	return SponsorModeNone
}

func RampSponsorRoutes() []SponsorRoute {
	out := make([]SponsorRoute, 0, len(rampSponsorModes))
	for key, mode := range rampSponsorModes {
		chain, token, _ := strings.Cut(key, ":")
		out = append(out, SponsorRoute{Chain: chain, Token: token, Mode: mode})
	}
	return out
}

var ErrSponsorDomainMismatch = errors.New("token EIP-712 domain does not match its DOMAIN_SEPARATOR")

type tokenCaller interface {
	CallContract(ctx context.Context, msg ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
}

var (
	selName            = common.FromHex("0x06fdde03")
	selVersion         = common.FromHex("0x54fd4d50")
	selDomainSeparator = common.FromHex("0x3644e515")
)

// ReadAuthorizationDomain reads the domain a token verifies EIP-3009 signatures
// under, and PROVES it by recomputing DOMAIN_SEPARATOR. Hardcoding would work
// for USDC ("USD Coin"/"2") and silently fail for USDT0 ("USD₮0", no version()
// function, version "1") as an opaque "invalid signature" after the user has
// already signed.
func ReadAuthorizationDomain(ctx context.Context, caller tokenCaller, chainID *big.Int, token string) (EIP712Domain, error) {
	addr := common.HexToAddress(token)
	call := func(sel []byte) ([]byte, error) {
		return caller.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: sel}, nil)
	}

	name, err := unpackString(call(selName))
	if err != nil || name == "" {
		return EIP712Domain{}, fmt.Errorf("sponsor: read name() of %s: %v", addr.Hex(), err)
	}
	version, verr := unpackString(call(selVersion))
	if verr != nil || version == "" {
		version = "1" // OpenZeppelin EIP712 default; USDT0 exposes no getter
	}
	sepRaw, err := call(selDomainSeparator)
	if err != nil || len(sepRaw) != 32 {
		return EIP712Domain{}, fmt.Errorf("sponsor: read DOMAIN_SEPARATOR() of %s: %v", addr.Hex(), err)
	}

	td := apitypes.TypedData{
		Types: apitypes.Types{"EIP712Domain": {
			{Name: "name", Type: "string"},
			{Name: "version", Type: "string"},
			{Name: "chainId", Type: "uint256"},
			{Name: "verifyingContract", Type: "address"},
		}},
		Domain: apitypes.TypedDataDomain{
			Name:              name,
			Version:           version,
			ChainId:           (*math.HexOrDecimal256)(chainID),
			VerifyingContract: addr.Hex(),
		},
	}
	computed, err := td.HashStruct("EIP712Domain", td.Domain.Map())
	if err != nil {
		return EIP712Domain{}, fmt.Errorf("sponsor: hash domain: %w", err)
	}
	if !bytes.Equal(computed, sepRaw) {
		return EIP712Domain{}, fmt.Errorf("%w: %s on chain %s (name=%q version=%q)", ErrSponsorDomainMismatch, addr.Hex(), chainID, name, version)
	}
	return EIP712Domain{Name: name, Version: version, ChainID: chainID.String(), VerifyingContract: addr.Hex()}, nil
}

func unpackString(raw []byte, callErr error) (string, error) {
	if callErr != nil {
		return "", callErr
	}
	typ, _ := abi.NewType("string", "", nil)
	vals, err := abi.Arguments{{Type: typ}}.Unpack(raw)
	if err != nil || len(vals) != 1 {
		return "", fmt.Errorf("unpack string: %v", err)
	}
	s, _ := vals[0].(string)
	return s, nil
}

var sponsorAmountShape = regexp.MustCompile(`^\d+(\.\d+)?$`)

// DecimalToBaseUnits converts a plain decimal string ("36.421800") to token base
// units without floats. More fractional digits than the token has is an error,
// never a silent truncation: the result is an exact on-chain transfer amount.
func DecimalToBaseUnits(amount string, decimals int) (*big.Int, error) {
	amount = strings.TrimSpace(amount)
	if !sponsorAmountShape.MatchString(amount) {
		return nil, fmt.Errorf("not a plain decimal: %q", amount)
	}
	whole, frac, _ := strings.Cut(amount, ".")
	frac = strings.TrimRight(frac, "0")
	if len(frac) > decimals {
		return nil, fmt.Errorf("%q has more than %d decimal places", amount, decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	out, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok {
		return nil, fmt.Errorf("parse %q", amount)
	}
	return out, nil
}
