package services

import (
	"context"
	"fmt"
	"log"
	"maps"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// PoolChainReader is the pool chain's read surface; *ethclient.Client is one.
type PoolChainReader interface {
	CallContract(ctx context.Context, call ethereum.CallMsg, blockNumber *big.Int) ([]byte, error)
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
	BalanceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (*big.Int, error)
}

// PoolChainGatesConfig is everything the TSA Liquidity boot gates read
// (spec 2026-10-08 §6.2).
type PoolChainGatesConfig struct {
	ChainID             int64
	ChainKey, Network   string
	Escrow, FeeManager  string
	Tokens              map[string]string // pool token symbol → address
	Decimals            map[string]int    // as the registry read them on-chain
	ReleaseAdmin        string            // the release signer's address; "" = no release key
	ReleaseGasFloorWei  *big.Int
	Chain               PoolChainReader
	Receipts            RawRPCCaller
	TreasuryLane        *TreasuryLane // the conversion treasury's lane on this chain
	CompanyPM           func(ctx context.Context) error
	MinPaymentWindowSec uint64
}

type PoolGateStatus struct {
	Open      bool      `json:"open"`
	Failures  []string  `json:"failures,omitempty"`
	Latched   string    `json:"latched,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// PoolOpener is the one question LP onboarding and the LP route ask of the
// pool (plan B P14): is it open? A nil PoolOpener reads closed.
type PoolOpener interface{ Open() bool }

// PoolGate is the pool's gate surface as boot hands it out (Plans B/C1 hold
// it). Boot returns a nil PoolGate, never a typed-nil *PoolChainGates, when no
// pool row exists; *PoolChainGates is nil-receiver safe and reads closed.
type PoolGate interface {
	Open() bool
	Status() PoolGateStatus
	Check(ctx context.Context) PoolGateStatus
	Close(reason string)
	ReleaseGasOK(ctx context.Context) bool
	ReprobeReceipts(ctx context.Context)
	Enabled() bool
	FloatBalances(ctx context.Context) ([]SponsorFloat, error)
}

var _ PoolGate = (*PoolChainGates)(nil)

// errNoPoolGates is what a nil *PoolChainGates reports: closed.
const errNoPoolGates = "pool: no gates configured"

// PoolChainGates keeps the pool closed unless every gate passes (fail-closed;
// decision 17: the configuration is the switch, these are the money guards).
type PoolChainGates struct {
	c  PoolChainGatesConfig
	mu sync.Mutex
	st PoolGateStatus
}

func NewPoolChainGates(c PoolChainGatesConfig) *PoolChainGates { return &PoolChainGates{c: c} }

func (g *PoolChainGates) Open() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.st.Open && g.st.Latched == ""
}

func (g *PoolChainGates) Status() PoolGateStatus {
	if g == nil {
		return PoolGateStatus{Failures: []string{errNoPoolGates}}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.st
	st.Failures = append([]string(nil), g.st.Failures...)
	return st
}

// Close shuts the pool in-process until a restart re-runs the gates (spec
// §12.2 step 4: a non-zero fee snapshot on a slice).
func (g *PoolChainGates) Close(reason string) {
	if g == nil {
		return // nothing to close: a nil gate is never open
	}
	if reason == "" {
		reason = "closed without a reason" // an empty latch would read as no latch
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.st.Latched = reason
	log.Printf("[liquidity] ALERT liquidity_pool_closed reason=%q — closed until a restart re-checks the gates", RedactAlertText(reason))
}

// ReprobeReceipts is the 5-minute re-check (jobs.NewReceiptProbe): every gate,
// receipts included (S38).
func (g *PoolChainGates) ReprobeReceipts(ctx context.Context) { g.Check(ctx) }

// Check runs §6.2 gates 1–7 plus the payment-window floor (plan P13).
func (g *PoolChainGates) Check(ctx context.Context) PoolGateStatus {
	if g == nil {
		return g.Status()
	}
	var fails []string
	fail := func(gate, reason string) { fails = append(fails, gate+": "+RedactAlertText(reason)) }

	// Every gate below reads the chain, so without a client the pool is closed
	// now. The client, the release signer's lane and the token decimals are
	// all dialled once at boot, so this stays closed until a restart: opening
	// on a later client would take orders the release key cannot send on.
	if g.c.Chain == nil {
		fail("bytecode", "no chain client on "+g.c.ChainKey+" (fix the RPC, then restart)")
		return g.record(fails)
	}
	// 1. Bytecode.
	for _, c := range []struct{ label, addr string }{{"escrow", g.c.Escrow}, {"fee_manager", g.c.FeeManager}} {
		code, err := g.c.Chain.CodeAt(ctx, common.HexToAddress(c.addr), nil)
		switch {
		case err != nil:
			fail("bytecode", c.label+" unreadable: "+err.Error())
		case len(code) == 0:
			fail("bytecode", c.label+" has no code at "+c.addr)
		}
	}
	// 2. Fee invariant (S2, M7).
	if reason := g.feeInvariant(ctx); reason != "" {
		fail("fee_invariant", reason)
		log.Printf("[liquidity] ALERT liquidity_pool_fee_invariant_broken chain=%s reason=%q", g.c.ChainKey, RedactAlertText(reason))
	}
	// Payment window ≥ honour window + grace + release margin (S3, M4).
	if vals, err := callP2PView(ctx, g.c.Chain, g.c.Escrow, "paymentWindow"); err != nil {
		fail("payment_window", "unreadable: "+err.Error())
	} else if w, err := paymentWindowOf(vals); err != nil {
		fail("payment_window", err.Error())
	} else if w < time.Duration(g.c.MinPaymentWindowSec)*time.Second {
		fail("payment_window", fmt.Sprintf("paymentWindow() is %s, below %d s", w, g.c.MinPaymentWindowSec))
	}
	// 3. The release EOA is an escrow admin.
	if g.c.ReleaseAdmin == "" {
		fail("release_admin", "no release key (PM_RELEASE_PRIVATE_KEY)")
	} else if vals, err := callP2PView(ctx, g.c.Chain, g.c.Escrow, "isAdmin", common.HexToAddress(g.c.ReleaseAdmin)); err != nil {
		fail("release_admin", "unreadable: "+err.Error())
	} else if ok, _ := vals[0].(bool); !ok {
		fail("release_admin", g.c.ReleaseAdmin+" is not an escrow admin")
	}
	// 4. The conversion treasury's lane (S21).
	if reason := g.treasuryLane(); reason != "" {
		fail("treasury_lane", reason)
	}
	// 5. Decimals ≥ 6, read on-chain (S6, S7).
	for _, sym := range slices.Sorted(maps.Keys(g.c.Tokens)) {
		if d, ok := g.c.Decimals[strings.ToUpper(sym)]; !ok {
			fail("decimals", sym+" decimals were not readable on-chain")
		} else if d < StableAssetDecimals {
			fail("decimals", fmt.Sprintf("%s has %d decimals, below %d", sym, d, StableAssetDecimals))
		}
	}
	// 6. Receipts (S38).
	if g.c.Receipts == nil {
		fail("receipts", "no RPC")
	} else {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := ProbeReceipts(pctx, g.c.Receipts)
		cancel()
		if err != nil {
			fail("receipts", err.Error())
			log.Printf("[liquidity] ALERT liquidity_pool_receipts_unreadable chain=%s err=%s", g.c.ChainKey, RedactAlertText(err.Error()))
		}
	}
	// 7. IT_PM_ID is an active PM (the slice buyer identity, S10).
	if g.c.CompanyPM == nil {
		fail("company_pm", "no check wired")
	} else if err := g.c.CompanyPM(ctx); err != nil {
		fail("company_pm", err.Error())
	}
	return g.record(fails)
}

// record stores one Check's outcome and alerts each failure.
func (g *PoolChainGates) record(fails []string) PoolGateStatus {
	g.mu.Lock()
	// A latch (Close) keeps the pool closed in every view until a restart (review I4).
	g.st.Open, g.st.Failures, g.st.CheckedAt = len(fails) == 0 && g.st.Latched == "", fails, time.Now()
	st := g.st
	g.mu.Unlock()
	for _, f := range fails {
		log.Printf("[liquidity] ALERT liquidity_pool_gate_failed chain=%s(%d) %s", g.c.ChainKey, g.c.ChainID, f)
	}
	return st
}

func (g *PoolChainGates) feeInvariant(ctx context.Context) string {
	if len(g.c.Tokens) == 0 {
		return "no pool tokens configured, so the zero fee cannot be proven"
	}
	for _, sym := range slices.Sorted(maps.Keys(g.c.Tokens)) {
		for _, feature := range []string{"p2p_platform", "p2p"} {
			row, err := readEscrowFeeRow(ctx, g.c.Chain, g.c.Escrow, g.c.Tokens[sym], feature)
			switch {
			case err != nil:
				return fmt.Sprintf("%s/%s unreadable: %s", feature, sym, err.Error())
			case row.noFeeManager || !strings.EqualFold(row.feeManager.Hex(), g.c.FeeManager):
				return fmt.Sprintf("escrow.feeManager() is %s, not the preset %s", row.feeManager.Hex(), g.c.FeeManager)
			case !row.active || row.bps != 0:
				return fmt.Sprintf("%s/%s is (bps %d, active %t); the pool needs (0, active)", feature, row.symbol, row.bps, row.active)
			}
		}
	}
	vals, err := callP2PView(ctx, g.c.Chain, g.c.Escrow, "feeBps")
	if err != nil {
		return "feeBps() unreadable: " + err.Error()
	}
	if bps, _ := vals[0].(uint16); bps != 0 {
		return fmt.Sprintf("legacy feeBps() is %d, want 0", bps)
	}
	return ""
}

func (g *PoolChainGates) treasuryLane() string {
	l := g.c.TreasuryLane
	if l == nil || l.SponsorLane == nil {
		return "no conversion-treasury lane on " + g.c.ChainKey
	}
	escrow := common.HexToAddress(g.c.Escrow)
	for _, r := range l.Routers {
		if r == escrow {
			return "the pool escrow is on the lane's LiFi router allowlist"
		}
	}
	allowed := false
	for _, a := range l.EscrowCalls {
		allowed = allowed || a == escrow
	}
	if !allowed {
		return "the lane's EscrowCalls does not list the pool escrow"
	}
	for _, sym := range slices.Sorted(maps.Keys(g.c.Tokens)) {
		if !strings.EqualFold(l.Tokens[sym], g.c.Tokens[sym]) {
			return "the lane does not hold " + sym + " at " + g.c.Tokens[sym]
		}
	}
	return ""
}

// Enabled / FloatBalances make the gates the GasFloatMonitor reader for the
// release EOA on the pool chain (spec §4.4, F11).
func (g *PoolChainGates) Enabled() bool {
	return g != nil && g.c.ReleaseAdmin != "" && g.c.ReleaseGasFloorWei != nil
}

func (g *PoolChainGates) FloatBalances(ctx context.Context) ([]SponsorFloat, error) {
	if !g.Enabled() || g.c.Chain == nil {
		chain := ""
		if g != nil {
			chain = g.c.ChainKey
		}
		return nil, fmt.Errorf("no release EOA, gas floor or chain client on %s", chain)
	}
	bal, err := g.c.Chain.BalanceAt(ctx, common.HexToAddress(g.c.ReleaseAdmin), nil)
	if err != nil {
		// Redacted: a transport error carries the RPC URL, which can hold a key.
		return nil, fmt.Errorf("release EOA balance on %s: %s", g.c.ChainKey, RedactAlertText(err.Error()))
	}
	return []SponsorFloat{{Chain: g.c.ChainKey, Address: g.c.ReleaseAdmin, BalanceWei: bal, FloorWei: g.c.ReleaseGasFloorWei}}, nil
}

// ReleaseGasOK: paid orders must always be able to release, so a pool open is
// refused below the floor (Plan C1 calls this).
func (g *PoolChainGates) ReleaseGasOK(ctx context.Context) bool {
	fl, err := g.FloatBalances(ctx)
	return err == nil && len(fl) == 1 && fl[0].BalanceWei != nil && fl[0].BalanceWei.Cmp(fl[0].FloorWei) >= 0
}
