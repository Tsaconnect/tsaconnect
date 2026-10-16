package services

import (
	"context"
	"errors"
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

// errPaymentWindowUnread: the window could not be read (an RPC failure, not
// a wrong window).
var errPaymentWindowUnread = errors.New("read the payment window")

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
		return fmt.Errorf("%w: %w", errPaymentWindowUnread, err)
	}
	if w != want {
		return fmt.Errorf("escrow payment window %s, the API assumes %s", w, want)
	}
	return nil
}

// DirectGateFailure is one failed gate. Unread means the chain could not be
// read (RPC error, timeout, no client): the escrow's state is unknown, not
// wrong, so it must not cut live trades off from their exits.
type DirectGateFailure struct {
	Gate, Reason string
	Unread       bool
}

func (f DirectGateFailure) String() string { return f.Gate + ": " + f.Reason }

// CheckDirectEscrowGates runs every direct-escrow gate and returns the
// failures; none means the escrow may be registered.
func CheckDirectEscrowGates(ctx context.Context, c DirectEscrowGatesConfig) []DirectGateFailure {
	var fails []DirectGateFailure
	fail := func(gate, reason string) {
		fails = append(fails, DirectGateFailure{Gate: gate, Reason: RedactAlertText(reason)})
	}
	unread := func(gate, reason string) {
		fails = append(fails, DirectGateFailure{Gate: gate, Reason: RedactAlertText(reason), Unread: true})
	}
	if c.Chain == nil {
		unread("bytecode", "no chain client on "+c.ChainKey)
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
			unread("bytecode", a.label+" unreadable: "+err.Error())
		case len(code) == 0:
			fail("bytecode", a.label+" has no code at "+a.addr)
		}
	}
	// eth_chainId is the configured chain (the EIP-712 domain uses it).
	if id, err := c.Chain.ChainID(ctx); err != nil {
		unread("chain_id", "eth_chainId unreadable: "+err.Error())
	} else if id.Int64() != c.ChainID {
		fail("chain_id", fmt.Sprintf("RPC reports %s, the preset is %d", id, c.ChainID))
	}
	// paymentWindow() is the 30 minutes every direct trade is stamped with.
	if err := CheckPaymentWindow(c.Adapter, p2pPaymentWindow); errors.Is(err, errPaymentWindowUnread) {
		unread("payment_window", err.Error())
	} else if err != nil {
		fail("payment_window", err.Error())
	}
	// feeManager() is the preset's own.
	if c.FeeManager == "" {
		fail("fee_manager", "the preset row names no FeeManager")
	} else if fm, err := addrView("feeManager"); err != nil {
		unread("fee_manager", "unreadable: "+err.Error())
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
			unread("token_accepted", sym+" unreadable: "+err.Error())
		} else if !ok {
			fail("token_accepted", sym+" at "+addr+" is not accepted by the escrow")
		}
		want := tokenDecimalsFor(c.ChainID, sym)
		if d, ok := c.Decimals[strings.ToUpper(sym)]; !ok {
			unread("decimals", sym+" decimals were not readable on-chain")
		} else if d != want {
			fail("decimals", fmt.Sprintf("%s has %d decimals on-chain, the API assumes %d", sym, d, want))
		}
	}
	// No hot key of ours is an admin: adminRelease/adminResolve reach every trade.
	for _, label := range slices.Sorted(maps.Keys(c.ForbiddenAdmins)) {
		addr := c.ForbiddenAdmins[label]
		if ok, err := boolView("isAdmin", common.HexToAddress(addr)); err != nil {
			unread("forbidden_admin", label+" unreadable: "+err.Error())
		} else if ok {
			fail("forbidden_admin", label+" "+addr+" is an escrow admin; only dispute arbiters may be")
		}
	}
	// systemWallet() is set: fees must have somewhere to go.
	if sw, err := addrView("systemWallet"); err != nil {
		unread("system_wallet", "unreadable: "+err.Error())
	} else if sw == (common.Address{}) {
		fail("system_wallet", "systemWallet() is the zero address")
	}
	// No forwarder: a forwarder can act as any party.
	if fw, err := addrView("trustedForwarder"); err != nil {
		unread("trusted_forwarder", "unreadable: "+err.Error())
	} else if fw != (common.Address{}) {
		fail("trusted_forwarder", "trustedForwarder() is "+fw.Hex()+", want zero")
	}
	// owner() is the preset's expected owner, and never a hot key of ours.
	if c.ExpectedOwner == "" {
		fail("owner", "the preset row names no expectedOwner")
	} else if o, err := addrView("owner"); err != nil {
		unread("owner", "unreadable: "+err.Error())
	} else if !strings.EqualFold(o.Hex(), c.ExpectedOwner) {
		fail("owner", fmt.Sprintf("owner() is %s, the preset expects %s", o.Hex(), c.ExpectedOwner))
	}
	for _, label := range slices.Sorted(maps.Keys(c.ForbiddenAdmins)) {
		if c.ExpectedOwner != "" && strings.EqualFold(c.ForbiddenAdmins[label], c.ExpectedOwner) {
			fail("owner", "the expected owner is "+label+", a hot key")
		}
	}
	// The FeeManager has the same owner: its owner sets the rates and can
	// upgrade it. owner() has the same selector on both contracts.
	if c.FeeManager != "" && c.ExpectedOwner != "" {
		if vals, err := callP2PView(ctx, c.Chain, c.FeeManager, "owner"); err != nil {
			unread("fee_manager_owner", "unreadable: "+err.Error())
		} else if o, ok := vals[0].(common.Address); !ok || !strings.EqualFold(o.Hex(), c.ExpectedOwner) {
			fail("fee_manager_owner", fmt.Sprintf("FeeManager.owner() is %v, the preset expects %s", vals[0], c.ExpectedOwner))
		}
	}
	// The RPC serves receipts: every confirm reads one.
	if c.Receipts == nil {
		unread("receipts", "no RPC")
	} else {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := ProbeReceipts(pctx, c.Receipts)
		cancel()
		if err != nil {
			unread("receipts", err.Error())
		}
	}
	return fails
}

