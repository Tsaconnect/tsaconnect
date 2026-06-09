package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// escrowCutoverABIJSON is the MINIMAL ABI for the two escrow getters the boot
// guard reads (configurable-FeeManager design §9 step 4 / I1 — the double-pay
// interlock). Every escrow (P2PEscrowV3, ProductEscrowV2, ServiceContactV2)
// gains these in its §3.1/§7 upgrade:
//
//	bool    public legacySplitEnabled;  // deployed TRUE; flipped FALSE at cutover
//	address public feeManager;          // FeeManager proxy for this chain
//
// If FEE_MODEL_V2_ENABLED is flipped ON while an escrow still has
// legacySplitEnabled==true (or feeManager==0x0, i.e. not yet wired/upgraded), the
// SAME fee is split BOTH on-chain (the legacy escrow split) AND off-chain (the v2
// engine) — a double-pay. The boot guard fails-safe before that can happen.
const escrowCutoverABIJSON = `[
  {"type":"function","name":"legacySplitEnabled","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"feeManager","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]}
]`

var parsedEscrowCutoverABI = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(escrowCutoverABIJSON))
	if err != nil {
		panic(fmt.Sprintf("escrow cutover guard: parse ABI: %v", err))
	}
	return parsed
}()

// EscrowRef identifies one on-chain escrow the boot guard must verify: its chain,
// proxy address, and a human label for the alert.
type EscrowRef struct {
	Label   string // "p2p", "product", "service-contact"
	ChainID int64
	Address string
}

// EscrowCutoverState is the pair of getters read for one escrow.
type EscrowCutoverState struct {
	LegacySplitEnabled bool
	FeeManager         string // hex address ("0x0000…0000" = unset)
}

// EscrowCutoverReader reads the two cutover getters off one escrow on a given
// chain. Kept narrow + injectable so the boot guard is unit-testable with a stub
// (mirrors FeeManagerChainReader). Routes by chainID so a P2P escrow on BSC is
// never read against the Sonic RPC.
type EscrowCutoverReader interface {
	ReadCutoverState(ctx context.Context, chainID int64, escrowAddress string) (EscrowCutoverState, error)
}

