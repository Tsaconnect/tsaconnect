package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"golang.org/x/sync/errgroup"
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

// limitsNotConfiguredSelector is IFeeConfig.LimitsNotConfigured(address): the one revert that
// means "this token has no limits yet". Any other revert is a chain problem, never "unset".
var limitsNotConfiguredSelector = crypto.Keccak256([]byte("LimitsNotConfigured(address)"))[:4]

const erc20DecimalsABIJSON = `[{"type":"function","name":"decimals","stateMutability":"view","inputs":[],"outputs":[{"name":"","type":"uint8"}]}]`

var parsedERC20DecimalsABI = func() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(erc20DecimalsABIJSON))
	if err != nil {
		panic(fmt.Sprintf("core fees: parse ABI: %v", err))
	}
	return parsed
}()

// CoreFeeToken is one token a set prices, with its decimals as read from the token.
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

	// The rows as read, in base units, for Plan's comparison.
	rawRates  map[rateKey]FeeConfigRate
	rawLimits map[common.Address]FeeConfigLimits
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

// tokens are the set's priced tokens on chainID with their decimals read from each token. A token
// whose decimals differ from what the rest of the API's amount math assumes is refused: every
// amount the admin types would be scaled wrong here, and every charge wrong elsewhere.
func (a *CoreFeeAdmin) tokens(ctx context.Context, chainID int64) ([]CoreFeeToken, error) {
	caller := a.reg.chains[chainID].gates.Chain
	var out []CoreFeeToken
	for _, sym := range coreFeeTokens {
		addr := a.tokenAddr(chainID, sym)
		if !common.IsHexAddress(addr) || common.HexToAddress(addr) == (common.Address{}) {
			continue
		}
		vals, err := callABIView(ctx, parsedERC20DecimalsABI, caller, addr, "decimals")
		if err != nil {
			return nil, fmt.Errorf("%s decimals: %w", sym, err)
		}
		d, _ := vals[0].(uint8)
		if want := tokenDecimalsFor(chainID, sym); int(d) != want {
			return nil, fmt.Errorf("%s at %s has %d decimals on-chain, the API assumes %d", sym, addr, d, want)
		}
		out = append(out, CoreFeeToken{Symbol: sym, Address: common.HexToAddress(addr).Hex(), Decimals: int(d)})
	}
	return out, nil
}