// directGatesBootBudget bounds the direct gates at boot. Well inside the
// deploy's 60 s health gate, which also covers everything else boot does: a
// slow BSC RPC must not roll back the deploy.
const directGatesBootBudget = 20 * time.Second

// directGatesRecheckEvery is how often an escrow whose gates could not be
// read is checked again.
const directGatesRecheckEvery = time.Minute

// gateDirectAtBoot runs the direct gates against cc's live chain. All pass:
// registered. A wrong value: unregistered (dormant). Only unreadable ones:
// kept registered for exits but closed to new activity, and re-checked in
// the background until they pass.
func (r *P2PEscrowRegistry) gateDirectAtBoot(bs *BlockchainService, cc config.P2PChainConfig, forbiddenAdmins map[string]string) {
	e := r.byChain[cc.ChainID]
	if e == nil {
		return
	}
	log.Printf("[config] P2P direct escrow gates chain=%s(%d) escrow=%s checking admins=%v",
		cc.ChainKey, cc.ChainID, cc.EscrowAddress, slices.Sorted(maps.Keys(forbiddenAdmins)))
	check := func(decimals map[string]int) []DirectGateFailure {
		gc := directGatesConfig(bs, cc, e, decimals, forbiddenAdmins)
		ctx, cancel := context.WithTimeout(context.Background(), directGatesBootBudget)
		defer cancel()
		return CheckDirectEscrowGates(ctx, gc)
	}
	if r.applyDirectGates(cc.ChainID, cc.ChainKey, check(e.decimals)) || !r.HasChain(cc.ChainID) {
		return
	}
	go func() {
		for range time.Tick(directGatesRecheckEvery) {
			// Decimals are read again too: the boot read may be what failed.
			decimals := e.decimals
			if client, _ := bs.ClientForChainID(cc.ChainID); client != nil {
				_, decimals = resolveAssetDecimals(cc.ChainID, cc.ChainKey, cc.Purpose, cc.Assets,
					func(sym string) string { return bs.TokenAddressForChainID(cc.ChainID, sym) }, client)
			}
			if r.recheckDirectGates(cc.ChainID, cc.ChainKey, check(decimals)) {
				return
			}
		}
	}()
}

