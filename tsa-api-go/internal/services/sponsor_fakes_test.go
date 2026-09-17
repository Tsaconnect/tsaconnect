package services

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	sponsorTestSigner  = "0x5a0b54d5dc17e0aadc383d2db43b0a0d3e029c4c"
	sponsorTestUser    = "0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A"
	sponsorTestDeposit = "0x2222222222222222222222222222222222222222"
	sponsorTestUSDC    = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"
	sponsorTestBscUSDT = "0x55d398326f99059fF775485246999027B3197955"
)

var sponsorTestNow = time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)

type fakeSponsorOrders struct {
	orders map[uuid.UUID]*models.PaycrestOrderRecord
}

func (f *fakeSponsorOrders) Get(_ context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error) {
	o, ok := f.orders[orderID]
	if !ok || o.UserID != userID {
		return nil, ErrPaycrestOrderNotYours
	}
	cp := *o
	return &cp, nil
}

type fakeSponsorStore struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*models.GasSponsorship
	// orders resolves a top-up's order outcome for CountUnusedTopUpsSince, as
	// the SQL join does; rows whose order it does not hold are not counted.
	orders *fakeSponsorOrders
}

func newFakeSponsorStore() *fakeSponsorStore {
	return &fakeSponsorStore{rows: map[uuid.UUID]*models.GasSponsorship{}}
}

func (f *fakeSponsorStore) Reserve(_ context.Context, row *models.GasSponsorship) (*models.GasSponsorship, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.OrderID == row.OrderID && r.Kind == row.Kind {
			cp := *r
			return &cp, nil
		}
	}
	if row.ID == uuid.Nil {
		row.ID = uuid.New()
	}
	row.CreatedAt = sponsorTestNow
	cp := *row
	f.rows[row.ID] = &cp
	return nil, nil
}

func (f *fakeSponsorStore) SetTxHash(_ context.Context, id uuid.UUID, txHash, amountWei string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok {
		r.TxHash, r.AmountWei = txHash, amountWei
	}
	return nil
}

func (f *fakeSponsorStore) Release(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok && r.TxHash == "" {
		delete(f.rows, id)
	}
	return nil
}

func (f *fakeSponsorStore) CountUnusedTopUpsSince(_ context.Context, userID uuid.UUID, since time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, r := range f.rows {
		if r.UserID != userID || r.Kind != models.GasSponsorshipKindTopUp || r.CreatedAt.Before(since) || f.orders == nil {
			continue
		}
		if o, ok := f.orders.orders[r.OrderID]; ok && topUpUnused(o.Status, o.AmountPaid) {
			n++
		}
	}
	return n, nil
}

func (f *fakeSponsorStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

// onlyRow returns a copy of the single row held, for tests that reserve
// exactly one and want to inspect its recorded fields. Panics (via a nil
// deref at the caller) is deliberately avoided by returning nil when the
// count isn't exactly one, so a bad assumption fails the assertion, not here.
func (f *fakeSponsorStore) onlyRow() *models.GasSponsorship {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rows) != 1 {
		return nil
	}
	for _, r := range f.rows {
		cp := *r
		return &cp
	}
	return nil
}

// fakeSponsorChain answers by selector so each test controls the
// authorizationState read and the simulation independently.
type fakeSponsorChain struct {
	mu         sync.Mutex
	used       bool
	simErr     error
	balances   map[string]*big.Int // lowercase address -> wei; default 1 ether
	extra      map[string][]byte   // selector hex -> canned return, checked first
	code       map[string][]byte   // lowercase address -> code; default none (an EOA)
	codeErr    error
	balanceErr error // BalanceAt failure, for FloatBalances "carry on past a bad RPC" tests
}

func (f *fakeSponsorChain) CodeAt(_ context.Context, account common.Address, _ *big.Int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.codeErr != nil {
		return nil, f.codeErr
	}
	return f.code[strings.ToLower(account.Hex())], nil
}

// eip7702Code is a delegation designator: 0xef0100 followed by the delegate.
var eip7702Code = append([]byte{0xef, 0x01, 0x00}, common.HexToAddress("0x8a67b5020ee254ef48e3b6a04927f39baf7e408a").Bytes()...)

func (f *fakeSponsorChain) CallContract(_ context.Context, msg ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sel := hex.EncodeToString(msg.Data[:4])
	if out, ok := f.extra[sel]; ok {
		return out, nil
	}
	switch sel {
	case "e94a0102": // authorizationState
		out := make([]byte, 32)
		if f.used {
			out[31] = 1
		}
		return out, nil
	case "e3ee160e": // transferWithAuthorization simulation
		return nil, f.simErr
	}
	return nil, errors.New("unexpected call " + sel)
}

func (f *fakeSponsorChain) BalanceAt(_ context.Context, account common.Address, _ *big.Int) (*big.Int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.balanceErr != nil {
		return nil, f.balanceErr
	}
	if b, ok := f.balances[strings.ToLower(account.Hex())]; ok {
		return b, nil
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil), nil
}