// VerifyEscrowCutoverReady is the design §9-step-4 / I1 boot guard. When flagOn
// is false, or no escrows are configured (the dormant default), it is a NO-OP
// (returns nil). When flagOn is true it reads each escrow's legacySplitEnabled +
// feeManager and returns a LOUD error if ANY escrow still splits on-chain
// (legacySplitEnabled==true) OR is not wired to a FeeManager (feeManager==0x0) —
// because flipping FEE_MODEL_V2_ENABLED ON while an escrow still splits
// double-pays the fee (on-chain split + off-chain v2 engine).
//
// A read error for any escrow is also fatal: if we cannot prove an escrow has
// stopped splitting, we must NOT proceed with the v2 engine active (fail-safe).
// The returned error aggregates every offending/unreadable escrow so the
// operator sees the full picture in one boot-failure line.
func VerifyEscrowCutoverReady(ctx context.Context, reader EscrowCutoverReader, escrows []EscrowRef, flagOn bool) error {
	if !flagOn {
		return nil // flag OFF — legacy behavior unchanged; guard is a no-op.
	}
	if len(escrows) == 0 {
		return nil // no escrows configured (dormant) — nothing to verify.
	}
	if reader == nil {
		// Flag is ON and escrows ARE configured but we have no way to read them —
		// we cannot prove they stopped splitting. Fail safe.
		return fmt.Errorf("FEE_MODEL_V2_ENABLED is ON with %d escrow(s) configured but no on-chain reader is available to verify legacySplitEnabled/feeManager — refusing to boot (cannot prove the escrows stopped splitting on-chain; would risk a double-pay)", len(escrows))
	}

	var problems []string
	for _, e := range escrows {
		if strings.TrimSpace(e.Address) == "" {
			continue // unconfigured escrow slot — not deployed, skip.
		}
		state, err := reader.ReadCutoverState(ctx, e.ChainID, e.Address)
		if err != nil {
			problems = append(problems, fmt.Sprintf(
				"%s@chain%d(%s): could not read cutover getters (%v) — cannot prove it stopped splitting",
				e.Label, e.ChainID, e.Address, err))
			continue
		}
		if state.LegacySplitEnabled {
			problems = append(problems, fmt.Sprintf(
				"%s@chain%d(%s): legacySplitEnabled==true — escrow STILL splits on-chain; the off-chain v2 engine would DOUBLE-PAY the fee",
				e.Label, e.ChainID, e.Address))
		}
		if isZeroOrEmptyAddress(state.FeeManager) {
			problems = append(problems, fmt.Sprintf(
				"%s@chain%d(%s): feeManager==0x0 — escrow not yet wired/upgraded to the FeeManager (live-read not active)",
				e.Label, e.ChainID, e.Address))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("escrow cutover guard FAILED (FEE_MODEL_V2_ENABLED is ON but the escrows are not cutover-ready — flipping the flag now would double-pay fees): %s",
			strings.Join(problems, "; "))
	}
	return nil
}

// isZeroOrEmptyAddress reports whether a hex address string is empty or the zero
// address (0x0000…0000).
func isZeroOrEmptyAddress(addr string) bool {
	a := strings.TrimSpace(addr)
	if a == "" {
		return true
	}
	return common.HexToAddress(a) == (common.Address{})
}

// evmEscrowCutoverReader reads the two getters via eth_call against per-chain RPC
// clients (each *ethclient.Client satisfies FeeManagerRPCClient.CallContract).
type evmEscrowCutoverReader struct {
	byChain map[int64]FeeManagerRPCClient
}

// NewEscrowCutoverReaderFromClients builds the production reader from a chainID →
// RPC client map. Returns nil when no chain has a reachable client (the guard
// then treats reader==nil as fail-safe when the flag is ON with escrows set).
func NewEscrowCutoverReaderFromClients(rpcByChain map[int64]FeeManagerRPCClient) EscrowCutoverReader {
	byChain := map[int64]FeeManagerRPCClient{}
	for chainID, c := range rpcByChain {
		if c == nil {
			continue
		}
		byChain[chainID] = c
	}
	if len(byChain) == 0 {
		return nil
	}
	return &evmEscrowCutoverReader{byChain: byChain}
}

func (r *evmEscrowCutoverReader) ReadCutoverState(ctx context.Context, chainID int64, escrowAddress string) (EscrowCutoverState, error) {
	client, ok := r.byChain[chainID]
	if !ok || client == nil {
		return EscrowCutoverState{}, fmt.Errorf("no RPC client for chain %d", chainID)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	to := common.HexToAddress(escrowAddress)

	legacy, err := callBoolGetter(ctx, client, to, "legacySplitEnabled")
	if err != nil {
		return EscrowCutoverState{}, fmt.Errorf("legacySplitEnabled: %w", err)
	}
	fm, err := callAddressGetter(ctx, client, to, "feeManager")
	if err != nil {
		return EscrowCutoverState{}, fmt.Errorf("feeManager: %w", err)
	}
	return EscrowCutoverState{LegacySplitEnabled: legacy, FeeManager: fm}, nil
}

func callBoolGetter(ctx context.Context, client FeeManagerRPCClient, to common.Address, method string) (bool, error) {
	data, err := parsedEscrowCutoverABI.Pack(method)
	if err != nil {
		return false, err
	}
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return false, err
	}
	vals, err := parsedEscrowCutoverABI.Unpack(method, out)
	if err != nil || len(vals) < 1 {
		return false, fmt.Errorf("decode %s: %w", method, err)
	}
	b, ok := vals[0].(bool)
	if !ok {
		return false, fmt.Errorf("%s: unexpected type %T", method, vals[0])
	}
	return b, nil
}

func callAddressGetter(ctx context.Context, client FeeManagerRPCClient, to common.Address, method string) (string, error) {
	data, err := parsedEscrowCutoverABI.Pack(method)
	if err != nil {
		return "", err
	}
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return "", err
	}
	vals, err := parsedEscrowCutoverABI.Unpack(method, out)
	if err != nil || len(vals) < 1 {
		return "", fmt.Errorf("decode %s: %w", method, err)
	}
	a, ok := vals[0].(common.Address)
	if !ok {
		return "", fmt.Errorf("%s: unexpected type %T", method, vals[0])
	}
	return a.Hex(), nil
}
