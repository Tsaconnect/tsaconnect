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
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ojimcy/tsa-api-go/internal/config"
)

// core_contracts.go — the core contract set (FeeConfig, OrderEscrow, P2PTradeEscrow) as the API
// uses it: slice 3 of docs/plans/2026-10-27-contracts-v2-migration.md. The registry checks each
// chain's set at boot and only then calls it ready; the FeeConfig reader and the fee-wallet card
// read through it. Nothing charges or escrows on it yet (slices 5–8).

// coreViewABI holds the views the boot gates read; the escrows share EscrowBase's, FeeConfig
// shares the access-control ones.
const coreViewABIJSON = `[
  {"type":"function","name":"feeConfig","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"defaultAdmin","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"pendingDefaultAdmin","stateMutability":"view","inputs":[],
   "outputs":[{"name":"newAdmin","type":"address"},{"name":"schedule","type":"uint48"}]},
  {"type":"function","name":"hasRole","stateMutability":"view",
   "inputs":[{"name":"role","type":"bytes32"},{"name":"account","type":"address"}],"outputs":[{"name":"","type":"bool"}]},
  {"type":"function","name":"trustedForwarder","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"address"}]},
  {"type":"function","name":"paymentWindow","stateMutability":"pure","inputs":[],"outputs":[{"name":"","type":"uint40"}]}
]`

var parsedCoreViewABI = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(coreViewABIJSON))
	if err != nil {
		panic(fmt.Sprintf("core contracts: parse ABI: %v", err))
	}
	return parsed
}()

// Every role a core contract grants. A hot key of ours may hold none of them: the admin and the
// resolvers move user funds, and an upgrader replaces the code.
var (
	coreEscrowRoles = map[string][32]byte{
		"DEFAULT_ADMIN": {}, "UPGRADER": roleHash("UPGRADER_ROLE"), "PAUSER": roleHash("PAUSER_ROLE"),
		"RESCUER": roleHash("RESCUER_ROLE"), "RESOLVER": roleHash("RESOLVER_ROLE"),
	}
	coreFeeConfigRoles = map[string][32]byte{
		"DEFAULT_ADMIN": {}, "UPGRADER": roleHash("UPGRADER_ROLE"), "CONFIG": roleHash("CONFIG_ROLE"),
	}
)

func roleHash(name string) [32]byte { return crypto.Keccak256Hash([]byte(name)) }

// CoreGatesConfig is everything the core-set gates read for one chain.
type CoreGatesConfig struct {
	Set config.CoreContracts
	// HotKeys are the signing keys the API holds (label → address): none may hold a role.
	HotKeys  map[string]string
	Chain    DirectChainReader
	Receipts RawRPCCaller
	Fees     *FeeConfigReader
}