func (f *fakeSponsorChain) ChainID() *big.Int { return big.NewInt(8453) }

type fakeSponsorBuilder struct {
	mu       sync.Mutex
	to       string
	data     []byte
	value    *big.Int
	gasLimit uint64
	err      error
}

func (f *fakeSponsorBuilder) BuildUnsignedTx(from, to string, callData []byte) ([]byte, error) {
	return f.BuildUnsignedCall(from, to, callData, nil, 0)
}

func (f *fakeSponsorBuilder) BuildUnsignedCall(_, to string, callData []byte, value *big.Int, gasLimit uint64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.to, f.data, f.value, f.gasLimit = to, callData, value, gasLimit
	if f.err != nil {
		return nil, f.err
	}
	return []byte(`{"unsigned":true}`), nil
}

// lastCall is the mutex-guarded accessor for what BuildUnsignedCall was last
// invoked with — reads the same fields under the same lock BuildUnsignedCall
// writes them under, instead of a data race waiting to happen under -race.
func (f *fakeSponsorBuilder) lastCall() (to string, data []byte, value *big.Int, gasLimit uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.to, f.data, f.value, f.gasLimit
}

type fakeSponsorNative struct {
	mu       sync.Mutex
	decimals int
	// decimalsByToken overrides `decimals` for specific token addresses, so one
	// lane can hold a 6dp and an 18dp stable at once.
	decimalsByToken map[string]int
	// decimalsErr simulates a transient RPC failure reading a token's
	// decimals (e.g. custody's own source-side read) — separate from
	// prepareErr, which is the send-side failure.
	decimalsErr   error
	estimate      uint64
	estimateErr   error
	gasPrice      *big.Int
	sentTo        string
	sentWei       *big.Int
	receiptStatus uint64
	receiptErr    error
	prepareErr    error

	// ERC-20 fields, for the treasury lane (Task 6). allowance is what
	// GetTokenAllowance answers with; nil means zero, so an approve is always
	// exercised unless a test sets one high enough to skip it.
	allowance    *big.Int
	allowanceErr error
	// allowanceDelay artificially widens the read-then-decide window inside
	// GetTokenAllowance, for the TreasuryLane.convertMu regression test: two
	// concurrent ExecuteRoute calls on one lane must not both observe the
	// same stale allowance.
	allowanceDelay time.Duration
	inFlight       int32 // atomic: calls currently inside GetTokenAllowance
	maxInFlight    int32 // atomic: high-water mark of the above

	approveToken   string
	approveOwner   string
	approveSpender string
	approveAmount  *big.Int
	approveErr     error

	transferToken string
	transferFrom  string
	transferTo    string
	transferAmt   *big.Int
	transferErr   error
}

func (f *fakeSponsorNative) PrepareNativeTransfer(_, to string, amountWei *big.Int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentTo, f.sentWei = to, new(big.Int).Set(amountWei)
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return []byte(`{"native":true}`), nil
}

func (f *fakeSponsorNative) EstimateGas(ethereum.CallMsg) (uint64, error) {
	return f.estimate, f.estimateErr
}
func (f *fakeSponsorNative) SuggestGasPrice() (*big.Int, error) { return f.gasPrice, nil }

// GetTokenDecimals answers per token address when a test needs a lane to hold
// tokens of differing precision (BSC stables are 18dp against 6dp elsewhere),
// falling back to the rig's single `decimals` otherwise.
func (f *fakeSponsorNative) GetTokenDecimals(token string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.decimalsErr != nil {
		return 0, f.decimalsErr
	}
	if d, ok := f.decimalsByToken[strings.ToLower(token)]; ok {
		return d, nil
	}
	return f.decimals, nil
}

func (f *fakeSponsorNative) WaitForReceiptContext(ctx context.Context, _ string, _ time.Duration) (*types.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.receiptErr != nil {
		return nil, f.receiptErr
	}
	return &types.Receipt{Status: f.receiptStatus}, nil
}

