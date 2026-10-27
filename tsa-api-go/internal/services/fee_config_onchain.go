package services

import (
	"context"
	"encoding/json"
	"errors"
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

// FeeConfigMaxBPS mirrors FeeConfig.MAX_BPS (10%); setRates reverts above it.
const FeeConfigMaxBPS = 1000

// ErrFeeConfigReverted marks a FeeConfig read the contract refused (an unset row or limits),
// as opposed to an RPC failure: the first is an answer, the second is "could not check".
var ErrFeeConfigReverted = errors.New("fee config call reverted")

// ErrFeeConfigDormant is returned by a nil reader or a chain without an RPC client.
var ErrFeeConfigDormant = errors.New("fee config reader not configured")

// maxUint128 bounds every uint128 field the contract stores.
var maxUint128 = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))

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
	BPS     uint16
	FlatFee *big.Int
	Active  bool
}

// MarshalJSON writes FlatFee as a decimal string: 18-decimal amounts overflow a JS number.
func (r FeeConfigRate) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		BPS     uint16 `json:"bps"`
		FlatFee string `json:"flatFee"`
		Active  bool   `json:"active"`
	}{r.BPS, baseUnitsString(r.FlatFee), r.Active})
}

// FeeConfigLimits are a token's value caps, in base units.
type FeeConfigLimits struct {
	MaxPerOrder *big.Int
	MaxOwed     *big.Int
}

// MarshalJSON writes both limits as decimal strings (see FeeConfigRate.MarshalJSON).
func (l FeeConfigLimits) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		MaxPerOrder string `json:"maxPerOrder"`
		MaxOwed     string `json:"maxOwed"`
	}{baseUnitsString(l.MaxPerOrder), baseUnitsString(l.MaxOwed)})
}

