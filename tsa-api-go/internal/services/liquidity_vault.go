package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ojimcy/tsa-api-go/internal/blockchain"
	"golang.org/x/sync/errgroup"
)

// LiquidityVault is the shared pool contract (TsaLiquidityPool, spec
// 2026-10-14) on the pool chain: reads, unsigned-tx builders, event decoding.
// It never signs: the operator's sends go through ChainSender.Send, LPs sign
// their own txs in the app.
type LiquidityVault struct {
	client  *blockchain.EVMClient
	addr    common.Address
	timeout time.Duration

	tokenMu sync.Mutex
	token   common.Address // the vault's token, read once (immutable on-chain)
}

var parsedLiquidityVaultABI abi.ABI

func init() {
	var err error
	parsedLiquidityVaultABI, err = abi.JSON(strings.NewReader(tsaLiquidityPoolABI))
	if err != nil {
		panic(fmt.Sprintf("liquidity vault ABI: %v", err))
	}
}

var ErrVaultUnavailable = errors.New("liquidity vault: not configured")

func NewLiquidityVault(client *blockchain.EVMClient, address string) *LiquidityVault {
	if client == nil || !common.IsHexAddress(address) {
		return nil
	}
	return &LiquidityVault{client: client, addr: common.HexToAddress(address), timeout: 10 * time.Second}
}

func (v *LiquidityVault) Address() string { return v.addr.Hex() }

// VaultState is one consistent reading of the pool's books, all at one block.
// There is no collateral (the no-collateral upgrade, 2026-10-14): what a fill
// or a claim may use is Liquid — the token balance less what is owed to
// pending withdrawals, which are paid first.
type VaultState struct {
	Block                   uint64
	Balance                 *big.Int // the vault's token balance
	TotalPendingWithdrawals *big.Int
	Outstanding             *big.Int // sold amount + LP spread the company still owes the pool
	TotalPrincipal          *big.Int
	Paused                  bool
}

// Liquid is the contract's liquid(): balance − totalPendingWithdrawals,
// never negative.
func (s *VaultState) Liquid() *big.Int {
	if s == nil || s.Balance == nil {
		return new(big.Int)
	}
	pending := s.TotalPendingWithdrawals
	if pending == nil {
		pending = new(big.Int)
	}
	l := new(big.Int).Sub(s.Balance, pending)
	if l.Sign() < 0 {
		return new(big.Int)
	}
	return l
}

// State reads the balance, pending withdrawals, outstanding, total principal
// and paused at the same block, so capacity is never computed from two
// different moments.
func (v *LiquidityVault) State(ctx context.Context) (*VaultState, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	head, err := v.client.Client.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: read head: %w", err)
	}
	return v.stateAt(ctx, head)
}

// StateAt is State at a given block (the reconciler compares the ledger,
// indexed up to a block, with the books at that same block).
func (v *LiquidityVault) StateAt(ctx context.Context, block uint64) (*VaultState, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	return v.stateAt(ctx, block)
}

func (v *LiquidityVault) stateAt(ctx context.Context, block uint64) (*VaultState, error) {
	at := new(big.Int).SetUint64(block)
	st := &VaultState{Block: block}
	if _, err := v.tokenAddress(ctx); err != nil { // cached after the first read
		return nil, err
	}
	// Every read is at the same block, so they run concurrently: an open
	// does this under the pool lock with a 3 s budget, one round trip, not five.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { st.Balance, err = v.balanceAt(gctx, at); return })
	for _, r := range []struct {
		method string
		into   **big.Int
	}{{"totalPendingWithdrawals", &st.TotalPendingWithdrawals}, {"outstanding", &st.Outstanding}, {"totalPrincipal", &st.TotalPrincipal}} {
		g.Go(func() error {
			out, err := v.callAt(gctx, at, r.method)
			if err != nil {
				return err
			}
			*r.into = out[0].(*big.Int)
			return nil
		})
	}
	g.Go(func() error {
		out, err := v.callAt(gctx, at, "paused")
		if err != nil {
			return err
		}
		st.Paused = out[0].(bool)
		return nil
	})
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return st, nil
}