// CheckCoreGates runs every core-set gate for one chain and returns the failures; none means the
// set may be used. Unread is "could not ask", not "wrong".
func CheckCoreGates(ctx context.Context, c CoreGatesConfig) []DirectGateFailure {
	g := &gateRun{}
	if c.Chain == nil {
		g.unread("bytecode", "no chain client on "+c.Set.ChainKey)
		return g.fails
	}
	view := func(addr, method string) ([]any, error) {
		return callABIView(ctx, parsedCoreViewABI, c.Chain, addr, method)
	}
	addrView := func(addr, method string) (common.Address, error) {
		return firstAddress(method)(view(addr, method))
	}
	hotKeys := distinctHotKeys(c.HotKeys)

	type contract struct {
		label, addr string
		roles       map[string][32]byte
		escrow      bool
	}
	contracts := []contract{{"FeeConfig", c.Set.FeeConfig, coreFeeConfigRoles, false},
		{"P2PTradeEscrow", c.Set.P2PEscrow, coreEscrowRoles, true}}
	if c.Set.OrderEscrow != "" {
		contracts = append(contracts, contract{"OrderEscrow", c.Set.OrderEscrow, coreEscrowRoles, true})
	}

	g.chainID(ctx, c.Chain, c.Set.ChainID)
	for _, k := range contracts {
		if !g.code(ctx, c.Chain, k.label, k.addr) {
			continue
		}
		if a, err := addrView(k.addr, "defaultAdmin"); err != nil {
			g.unread("admin", k.label+" unreadable: "+err.Error())
		} else if !strings.EqualFold(a.Hex(), c.Set.Admin) {
			g.fail("admin", fmt.Sprintf("%s.defaultAdmin() is %s, the set expects %s", k.label, a.Hex(), c.Set.Admin))
		}
		// A handover in progress is only visible here until it is accepted.
		if vals, err := view(k.addr, "pendingDefaultAdmin"); err != nil {
			g.unread("admin", k.label+" pending admin unreadable: "+err.Error())
		} else if p, _ := vals[0].(common.Address); p != (common.Address{}) {
			if label, hot := hotKeys[p]; hot {
				g.fail("admin", fmt.Sprintf("%s admin handover pending to %s %s, a hot key", k.label, label, p.Hex()))
			} else {
				log.Printf("[config] core contracts %s admin handover pending to %s (accepting it moves every role)", k.label, p.Hex())
			}
		}
		checkHotKeyRoles(ctx, g, c.Chain, k.label, k.addr, k.roles, hotKeys)
		if !k.escrow {
			continue
		}
		// The escrow reads the set's own FeeConfig (an immutable: a wrong one needs a new implementation).
		if fc, err := addrView(k.addr, "feeConfig"); err != nil {
			g.unread("fee_config", k.label+" unreadable: "+err.Error())
		} else if !strings.EqualFold(fc.Hex(), c.Set.FeeConfig) {
			g.fail("fee_config", fmt.Sprintf("%s.feeConfig() is %s, not the set's %s", k.label, fc.Hex(), c.Set.FeeConfig))
		}
		// The forwarder is the one the set names (or none): it relays calls as their signer, so an
		// unknown one could act as any user.
		if fw, err := addrView(k.addr, "trustedForwarder"); err != nil {
			g.unread("trusted_forwarder", k.label+" unreadable: "+err.Error())
		} else if want := common.HexToAddress(c.Set.Forwarder); fw != want {
			g.fail("trusted_forwarder", fmt.Sprintf("%s.trustedForwarder() is %s, the set expects %s", k.label, fw.Hex(), want.Hex()))
		}
	}
	if label, hot := hotKeys[common.HexToAddress(c.Set.Admin)]; hot {
		g.fail("admin", "the set's admin is "+label+", a hot key")
	}
	// paymentWindow() is the 30 minutes every trade is stamped with.
	if vals, err := view(c.Set.P2PEscrow, "paymentWindow"); err != nil {
		g.unread("payment_window", "unreadable: "+err.Error())
	} else if w, ok := vals[0].(*big.Int); !ok || time.Duration(w.Int64())*time.Second != p2pPaymentWindow {
		g.fail("payment_window", fmt.Sprintf("paymentWindow() is %v s, the API assumes %s", vals[0], p2pPaymentWindow))
	}
	// Fees have somewhere to go.
	if r, err := c.Fees.FeeRecipient(ctx, c.Set.ChainID, c.Set.FeeConfig); err != nil {
		g.unread("fee_recipient", "unreadable: "+err.Error())
	} else if r == (common.Address{}) {
		g.fail("fee_recipient", "feeRecipient() is the zero address")
	}
	// The RPC serves receipts: every confirm reads one.
	g.receipts(ctx, c.Receipts)
	return g.fails
}

// distinctHotKeys keys the hot keys by address (one address can be listed under two labels, e.g.
// the configured operator and its known production address), keeping the first label by name.
func distinctHotKeys(byLabel map[string]string) map[common.Address]string {
	out := map[common.Address]string{}
	for _, label := range slices.Sorted(maps.Keys(byLabel)) {
		a := common.HexToAddress(byLabel[label])
		if _, seen := out[a]; !seen {
			out[a] = label
		}
	}
	return out
}

