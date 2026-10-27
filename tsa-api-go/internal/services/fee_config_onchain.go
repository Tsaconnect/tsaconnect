package services

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// fee_config_onchain.go — the on-chain FeeConfig (smart-contracts/contracts/core/FeeConfig.sol),
// successor of FeeManager (docs/plans/2026-10-27-contracts-v2-migration.md, slice 3).
//
// Differences from FeeManager that callers must respect:
//   - Rows are keyed by (bytes32 feature, token ADDRESS), not (feature, token symbol), and there
//     is no wildcard token: every (feature, token) on a chain has its own row.
//   - The flat fee is in the token's own base units (Sonic stables 6 decimals, BSC 18), not 1e6 USD.
//   - rateFor reverts for an unset or inactive row (fail closed); RawRate reads any row.
//   - Per-token value limits and one fee recipient per chain live here too.
//   - Writes need CONFIG_ROLE (the backend only builds calldata; the admin signs).

// On-chain FeeConfig feature keys.
const (
	FeeConfigFeatureOrder       = "order"        // bps = merchant fee (seller pays); flatFee = buyer's flat fee
	FeeConfigFeatureP2PPlatform = "p2p_platform" // bps, no flat fee
	FeeConfigFeatureP2PGas      = "p2p_gas"      // bps, no flat fee
)

const feeConfigABIJSON = `[
  {"type":"function","name":"rawRate","stateMutability":"view",
   "inputs":[{"name":"feature","type":"bytes32"},{"name":"token","type":"address"}],
   "outputs":[{"name":"","type":"tuple","components":[
     {"name":"bps","type":"uint16"},{"name":"flatFee","type":"uint128"},{"name":"active","type":"bool"}]}]},
  {"type":"function","name":"limitsFor","stateMutability":"view",
   "inputs":[{"name":"token","type":"address"}],
   "outputs":[{"name":"","type":"tuple","components":[
     {"name":"maxPerOrder","type":"uint128"},{"name":"maxOwed","type":"uint128"}]}]},
  {"type":"function","name":"feeRecipient","stateMutability":"view","inputs":[],
   "outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"setRates","stateMutability":"nonpayable",
   "inputs":[{"name":"features","type":"bytes32[]"},{"name":"tokens","type":"address[]"},
     {"name":"rs","type":"tuple[]","components":[
       {"name":"bps","type":"uint16"},{"name":"flatFee","type":"uint128"},{"name":"active","type":"bool"}]}],
   "outputs":[]},
  {"type":"function","name":"setLimits","stateMutability":"nonpayable",
   "inputs":[{"name":"token","type":"address"},
     {"name":"l","type":"tuple","components":[{"name":"maxPerOrder","type":"uint128"},{"name":"maxOwed","type":"uint128"}]}],
   "outputs":[]},
  {"type":"function","name":"setFeeRecipient","stateMutability":"nonpayable",
   "inputs":[{"name":"recipient","type":"address"}],"outputs":[]}
]`

var parsedFeeConfigABI = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(feeConfigABIJSON))
	if err != nil {
		panic(fmt.Sprintf("fee config: parse ABI: %v", err))
	}
	return parsed
}()

// FeeConfigRate is one FeeConfig row. FlatFee is in the token's base units.
type FeeConfigRate struct {
	BPS     uint16   `json:"bps"`
	FlatFee *big.Int `json:"flatFee"`
	Active  bool     `json:"active"`
}

// FeeConfigLimits are a token's value caps, in base units.
type FeeConfigLimits struct {
	MaxPerOrder *big.Int `json:"maxPerOrder"`
	MaxOwed     *big.Int `json:"maxOwed"`
}

// feeConfigRateTuple / feeConfigLimitsTuple mirror the Solidity structs for ABI encoding
// (go-ethereum binds tuples positionally by exported field).
type feeConfigRateTuple struct {
	Bps     uint16
	FlatFee *big.Int
	Active  bool
}

type feeConfigLimitsTuple struct {
	MaxPerOrder *big.Int
	MaxOwed     *big.Int
}

// FeeConfigFeatureKey is the bytes32 key of a feature name: its ASCII bytes, right-padded with
// zeros (Solidity's bytes32("order") and ethers' encodeBytes32String).
func FeeConfigFeatureKey(name string) ([32]byte, error) {
	var key [32]byte
	if name == "" || len(name) > 31 {
		return key, fmt.Errorf("fee config: feature %q must be 1-31 bytes", name)
	}
	copy(key[:], name)
	return key, nil
}

// FeeConfigReader reads one chain's FeeConfig. It reuses the FeeManager chain reader's eth_call
// and receipt plumbing rather than restating it.
type FeeConfigReader struct {
	chain *evmFeeManagerChainReader
}

// NewFeeConfigReader returns nil when caller is nil, so callers keep the path dormant.
func NewFeeConfigReader(caller FeeManagerRPCClient) *FeeConfigReader {
	if caller == nil {
		return nil
	}
	return &FeeConfigReader{chain: &evmFeeManagerChainReader{caller: caller}}
}

