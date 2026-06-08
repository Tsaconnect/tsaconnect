package services

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// feeManagerABIJSON is the minimal ABI for the on-chain FeeManager (design §3).
// Only the surfaces the backend needs:
//   - rateFor(string,string) view → (uint16 bps, uint64 flatUSD, bool mcgpExempt, bool active)
//   - setRate(string,string,(uint16,uint64,bool,bool)) onlyOwner — encoded into
//     the unsigned tx the multisig signs (the backend never broadcasts).
//   - RateSet event (decode/transparency; not subscribed here).
//
// The Rate tuple component order MUST match the Solidity struct:
//
//	struct Rate { uint16 bps; uint64 flatUSD; bool mcgpExempt; bool active; }
//
// flatUSD is uint64 in 1e6 units ($0.10 == 100000), matching FeeConfig.FlatAmountUSD.
const feeManagerABIJSON = `[
  {"type":"function","name":"rateFor","stateMutability":"view",
   "inputs":[{"name":"feature","type":"string"},{"name":"token","type":"string"}],
   "outputs":[{"name":"bps","type":"uint16"},{"name":"flatUSD","type":"uint64"},{"name":"mcgpExempt","type":"bool"},{"name":"active","type":"bool"}]},
  {"type":"function","name":"setRate","stateMutability":"nonpayable",
   "inputs":[{"name":"feature","type":"string"},{"name":"token","type":"string"},
     {"name":"r","type":"tuple","components":[
       {"name":"bps","type":"uint16"},{"name":"flatUSD","type":"uint64"},{"name":"mcgpExempt","type":"bool"},{"name":"active","type":"bool"}]}]},
  {"type":"event","name":"RateSet","anonymous":false,
   "inputs":[{"name":"key","type":"bytes32","indexed":true},{"name":"feature","type":"string","indexed":false},{"name":"token","type":"string","indexed":false},
     {"name":"bps","type":"uint16","indexed":false},{"name":"flatUSD","type":"uint64","indexed":false},{"name":"mcgpExempt","type":"bool","indexed":false},{"name":"active","type":"bool","indexed":false}]}
]`

// parsedFeeManagerABI is the compiled FeeManager ABI (built once at init).
var parsedFeeManagerABI = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(feeManagerABIJSON))
	if err != nil {
		panic(fmt.Sprintf("fee manager: parse ABI: %v", err))
	}
	return parsed
}()

// feeManagerRateTuple is the Go mirror of the Solidity `Rate` struct, used for
// ABI encoding setRate calldata. Field NAMES + ORDER must match the ABI tuple
// components exactly (go-ethereum binds tuples positionally by exported field).
type feeManagerRateTuple struct {
	Bps        uint16
	FlatUSD    uint64
	McgpExempt bool
	Active     bool
}

// FeeManagerRate is the resolved on-chain rate the backend reads/writes. It is
// the decoded form of rateFor / the input to setRate. BPS is clamped to
// [0, MaxFeeBPS] by the caller (the contract enforces MAX_BPS too).
type FeeManagerRate struct {
	BPS        uint16 `json:"bps"`
	FlatUSD    uint64 `json:"flatUsd"` // 1e6 units
	MCGPExempt bool   `json:"mcgpExempt"`
	Active     bool   `json:"active"`
}

// flatUSDToUnits converts a float USD value (FeeConfig.FlatAmountUSD semantics)
// to the contract's uint64 1e6-unit representation ($0.10 → 100000). Negative
// values clamp to 0.
func flatUSDToUnits(flatUSD float64) uint64 {
	if flatUSD <= 0 {
		return 0
	}
	// Round to nearest 1e6 unit to avoid float truncation surprises.
	return uint64(flatUSD*1e6 + 0.5)
}

// flatUnitsToUSD is the inverse of flatUSDToUnits (1e6 units → float USD).
func flatUnitsToUSD(units uint64) float64 {
	return float64(units) / 1e6
}

// PackSetRate ABI-encodes setRate(feature, token, (bps,flatUSD,mcgpExempt,active))
// into the calldata the multisig signs (design §5 plan tx `data`). It is the
// only authoritative write under A1 — there are no escrow setters.
func PackSetRate(feature, token string, r FeeManagerRate) ([]byte, error) {
	data, err := parsedFeeManagerABI.Pack("setRate", feature, token, feeManagerRateTuple{
		Bps:        r.BPS,
		FlatUSD:    r.FlatUSD,
		McgpExempt: r.MCGPExempt,
		Active:     r.Active,
	})
	if err != nil {
		return nil, fmt.Errorf("pack setRate(%s,%s): %w", feature, token, err)
	}
	return data, nil
}

// SetRateFunctionSig is the human-readable function signature surfaced in the
// plan response (advisory for the signer UI; `data` is load-bearing).
const SetRateFunctionSig = "setRate(string,string,(uint16,uint64,bool,bool))"

// FeeManagerChainReader is the read-only on-chain surface the set-fee confirm
// step + drift reconciler need. Kept narrow + injectable so both are unit-
// testable without a live RPC (mirrors RewardChainReader). The production
// implementation routes by chainID to the right chain's RPC.
//
// Both methods take chainID so a multi-chain reader routes unambiguously — a tx
// on chain A is never verified against chain B's RPC (a correctness bug for the
// confirm read-back), and rateFor always hits the chain that owns feeManager.
type FeeManagerChainReader interface {
	// RateFor reads the FeeManager's rateFor(feature, token) on `feeManager` on
	// the given chain.
	RateFor(ctx context.Context, chainID int64, feeManager, feature, token string) (FeeManagerRate, error)
	// TxSucceeded reports whether txHash mined with receipt status == 1 on the
	// given chain. A missing/unmined/failed receipt yields (false, err) so the
	// caller refuses the confirmation rather than trusting an unverified hash.
	TxSucceeded(ctx context.Context, chainID int64, txHash string) (bool, error)
}