// checkHotKeyRoles reads every (hot key, role) pair on one contract side by side and records any
// role a hot key holds. Failures are added in a fixed order.
func checkHotKeyRoles(ctx context.Context, g *gateRun, chain contractCaller, label, addr string,
	roles map[string][32]byte, hotKeys map[common.Address]string) {
	type pair struct {
		key  common.Address
		role string
	}
	var pairs []pair
	for _, key := range slices.SortedFunc(maps.Keys(hotKeys), func(a, b common.Address) int { return strings.Compare(a.Hex(), b.Hex()) }) {
		for _, role := range slices.Sorted(maps.Keys(roles)) {
			pairs = append(pairs, pair{key, role})
		}
	}
	held := make([]bool, len(pairs))
	errs := make([]error, len(pairs))
	var wg sync.WaitGroup
	for i, p := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			vals, err := callABIView(ctx, parsedCoreViewABI, chain, addr, "hasRole", roles[p.role], p.key)
			if err == nil {
				held[i], _ = vals[0].(bool)
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, p := range pairs {
		switch {
		case errs[i] != nil:
			g.unread("hot_key_role", fmt.Sprintf("%s %s on %s unreadable: %v", hotKeys[p.key], p.role, label, errs[i]))
		case held[i]:
			g.fail("hot_key_role", fmt.Sprintf("%s %s holds %s on %s", hotKeys[p.key], p.key.Hex(), p.role, label))
		}
	}
}

// coreChain is one chain's set and its gate state. Guarded by CoreRegistry.mu.
type coreChain struct {
	set       config.CoreContracts
	gates     CoreGatesConfig
	ready     bool
	checkedAt time.Time
	lastFails string // the last pass's failures, so an unchanged verdict is not logged every tick
}

// CoreRegistry is the selected core set, one entry per chain. A nil registry is a dormant set.
type CoreRegistry struct {
	setName string
	fees    *FeeConfigReader
	mu      sync.Mutex
	chains  map[int64]*coreChain
	now     func() time.Time
}

// coreGatesReadyRecheck is how often a ready chain is gated again; a chain that is not ready is
// gated on every tick (CoreGatesInterval), so a fix on-chain takes effect without a restart.
const coreGatesReadyRecheck = 10 * time.Minute

// CoreGatesInterval is the gate worker's tick.
const CoreGatesInterval = time.Minute

// NewCoreRegistry builds the registry for cfg's selected set over bs's chain clients. It returns
// nil when the set is dormant. hotKeys are the signing keys the API holds (label → address).
func NewCoreRegistry(cfg *config.Config, bs *BlockchainService, hotKeys map[string]string) *CoreRegistry {
	if len(cfg.CoreContracts) == 0 {
		return nil
	}
	rpcByChain := map[int64]FeeManagerRPCClient{}
	r := &CoreRegistry{setName: cfg.CoreSet, chains: map[int64]*coreChain{}, now: time.Now}
	for chainID, set := range cfg.CoreContracts {
		cc := &coreChain{set: set, gates: CoreGatesConfig{Set: set, HotKeys: hotKeys}}
		// Assigned only when non-nil: a nil *ethclient.Client in the interface would panic.
		if client, _ := bs.ClientForChainID(chainID); client != nil && client.Client != nil {
			cc.gates.Chain, cc.gates.Receipts = client.Client, client.Client.Client()
			rpcByChain[chainID] = client.Client
		}
		r.chains[chainID] = cc
	}
	r.fees = NewFeeConfigReader(rpcByChain)
	for _, cc := range r.chains {
		cc.gates.Fees = r.fees
	}
	return r
}

// ErrCoreSetNotReady is returned by Gate while a chain's set has not passed its gates.
var ErrCoreSetNotReady = errors.New("core contract set not ready")

// Gate is the gate worker's tick. A wrong value takes a chain out of service; a pass puts it in.
// A gate that only could not be read leaves the chain as it was: an RPC blip neither opens a set
// that never passed nor closes one that did. It returns ErrCoreSetNotReady while any chain is not
// ready, so the scheduler's failure-streak alert pages on a set that stays shut.
func (r *CoreRegistry) Gate(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var notReady []string
	for _, chainID := range slices.Sorted(maps.Keys(r.chains)) {
		r.mu.Lock()
		cc := r.chains[chainID]
		due := !cc.ready || r.now().Sub(cc.checkedAt) >= coreGatesReadyRecheck
		gates, wasReady := cc.gates, cc.ready
		r.mu.Unlock()
		if due {
			fails := CheckCoreGates(ctx, gates)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.apply(cc, wasReady, fails)
		}
		r.mu.Lock()
		if !cc.ready {
			notReady = append(notReady, fmt.Sprintf("%s(%d)", cc.set.ChainKey, chainID))
		}
		r.mu.Unlock()
	}
	if len(notReady) > 0 {
		return fmt.Errorf("%w: set=%s chains=%v", ErrCoreSetNotReady, r.setName, notReady)
	}
	return nil
}

func (r *CoreRegistry) apply(cc *coreChain, wasReady bool, fails []DirectGateFailure) {
	wrong := 0
	for _, f := range fails {
		if !f.Unread {
			wrong++
		}
	}
	ready := wrong == 0 && (len(fails) == 0 || wasReady)
	r.mu.Lock()
	cc.ready, cc.checkedAt = ready, r.now()
	verdict := fmt.Sprint(fails)
	changed := verdict != cc.lastFails || ready != wasReady
	cc.lastFails = verdict
	r.mu.Unlock()
	if !changed {
		return
	}
	chain := fmt.Sprintf("set=%s chain=%s(%d)", r.setName, cc.set.ChainKey, cc.set.ChainID)
	switch {
	case len(fails) == 0:
		log.Printf("[config] core contracts gates OK %s — ready", chain)
		return
	case ready:
		log.Printf("[config] WARNING core_contracts_gates_unread %s failures=%d — staying ready until a wrong value is read", chain, len(fails))
	case wasReady:
		log.Printf("[config] ALERT core_contracts_out_of_service %s failures=%d — a gate that passed now fails", chain, len(fails))
	default:
		log.Printf("[config] ALERT core_contracts_not_ready %s failures=%d — re-checking every %s", chain, len(fails), CoreGatesInterval)
	}
	logGateFailures("core_contracts_gate_failed", cc.set.ChainKey, cc.set.ChainID, fails)
}

// Ready returns chainID's set while its gates pass.
func (r *CoreRegistry) Ready(chainID int64) (config.CoreContracts, bool) {
	if r == nil {
		return config.CoreContracts{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cc, ok := r.chains[chainID]
	if !ok || !cc.ready {
		return config.CoreContracts{}, false
	}
	return cc.set, true
}

// Fees is the FeeConfig reader over the set's chains (nil when dormant or no chain has a client).
func (r *CoreRegistry) Fees() *FeeConfigReader {
	if r == nil {
		return nil
	}
	return r.fees
}

// SystemWallets reports each chain's FeeConfig feeRecipient for the fee-wallet card, read side by
// side. A chain whose gates have not passed is reported unverified with the reason: its recipient
// is not a destination anyone should rely on.
func (r *CoreRegistry) SystemWallets() []FeeSinkStatus {
	if r == nil {
		return nil
	}
	ids := slices.Sorted(maps.Keys(r.chains))
	out := make([]FeeSinkStatus, len(ids))
	var wg sync.WaitGroup
	for i, chainID := range ids {
		set := r.chains[chainID].set
		out[i] = FeeSinkStatus{Label: "FeeConfig (" + r.setName + " core set)", ChainID: chainID, ChainKey: set.ChainKey, Contract: set.FeeConfig}
		if _, ok := r.Ready(chainID); !ok {
			out[i].Reason = "the core set has not passed its boot gates"
			continue
		}
		wg.Add(1)
		go func(row *FeeSinkStatus) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			addr, err := r.fees.FeeRecipient(ctx, chainID, set.FeeConfig)
			switch {
			case err != nil:
				row.Reason = "could not read feeRecipient: " + RedactAlertText(err.Error())
			case addr == (common.Address{}):
				row.Reason = "feeRecipient() is the zero address"
			default:
				row.SystemWallet, row.Source = addr.Hex(), FeeSinkSourceFeeRecipient
			}
		}(&out[i])
	}
	wg.Wait()
	return out
}
