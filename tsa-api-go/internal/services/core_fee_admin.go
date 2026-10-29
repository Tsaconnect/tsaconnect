package services

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// core_fee_admin.go — fee authoring on a core set's FeeConfig (slice 4 of
// docs/plans/2026-10-27-contracts-v2-migration.md). The chain is the only record (owner
// 2026-10-28): the console reads the live rows, the admin edits them, the API builds the
// CONFIG_ROLE txs, the admin's wallet signs, and Verify re-reads the chain. Nothing is stored.

// ErrCoreFeeInvalid marks a request the admin must correct (a 400, never a chain problem).
var ErrCoreFeeInvalid = errors.New("invalid fee change")

// coreFeeTokens are the tokens a core set prices: the stables both escrows take.
var coreFeeTokens = []string{"USDC", "USDT"}

var coreFeeFeatures = []string{FeeConfigFeatureOrder, FeeConfigFeatureP2PPlatform, FeeConfigFeatureP2PGas}

// CoreFeeToken is one token a set prices, with the decimals the API's amount math assumes.
type CoreFeeToken struct {
	Symbol   string `json:"symbol"`
	Address  string `json:"address"`
	Decimals int    `json:"decimals"`
}

// CoreFeeRate is one (feature, token) row. Flat is in whole tokens ("0.10"); only the order
// feature has one (the buyer's flat fee).
type CoreFeeRate struct {
	Feature string `json:"feature"`
	Token   string `json:"token"`
	BPS     uint16 `json:"bps"`
	Flat    string `json:"flat"`
	Active  bool   `json:"active"`
}

// CoreFeeLimits are a token's value caps in whole tokens. Configured is false while the token
// has none (every escrow call in it reverts until it does).
type CoreFeeLimits struct {
	Token       string `json:"token"`
	Configured  bool   `json:"configured"`
	MaxPerOrder string `json:"maxPerOrder"`
	MaxOwed     string `json:"maxOwed"`
}

// CoreFeeState is a chain's live FeeConfig.
type CoreFeeState struct {
	ChainID      int64           `json:"chainId"`
	ChainKey     string          `json:"chainKey"`
	Set          string          `json:"set"`
	FeeConfig    string          `json:"feeConfig"`
	Admin        string          `json:"admin"`
	FeeRecipient string          `json:"feeRecipient"`
	MaxBPS       int             `json:"maxBps"`
	Tokens       []CoreFeeToken  `json:"tokens"`
	Rates        []CoreFeeRate   `json:"rates"`
	Limits       []CoreFeeLimits `json:"limits"`
}

// CoreFeeChange is what the admin wants: only the rows named change; the rest stay as they are.
type CoreFeeChange struct {
	Rates        []CoreFeeRate   `json:"rates"`
	Limits       []CoreFeeLimits `json:"limits"`
	FeeRecipient string          `json:"feeRecipient"`
}

// CoreFeeTx is one unsigned CONFIG_ROLE call for the admin's wallet.
type CoreFeeTx struct {
	ChainID int64  `json:"chainId"`
	To      string `json:"to"`
	Data    string `json:"data"`
	Summary string `json:"summary"`
}

// CoreFeePlan is the txs that turn the live state into the change (none when it already matches).
type CoreFeePlan struct {
	Txs     []CoreFeeTx `json:"txs"`
	Changes []string    `json:"changes"`
}

// CoreFeeChain is one chain of the selected set as the console lists it.
type CoreFeeChain struct {
	ChainID  int64  `json:"chainId"`
	ChainKey string `json:"chainKey"`
	Set      string `json:"set"`
	Ready    bool   `json:"ready"`
}

// CoreFeeAdmin authors fees on the selected core set. Nil when the set is dormant.
type CoreFeeAdmin struct {
	reg       *CoreRegistry
	tokenAddr func(chainID int64, symbol string) string
}

// NewCoreFeeAdmin returns nil for a dormant registry. tokenAddr is
// BlockchainService.TokenAddressForChainID.
func NewCoreFeeAdmin(reg *CoreRegistry, tokenAddr func(chainID int64, symbol string) string) *CoreFeeAdmin {
	if reg == nil {
		return nil
	}
	return &CoreFeeAdmin{reg: reg, tokenAddr: tokenAddr}
}