func baseUnitsString(x *big.Int) string {
	if x == nil {
		return "0"
	}
	return x.String()
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
// zeros (Solidity's bytes32("order") and ethers' encodeBytes32String). Only the three features
// the contracts read are accepted, so a typo cannot write a row nothing will ever use.
func FeeConfigFeatureKey(name string) ([32]byte, error) {
	var key [32]byte
	switch name {
	case FeeConfigFeatureOrder, FeeConfigFeatureP2PPlatform, FeeConfigFeatureP2PGas:
	default:
		return key, fmt.Errorf("fee config: unknown feature %q", name)
	}
	copy(key[:], name)
	return key, nil
}

// FeeConfigReader reads each chain's FeeConfig, routed by chainID. It reuses the FeeManager chain
// reader's eth_call and receipt plumbing rather than restating it.
type FeeConfigReader struct {
	byChain map[int64]*evmFeeManagerChainReader
}

// NewFeeConfigReader builds the reader from a chainID → RPC client map. Chains with a nil client
// are skipped; it returns nil when none is left, so callers keep the path dormant.
func NewFeeConfigReader(rpcByChain map[int64]FeeManagerRPCClient) *FeeConfigReader {
	byChain := feeChainReaders(rpcByChain)
	if len(byChain) == 0 {
		return nil
	}
	return &FeeConfigReader{byChain: byChain}
}

func (r *FeeConfigReader) chain(chainID int64) (*evmFeeManagerChainReader, error) {
	if r == nil {
		return nil, ErrFeeConfigDormant
	}
	c, ok := r.byChain[chainID]
	if !ok {
		return nil, fmt.Errorf("chain %d: %w", chainID, ErrFeeConfigDormant)
	}
	return c, nil
}

// read eth_calls feeConfig on chainID, marking a contract revert with ErrFeeConfigReverted.
func (r *FeeConfigReader) read(ctx context.Context, chainID int64, feeConfig, what string, data []byte) ([]byte, error) {
	c, err := r.chain(chainID)
	if err != nil {
		return nil, err
	}
	out, err := c.call(ctx, feeConfig, data)
	if isRevert(err) {
		return nil, fmt.Errorf("%s: %w: %v", what, ErrFeeConfigReverted, err)
	}
	if err != nil {
		return nil, fmt.Errorf("%s call: %w", what, err)
	}
	return out, nil
}

// RawRate reads a row whether or not it is active (rateFor would revert on an inactive one).
func (r *FeeConfigReader) RawRate(ctx context.Context, chainID int64, feeConfig, feature string, token common.Address) (FeeConfigRate, error) {
	key, err := FeeConfigFeatureKey(feature)
	if err != nil {
		return FeeConfigRate{}, err
	}
	data, err := parsedFeeConfigABI.Pack("rawRate", key, token)
	if err != nil {
		return FeeConfigRate{}, fmt.Errorf("pack rawRate(%s,%s): %w", feature, token.Hex(), err)
	}
	out, err := r.read(ctx, chainID, feeConfig, fmt.Sprintf("rawRate(%s,%s)", feature, token.Hex()), data)
	if err != nil {
		return FeeConfigRate{}, err
	}
	var res struct{ Arg0 feeConfigRateTuple }
	if err := parsedFeeConfigABI.UnpackIntoInterface(&res, "rawRate", out); err != nil {
		return FeeConfigRate{}, fmt.Errorf("decode rawRate: %w", err)
	}
	return FeeConfigRate{BPS: res.Arg0.Bps, FlatFee: res.Arg0.FlatFee, Active: res.Arg0.Active}, nil
}

// LimitsFor reads a token's limits. The contract reverts (LimitsNotConfigured) for a token with
// none, which surfaces here as ErrFeeConfigReverted.
func (r *FeeConfigReader) LimitsFor(ctx context.Context, chainID int64, feeConfig string, token common.Address) (FeeConfigLimits, error) {
	data, err := parsedFeeConfigABI.Pack("limitsFor", token)
	if err != nil {
		return FeeConfigLimits{}, fmt.Errorf("pack limitsFor(%s): %w", token.Hex(), err)
	}
	out, err := r.read(ctx, chainID, feeConfig, fmt.Sprintf("limitsFor(%s)", token.Hex()), data)
	if err != nil {
		return FeeConfigLimits{}, err
	}
	var res struct{ Arg0 feeConfigLimitsTuple }
	if err := parsedFeeConfigABI.UnpackIntoInterface(&res, "limitsFor", out); err != nil {
		return FeeConfigLimits{}, fmt.Errorf("decode limitsFor: %w", err)
	}
	return FeeConfigLimits{MaxPerOrder: res.Arg0.MaxPerOrder, MaxOwed: res.Arg0.MaxOwed}, nil
}

// FeeRecipient reads where every fee on this chain is paid.
func (r *FeeConfigReader) FeeRecipient(ctx context.Context, chainID int64, feeConfig string) (common.Address, error) {
	data, err := parsedFeeConfigABI.Pack("feeRecipient")
	if err != nil {
		return common.Address{}, fmt.Errorf("pack feeRecipient: %w", err)
	}
	out, err := r.read(ctx, chainID, feeConfig, "feeRecipient", data)
	if err != nil {
		return common.Address{}, err
	}
	vals, err := parsedFeeConfigABI.Unpack("feeRecipient", out)
	if err != nil {
		return common.Address{}, fmt.Errorf("decode feeRecipient: %w", err)
	}
	if len(vals) == 0 {
		return common.Address{}, errors.New("decode feeRecipient: empty result")
	}
	addr, ok := vals[0].(common.Address)
	if !ok {
		return common.Address{}, fmt.Errorf("feeRecipient unexpected type %T", vals[0])
	}
	return addr, nil
}

// TxSucceeded reports whether a mined tx on chainID succeeded. A success says nothing about what
// the tx did: a flow that confirms a config change must re-read the row (RawRate, LimitsFor,
// FeeRecipient) and compare it with the intent, never trust a caller-supplied hash alone.
func (r *FeeConfigReader) TxSucceeded(ctx context.Context, chainID int64, txHash string) (bool, error) {
	c, err := r.chain(chainID)
	if err != nil {
		return false, err
	}
	return c.txSucceeded(ctx, txHash)
}

// FeeConfigRateRow is one row of a setRates batch.
type FeeConfigRateRow struct {
	Feature string
	Token   common.Address
	Rate    FeeConfigRate
}

// PackFeeConfigSetRates encodes setRates for the admin to sign. It refuses what the contract would
// revert on (and duplicate rows, whose last write would silently win), so the admin is never
// asked to sign a doomed tx.
func PackFeeConfigSetRates(rows []FeeConfigRateRow) ([]byte, error) {
	if len(rows) == 0 {
		return nil, errors.New("fee config: no rows to set")
	}
	features := make([][32]byte, len(rows))
	tokens := make([]common.Address, len(rows))
	rates := make([]feeConfigRateTuple, len(rows))
	seen := make(map[[32]byte]map[common.Address]bool, 3)
	for i, row := range rows {
		key, err := FeeConfigFeatureKey(row.Feature)
		if err != nil {
			return nil, err
		}
		if row.Token == (common.Address{}) {
			return nil, fmt.Errorf("fee config: row %d has no token address", i)
		}
		if seen[key] == nil {
			seen[key] = map[common.Address]bool{}
		}
		if seen[key][row.Token] {
			return nil, fmt.Errorf("fee config: row %d repeats %s for %s", i, row.Feature, row.Token.Hex())
		}
		seen[key][row.Token] = true
		if row.Rate.BPS > FeeConfigMaxBPS {
			return nil, fmt.Errorf("fee config: row %d rate %d bps is above the %d bps cap", i, row.Rate.BPS, FeeConfigMaxBPS)
		}
		flat := row.Rate.FlatFee
		if flat == nil {
			flat = new(big.Int)
		}
		if err := checkUint128(flat); err != nil {
			return nil, fmt.Errorf("fee config: row %d flat fee: %w", i, err)
		}
		features[i], tokens[i] = key, row.Token
		rates[i] = feeConfigRateTuple{Bps: row.Rate.BPS, FlatFee: flat, Active: row.Rate.Active}
	}
	return parsedFeeConfigABI.Pack("setRates", features, tokens, rates)
}

// PackFeeConfigSetLimits encodes setLimits for one token. Both limits must be positive.
func PackFeeConfigSetLimits(token common.Address, l FeeConfigLimits) ([]byte, error) {
	if token == (common.Address{}) {
		return nil, errors.New("fee config: limits need a token address")
	}
	if l.MaxPerOrder == nil || l.MaxOwed == nil || l.MaxPerOrder.Sign() <= 0 || l.MaxOwed.Sign() <= 0 {
		return nil, errors.New("fee config: limits must both be positive")
	}
	if err := checkUint128(l.MaxPerOrder); err != nil {
		return nil, fmt.Errorf("fee config: max per order: %w", err)
	}
	if err := checkUint128(l.MaxOwed); err != nil {
		return nil, fmt.Errorf("fee config: max owed: %w", err)
	}
	return parsedFeeConfigABI.Pack("setLimits", token, feeConfigLimitsTuple{MaxPerOrder: l.MaxPerOrder, MaxOwed: l.MaxOwed})
}

// PackFeeConfigSetFeeRecipient encodes setFeeRecipient.
func PackFeeConfigSetFeeRecipient(recipient common.Address) ([]byte, error) {
	if recipient == (common.Address{}) {
		return nil, errors.New("fee config: zero fee recipient")
	}
	return parsedFeeConfigABI.Pack("setFeeRecipient", recipient)
}

func checkUint128(x *big.Int) error {
	if x.Sign() < 0 {
		return errors.New("negative")
	}
	if x.Cmp(maxUint128) > 0 {
		return errors.New("does not fit uint128")
	}
	return nil
}
