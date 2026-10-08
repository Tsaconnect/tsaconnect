package services

import (
	"context"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// readEscrowFeeRow is the one chain read of "what fee would this escrow charge
// for feature on this token": escrow.feeManager() → escrow.tokenSymbol(token)
// → feeManager.rateFor(feature, symbol). Extracted from EscrowService so the
// product escrow's pricing and the pool's fee invariant (spec §6.2 gate 2)
// read the same thing the same way. The P2P and product escrows share the
// feeManager()/tokenSymbol() signatures, so parsedEscrowABI encodes both.
func readEscrowFeeRow(ctx context.Context, caller contractCaller, escrowAddr, tokenAddr, feature string) (escrowRate, error) {
	if caller == nil {
		return escrowRate{}, fmt.Errorf("blockchain client not available")
	}
	if !common.IsHexAddress(tokenAddr) {
		return escrowRate{}, fmt.Errorf("token address %q not configured", tokenAddr)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	call := func(to common.Address, abiDef abi.ABI, method string, args ...any) ([]any, error) {
		data, err := abiDef.Pack(method, args...)
		if err != nil {
			return nil, fmt.Errorf("pack %s: %w", method, err)
		}
		out, err := caller.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
		if err != nil {
			return nil, fmt.Errorf("%s call: %w", method, err)
		}
		vals, err := abiDef.Unpack(method, out)
		if err != nil || len(vals) == 0 {
			return nil, fmt.Errorf("decode %s: %v", method, err)
		}
		return vals, nil
	}
	escrow := common.HexToAddress(escrowAddr)
	vals, err := call(escrow, parsedEscrowABI, "feeManager")
	if err != nil {
		return escrowRate{}, err
	}
	fm, _ := vals[0].(common.Address)
	if fm == (common.Address{}) {
		return escrowRate{noFeeManager: true}, nil
	}
	vals, err = call(escrow, parsedEscrowABI, "tokenSymbol", common.HexToAddress(tokenAddr))
	if err != nil {
		return escrowRate{}, err
	}
	symbol, _ := vals[0].(string)
	vals, err = call(fm, parsedFeeManagerABI, "rateFor", feature, symbol)
	if err != nil {
		return escrowRate{}, err
	}
	if len(vals) < 4 {
		return escrowRate{}, fmt.Errorf("decode rateFor: %d values", len(vals))
	}
	bps, _ := vals[0].(uint16)
	flat, _ := vals[1].(uint64)
	exempt, _ := vals[2].(bool)
	active, _ := vals[3].(bool)
	return escrowRate{bps: int(bps), flatUSD: flat, exempt: exempt, active: active, feeManager: fm, symbol: symbol}, nil
}

// callP2PView is one read-only call against a P2P escrow's own ABI.
func callP2PView(ctx context.Context, caller contractCaller, escrowAddr, method string, args ...any) ([]any, error) {
	if caller == nil {
		return nil, fmt.Errorf("blockchain client not available")
	}
	data, err := parsedP2PEscrowABI.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", method, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	to := common.HexToAddress(escrowAddr)
	out, err := caller.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("%s call: %w", method, err)
	}
	vals, err := parsedP2PEscrowABI.Unpack(method, out)
	if err != nil || len(vals) == 0 {
		return nil, fmt.Errorf("decode %s: %v", method, err)
	}
	return vals, nil
}