// Chains lists the set's chains and whether each has passed its gates.
func (a *CoreFeeAdmin) Chains() []CoreFeeChain {
	out := []CoreFeeChain{}
	for _, chainID := range slices.Sorted(maps.Keys(a.reg.chains)) {
		_, ready := a.reg.Ready(chainID)
		set := a.reg.chains[chainID].set
		out = append(out, CoreFeeChain{ChainID: chainID, ChainKey: set.ChainKey, Set: a.reg.setName, Ready: ready})
	}
	return out
}

func (a *CoreFeeAdmin) tokens(chainID int64) []CoreFeeToken {
	var out []CoreFeeToken
	for _, sym := range coreFeeTokens {
		if addr := a.tokenAddr(chainID, sym); common.IsHexAddress(addr) && common.HexToAddress(addr) != (common.Address{}) {
			out = append(out, CoreFeeToken{Symbol: sym, Address: common.HexToAddress(addr).Hex(), Decimals: tokenDecimalsFor(chainID, sym)})
		}
	}
	return out
}

// State reads chainID's live FeeConfig. Only a set that passed its gates is authored on.
func (a *CoreFeeAdmin) State(ctx context.Context, chainID int64) (*CoreFeeState, error) {
	set, ok := a.reg.Ready(chainID)
	if !ok {
		return nil, fmt.Errorf("chain %d: %w", chainID, ErrCoreSetNotReady)
	}
	fees := a.reg.Fees()
	st := &CoreFeeState{ChainID: chainID, ChainKey: set.ChainKey, Set: a.reg.setName, FeeConfig: set.FeeConfig,
		Admin: set.Admin, MaxBPS: FeeConfigMaxBPS, Tokens: a.tokens(chainID)}
	recipient, err := fees.FeeRecipient(ctx, chainID, set.FeeConfig)
	if err != nil {
		return nil, err
	}
	st.FeeRecipient = recipient.Hex()
	for _, t := range st.Tokens {
		addr := common.HexToAddress(t.Address)
		for _, f := range coreFeeFeatures {
			r, err := fees.RawRate(ctx, chainID, set.FeeConfig, f, addr)
			if err != nil {
				return nil, err
			}
			st.Rates = append(st.Rates, CoreFeeRate{Feature: f, Token: t.Symbol, BPS: r.BPS,
				Flat: BaseUnitsToDecimal(r.FlatFee, t.Decimals), Active: r.Active})
		}
		l, err := fees.LimitsFor(ctx, chainID, set.FeeConfig, addr)
		switch {
		case errors.Is(err, ErrFeeConfigReverted):
			st.Limits = append(st.Limits, CoreFeeLimits{Token: t.Symbol, MaxPerOrder: "0", MaxOwed: "0"})
		case err != nil:
			return nil, err
		default:
			st.Limits = append(st.Limits, CoreFeeLimits{Token: t.Symbol, Configured: true,
				MaxPerOrder: BaseUnitsToDecimal(l.MaxPerOrder, t.Decimals), MaxOwed: BaseUnitsToDecimal(l.MaxOwed, t.Decimals)})
		}
	}
	return st, nil
}

// coreFeeTarget is a change resolved against the chain: base units and addresses.
type coreFeeTarget struct {
	rates     []FeeConfigRateRow
	limits    map[common.Address]FeeConfigLimits
	recipient *common.Address
}