func directGatesConfig(bs *BlockchainService, cc config.P2PChainConfig, e *chainEscrow, decimals map[string]int, forbiddenAdmins map[string]string) DirectEscrowGatesConfig {
	tokens := map[string]string{}
	for _, sym := range cc.Assets {
		tokens[sym] = bs.TokenAddressForChainID(cc.ChainID, sym)
	}
	gc := DirectEscrowGatesConfig{
		ChainID: cc.ChainID, ChainKey: cc.ChainKey, Escrow: cc.EscrowAddress, FeeManager: cc.FeeManager,
		ExpectedOwner: cc.ExpectedOwner, Tokens: tokens, Decimals: decimals,
		ForbiddenAdmins: forbiddenAdmins, Adapter: e.service,
	}
	// Assigned only when non-nil: a nil *ethclient.Client in the reader
	// interface would read as a client and panic on the first gate.
	if client, _ := bs.ClientForChainID(cc.ChainID); client != nil && client.Client != nil {
		gc.Chain, gc.Receipts = client.Client, client.Client.Client()
	}
	return gc
}

func logGateFailures(chainKey string, chainID int64, fails []DirectGateFailure) (wrong int) {
	for _, f := range fails {
		kind := "wrong"
		if f.Unread {
			kind = "unread"
		} else {
			wrong++
		}
		log.Printf("[config] ALERT p2p_direct_escrow_gate_failed chain=%s(%d) kind=%s %s", chainKey, chainID, kind, f)
	}
	return wrong
}

// applyDirectGates applies the boot gates' verdict and reports whether the
// escrow is registered and open. Any wrong value unregisters it (dormant: new
// activity and exits alike fail loud until fixed and restarted). Only unread
// gates keep it registered but unverified: exits work, new activity is
// refused until a re-check passes.
func (r *P2PEscrowRegistry) applyDirectGates(chainID int64, chainKey string, fails []DirectGateFailure) bool {
	if len(fails) == 0 {
		log.Printf("[config] P2P direct escrow gates OK chain=%s(%d)", chainKey, chainID)
		return true
	}
	if logGateFailures(chainKey, chainID, fails) > 0 {
		delete(r.byChain, chainID)
		log.Printf("[config] ALERT p2p_direct_escrow_dormant chain=%s(%d) failures=%d — the direct escrow is NOT registered; fix it and restart",
			chainKey, chainID, len(fails))
		return false
	}
	r.byChain[chainID].unverified.Store(true)
	log.Printf("[config] ALERT p2p_direct_escrow_unverified chain=%s(%d) failures=%d — exits only until the gates can be read; re-checking every %s",
		chainKey, chainID, len(fails), directGatesRecheckEvery)
	return false
}

// recheckDirectGates is the background re-check of an unverified escrow. It
// opens the escrow only when every gate passes; a wrong value found now keeps
// it exits-only (the registry is not changed after boot) and ALERTs each time.
// Reports whether re-checking is done.
func (r *P2PEscrowRegistry) recheckDirectGates(chainID int64, chainKey string, fails []DirectGateFailure) bool {
	if len(fails) == 0 {
		r.byChain[chainID].unverified.Store(false)
		log.Printf("[config] P2P direct escrow gates OK on re-check chain=%s(%d) — open to new activity", chainKey, chainID)
		return true
	}
	if logGateFailures(chainKey, chainID, fails) > 0 {
		log.Printf("[config] ALERT p2p_direct_escrow_wrong_on_recheck chain=%s(%d) — staying exits-only; fix it and restart", chainKey, chainID)
	}
	return false
}