// FeeManagerRPCClient is the read-only RPC surface the chain reader needs (the
// *ethclient.Client embedded in blockchain.EVMClient satisfies it). Exported so
// the main wiring can build the per-chain client map without re-stating the
// interface inline.
type FeeManagerRPCClient interface {
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	TransactionReceipt(ctx context.Context, txHash common.Hash) (*types.Receipt, error)
}

// feeManagerRPCCaller is the internal alias used throughout this file.
type feeManagerRPCCaller = FeeManagerRPCClient

// evmFeeManagerChainReader reads rateFor + tx receipts via eth_call /
// eth_getTransactionReceipt against ONE chain's RPC. It is the per-chain building
// block of the multi-chain reader (one per configured chain).
type evmFeeManagerChainReader struct {
	caller feeManagerRPCCaller
}

func (r *evmFeeManagerChainReader) rateFor(ctx context.Context, feeManager, feature, token string) (FeeManagerRate, error) {
	data, err := parsedFeeManagerABI.Pack("rateFor", feature, token)
	if err != nil {
		return FeeManagerRate{}, fmt.Errorf("pack rateFor(%s,%s): %w", feature, token, err)
	}
	out, err := r.call(ctx, feeManager, data)
	if err != nil {
		return FeeManagerRate{}, fmt.Errorf("rateFor(%s,%s) call: %w", feature, token, err)
	}
	vals, err := parsedFeeManagerABI.Unpack("rateFor", out)
	if err != nil || len(vals) < 4 {
		return FeeManagerRate{}, fmt.Errorf("decode rateFor: %w", err)
	}
	bps, ok := vals[0].(uint16)
	if !ok {
		return FeeManagerRate{}, fmt.Errorf("rateFor bps unexpected type %T", vals[0])
	}
	flat, ok := vals[1].(uint64)
	if !ok {
		return FeeManagerRate{}, fmt.Errorf("rateFor flatUSD unexpected type %T", vals[1])
	}
	exempt, ok := vals[2].(bool)
	if !ok {
		return FeeManagerRate{}, fmt.Errorf("rateFor mcgpExempt unexpected type %T", vals[2])
	}
	active, ok := vals[3].(bool)
	if !ok {
		return FeeManagerRate{}, fmt.Errorf("rateFor active unexpected type %T", vals[3])
	}
	return FeeManagerRate{BPS: bps, FlatUSD: flat, MCGPExempt: exempt, Active: active}, nil
}

func (r *evmFeeManagerChainReader) txSucceeded(ctx context.Context, txHash string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	h := strings.TrimSpace(txHash)
	if h == "" {
		return false, fmt.Errorf("empty tx hash")
	}
	receipt, err := r.caller.TransactionReceipt(ctx, common.HexToHash(h))
	if err != nil {
		return false, fmt.Errorf("fetch receipt for %s: %w", h, err)
	}
	if receipt == nil {
		return false, fmt.Errorf("nil receipt for %s (not mined?)", h)
	}
	return receipt.Status == types.ReceiptStatusSuccessful, nil
}

// call performs a read-only eth_call against `to` with a short timeout.
func (r *evmFeeManagerChainReader) call(ctx context.Context, to string, data []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addr := common.HexToAddress(to)
	return r.caller.CallContract(ctx, ethereum.CallMsg{To: &addr, Data: data}, nil)
}

// multiChainFeeManagerReader routes a RateFor / TxSucceeded call to the right
// chain's RPC by chainID (built at construction from cfg.FeeManagerChains). It is
// the production FeeManagerChainReader used by the admin set-fee flow + the drift
// reconciler, which span every configured chain. The single-RPC
// evmFeeManagerChainReader is the per-chain building block (one per chain here).
type multiChainFeeManagerReader struct {
	byChain map[int64]*evmFeeManagerChainReader
}

// NewMultiChainFeeManagerReaderFromClients builds the production reader from a
// chainID → RPC client map (each *ethclient.Client satisfies FeeManagerRPCClient).
// Chains with a nil client are skipped (the admin flow then surfaces a read error
// for that chain rather than crashing). Returns nil when no chain has a reachable
// RPC, so callers leave the flow dormant.
func NewMultiChainFeeManagerReaderFromClients(rpcByChain map[int64]FeeManagerRPCClient) FeeManagerChainReader {
	byChain := map[int64]*evmFeeManagerChainReader{}
	for chainID, caller := range rpcByChain {
		if caller == nil {
			continue
		}
		byChain[chainID] = &evmFeeManagerChainReader{caller: caller}
	}
	if len(byChain) == 0 {
		return nil
	}
	return &multiChainFeeManagerReader{byChain: byChain}
}

func (m *multiChainFeeManagerReader) RateFor(ctx context.Context, chainID int64, feeManager, feature, token string) (FeeManagerRate, error) {
	r, ok := m.byChain[chainID]
	if !ok {
		return FeeManagerRate{}, fmt.Errorf("no RPC client for chain %d", chainID)
	}
	return r.rateFor(ctx, feeManager, feature, token)
}

func (m *multiChainFeeManagerReader) TxSucceeded(ctx context.Context, chainID int64, txHash string) (bool, error) {
	r, ok := m.byChain[chainID]
	if !ok {
		return false, fmt.Errorf("no RPC client for chain %d", chainID)
	}
	return r.txSucceeded(ctx, txHash)
}