// resolve validates change against st and converts it to on-chain values.
func (a *CoreFeeAdmin) resolve(st *CoreFeeState, change CoreFeeChange) (*coreFeeTarget, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrCoreFeeInvalid, fmt.Sprintf(format, args...))
	}
	tokens := map[string]CoreFeeToken{}
	for _, t := range st.Tokens {
		tokens[t.Symbol] = t
	}
	token := func(sym string) (CoreFeeToken, error) {
		t, ok := tokens[strings.ToUpper(strings.TrimSpace(sym))]
		if !ok {
			return t, bad("token %q is not priced on chain %d", sym, st.ChainID)
		}
		return t, nil
	}
	amount := func(what, v string, t CoreFeeToken) (*big.Int, error) {
		units, err := DecimalToBaseUnits(v, t.Decimals)
		if err != nil {
			return nil, bad("%s: %v", what, err)
		}
		if err := checkUint128(units); err != nil {
			return nil, bad("%s: %v", what, err)
		}
		return units, nil
	}
	out := &coreFeeTarget{limits: map[common.Address]FeeConfigLimits{}}
	for _, r := range change.Rates {
		t, err := token(r.Token)
		if err != nil {
			return nil, err
		}
		if _, err := FeeConfigFeatureKey(r.Feature); err != nil {
			return nil, bad("%v", err)
		}
		if int(r.BPS) > FeeConfigMaxBPS {
			return nil, bad("%s %s: %d bps is above the %d bps cap", r.Feature, t.Symbol, r.BPS, FeeConfigMaxBPS)
		}
		flatIn := r.Flat
		if strings.TrimSpace(flatIn) == "" {
			flatIn = "0"
		}
		flat, err := amount(r.Feature+" "+t.Symbol+" flat fee", flatIn, t)
		if err != nil {
			return nil, err
		}
		// The P2P escrow reads only bps; a flat fee there would look charged and never be.
		if r.Feature != FeeConfigFeatureOrder && flat.Sign() != 0 {
			return nil, bad("%s has no flat fee", r.Feature)
		}
		out.rates = append(out.rates, FeeConfigRateRow{Feature: r.Feature, Token: common.HexToAddress(t.Address),
			Rate: FeeConfigRate{BPS: r.BPS, FlatFee: flat, Active: r.Active}})
	}
	for _, l := range change.Limits {
		t, err := token(l.Token)
		if err != nil {
			return nil, err
		}
		perOrder, err := amount(t.Symbol+" max per order", l.MaxPerOrder, t)
		if err != nil {
			return nil, err
		}
		owed, err := amount(t.Symbol+" max owed", l.MaxOwed, t)
		if err != nil {
			return nil, err
		}
		if perOrder.Sign() <= 0 || owed.Sign() <= 0 {
			return nil, bad("%s limits must both be above zero", t.Symbol)
		}
		addr := common.HexToAddress(t.Address)
		if _, dup := out.limits[addr]; dup {
			return nil, bad("%s limits are given twice", t.Symbol)
		}
		out.limits[addr] = FeeConfigLimits{MaxPerOrder: perOrder, MaxOwed: owed}
	}
	if r := strings.TrimSpace(change.FeeRecipient); r != "" {
		if !common.IsHexAddress(r) || common.HexToAddress(r) == (common.Address{}) {
			return nil, bad("fee recipient %q is not a valid address", r)
		}
		addr := common.HexToAddress(r)
		// A hot key's balance is only as safe as the server holding it: fees must not pile up there.
		if label, hot := a.reg.hotKeys(st.ChainID)[addr]; hot {
			return nil, bad("fee recipient %s is %s, a hot key", addr.Hex(), label)
		}
		out.recipient = &addr
	}
	return out, nil
}