// RawRate reads a row whether or not it is active (rateFor would revert on an inactive one).
func (r *FeeConfigReader) RawRate(ctx context.Context, feeConfig, feature string, token common.Address) (FeeConfigRate, error) {
	key, err := FeeConfigFeatureKey(feature)
	if err != nil {
		return FeeConfigRate{}, err
	}
	data, err := parsedFeeConfigABI.Pack("rawRate", key, token)
	if err != nil {
		return FeeConfigRate{}, fmt.Errorf("pack rawRate(%s,%s): %w", feature, token.Hex(), err)
	}
	out, err := r.chain.call(ctx, feeConfig, data)
	if err != nil {
		return FeeConfigRate{}, fmt.Errorf("rawRate(%s,%s) call: %w", feature, token.Hex(), err)
	}
	var res struct{ Arg0 feeConfigRateTuple }
	if err := parsedFeeConfigABI.UnpackIntoInterface(&res, "rawRate", out); err != nil {
		return FeeConfigRate{}, fmt.Errorf("decode rawRate: %w", err)
	}
	return FeeConfigRate{BPS: res.Arg0.Bps, FlatFee: res.Arg0.FlatFee, Active: res.Arg0.Active}, nil
}

// LimitsFor reads a token's limits. The contract reverts (LimitsNotConfigured) for a token with
// none, which surfaces here as a call error.
func (r *FeeConfigReader) LimitsFor(ctx context.Context, feeConfig string, token common.Address) (FeeConfigLimits, error) {
	data, err := parsedFeeConfigABI.Pack("limitsFor", token)
	if err != nil {
		return FeeConfigLimits{}, fmt.Errorf("pack limitsFor(%s): %w", token.Hex(), err)
	}
	out, err := r.chain.call(ctx, feeConfig, data)
	if err != nil {
		return FeeConfigLimits{}, fmt.Errorf("limitsFor(%s) call: %w", token.Hex(), err)
	}
	var res struct{ Arg0 feeConfigLimitsTuple }
	if err := parsedFeeConfigABI.UnpackIntoInterface(&res, "limitsFor", out); err != nil {
		return FeeConfigLimits{}, fmt.Errorf("decode limitsFor: %w", err)
	}
	return FeeConfigLimits{MaxPerOrder: res.Arg0.MaxPerOrder, MaxOwed: res.Arg0.MaxOwed}, nil
}

// FeeRecipient reads where every fee on this chain is paid.
func (r *FeeConfigReader) FeeRecipient(ctx context.Context, feeConfig string) (common.Address, error) {
	data, err := parsedFeeConfigABI.Pack("feeRecipient")
	if err != nil {
		return common.Address{}, fmt.Errorf("pack feeRecipient: %w", err)
	}
	out, err := r.chain.call(ctx, feeConfig, data)
	if err != nil {
		return common.Address{}, fmt.Errorf("feeRecipient call: %w", err)
	}
	vals, err := parsedFeeConfigABI.Unpack("feeRecipient", out)
	if err != nil || len(vals) == 0 {
		return common.Address{}, fmt.Errorf("decode feeRecipient: %v", err)
	}
	addr, ok := vals[0].(common.Address)
	if !ok {
		return common.Address{}, fmt.Errorf("feeRecipient unexpected type %T", vals[0])
	}
	return addr, nil
}

// TxSucceeded reports whether a mined tx succeeded (for confirming a signed config change).
func (r *FeeConfigReader) TxSucceeded(ctx context.Context, txHash string) (bool, error) {
	return r.chain.txSucceeded(ctx, txHash)
}

// FeeConfigRateRow is one row of a setRates batch.
type FeeConfigRateRow struct {
	Feature string
	Token   common.Address
	Rate    FeeConfigRate
}

// PackFeeConfigSetRates encodes setRates for the admin to sign. All-or-nothing on-chain.
func PackFeeConfigSetRates(rows []FeeConfigRateRow) ([]byte, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("fee config: no rows to set")
	}
	features := make([][32]byte, len(rows))
	tokens := make([]common.Address, len(rows))
	rates := make([]feeConfigRateTuple, len(rows))
	for i, row := range rows {
		key, err := FeeConfigFeatureKey(row.Feature)
		if err != nil {
			return nil, err
		}
		if row.Token == (common.Address{}) {
			return nil, fmt.Errorf("fee config: row %d has no token address", i)
		}
		flat := row.Rate.FlatFee
		if flat == nil {
			flat = new(big.Int)
		}
		if flat.Sign() < 0 {
			return nil, fmt.Errorf("fee config: row %d has a negative flat fee", i)
		}
		features[i], tokens[i] = key, row.Token
		rates[i] = feeConfigRateTuple{Bps: row.Rate.BPS, FlatFee: flat, Active: row.Rate.Active}
	}
	return parsedFeeConfigABI.Pack("setRates", features, tokens, rates)
}

// PackFeeConfigSetLimits encodes setLimits for one token. Both limits must be positive.
func PackFeeConfigSetLimits(token common.Address, l FeeConfigLimits) ([]byte, error) {
	if l.MaxPerOrder == nil || l.MaxOwed == nil || l.MaxPerOrder.Sign() <= 0 || l.MaxOwed.Sign() <= 0 {
		return nil, fmt.Errorf("fee config: limits must both be positive")
	}
	return parsedFeeConfigABI.Pack("setLimits", token, feeConfigLimitsTuple{MaxPerOrder: l.MaxPerOrder, MaxOwed: l.MaxOwed})
}

// PackFeeConfigSetFeeRecipient encodes setFeeRecipient.
func PackFeeConfigSetFeeRecipient(recipient common.Address) ([]byte, error) {
	if recipient == (common.Address{}) {
		return nil, fmt.Errorf("fee config: zero fee recipient")
	}
	return parsedFeeConfigABI.Pack("setFeeRecipient", recipient)
}