// State reads chainID's live FeeConfig, every row side by side. Only a set that passed its gates
// is authored on.
func (a *CoreFeeAdmin) State(ctx context.Context, chainID int64) (*CoreFeeState, error) {
	set, ok := a.reg.Ready(chainID)
	if !ok {
		return nil, fmt.Errorf("chain %d: %w", chainID, ErrCoreSetNotReady)
	}
	tokens, err := a.tokens(ctx, chainID)
	if err != nil {
		return nil, err
	}
	fees := a.reg.Fees()
	st := &CoreFeeState{ChainID: chainID, ChainKey: set.ChainKey, Set: a.reg.setName, FeeConfig: set.FeeConfig,
		Admin: set.Admin, MaxBPS: FeeConfigMaxBPS, Tokens: tokens,
		Rates: make([]CoreFeeRate, len(tokens)*len(coreFeeFeatures)), Limits: make([]CoreFeeLimits, len(tokens)),
		rawRates: map[rateKey]FeeConfigRate{}, rawLimits: map[common.Address]FeeConfigLimits{}}
	raw := make([]FeeConfigRate, len(st.Rates))
	rawLimits := make([]*FeeConfigLimits, len(tokens))

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		r, err := fees.FeeRecipient(gctx, chainID, set.FeeConfig)
		st.FeeRecipient = r.Hex()
		return err
	})
	for ti, t := range tokens {
		addr := common.HexToAddress(t.Address)
		for fi, f := range coreFeeFeatures {
			i := ti*len(coreFeeFeatures) + fi
			g.Go(func() error {
				r, err := fees.RawRate(gctx, chainID, set.FeeConfig, f, addr)
				raw[i] = r
				return err
			})
		}
		g.Go(func() error {
			l, err := fees.LimitsFor(gctx, chainID, set.FeeConfig, addr)
			if errors.Is(err, ErrFeeConfigReverted) {
				if data := revertData(err); data != nil && bytes.Equal(data[:4], limitsNotConfiguredSelector) {
					return nil // unset: rawLimits[ti] stays nil
				}
			}
			if err == nil {
				rawLimits[ti] = &l
			}
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	for ti, t := range tokens {
		addr := common.HexToAddress(t.Address)
		for fi, f := range coreFeeFeatures {
			r := raw[ti*len(coreFeeFeatures)+fi]
			if r.FlatFee == nil {
				r.FlatFee = new(big.Int)
			}
			st.rawRates[rateKey{f, addr}] = r
			st.Rates[ti*len(coreFeeFeatures)+fi] = CoreFeeRate{Feature: f, Token: t.Symbol, BPS: r.BPS,
				Flat: BaseUnitsToDecimal(r.FlatFee, t.Decimals), Active: r.Active}
		}
		if l := rawLimits[ti]; l != nil {
			st.rawLimits[addr] = *l
			st.Limits[ti] = CoreFeeLimits{Token: t.Symbol, Configured: true,
				MaxPerOrder: BaseUnitsToDecimal(l.MaxPerOrder, t.Decimals), MaxOwed: BaseUnitsToDecimal(l.MaxOwed, t.Decimals)}
		} else {
			st.Limits[ti] = CoreFeeLimits{Token: t.Symbol, MaxPerOrder: "0", MaxOwed: "0"}
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
	seen := map[rateKey]bool{}
	for _, r := range change.Rates {
		t, err := token(r.Token)
		if err != nil {
			return nil, err
		}
		if _, err := FeeConfigFeatureKey(r.Feature); err != nil {
			return nil, bad("%v", err)
		}
		// Two rows for one key: whichever the chain ends up with, Verify would report the other.
		k := rateKey{r.Feature, common.HexToAddress(t.Address)}
		if seen[k] {
			return nil, bad("%s %s is given twice", r.Feature, t.Symbol)
		}
		seen[k] = true
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
		out.rates = append(out.rates, FeeConfigRateRow{Feature: r.Feature, Token: k.token,
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
		// One order alone is owed by the escrow: a per-order cap above the owed cap can never be used.
		if perOrder.Cmp(owed) > 0 {
			return nil, bad("%s max per order (%s) is above max owed (%s)", t.Symbol, l.MaxPerOrder, l.MaxOwed)
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
	var rows []FeeConfigRateRow
	for _, r := range target.rates {
		cur := st.rawRates[rateKey{r.Feature, r.Token}]
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
	for _, addr := range slices.SortedFunc(maps.Keys(target.limits), func(x, y common.Address) int { return strings.Compare(x.Hex(), y.Hex()) }) {
		l, t := target.limits[addr], st.tokenByAddress(addr)
		if cur, ok := st.rawLimits[addr]; ok && cur.MaxPerOrder.Cmp(l.MaxPerOrder) == 0 && cur.MaxOwed.Cmp(l.MaxOwed) == 0 {
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

// ErrCoreFeeTxUnproven: a tx hash given to Verify is not a mined, successful call to the set's
// FeeConfig on that chain.
var ErrCoreFeeTxUnproven = errors.New("fee tx not proven")

// Verify re-reads chainID and lists every part of change the chain does not hold yet (empty:
// applied). txHashes must each be a successful call to this FeeConfig; a change is recorded as
// applied only on that evidence, so an unchanged request with made-up hashes proves nothing.
func (a *CoreFeeAdmin) Verify(ctx context.Context, chainID int64, change CoreFeeChange, txHashes []string) ([]string, error) {
	plan, err := a.Plan(ctx, chainID, change)
	if err != nil {
		return nil, err
	}
	if len(plan.Changes) > 0 {
		return plan.Changes, nil
	}
	set, _ := a.reg.Ready(chainID)
	if len(txHashes) == 0 {
		return nil, fmt.Errorf("%w: no tx hashes", ErrCoreFeeTxUnproven)
	}
	for _, h := range txHashes {
		if err := a.reg.Fees().TxCalled(ctx, chainID, h, set.FeeConfig); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrCoreFeeTxUnproven, h, err)
		}
	}
	return nil, nil
}

type rateKey struct {
	feature string
	token   common.Address
}

func (st *CoreFeeState) tokenByAddress(addr common.Address) CoreFeeToken {
	for _, t := range st.Tokens {
		if common.HexToAddress(t.Address) == addr {
			return t
		}
	}
	return CoreFeeToken{}
}

// describeRate says what a row charges; an inactive row still shows its stored values, so an edit
// to one is never shown as "off → off".
func describeRate(r FeeConfigRate, t CoreFeeToken) string {
	s := fmt.Sprintf("%d bps", r.BPS)
	if r.FlatFee != nil && r.FlatFee.Sign() > 0 {
		s += " + " + BaseUnitsToDecimal(r.FlatFee, t.Decimals) + " " + t.Symbol
	}
	if !r.Active {
		s = "off (" + s + ")"
	}
	return s
}
