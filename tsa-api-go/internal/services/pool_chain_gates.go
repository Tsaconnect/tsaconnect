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
	SuggestGasPrice(ctx context.Context) (*big.Int, error)
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
	// Vault set: the shared pool (spec 2026-10-14). Its gates replace the
	// escrow's (fee invariant, payment window, escrow admin, company PM).
	Vault string
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
	ReleaseGasOK(ctx context.Context, releases int, fresh bool) bool
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

	gasMu   sync.Mutex
	gasRead releaseGasReading
}

// releaseGasReading is the release key's balance and the gas price, as last
// read for ReleaseGasOK.
type releaseGasReading struct {
	balance, price *big.Int
	at             time.Time
}

const (
	// releaseGasFresh: a quote or open within this of the last reading reuses
	// it rather than spending two RPCs (single-flight: gasMu is held across
	// the read).
	releaseGasFresh = 5 * time.Second
	// releaseGasStale: a failed read (a 429) falls back to a reading this
	// young rather than closing the leg.
	releaseGasStale = 30 * time.Second
)

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
	if g.c.Vault != "" {
		g.vaultGates(ctx, fail)
		g.commonGates(ctx, fail)
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
	g.commonGates(ctx, fail)
	// 7. IT_PM_ID is an active PM (the slice buyer identity, S10).
	if g.c.CompanyPM == nil {
		fail("company_pm", "no check wired")
	} else if err := g.c.CompanyPM(ctx); err != nil {
		fail("company_pm", err.Error())
	}
	return g.record(fails)
}

// commonGates are the gates every pool kind shares: token decimals read
// on-chain (S6, S7) and receipts (S38).
func (g *PoolChainGates) commonGates(ctx context.Context, fail func(gate, reason string)) {
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
}