// tokenAddress is the vault's token (read once: it never changes).
func (v *LiquidityVault) tokenAddress(ctx context.Context) (common.Address, error) {
	v.tokenMu.Lock()
	defer v.tokenMu.Unlock()
	if v.token != (common.Address{}) {
		return v.token, nil
	}
	out, err := v.callAt(ctx, nil, "token")
	if err != nil {
		return common.Address{}, err
	}
	v.token = out[0].(common.Address)
	return v.token, nil
}

// balanceAt is the vault's token balance at block.
func (v *LiquidityVault) balanceAt(ctx context.Context, at *big.Int) (*big.Int, error) {
	tok, err := v.tokenAddress(ctx)
	if err != nil {
		return nil, err
	}
	data, err := erc20BalanceOfCalldata(v.addr.Hex())
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: %w", err)
	}
	raw, err := v.client.Client.CallContract(ctx, ethereum.CallMsg{To: &tok, Data: data}, at)
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: call balanceOf: %w", err)
	}
	out, err := erc20BalanceOfABI.Unpack("balanceOf", raw)
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("liquidity vault: decode balanceOf: %v", err)
	}
	return out[0].(*big.Int), nil
}

// VaultFillCaps are the contract's own limits on a fill (TsaLiquidityPool
// fill(): amount + spread ≤ MaxFillPerTx, spread × 10000 ≤ amount ×
// MaxSpreadBps, and amount + spread counted in a fixed window of FillWindow
// seconds from FillWindowStart, at most FillWindowCap), read at one block.
type VaultFillCaps struct {
	MaxFillPerTx    *big.Int
	MaxSpreadBps    *big.Int
	FillWindow      *big.Int // seconds
	FillWindowStart *big.Int // unix seconds
	FilledInWindow  *big.Int
	FillWindowCap   *big.Int
}

// WindowCurrent: the window FilledInWindow counts is still open at now (the
// contract rolls it on the first fill at or after start + window).
func (c *VaultFillCaps) WindowCurrent(now time.Time) bool {
	if c == nil || c.FillWindowStart == nil || c.FillWindow == nil {
		return false
	}
	end := new(big.Int).Add(c.FillWindowStart, c.FillWindow)
	return end.Cmp(big.NewInt(now.Unix())) > 0
}

