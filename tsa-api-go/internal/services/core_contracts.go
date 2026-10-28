package services

import (
	"context"
	"fmt"
	"log"
	"maps"
	"math/big"
	"slices"
	"strings"
	"sync/atomic"
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
// set may be used. It reuses the direct gates' failure type: Unread is "could not ask", not "wrong".
func CheckCoreGates(ctx context.Context, c CoreGatesConfig) []DirectGateFailure {
	var fails []DirectGateFailure
	fail := func(gate, reason string) {
		fails = append(fails, DirectGateFailure{Gate: gate, Reason: RedactAlertText(reason)})
	}
	unread := func(gate, reason string) {
		fails = append(fails, DirectGateFailure{Gate: gate, Reason: RedactAlertText(reason), Unread: true})
	}
	if c.Chain == nil {
		unread("bytecode", "no chain client on "+c.Set.ChainKey)
		return fails
	}
	view := func(addr, method string, args ...any) (any, error) {
		vals, err := callABIView(ctx, parsedCoreViewABI, c.Chain, addr, method, args...)
		if err != nil {
			return nil, err
		}
		return vals[0], nil
	}
	addrView := func(addr, method string) (common.Address, error) {
		v, err := view(addr, method)
		if err != nil {
			return common.Address{}, err
		}
		a, ok := v.(common.Address)
		if !ok {
			return common.Address{}, fmt.Errorf("decode %s: unexpected %T", method, v)
		}
		return a, nil
	}

	type contract struct {
		label, addr string
		escrow      bool
	}
	contracts := []contract{{"FeeConfig", c.Set.FeeConfig, false}, {"P2PTradeEscrow", c.Set.P2PEscrow, true}}
	if c.Set.OrderEscrow != "" {
		contracts = append(contracts, contract{"OrderEscrow", c.Set.OrderEscrow, true})
	}

	if id, err := c.Chain.ChainID(ctx); err != nil {
		unread("chain_id", "eth_chainId unreadable: "+err.Error())
	} else if id.Int64() != c.Set.ChainID {
		fail("chain_id", fmt.Sprintf("RPC reports %s, the set is on %d", id, c.Set.ChainID))
	}
	for _, k := range contracts {
		code, err := c.Chain.CodeAt(ctx, common.HexToAddress(k.addr), nil)
		switch {
		case err != nil:
			unread("bytecode", k.label+" unreadable: "+err.Error())
			continue
		case len(code) == 0:
			fail("bytecode", k.label+" has no code at "+k.addr)
			continue
		}
		// The set's admin holds every contract (two-step transfer, so a handover shows here).
		if a, err := addrView(k.addr, "defaultAdmin"); err != nil {
			unread("admin", k.label+" unreadable: "+err.Error())
		} else if !strings.EqualFold(a.Hex(), c.Set.Admin) {
			fail("admin", fmt.Sprintf("%s.defaultAdmin() is %s, the set expects %s", k.label, a.Hex(), c.Set.Admin))
		}
		roles := coreFeeConfigRoles
		if k.escrow {
			roles = coreEscrowRoles
		}
		for _, label := range slices.Sorted(maps.Keys(c.HotKeys)) {
			key := common.HexToAddress(c.HotKeys[label])
			for _, role := range slices.Sorted(maps.Keys(roles)) {
				v, err := view(k.addr, "hasRole", roles[role], key)
				if err != nil {
					unread("hot_key_role", fmt.Sprintf("%s %s on %s unreadable: %v", label, role, k.label, err))
				} else if held, _ := v.(bool); held {
					fail("hot_key_role", fmt.Sprintf("%s %s holds %s on %s", label, key.Hex(), role, k.label))
				}
			}
		}
		if !k.escrow {
			continue
		}
		// The escrow reads the set's own FeeConfig (an immutable: a wrong one needs a new implementation).
		if fc, err := addrView(k.addr, "feeConfig"); err != nil {
			unread("fee_config", k.label+" unreadable: "+err.Error())
		} else if !strings.EqualFold(fc.Hex(), c.Set.FeeConfig) {
			fail("fee_config", fmt.Sprintf("%s.feeConfig() is %s, not the set's %s", k.label, fc.Hex(), c.Set.FeeConfig))
		}
		// The forwarder is the one the set names (or none): it relays calls as their signer, so an
		// unknown one could act as any user.
		if fw, err := addrView(k.addr, "trustedForwarder"); err != nil {
			unread("trusted_forwarder", k.label+" unreadable: "+err.Error())
		} else if want := common.HexToAddress(c.Set.Forwarder); fw != want {
			fail("trusted_forwarder", fmt.Sprintf("%s.trustedForwarder() is %s, the set expects %s", k.label, fw.Hex(), want.Hex()))
		}
	}
	for _, label := range slices.Sorted(maps.Keys(c.HotKeys)) {
		if strings.EqualFold(c.HotKeys[label], c.Set.Admin) {
			fail("admin", "the set's admin is "+label+", a hot key")
		}
	}
	// paymentWindow() is the 30 minutes every trade is stamped with.
	if v, err := view(c.Set.P2PEscrow, "paymentWindow"); err != nil {
		unread("payment_window", "unreadable: "+err.Error())
	} else if w, ok := v.(*big.Int); !ok || time.Duration(w.Int64())*time.Second != p2pPaymentWindow {
		fail("payment_window", fmt.Sprintf("paymentWindow() is %v s, the API assumes %s", v, p2pPaymentWindow))
	}
	// Fees have somewhere to go.
	if r, err := c.Fees.FeeRecipient(ctx, c.Set.ChainID, c.Set.FeeConfig); err != nil {
		unread("fee_recipient", "unreadable: "+err.Error())
	} else if r == (common.Address{}) {
		fail("fee_recipient", "feeRecipient() is the zero address")
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

// coreChain is one chain's set and whether its gates have passed.
type coreChain struct {
	set   config.CoreContracts
	ready atomic.Bool
	gates CoreGatesConfig
}

// CoreRegistry is the selected core set, one entry per chain. A nil registry is a dormant set.
type CoreRegistry struct {
	setName string
	chains  map[int64]*coreChain
	fees    *FeeConfigReader
}

// coreGatesRecheckEvery is how often a chain whose gates failed is checked again: a role fix or a
// transfer on-chain then takes effect without a restart.
const coreGatesRecheckEvery = time.Minute

// coreGatesBudget bounds one gate pass (each gate is one or more 10 s reads).
const coreGatesBudget = 2 * time.Minute

// NewCoreRegistry builds the registry for cfg's selected set over bs's chain clients. It returns
// nil when the set is dormant. hotKeys are the signing keys the API holds (label → address).
func NewCoreRegistry(cfg *config.Config, bs *BlockchainService, hotKeys map[string]string) *CoreRegistry {
	if len(cfg.CoreContracts) == 0 {
		return nil
	}
	rpcByChain := map[int64]FeeManagerRPCClient{}
	r := &CoreRegistry{setName: cfg.CoreSet, chains: map[int64]*coreChain{}}
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

// Start gates every chain in the background (nothing is ready until its gates pass) and re-checks
// a failing chain until it passes. Nothing reads the set yet, so boot never waits on it.
func (r *CoreRegistry) Start() {
	if r == nil {
		return
	}
	for _, cc := range r.chains {
		go r.gate(cc)
	}
}

func (r *CoreRegistry) gate(cc *coreChain) {
	for {
		ctx, cancel := context.WithTimeout(context.Background(), coreGatesBudget)
		fails := CheckCoreGates(ctx, cc.gates)
		cancel()
		if len(fails) == 0 {
			cc.ready.Store(true)
			log.Printf("[config] core contracts %s gates OK chain=%s(%d) — ready", r.setName, cc.set.ChainKey, cc.set.ChainID)
			return
		}
		logGateFailures("core_contracts_gate_failed", cc.set.ChainKey, cc.set.ChainID, fails)
		log.Printf("[config] ALERT core_contracts_not_ready set=%s chain=%s(%d) failures=%d — re-checking every %s",
			r.setName, cc.set.ChainKey, cc.set.ChainID, len(fails), coreGatesRecheckEvery)
		time.Sleep(coreGatesRecheckEvery)
	}
}

// Ready returns chainID's set once its gates have passed.
func (r *CoreRegistry) Ready(chainID int64) (config.CoreContracts, bool) {
	if r == nil {
		return config.CoreContracts{}, false
	}
	cc, ok := r.chains[chainID]
	if !ok || !cc.ready.Load() {
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

// SystemWallets reports each chain's FeeConfig feeRecipient for the fee-wallet card. Every fee the
// core set takes (orders, P2P, service contact) is paid there, whether or not the gates passed.
func (r *CoreRegistry) SystemWallets() []FeeSinkStatus {
	if r == nil {
		return nil
	}
	out := make([]FeeSinkStatus, 0, len(r.chains))
	for _, chainID := range slices.Sorted(maps.Keys(r.chains)) {
		set := r.chains[chainID].set
		row := FeeSinkStatus{Label: "FeeConfig (" + r.setName + " core set)", ChainID: chainID, ChainKey: set.ChainKey, Contract: set.FeeConfig}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		addr, err := r.fees.FeeRecipient(ctx, chainID, set.FeeConfig)
		cancel()
		switch {
		case err != nil:
			row.Reason = "could not read feeRecipient: " + RedactAlertText(err.Error())
		case addr == (common.Address{}):
			row.Reason = "feeRecipient() is the zero address"
		default:
			row.SystemWallet, row.Source = addr.Hex(), FeeSinkSourceFeeRecipient
		}
		out = append(out, row)
	}
	return out
}