// Plan builds the CONFIG_ROLE txs that turn chainID's live FeeConfig into change: one setRates
// for every changed row, one setLimits per changed token, one setFeeRecipient. Rows already at
// the wanted value are left out, so an unchanged request plans nothing.
func (a *CoreFeeAdmin) Plan(ctx context.Context, chainID int64, change CoreFeeChange) (*CoreFeePlan, error) {
	st, err := a.State(ctx, chainID)
	if err != nil {
		return nil, err
	}
	target, err := a.resolve(st, change)
	if err != nil {
		return nil, err
	}
	plan := &CoreFeePlan{Txs: []CoreFeeTx{}, Changes: []string{}}
	tx := func(data []byte, summary string) {
		plan.Txs = append(plan.Txs, CoreFeeTx{ChainID: chainID, To: st.FeeConfig, Data: hexutil.Encode(data), Summary: summary})
	}
	live := liveRates(st)
	var rows []FeeConfigRateRow
	for _, r := range target.rates {
		cur := live[rateKey{r.Feature, r.Token}]
		if cur.BPS == r.Rate.BPS && cur.FlatFee.Cmp(r.Rate.FlatFee) == 0 && cur.Active == r.Rate.Active {
			continue
		}
		rows = append(rows, r)
		t := st.tokenByAddress(r.Token)
		plan.Changes = append(plan.Changes, fmt.Sprintf("%s %s: %s → %s", r.Feature, t.Symbol,
			describeRate(cur, t), describeRate(r.Rate, t)))
	}
	if len(rows) > 0 {
		data, err := PackFeeConfigSetRates(rows)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCoreFeeInvalid, err)
		}
		tx(data, fmt.Sprintf("setRates (%d rows)", len(rows)))
	}
	liveLimits := liveLimitsByAddress(st)
	for _, addr := range slices.SortedFunc(maps.Keys(target.limits), func(x, y common.Address) int { return strings.Compare(x.Hex(), y.Hex()) }) {
		l, t := target.limits[addr], st.tokenByAddress(addr)
		if cur, ok := liveLimits[addr]; ok && cur.MaxPerOrder.Cmp(l.MaxPerOrder) == 0 && cur.MaxOwed.Cmp(l.MaxOwed) == 0 {
			continue
		}
		data, err := PackFeeConfigSetLimits(addr, l)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCoreFeeInvalid, err)
		}
		tx(data, "setLimits "+t.Symbol)
		plan.Changes = append(plan.Changes, fmt.Sprintf("%s limits → %s per order, %s owed", t.Symbol,
			BaseUnitsToDecimal(l.MaxPerOrder, t.Decimals), BaseUnitsToDecimal(l.MaxOwed, t.Decimals)))
	}
	if target.recipient != nil && !strings.EqualFold(target.recipient.Hex(), st.FeeRecipient) {
		data, err := PackFeeConfigSetFeeRecipient(*target.recipient)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrCoreFeeInvalid, err)
		}
		tx(data, "setFeeRecipient")
		plan.Changes = append(plan.Changes, fmt.Sprintf("fee recipient: %s → %s", st.FeeRecipient, target.recipient.Hex()))
	}
	return plan, nil
}

// Verify re-reads chainID and lists every part of change the chain does not hold yet (empty: applied).
func (a *CoreFeeAdmin) Verify(ctx context.Context, chainID int64, change CoreFeeChange) ([]string, error) {
	plan, err := a.Plan(ctx, chainID, change)
	if err != nil {
		return nil, err
	}
	return plan.Changes, nil
}

type rateKey struct {
	feature string
	token   common.Address
}

func liveRates(st *CoreFeeState) map[rateKey]FeeConfigRate {
	out := map[rateKey]FeeConfigRate{}
	for _, r := range st.Rates {
		t := st.tokenBySymbol(r.Token)
		flat, _ := DecimalToBaseUnits(r.Flat, t.Decimals)
		out[rateKey{r.Feature, common.HexToAddress(t.Address)}] = FeeConfigRate{BPS: r.BPS, FlatFee: flat, Active: r.Active}
	}
	return out
}

func liveLimitsByAddress(st *CoreFeeState) map[common.Address]FeeConfigLimits {
	out := map[common.Address]FeeConfigLimits{}
	for _, l := range st.Limits {
		if !l.Configured {
			continue
		}
		t := st.tokenBySymbol(l.Token)
		perOrder, _ := DecimalToBaseUnits(l.MaxPerOrder, t.Decimals)
		owed, _ := DecimalToBaseUnits(l.MaxOwed, t.Decimals)
		out[common.HexToAddress(t.Address)] = FeeConfigLimits{MaxPerOrder: perOrder, MaxOwed: owed}
	}
	return out
}

func (st *CoreFeeState) tokenBySymbol(sym string) CoreFeeToken {
	for _, t := range st.Tokens {
		if t.Symbol == sym {
			return t
		}
	}
	return CoreFeeToken{}
}

func (st *CoreFeeState) tokenByAddress(addr common.Address) CoreFeeToken {
	for _, t := range st.Tokens {
		if common.HexToAddress(t.Address) == addr {
			return t
		}
	}
	return CoreFeeToken{}
}

func describeRate(r FeeConfigRate, t CoreFeeToken) string {
	if !r.Active {
		return "off"
	}
	s := fmt.Sprintf("%d bps", r.BPS)
	if r.FlatFee != nil && r.FlatFee.Sign() > 0 {
		s += " + " + BaseUnitsToDecimal(r.FlatFee, t.Decimals) + " " + t.Symbol
	}
	return s
}