func (f *fakeSponsorNative) GetTokenAllowance(_, _, _ string) (*big.Int, error) {
	n := atomic.AddInt32(&f.inFlight, 1)
	defer atomic.AddInt32(&f.inFlight, -1)
	for {
		old := atomic.LoadInt32(&f.maxInFlight)
		if n <= old || atomic.CompareAndSwapInt32(&f.maxInFlight, old, n) {
			break
		}
	}
	if f.allowanceDelay > 0 {
		time.Sleep(f.allowanceDelay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.allowanceErr != nil {
		return nil, f.allowanceErr
	}
	if f.allowance != nil {
		return new(big.Int).Set(f.allowance), nil
	}
	return big.NewInt(0), nil
}

// maxAllowanceConcurrency is the high-water mark of concurrent
// GetTokenAllowance calls observed — 1 proves a caller-side lock (e.g.
// TreasuryLane.convertMu) serialised the allowance-read-then-decide section.
func (f *fakeSponsorNative) maxAllowanceConcurrency() int32 {
	return atomic.LoadInt32(&f.maxInFlight)
}

func (f *fakeSponsorNative) PrepareERC20Approve(tokenAddress, owner, spender string, amount *big.Int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approveToken, f.approveOwner, f.approveSpender = tokenAddress, owner, spender
	f.approveAmount = new(big.Int).Set(amount)
	if f.approveErr != nil {
		return nil, f.approveErr
	}
	return []byte(`{"approve":true}`), nil
}

// approveCall is the mutex-guarded accessor for the last PrepareERC20Approve
// call, mirroring PrepareERC20Approve's own lock.
func (f *fakeSponsorNative) approveCall() (token, owner, spender string, amount *big.Int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.approveToken, f.approveOwner, f.approveSpender, f.approveAmount
}

func (f *fakeSponsorNative) PrepareERC20Transfer(tokenAddress, from, to string, amount *big.Int) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transferToken, f.transferFrom, f.transferTo = tokenAddress, from, to
	f.transferAmt = new(big.Int).Set(amount)
	if f.transferErr != nil {
		return nil, f.transferErr
	}
	return []byte(`{"transfer":true}`), nil
}

// transferCall is the mutex-guarded accessor for the last
// PrepareERC20Transfer call, mirroring PrepareERC20Transfer's own lock.
func (f *fakeSponsorNative) transferCall() (token, from, to string, amount *big.Int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.transferToken, f.transferFrom, f.transferTo, f.transferAmt
}

type sponsorRig struct {
	orders  *fakeSponsorOrders
	store   *fakeSponsorStore
	chain   *fakeSponsorChain
	builder *fakeSponsorBuilder
	native  *fakeSponsorNative
	signer  *fakeRelaySigner
	lane    *SponsorLane
	userID  uuid.UUID
}

// newSponsorRig wires one lane under chainKey holding tokens base:USDC and
// bsc:USDT addresses, and one open sell order for the given chain/token.
func newSponsorRig(chainKey, token string) (*sponsorRig, uuid.UUID) {
	userID, orderID := uuid.New(), uuid.New()
	// Fixed rather than relative to sponsorTestNow: the ethers fixture in
	// authorization_relayer_test.go's validAuth() was signed over a specific
	// validBefore, so the order's expiry must be pinned to match it.
	validUntil := time.Unix(1790000600, 0).UTC()
	r := &sponsorRig{
		orders: &fakeSponsorOrders{orders: map[uuid.UUID]*models.PaycrestOrderRecord{
			orderID: {
				ID: orderID, UserID: userID,
				Direction: models.PaycrestDirectionSell, Status: models.PaycrestStatusAwaitingDeposit,
				ChainKey: chainKey, Token: token, TokenAmount: "36.421800",
				WalletAddress: sponsorTestUser, DepositAddress: sponsorTestDeposit, ValidUntil: &validUntil,
			},
		}},
		store:   newFakeSponsorStore(),
		chain:   &fakeSponsorChain{balances: map[string]*big.Int{}},
		builder: &fakeSponsorBuilder{},
		native:  &fakeSponsorNative{decimals: 6, estimate: 50_000, gasPrice: big.NewInt(1_000_000_000), receiptStatus: 1},
		signer:  &fakeRelaySigner{addr: sponsorTestSigner, txHash: "0xsponsored"},
		userID:  userID,
	}
	r.store.orders = r.orders
	r.lane = &SponsorLane{
		ChainKey: chainKey, Chain: r.chain, Builder: r.builder, Native: r.native, Signer: r.signer,
		Guard: NewSponsorGuard("ramp_sponsor_"+chainKey, SponsorLimits{
			DailyGasBudget: big.NewInt(10_000_000), BalanceFloor: big.NewInt(1_000),
		}, func() time.Time { return sponsorTestNow }),
		Tokens:      map[string]string{"USDC": sponsorTestUSDC, "USDT": sponsorTestBscUSDT},
		TopUpMaxWei: big.NewInt(1_000_000_000_000_000), // 0.001 native
	}
	return r, orderID
}

func (r *sponsorRig) lanes() map[string]*SponsorLane {
	return map[string]*SponsorLane{r.lane.ChainKey: r.lane}
}

// withBaseUSDCDomain makes the rig's chain answer name()/version()/
// DOMAIN_SEPARATOR() as Base's real USDC contract does, so a relayer test can
// read and verify the EIP-712 domain and recover a signature against it.
// Default extra stays empty: TestReadAuthorizationDomain-style tests elsewhere
// rely on name() failing when no domain has been configured.
func withBaseUSDCDomain(t *testing.T, r *sponsorRig) {
	t.Helper()
	r.chain.extra = map[string][]byte{
		"06fdde03": abiString(t, "USD Coin"),
		"54fd4d50": abiString(t, "2"),
		"3644e515": common.FromHex("0x02fa7265e7c5d81118673727957699e4d68f74cd74b7db77da710fe8a2c7834f"),
	}
}