// FillCaps reads the fill limits at the head block.
func (v *LiquidityVault) FillCaps(ctx context.Context) (*VaultFillCaps, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	head, err := v.client.Client.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: read head: %w", err)
	}
	at := new(big.Int).SetUint64(head)
	c := &VaultFillCaps{}
	g, gctx := errgroup.WithContext(ctx)
	for _, r := range []struct {
		method string
		into   **big.Int
	}{{"maxFillPerTx", &c.MaxFillPerTx}, {"maxSpreadBps", &c.MaxSpreadBps}, {"fillWindow", &c.FillWindow},
		{"fillWindowStart", &c.FillWindowStart}, {"filledInWindow", &c.FilledInWindow}, {"fillWindowCap", &c.FillWindowCap}} {
		g.Go(func() error {
			out, err := v.callAt(gctx, at, r.method)
			if err != nil {
				return err
			}
			*r.into = out[0].(*big.Int)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return c, nil
}

// VaultLimits are the contract's own deposit limits (TsaLiquidityPool
// deposit(): amount ≥ MinDeposit, principal + amount ≤ MaxPerLP,
// totalPrincipal + amount ≤ TotalCap), in token base units. Only the pool
// owner changes them (setLimits); the API never keeps a copy of its own.
type VaultLimits struct {
	MinDeposit *big.Int
	MaxPerLP   *big.Int
	TotalCap   *big.Int
}

// Limits reads the deposit limits at the head block.
func (v *LiquidityVault) Limits(ctx context.Context) (*VaultLimits, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	head, err := v.client.Client.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: read head: %w", err)
	}
	at := new(big.Int).SetUint64(head)
	l := &VaultLimits{}
	g, gctx := errgroup.WithContext(ctx)
	for _, r := range []struct {
		method string
		into   **big.Int
	}{{"minDeposit", &l.MinDeposit}, {"maxPerLp", &l.MaxPerLP}, {"totalCap", &l.TotalCap}} {
		g.Go(func() error {
			out, err := v.callAt(gctx, at, r.method)
			if err != nil {
				return err
			}
			*r.into = out[0].(*big.Int)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return l, nil
}

// VaultPosition is one LP's on-chain position.
type VaultPosition struct {
	Principal *big.Int
	Claimable *big.Int
	Allowed   bool // on the depositor allow-list (withdraw/claim never need it)
	// Withdrawable is the principal past its deposit lock; LockedUntil is when
	// the rest unlocks (zero when nothing is locked).
	Withdrawable *big.Int
	LockedUntil  time.Time
	// PendingWithdrawal is withdrawn principal the pool has not paid yet
	// (paid to this wallet only, by payPending).
	PendingWithdrawal *big.Int
}

func (v *LiquidityVault) Position(ctx context.Context, lp string) (*VaultPosition, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	if !common.IsHexAddress(lp) {
		return nil, fmt.Errorf("liquidity vault: bad LP address %q", lp)
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	head, err := v.client.Client.BlockNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: read head: %w", err)
	}
	at, who := new(big.Int).SetUint64(head), common.HexToAddress(lp)
	pos := &VaultPosition{}
	out, err := v.callAt(ctx, at, "principal", who)
	if err != nil {
		return nil, err
	}
	pos.Principal = out[0].(*big.Int)
	if out, err = v.callAt(ctx, at, "claimable", who); err != nil {
		return nil, err
	}
	pos.Claimable = out[0].(*big.Int)
	if out, err = v.callAt(ctx, at, "depositor", who); err != nil {
		return nil, err
	}
	pos.Allowed = out[0].(bool)
	if out, err = v.callAt(ctx, at, "withdrawable", who); err != nil {
		return nil, err
	}
	pos.Withdrawable = out[0].(*big.Int)
	if out, err = v.callAt(ctx, at, "pendingWithdrawal", who); err != nil {
		return nil, err
	}
	pos.PendingWithdrawal = out[0].(*big.Int)
	if pos.Withdrawable.Cmp(pos.Principal) < 0 {
		if out, err = v.callAt(ctx, at, "lockedUntil", who); err != nil {
			return nil, err
		}
		pos.LockedUntil = time.Unix(out[0].(*big.Int).Int64(), 0).UTC()
	}
	return pos, nil
}

// Wiring is what the pool gates check before any order is taken.
type VaultWiring struct {
	Token     common.Address
	Operator  bool // the release key is one of the vault's operators
	Recipient bool // the treasury may receive fills
}

func (v *LiquidityVault) Wiring(ctx context.Context, operator, treasury string) (*VaultWiring, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	w := &VaultWiring{}
	out, err := v.callAt(ctx, nil, "token")
	if err != nil {
		return nil, err
	}
	w.Token = out[0].(common.Address)
	if out, err = v.callAt(ctx, nil, "operators", common.HexToAddress(operator)); err != nil {
		return nil, err
	}
	w.Operator = out[0].(bool)
	if out, err = v.callAt(ctx, nil, "recipient", common.HexToAddress(treasury)); err != nil {
		return nil, err
	}
	w.Recipient = out[0].(bool)
	return w, nil
}

// VaultRoles are the wallets the console signs with.
type VaultRoles struct {
	CollateralProvider string
	Guardian           string
}

func (v *LiquidityVault) Roles(ctx context.Context) (*VaultRoles, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	r := &VaultRoles{}
	for _, f := range []struct {
		method string
		into   *string
	}{{"collateralProvider", &r.CollateralProvider}, {"guardian", &r.Guardian}} {
		out, err := v.callAt(ctx, nil, f.method)
		if err != nil {
			return nil, err
		}
		*f.into = out[0].(common.Address).Hex()
	}
	return r, nil
}

// PendingWithdrawal is lp's pending withdrawal at the head block.
func (v *LiquidityVault) PendingWithdrawal(ctx context.Context, lp string) (*big.Int, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	if !common.IsHexAddress(lp) {
		return nil, fmt.Errorf("liquidity vault: bad LP address %q", lp)
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	out, err := v.callAt(ctx, nil, "pendingWithdrawal", common.HexToAddress(lp))
	if err != nil {
		return nil, err
	}
	return out[0].(*big.Int), nil
}

// FillUsed reports whether ref has already been filled on-chain.
func (v *LiquidityVault) FillUsed(ctx context.Context, ref [32]byte) (bool, error) {
	if v == nil {
		return false, ErrVaultUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	out, err := v.callAt(ctx, nil, "usedFillRef", ref)
	if err != nil {
		return false, err
	}
	return out[0].(bool), nil
}

func (v *LiquidityVault) callAt(ctx context.Context, block *big.Int, method string, args ...any) ([]any, error) {
	data, err := parsedLiquidityVaultABI.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: pack %s: %w", method, err)
	}
	raw, err := v.client.Client.CallContract(ctx, ethereum.CallMsg{To: &v.addr, Data: data}, block)
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: call %s: %w", method, err)
	}
	out, err := parsedLiquidityVaultABI.Unpack(method, raw)
	if err != nil || len(out) == 0 {
		return nil, fmt.Errorf("liquidity vault: decode %s: %v", method, err)
	}
	return out, nil
}

// ─── Unsigned-tx builders ───────────────────────────────────────────────────

// PrepareFill is the operator's fill (from = the release key).
func (v *LiquidityVault) PrepareFill(from, to string, amount, spread *big.Int, ref [32]byte) ([]byte, error) {
	return v.build(from, "fill", common.HexToAddress(to), amount, spread, ref)
}

// PrepareSetDepositor is the operator's allow-list write.
func (v *LiquidityVault) PrepareSetDepositor(from, account string, allowed bool) ([]byte, error) {
	return v.build(from, "setDepositor", common.HexToAddress(account), allowed)
}

// PrepareClaimFor pays an LP's earnings to the LP itself; anyone may send it.
func (v *LiquidityVault) PrepareClaimFor(from, lp string) ([]byte, error) {
	return v.build(from, "claimFor", common.HexToAddress(lp))
}

// PreparePayPending is the operator's payPending(lp): the LP's pending
// withdrawal, as far as the balance allows, paid to the LP and nowhere else.
func (v *LiquidityVault) PreparePayPending(from, lp string) ([]byte, error) {
	if !common.IsHexAddress(lp) {
		return nil, fmt.Errorf("liquidity vault: bad LP address %q", lp)
	}
	return v.build(from, "payPending", common.HexToAddress(lp))
}

// The LP's own txs, signed in the app.
func (v *LiquidityVault) PrepareDeposit(lp string, amount *big.Int) ([]byte, error) {
	return v.build(lp, "deposit", amount)
}

func (v *LiquidityVault) PrepareWithdraw(lp string, amount *big.Int) ([]byte, error) {
	return v.build(lp, "withdraw", amount)
}

func (v *LiquidityVault) PrepareClaim(lp string) ([]byte, error) { return v.build(lp, "claim") }

func (v *LiquidityVault) PrepareClaimTo(lp, to string) ([]byte, error) {
	if !common.IsHexAddress(to) {
		return nil, fmt.Errorf("liquidity vault: bad claim destination %q", to)
	}
	return v.build(lp, "claimTo", common.HexToAddress(to))
}

// build estimates gas (a call that would revert is refused here, never sent)
// and returns the UnsignedTx JSON every signer in this codebase takes. The
// nonce is a hint: a KeyLane always chooses its own.
func (v *LiquidityVault) build(from, method string, args ...any) ([]byte, error) {
	if v == nil {
		return nil, ErrVaultUnavailable
	}
	data, err := parsedLiquidityVaultABI.Pack(method, args...)
	if err != nil {
		return nil, fmt.Errorf("liquidity vault: pack %s: %w", method, err)
	}
	unsigned, err := v.client.PrepareContractCall(from, v.addr.Hex(), data, nil, 0)
	if errors.Is(err, blockchain.ErrEstimateGas) {
		return nil, withRevertReason(err)
	}
	return unsigned, err
}

// ─── Events ─────────────────────────────────────────────────────────────────

// Vault event kinds, as stored in liquidity_ledger.kind. Withdrawn carries
// what a withdraw paid at once; WithdrawalPending the rest of it (no longer
// principal), WithdrawalPaid a later payPending of it.
const (
	VaultEventDeposited         = "deposited"
	VaultEventWithdrawn         = "withdrawn"
	VaultEventWithdrawalPending = "withdrawal_pending"
	VaultEventWithdrawalPaid    = "withdrawal_paid"
	VaultEventClaimed           = "claimed"
	VaultEventFilled            = "filled"
	VaultEventRefilled          = "refilled"
	VaultEventDepositorSet      = "depositor_set"
	VaultEventOperatorSet       = "operator_set"
	vaultEventLegacySettled     = "settled_from_collateral" // rows indexed before the no-collateral upgrade
)

var vaultEventKinds = map[string]string{
	"Deposited": VaultEventDeposited, "Withdrawn": VaultEventWithdrawn, "Claimed": VaultEventClaimed,
	"WithdrawalPending": VaultEventWithdrawalPending, "WithdrawalPaid": VaultEventWithdrawalPaid,
	"Filled": VaultEventFilled, "Refilled": VaultEventRefilled,
	"DepositorSet": VaultEventDepositorSet, "OperatorSet": VaultEventOperatorSet,
	// Emitted only before the no-collateral upgrade (testnet history): kept so
	// a ledger rebuilt from the deploy block still subtracts those settles.
	"SettledFromCollateral": vaultEventLegacySettled,
}

// VaultEvent is one decoded vault log. Fields a kind does not carry are zero.
type VaultEvent struct {
	Kind     string
	Account  string // lp, or the depositor named by DepositorSet
	To       string // claim or fill destination
	Amount   *big.Int
	Spread   *big.Int
	Ref      string // 0x-hex bytes32 for fills and refills
	Allowed  bool   // DepositorSet
	TxHash   string
	LogIndex uint
	Block    uint64
}

// VaultEventTopics is every event the indexer asks the node for.
func VaultEventTopics() []common.Hash {
	out := make([]common.Hash, 0, len(vaultEventKinds))
	for name := range vaultEventKinds {
		out = append(out, parsedLiquidityVaultABI.Events[name].ID)
	}
	return out
}

// DecodeVaultLog decodes one log emitted by the vault; ok is false for a log
// that is not one of the vault's events (or not from the vault).
func (v *LiquidityVault) DecodeVaultLog(l types.Log) (VaultEvent, bool) {
	if v == nil || l.Address != v.addr || len(l.Topics) == 0 {
		return VaultEvent{}, false
	}
	ev, err := parsedLiquidityVaultABI.EventByID(l.Topics[0])
	if err != nil {
		return VaultEvent{}, false
	}
	kind, ok := vaultEventKinds[ev.Name]
	if !ok {
		return VaultEvent{}, false
	}
	vals := map[string]any{}
	if err := parsedLiquidityVaultABI.UnpackIntoMap(vals, ev.Name, l.Data); err != nil {
		return VaultEvent{}, false
	}
	indexed := 1
	for _, in := range ev.Inputs {
		if in.Indexed {
			if indexed >= len(l.Topics) {
				return VaultEvent{}, false
			}
			t := l.Topics[indexed]
			indexed++
			switch in.Type.T {
			case abi.AddressTy:
				vals[in.Name] = common.BytesToAddress(t.Bytes())
			default:
				vals[in.Name] = [32]byte(t)
			}
		}
	}
	out := VaultEvent{Kind: kind, TxHash: l.TxHash.Hex(), LogIndex: l.Index, Block: l.BlockNumber}
	if a, ok := vals["lp"].(common.Address); ok {
		out.Account = a.Hex()
	}
	if a, ok := vals["account"].(common.Address); ok {
		out.Account = a.Hex()
	}
	if a, ok := vals["to"].(common.Address); ok {
		out.To = a.Hex()
	}
	if n, ok := vals["amount"].(*big.Int); ok {
		out.Amount = n
	}
	if n, ok := vals["spread"].(*big.Int); ok {
		out.Spread = n
	}
	if r, ok := vals["ref"].([32]byte); ok {
		out.Ref = "0x" + common.Bytes2Hex(r[:])
	}
	if b, ok := vals["allowed"].(bool); ok {
		out.Allowed = b
	}
	return out, true
}

// FilledIn finds the Filled event for ref in a receipt; nil when the receipt
// does not carry it (another call, a revert, or another vault).
func (v *LiquidityVault) FilledIn(r *types.Receipt, ref [32]byte) *VaultEvent {
	if v == nil || r == nil {
		return nil
	}
	want := "0x" + common.Bytes2Hex(ref[:])
	for _, l := range r.Logs {
		if l == nil {
			continue
		}
		if ev, ok := v.DecodeVaultLog(*l); ok && ev.Kind == VaultEventFilled && ev.Ref == want {
			return &ev
		}
	}
	return nil
}
