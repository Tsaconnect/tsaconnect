package services

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// p2p_direct_gates.go — the pieces the core contract set's boot gates (core_contracts.go) are
// built from. The V3 direct-escrow gates they once also served went with the hard switch to
// P2PTradeEscrow (slice 8): a P2P chain is open to new activity while its core set is ready.

// DirectChainReader is a chain as the boot gates read it; *ethclient.Client is one.
type DirectChainReader interface {
	contractCaller
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
	ChainID(ctx context.Context) (*big.Int, error)
}

// DirectGateFailure is one failed gate. Unread means the chain could not be
// read (RPC error, timeout, no client): the escrow's state is unknown, not
// wrong, so it must not cut live trades off from their exits.
type DirectGateFailure struct {
	Gate, Reason string
	Unread       bool
}

func (f DirectGateFailure) String() string { return f.Gate + ": " + f.Reason }

// gateRun collects one gate pass's failures.
type gateRun struct{ fails []DirectGateFailure }

func (g *gateRun) fail(gate, reason string) {
	g.fails = append(g.fails, DirectGateFailure{Gate: gate, Reason: RedactAlertText(reason)})
}

func (g *gateRun) unread(gate, reason string) {
	g.fails = append(g.fails, DirectGateFailure{Gate: gate, Reason: RedactAlertText(reason), Unread: true})
}

// code reports whether addr has bytecode, recording why not.
func (g *gateRun) code(ctx context.Context, chain DirectChainReader, label, addr string) bool {
	code, err := chain.CodeAt(ctx, common.HexToAddress(addr), nil)
	switch {
	case err != nil:
		g.unread("bytecode", label+" unreadable: "+err.Error())
		return false
	case len(code) == 0:
		g.fail("bytecode", label+" has no code at "+addr)
		return false
	}
	return true
}

// chainID checks that the RPC reports want.
func (g *gateRun) chainID(ctx context.Context, chain DirectChainReader, want int64) {
	if id, err := chain.ChainID(ctx); err != nil {
		g.unread("chain_id", "eth_chainId unreadable: "+err.Error())
	} else if id.Int64() != want {
		g.fail("chain_id", fmt.Sprintf("RPC reports %s, the config says %d", id, want))
	}
}

// receipts checks that the RPC serves receipts.
func (g *gateRun) receipts(ctx context.Context, rpc RawRPCCaller) {
	if rpc == nil {
		g.unread("receipts", "no RPC")
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := ProbeReceipts(pctx, rpc); err != nil {
		g.unread("receipts", err.Error())
	}
}

// firstAddress decodes a view's single address result.
func firstAddress(method string) func([]any, error) (common.Address, error) {
	return func(vals []any, err error) (common.Address, error) {
		if err != nil {
			return common.Address{}, err
		}
		a, ok := vals[0].(common.Address)
		if !ok {
			return common.Address{}, fmt.Errorf("decode %s: unexpected %T", method, vals[0])
		}
		return a, nil
	}
}

// logGateFailures logs each failure and returns how many are wrong values (not just unread).
func logGateFailures(metric, chainKey string, chainID int64, fails []DirectGateFailure) (wrong int) {
	for _, f := range fails {
		kind := "wrong"
		if f.Unread {
			kind = "unread"
		} else {
			wrong++
		}
		log.Printf("[config] ALERT %s chain=%s(%d) kind=%s %s", metric, chainKey, chainID, kind, f)
	}
	return wrong
}
