package services

import (
	"context"
	"fmt"
	"log"
	"maps"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// DirectChainReader is a direct escrow's chain as its boot gates read it;
// *ethclient.Client is one.
type DirectChainReader interface {
	contractCaller
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
	ChainID(ctx context.Context) (*big.Int, error)
}

// DirectEscrowGatesConfig is everything the direct-escrow boot gates read
// (plan p2p-bsc API-9). They guard a direct-audience escrow only: the one
// whose admins must be dispute arbiters alone (decisions 2026-10-12/13).
type DirectEscrowGatesConfig struct {
	ChainID       int64
	ChainKey      string
	Escrow        string
	FeeManager    string            // the preset row's
	ExpectedOwner string            // the preset row's
	Tokens        map[string]string // asset symbol → token address
	Decimals      map[string]int    // as the registry read them on-chain
	// ForbiddenAdmins are the hot keys the API knows (label → address): none
	// may be an admin of a direct escrow, whose admins are arbiters only.
	ForbiddenAdmins map[string]string
	Adapter         P2PEscrowAdapter // paymentWindow()
	Chain           DirectChainReader
	Receipts        RawRPCCaller
}

// CheckPaymentWindow: an escrow's paymentWindow() must equal want, the window
// the API stamps on trades and the expiry canceller assumes. An escrow that
// cannot say is refused too.
func CheckPaymentWindow(e P2PEscrowAdapter, want time.Duration) error {
	r, ok := e.(interface{ ReadPaymentWindow() (time.Duration, error) })
	if !ok {
		return fmt.Errorf("the escrow cannot report its payment window")
	}
	w, err := r.ReadPaymentWindow()
	if err != nil {
		return fmt.Errorf("read the payment window: %w", err)
	}
	if w != want {
		return fmt.Errorf("escrow payment window %s, the API assumes %s", w, want)
	}
	return nil
}

// CheckDirectEscrowGates runs every direct-escrow gate and returns the
// failures as "gate: reason"; none means the escrow may be registered.
func CheckDirectEscrowGates(ctx context.Context, c DirectEscrowGatesConfig) []string {
	var fails []string
	fail := func(gate, reason string) { fails = append(fails, gate+": "+RedactAlertText(reason)) }
	if c.Chain == nil {
		fail("bytecode", "no chain client on "+c.ChainKey)
		return fails
	}
	addrView := func(method string, args ...any) (common.Address, error) {
		vals, err := callP2PView(ctx, c.Chain, c.Escrow, method, args...)
		if err != nil {
			return common.Address{}, err
		}
		a, ok := vals[0].(common.Address)
		if !ok {
			return common.Address{}, fmt.Errorf("decode %s: unexpected %T", method, vals[0])
		}
		return a, nil
	}
	boolView := func(method string, args ...any) (bool, error) {
		vals, err := callP2PView(ctx, c.Chain, c.Escrow, method, args...)
		if err != nil {
			return false, err
		}
		b, ok := vals[0].(bool)
		if !ok {
			return false, fmt.Errorf("decode %s: unexpected %T", method, vals[0])
		}
		return b, nil
	}

	// Bytecode at the escrow and its FeeManager.
	for _, a := range []struct{ label, addr string }{{"escrow", c.Escrow}, {"fee_manager", c.FeeManager}} {
		if a.addr == "" {
			continue // the fee_manager gate names a missing preset
		}
		code, err := c.Chain.CodeAt(ctx, common.HexToAddress(a.addr), nil)
		switch {
		case err != nil:
			fail("bytecode", a.label+" unreadable: "+err.Error())
		case len(code) == 0:
			fail("bytecode", a.label+" has no code at "+a.addr)
		}
	}
	// eth_chainId is the configured chain (the EIP-712 domain uses it).
	if id, err := c.Chain.ChainID(ctx); err != nil {
		fail("chain_id", "eth_chainId unreadable: "+err.Error())
	} else if id.Int64() != c.ChainID {
		fail("chain_id", fmt.Sprintf("RPC reports %s, the preset is %d", id, c.ChainID))
	}
	// paymentWindow() is the 30 minutes every direct trade is stamped with.
	if err := CheckPaymentWindow(c.Adapter, p2pPaymentWindow); err != nil {
		fail("payment_window", err.Error())
	}
	// feeManager() is the preset's own.
	if c.FeeManager == "" {
		fail("fee_manager", "the preset row names no FeeManager")
	} else if fm, err := addrView("feeManager"); err != nil {
		fail("fee_manager", "unreadable: "+err.Error())
	} else if !strings.EqualFold(fm.Hex(), c.FeeManager) {
		fail("fee_manager", fmt.Sprintf("escrow.feeManager() is %s, not the preset %s", fm.Hex(), c.FeeManager))
	}
	// Every asset accepted, with the decimals the API's amount math assumes.
	if len(c.Tokens) == 0 {
		fail("token_accepted", "no assets configured")
	}
	for _, sym := range slices.Sorted(maps.Keys(c.Tokens)) {
		addr := c.Tokens[sym]
		if !common.IsHexAddress(addr) {
			fail("token_accepted", sym+" has no token address")
		} else if ok, err := boolView("acceptedTokens", common.HexToAddress(addr)); err != nil {
			fail("token_accepted", sym+" unreadable: "+err.Error())
		} else if !ok {
			fail("token_accepted", sym+" at "+addr+" is not accepted by the escrow")
		}
		want := tokenDecimalsFor(c.ChainID, sym)
		if d, ok := c.Decimals[strings.ToUpper(sym)]; !ok {
			fail("decimals", sym+" decimals were not readable on-chain")
		} else if d != want {
			fail("decimals", fmt.Sprintf("%s has %d decimals on-chain, the API assumes %d", sym, d, want))
		}
	}
	// No hot key of ours is an admin: adminRelease/adminResolve reach every trade.
	for _, label := range slices.Sorted(maps.Keys(c.ForbiddenAdmins)) {
		addr := c.ForbiddenAdmins[label]
		if ok, err := boolView("isAdmin", common.HexToAddress(addr)); err != nil {
			fail("forbidden_admin", label+" unreadable: "+err.Error())
		} else if ok {
			fail("forbidden_admin", label+" "+addr+" is an escrow admin; only dispute arbiters may be")
		}
	}
	// systemWallet() is set: fees must have somewhere to go.
	if sw, err := addrView("systemWallet"); err != nil {
		fail("system_wallet", "unreadable: "+err.Error())
	} else if sw == (common.Address{}) {
		fail("system_wallet", "systemWallet() is the zero address")
	}
	// No forwarder: a forwarder can act as any party.
	if fw, err := addrView("trustedForwarder"); err != nil {
		fail("trusted_forwarder", "unreadable: "+err.Error())
	} else if fw != (common.Address{}) {
		fail("trusted_forwarder", "trustedForwarder() is "+fw.Hex()+", want zero")
	}
	// owner() is the preset's expected owner, and never a hot key of ours.
	if c.ExpectedOwner == "" {
		fail("owner", "the preset row names no expectedOwner")
	} else if o, err := addrView("owner"); err != nil {
		fail("owner", "unreadable: "+err.Error())
	} else if !strings.EqualFold(o.Hex(), c.ExpectedOwner) {
		fail("owner", fmt.Sprintf("owner() is %s, the preset expects %s", o.Hex(), c.ExpectedOwner))
	}
	for _, label := range slices.Sorted(maps.Keys(c.ForbiddenAdmins)) {
		if c.ExpectedOwner != "" && strings.EqualFold(c.ForbiddenAdmins[label], c.ExpectedOwner) {
			fail("owner", "the expected owner is "+label+", a hot key")
		}
	}
	// The RPC serves receipts: every confirm reads one.
	if c.Receipts == nil {
		fail("receipts", "no RPC")
	} else {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := ProbeReceipts(pctx, c.Receipts)
		cancel()
		if err != nil {
			fail("receipts", err.Error())
		}
	}
	return fails
}

// directGatesBootBudget bounds the direct gates at boot. Well inside the
// deploy's 60 s health gate, which also covers everything else boot does: a
// slow BSC RPC must leave the direct escrow dormant, not roll back the deploy.
const directGatesBootBudget = 20 * time.Second

// gateDirectAtBoot runs the direct gates against cc's live chain and keeps
// the escrow registered only if they all pass.
func (r *P2PEscrowRegistry) gateDirectAtBoot(bs *BlockchainService, cc config.P2PChainConfig, forbiddenAdmins map[string]string) {
	e := r.byChain[cc.ChainID]
	if e == nil {
		return
	}
	tokens := map[string]string{}
	for _, sym := range cc.Assets {
		tokens[sym] = bs.TokenAddressForChainID(cc.ChainID, sym)
	}
	gc := DirectEscrowGatesConfig{
		ChainID: cc.ChainID, ChainKey: cc.ChainKey, Escrow: cc.EscrowAddress, FeeManager: cc.FeeManager,
		ExpectedOwner: cc.ExpectedOwner, Tokens: tokens, Decimals: e.decimals,
		ForbiddenAdmins: forbiddenAdmins, Adapter: e.service,
	}
	// Assigned only when non-nil: a nil *ethclient.Client in the reader
	// interface would read as a client and panic on the first gate.
	if client, _ := bs.ClientForChainID(cc.ChainID); client != nil && client.Client != nil {
		gc.Chain, gc.Receipts = client.Client, client.Client.Client()
	}
	ctx, cancel := context.WithTimeout(context.Background(), directGatesBootBudget)
	defer cancel()
	log.Printf("[config] P2P direct escrow gates chain=%s(%d) escrow=%s checking admins=%v",
		cc.ChainKey, cc.ChainID, cc.EscrowAddress, slices.Sorted(maps.Keys(forbiddenAdmins)))
	r.applyDirectGates(cc.ChainID, cc.ChainKey, CheckDirectEscrowGates(ctx, gc))
}

// applyDirectGates keeps chainID's escrow only when fails is empty; on any
// failure it unregisters it (dormant: new activity and exits alike fail loud
// until fixed) with an ALERT per failed check.
func (r *P2PEscrowRegistry) applyDirectGates(chainID int64, chainKey string, fails []string) bool {
	if len(fails) == 0 {
		log.Printf("[config] P2P direct escrow gates OK chain=%s(%d)", chainKey, chainID)
		return true
	}
	for _, f := range fails {
		log.Printf("[config] ALERT p2p_direct_escrow_gate_failed chain=%s(%d) %s", chainKey, chainID, f)
	}
	delete(r.byChain, chainID)
	log.Printf("[config] ALERT p2p_direct_escrow_dormant chain=%s(%d) failures=%d — the direct escrow is NOT registered; fix it and restart",
		chainKey, chainID, len(fails))
	return false
}