// vaultGates: the vault has code, holds the pool token, the release key is its
// operator, the treasury may receive fills, it is not paused, and the
// treasury lane holds the token it will deliver.
func (g *PoolChainGates) vaultGates(ctx context.Context, fail func(gate, reason string)) {
	vault := common.HexToAddress(g.c.Vault)
	code, err := g.c.Chain.CodeAt(ctx, vault, nil)
	switch {
	case err != nil:
		fail("bytecode", "vault unreadable: "+err.Error())
		return
	case len(code) == 0:
		fail("bytecode", "vault has no code at "+g.c.Vault)
		return
	}
	read := func(method string, args ...any) (any, bool) {
		data, err := parsedLiquidityVaultABI.Pack(method, args...)
		if err != nil {
			fail("vault", method+": "+err.Error())
			return nil, false
		}
		raw, err := g.c.Chain.CallContract(ctx, ethereum.CallMsg{To: &vault, Data: data}, nil)
		if err != nil {
			fail("vault", method+" unreadable: "+err.Error())
			return nil, false
		}
		out, err := parsedLiquidityVaultABI.Unpack(method, raw)
		if err != nil || len(out) == 0 {
			fail("vault", method+" undecodable")
			return nil, false
		}
		return out[0], true
	}
	for _, sym := range slices.Sorted(maps.Keys(g.c.Tokens)) {
		if v, ok := read("token"); ok && !strings.EqualFold(v.(common.Address).Hex(), g.c.Tokens[sym]) {
			fail("vault", fmt.Sprintf("token() is %s, not the pool's %s %s", v.(common.Address).Hex(), sym, g.c.Tokens[sym]))
		}
	}
	if g.c.ReleaseAdmin == "" {
		fail("release_admin", "no release key (PM_RELEASE_PRIVATE_KEY)")
	} else if v, ok := read("operator"); ok && !strings.EqualFold(v.(common.Address).Hex(), g.c.ReleaseAdmin) {
		fail("release_admin", fmt.Sprintf("the vault's operator is %s, not the release key %s", v.(common.Address).Hex(), g.c.ReleaseAdmin))
	}
	l := g.c.TreasuryLane
	if l == nil || l.SponsorLane == nil || l.Signer == nil {
		fail("treasury_lane", "no conversion-treasury lane on "+g.c.ChainKey)
	} else {
		if v, ok := read("recipient", common.HexToAddress(l.Signer.Address())); ok && !v.(bool) {
			fail("vault", "the conversion treasury "+l.Signer.Address()+" is not an approved fill recipient")
		}
		for _, sym := range slices.Sorted(maps.Keys(g.c.Tokens)) {
			if !strings.EqualFold(l.Tokens[sym], g.c.Tokens[sym]) {
				fail("treasury_lane", "the lane does not hold "+sym+" at "+g.c.Tokens[sym])
			}
		}
	}
	if v, ok := read("paused"); ok && v.(bool) {
		fail("vault", "the vault is paused")
	}
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

// poolReleaseCallGas budgets one adminRelease of a slice from the release key:
// the same allowance the treasury makes for one escrow call.
const poolReleaseCallGas = treasuryEscrowCallGas

// ReleaseGasOK: paid orders must always be able to release, so a pool open is
// refused unless the release key can pay for `releases` release-key calls — the
// caller passes every slice still owed one on this chain plus the new order's
// (ITService.poolReleaseGasOK) — at
// today's gas price (times gasFeeBuffer). The release floor is alert-only
// (owner 2026-10-13): below it this logs the refill ALERT and the gas float
// monitor keeps alerting, but the pool stays open while the key can pay.
//
// fresh (an open) reads the balance and gas price now, never a cached or stale
// reading; a quote may reuse one.
func (g *PoolChainGates) ReleaseGasOK(ctx context.Context, releases int, fresh bool) bool {
	r, ok := g.releaseReading(ctx, fresh)
	if !ok {
		return false
	}
	if g.c.ReleaseGasFloorWei != nil && r.balance.Cmp(g.c.ReleaseGasFloorWei) < 0 {
		log.Printf("[liquidity] ALERT liquidity_pool_release_below_floor_total=1 chain=%s address=%s balance_wei=%s floor_wei=%s — refill; opens continue while the key can pay its releases",
			g.c.ChainKey, g.c.ReleaseAdmin, r.balance, g.c.ReleaseGasFloorWei)
	}
	if releases < 1 {
		releases = 1
	}
	need := feeNeed(poolReleaseCallGas*uint64(releases), r.price, nil)
	if r.balance.Cmp(need) < 0 {
		log.Printf("[liquidity] ALERT liquidity_pool_release_gas_short_total=1 chain=%s address=%s balance_wei=%s needed_wei=%s releases=%d",
			g.c.ChainKey, g.c.ReleaseAdmin, r.balance, need, releases)
		return false
	}
	return true
}

// releaseReading is the release key's balance and gas price, reused for
// releaseGasFresh and, when a read fails, for releaseGasStale.
func (g *PoolChainGates) releaseReading(ctx context.Context, fresh bool) (releaseGasReading, bool) {
	if !g.Enabled() || g.c.Chain == nil {
		return releaseGasReading{}, false
	}
	g.gasMu.Lock()
	defer g.gasMu.Unlock()
	now := time.Now()
	last := g.gasRead
	if fresh {
		last = releaseGasReading{} // neither reused nor a fallback
	}
	if last.balance != nil && now.Sub(last.at) < releaseGasFresh {
		return last, true
	}
	fl, err := g.FloatBalances(ctx)
	var price *big.Int
	if err == nil {
		price, err = g.c.Chain.SuggestGasPrice(ctx)
	}
	if err != nil || len(fl) != 1 || fl[0].BalanceWei == nil || price == nil {
		if last.balance != nil && now.Sub(last.at) < releaseGasStale {
			return last, true
		}
		log.Printf("[liquidity] release gas reading on %s unavailable: %s", g.c.ChainKey, RedactAlertText(fmt.Sprint(err)))
		return releaseGasReading{}, false
	}
	g.gasRead = releaseGasReading{balance: fl[0].BalanceWei, price: price, at: now}
	return g.gasRead, true
}
