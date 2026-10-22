package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// ── fakes ──────────────────────────────────────────────────────────────────

// fakeLiFiQuoter stands in for LiFiQuoteClient. Counting calls is half the
// point: several tests assert that NO quote was requested, which is how
// "observe only" and "refuse before spending" are actually pinned.
type fakeLiFiQuoter struct {
	mu    sync.Mutex
	route *LiFiRoute
	err   error
	calls int
	last  LiFiQuoteRequest
	// all is every request, in order — for a test about BOTH asks a quoter makes.
	all []LiFiQuoteRequest
	// routes answers a SEQUENCE of quotes, one per call, falling back to
	// route once exhausted. The sell quoter asks twice — once to learn the
	// route's cost, once carrying our fee — and the two answers differ.
	routes []*LiFiRoute
	// errAfter delays err until after this many successful calls, so a test
	// can make the FIRST quote succeed and the fee-carrying one fail. The
	// zero value fails from the first call, which is what every existing
	// fixture expects.
	errAfter int
}

func (f *fakeLiFiQuoter) Quote(_ context.Context, req LiFiQuoteRequest) (*LiFiRoute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = req
	f.all = append(f.all, req)
	if f.err != nil && f.calls > f.errAfter {
		return nil, f.err
	}
	if len(f.routes) > 0 {
		r := f.routes[0]
		f.routes = f.routes[1:]
		return r, nil
	}
	return f.route, nil
}

func (f *fakeLiFiQuoter) quoteCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLiFiQuoter) lastRequest() LiFiQuoteRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// fakeTransferVerifier stands in for LogTransferVerifier through the existing
// ERC20TransferVerifier interface.
type fakeTransferVerifier struct {
	mu     sync.Mutex
	amount *big.Int
	err    error
	calls  int
	// errByTx / amountByTx answer for ONE transaction, overriding the defaults.
	// The bridge landing and the top-up are different transactions on the same
	// chain, so a test that needs one to verify and the other not has to be able
	// to say which is which.
	errByTx    map[string]error
	amountByTx map[string]*big.Int
}

func (f *fakeTransferVerifier) VerifyTransfer(_ context.Context, txHash, _, _ string) (*big.Int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if err, ok := f.errByTx[txHash]; ok {
		return nil, err
	}
	if amount, ok := f.amountByTx[txHash]; ok {
		return new(big.Int).Set(amount), nil
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.amount == nil {
		return new(big.Int), nil
	}
	return new(big.Int).Set(f.amount), nil
}

// fakeLiFiStatus serves LiFi's /status shape so the orchestrator can be driven
// through the REAL LiFiStatusClient (the constructor takes the concrete type),
// parsing included.
type fakeLiFiStatus struct {
	mu        sync.Mutex
	status    string
	substatus string
	amount    string
	toAddress string
	// receivingToken is the token LiFi says it delivered. A native coin
	// leaves no Transfer log to read, so on that path this is the only thing
	// that says the user got the asset they actually asked for.
	receivingToken string
	// chainID is receiving.chainId. Settable so a BSC-destination test can
	// report 56 rather than the polygon default every other test wants.
	chainID int64
	srv     *httptest.Server
}

func newFakeLiFiStatus(t *testing.T, toAddress string) *fakeLiFiStatus {
	t.Helper()
	f := &fakeLiFiStatus{status: LiFiStatusPending, substatus: LiFiSubstatusCompleted, toAddress: toAddress, chainID: 137}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		body := map[string]any{
			"status":    f.status,
			"substatus": f.substatus,
			"receiving": map[string]any{
				"txHash":    "0xdest",
				"chainId":   f.chainID,
				"amount":    f.amount,
				"toAddress": f.toAddress,
				"token":     map[string]any{"address": f.receivingToken},
			},
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeLiFiStatus) set(status, amount string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.amount = status, amount
}

func (f *fakeLiFiStatus) setSubstatus(sub string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.substatus = sub
}

func (f *fakeLiFiStatus) setToken(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.receivingToken = addr
}

func (f *fakeLiFiStatus) setRecipient(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toAddress = addr
}

// stubLegStore wraps the real store so a test can force the two outcomes a
// concurrent tick produces — Reserve losing the row, and Confirm/Fail finding
// the row already terminal — without needing two goroutines to collide.
type stubLegStore struct {
	rampLegStore
	reserveExisting *models.RampLeg
	confirmErr      error
	failErr         error
}

func (s *stubLegStore) Reserve(ctx context.Context, leg *models.RampLeg) (*models.RampLeg, error) {
	if s.reserveExisting != nil {
		return s.reserveExisting, nil
	}
	return s.rampLegStore.Reserve(ctx, leg)
}

func (s *stubLegStore) Confirm(ctx context.Context, id uuid.UUID, amountReceived string, at time.Time) error {
	if s.confirmErr != nil {
		return s.confirmErr
	}
	return s.rampLegStore.Confirm(ctx, id, amountReceived, at)
}

func (s *stubLegStore) Fail(ctx context.Context, id uuid.UUID, reason string) error {
	if s.failErr != nil {
		return s.failErr
	}
	return s.rampLegStore.Fail(ctx, id, reason)
}

func (s *stubLegStore) FailExpected(ctx context.Context, id uuid.UUID, txHash, reason string) error {
	if s.failErr != nil {
		return s.failErr
	}
	return s.rampLegStore.FailExpected(ctx, id, txHash, reason)
}

// ── rig ────────────────────────────────────────────────────────────────────

// conversionRig is one converted buy mid-flight: Paycrest has already paid the
// treasury 100 USDC on base (holder=treasury), the user asked for USDT on
// polygon, and the promise is 99.5 (100 less 50 bps).
type conversionRig struct {
	t         *testing.T
	db        *gorm.DB
	svc       *ConversionOrchestrator
	legStore  *GormRampLegStore
	lifi      *fakeLiFiQuoter
	transfers *fakeTransferVerifier // base: proves Paycrest's delivery
	// dstTransfers is polygon: proves what the BRIDGE actually credited the
	// user. A separate verifier because each one wraps a single chain's RPC.
	dstTransfers *fakeTransferVerifier
	status       *fakeLiFiStatus
	treasury     *Treasury
	chains       *BlockchainService
	src          *sponsorRig // base: where Paycrest delivered
	dst          *sponsorRig // polygon: where the user is paid
	order        *models.PaycrestOrderRecord
	clock        time.Time
}

func newConversionRig(t *testing.T) *conversionRig {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}, &models.RampLeg{}, &models.SellerPayoutOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	src, _ := newSponsorRig("base", "USDC")
	dst, _ := newSponsorRig("polygon", "USDT")
	r := &conversionRig{t: t, db: db, clock: sponsorTestNow, src: src, dst: dst}

	r.treasury = NewTreasury(map[string]*TreasuryLane{
		"base":    {SponsorLane: src.lane, ChainID: 8453, Routers: treasuryTestRouters},
		"polygon": {SponsorLane: dst.lane, ChainID: 137, Routers: treasuryTestRouters},
	}, TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 10000},
		func() time.Time { return r.clock })

	// The USD fields price the round trip: 100 in, 99.8 out is a 20 bps LiFi
	// cost, comfortably inside the 50 bps budget set below.
	r.lifi = &fakeLiFiQuoter{route: &LiFiRoute{
		To: treasuryTestRouter, Data: rigRouteData(t), ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000),
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across",
	}}
	r.transfers = &fakeTransferVerifier{amount: big.NewInt(100_000000)}
	r.dstTransfers = &fakeTransferVerifier{amount: new(big.Int)}
	r.status = newFakeLiFiStatus(t, sponsorTestUser)
	r.legStore = &GormRampLegStore{DB: db}

	r.chains = &BlockchainService{cfg: &config.Config{Networks: map[string]config.NetworkConfig{
		"mainnet": {
			Chains:         map[string]config.ChainConfig{"base": {ChainID: 8453}, "polygon": {ChainID: 137}},
			TokenAddresses: map[string]string{"base:USDC": sponsorTestUSDC, "polygon:USDT": sponsorTestBscUSDT},
		},
	}}}

	r.order = &models.PaycrestOrderRecord{
		ID: uuid.New(), UserID: uuid.New(),
		Direction: models.PaycrestDirectionBuy, Status: models.PaycrestStatusConverting,
		Reference: "conv-" + uuid.NewString(),
		FiatCode:  "NGN", FiatAmount: "138000",
		Token: "USDC", ChainKey: "base", Network: "base",
		RouteKind: models.RouteKindConverted, RequestedToken: "USDT", RequestedChain: "polygon",
		// TokenAmount is a plain human-readable decimal string — exactly
		// what CreateBuy writes (Paycrest's own order.Amount) and what a
		// direct order has always stored, NEVER base units. "100" at the
		// rig's 6dp USDC is 100000000 base units, which is why every other
		// fixture/assertion in this file that already used that number
		// keeps working unchanged.
		MarkupBps: 50, TokenAmount: "100", PromisedAmount: "99500000",
		Holder: models.HolderTreasury, WalletAddress: sponsorTestUser,
		TxHash: "0xpaycrestdelivery", CreatedAt: r.clock,
	}
	if err := db.Create(r.order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	r.svc = r.newOrchestrator(r.legStore)
	return r
}

// newOrchestrator builds a service over a (possibly wrapped) leg store. Both
// chains get a verifier: the treasury can take custody on either.
func (r *conversionRig) newOrchestrator(legs rampLegStore) *ConversionOrchestrator {
	svc := NewConversionOrchestrator(r.db, r.treasury, legs, r.lifi,
		NewLiFiStatusClient(r.status.srv.URL, ""),
		map[string]ERC20TransferVerifier{"base": r.transfers, "polygon": r.dstTransfers},
		r.chains, 30*time.Minute, 0, 0, 0, func() time.Time { return r.clock })
	return svc
}

// seedLeg inserts a leg stamped on the INJECTED clock. GORM would otherwise
// stamp CreatedAt from the wall clock, and the stall detector reads leg
// timestamps — so a wall-clock row would sit permanently in the future.
func (r *conversionRig) seedLeg(t *testing.T, leg *models.RampLeg) *models.RampLeg {
	t.Helper()
	leg.CreatedAt = r.clock.UTC()
	if _, err := r.legStore.Reserve(context.Background(), leg); err != nil {
		t.Fatalf("seed leg: %v", err)
	}
	return leg
}

func (r *conversionRig) reload(t *testing.T) *models.PaycrestOrderRecord {
	t.Helper()
	var out models.PaycrestOrderRecord
	if err := r.db.First(&out, "id = ?", r.order.ID).Error; err != nil {
		t.Fatalf("reload order: %v", err)
	}
	return &out
}

func (r *conversionRig) legsOfKind(t *testing.T, kind string) []models.RampLeg {
	t.Helper()
	all, err := r.legStore.ForOrder(context.Background(), r.order.ID)
	if err != nil {
		t.Fatalf("legs: %v", err)
	}
	var out []models.RampLeg
	for _, l := range all {
		if l.Kind == kind {
			out = append(out, l)
		}
	}
	return out
}

// broadcasts counts every signed transaction across both lanes: the money
// question in almost every test here is "how many times did we send?".
func (r *conversionRig) broadcasts() int {
	return r.src.signer.calls() + r.dst.signer.calls()
}

// completeBridge makes LiFi report the bridge as DONE/COMPLETED with what
// landed AND makes the destination chain agree, which is the normal case. A
// settlement needs both: LiFi only names the transaction, the chain says what
// the wallet was credited.
func (r *conversionRig) completeBridge(t *testing.T, amount string) {
	t.Helper()
	r.status.set(LiFiStatusDone, amount)
	r.status.setSubstatus(LiFiSubstatusCompleted)
	v, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		t.Fatalf("bad amount %q", amount)
	}
	r.dstTransfers.mu.Lock()
	r.dstTransfers.amount = v
	r.dstTransfers.mu.Unlock()
}

// mustAdvance runs one step against the CURRENT row and fails the test on
// anything except ErrLiFiNoRoute, the one retryable outcome the 30-minute
// window exists to absorb.
func mustAdvance(t *testing.T, r *conversionRig) {
	t.Helper()
	if err := r.svc.Advance(context.Background(), r.reload(t)); err != nil && !errors.Is(err, ErrLiFiNoRoute) {
		t.Fatalf("Advance: %v", err)
	}
}

// ── the money rules ────────────────────────────────────────────────────────

func TestOrchestrator_SettlesWhenTheConversionMeetsThePromise(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t), ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}
	if err := r.svc.Advance(context.Background(), r.order); err != nil {
		t.Fatal(err)
	}
	r.completeBridge(t, "99800000") // LiFi reports DONE with what landed
	if err := r.svc.Advance(context.Background(), r.reload(t)); err != nil {
		t.Fatal(err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusSettled || got.Holder != models.HolderUser {
		t.Fatalf("order = %s/%s; want settled and held by the user", got.Status, got.Holder)
	}
	if got.OwedToUser != "0" {
		t.Fatalf("owedToUser = %q; a met promise owes nothing", got.OwedToUser)
	}
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusConfirmed || legs[0].AmountReceived != "99800000" {
		t.Fatalf("bridge legs = %+v; want one confirmed leg recording what landed", legs)
	}
	if legs[0].TxHash == "" || legs[0].LiFiTool != "across" {
		t.Fatalf("bridge leg = %+v; want the broadcast hash and the tool recorded", legs[0])
	}
	// The route is quoted fresh, from the treasury, straight to the user.
	req := r.lifi.lastRequest()
	if req.FromAddress != r.treasury.Address() || req.ToAddress != r.order.WalletAddress {
		t.Fatalf("quote = %+v; want treasury -> the user's own wallet", req)
	}
	if req.FromChainID != 8453 || req.ToChainID != 137 || req.FromAmount != "100000000" {
		t.Fatalf("quote = %+v; want base -> polygon for the full delivered amount", req)
	}
}

func TestOrchestrator_TopsUpAShortfallInsideTwentyBps(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t), ToAmount: big.NewInt(99_600000), ToAmountMin: big.NewInt(99_500000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}
	mustAdvance(t, r)
	r.completeBridge(t, "99400000") // 10 bps under the 99_500000 promise
	mustAdvance(t, r)
	legs := r.legsOfKind(t, models.RampLegKindDeliver)
	if len(legs) != 1 || legs[0].Amount != "100000" {
		t.Fatalf("deliver legs = %+v; want one for the 0.1 USDC shortfall", legs)
	}
	// The top-up is paid in the token the user actually received, on the chain
	// they received it — not in the token Paycrest delivered to us.
	if legs[0].ChainKey != "polygon" || legs[0].Token != "USDT" || legs[0].ToAddr != r.order.WalletAddress {
		t.Fatalf("deliver leg = %+v; want the requested token on the requested chain, to the user", legs[0])
	}
	if r.reload(t).Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s, want settled after the top-up", r.reload(t).Status)
	}
}

func TestOrchestrator_RefundsInTheRouteTokenAfterTheRetryWindow(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.err = ErrLiFiNoRoute
	mustAdvance(t, r) // attempt 1 fails
	r.clock = r.clock.Add(31 * time.Minute)
	mustAdvance(t, r) // broadcasts the refund; a broadcast is not proof, so it stays pending
	mustAdvance(t, r) // observeRefund verifies it on-chain and closes the order
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRefunded || got.Fault != models.FaultPlatform {
		t.Fatalf("order = %s fault=%s; want refunded, our fault", got.Status, got.Fault)
	}
	legs := r.legsOfKind(t, models.RampLegKindRefund)
	// Markup refunded: the user gets the FULL Paycrest amount, not the promise.
	if len(legs) != 1 || legs[0].Amount != "100000000" || legs[0].ToAddr != r.order.WalletAddress {
		t.Fatalf("refund legs = %+v; want the full amount to the order's own wallet", legs)
	}
	// Refunded in what the treasury actually holds: the route token, on the
	// chain Paycrest delivered to.
	if legs[0].ChainKey != "base" || legs[0].Token != "USDC" {
		t.Fatalf("refund leg = %+v; want USDC on base", legs[0])
	}
	token, _, to, amount := r.src.native.transferCall()
	if token != sponsorTestUSDC || !strings.EqualFold(to, r.order.WalletAddress) || amount.Cmp(big.NewInt(100_000000)) != 0 {
		t.Fatalf("transfer = %s %s %v; want the full USDC amount to the user", token, to, amount)
	}
}

// ── a lane that cannot pay for gas ─────────────────────────────────────────

func setSourceGas(r *conversionRig, wei *big.Int) {
	r.src.chain.balances[strings.ToLower(sponsorTestSigner)] = wei
}

// A treasury lane that cannot pay for the bridge sends nothing and fails the
// tick, so the worker's failure-streak alert pages someone. A tick that
// "succeeds" while every conversion is stuck is how an empty lane goes
// unnoticed.
func TestOrchestrator_ALaneThatCannotPayFailsTheTickAndSendsNothing(t *testing.T) {
	r := newConversionRig(t)
	setSourceGas(r, big.NewInt(1))

	err := r.svc.Advance(context.Background(), r.reload(t))

	if !errors.Is(err, ErrRelayerUnderfunded) {
		t.Fatalf("Advance = %v, want ErrRelayerUnderfunded so the tick reports it", err)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d, want none", r.broadcasts())
	}
}

// A refund blocked on gas waits without writing a leg per tick — each one
// would read as fresh activity and hide the order from the stall detector —
// and goes out on its own once the lane is topped up.
func TestOrchestrator_ARefundWaitsForGasWithoutPilingUpLegs(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.err = ErrLiFiNoRoute
	mustAdvance(t, r) // attempt 1 fails
	r.clock = r.clock.Add(31 * time.Minute)
	setSourceGas(r, big.NewInt(1))

	for i := 0; i < 3; i++ {
		if err := r.svc.Advance(context.Background(), r.reload(t)); !errors.Is(err, ErrRelayerUnderfunded) {
			t.Fatalf("tick %d: Advance = %v, want ErrRelayerUnderfunded", i, err)
		}
	}
	if legs := r.legsOfKind(t, models.RampLegKindRefund); len(legs) != 0 {
		t.Fatalf("refund legs = %d; a refund that cannot pay for gas must not write one per tick", len(legs))
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusRefunding {
		t.Fatalf("status = %s, want refunding while it waits", got.Status)
	}

	setSourceGas(r, new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	mustAdvance(t, r)
	if legs := r.legsOfKind(t, models.RampLegKindRefund); len(legs) != 1 || legs[0].TxHash == "" {
		t.Fatalf("refund legs = %+v; want the refund broadcast once gas is back", legs)
	}
}

// ── one leg at a time ──────────────────────────────────────────────────────

func TestOrchestrator_NeverSendsTwiceForOneAttempt(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	after := r.broadcasts()
	if after == 0 {
		t.Fatal("first Advance broadcast nothing")
	}
	mustAdvance(t, r) // the leg is still pending: observe only
	if r.broadcasts() != after {
		t.Fatalf("broadcasts = %d, want %d — a pending leg must never be re-sent", r.broadcasts(), after)
	}
	if legs := r.legsOfKind(t, models.RampLegKindBridge); len(legs) != 1 {
		t.Fatalf("bridge legs = %d, want exactly one", len(legs))
	}
	if r.lifi.quoteCalls() != 1 {
		t.Fatalf("quotes = %d, want 1 — a pending leg must not be re-quoted", r.lifi.quoteCalls())
	}
}

// The leg row is the lock, and it is taken BEFORE the quote — otherwise two
// ticks can both sit in a slow LiFi round-trip and both broadcast. Proof: a
// quote that fails still leaves a leg row behind.
func TestOrchestrator_ReservesTheLegBeforeQuoting(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.err = ErrLiFiNoRoute

	mustAdvance(t, r)

	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 {
		t.Fatalf("bridge legs = %+v; the attempt must be reserved before the quote is asked for", legs)
	}
	if legs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("bridge leg = %+v; a quote failure never broadcast, so the attempt must be freed", legs[0])
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; a failed quote must send nothing", r.broadcasts())
	}
}

// A tick that loses the Reserve race does nothing at all: no quote, no send.
func TestOrchestrator_DoesNothingWhenAnotherTickOwnsTheAttempt(t *testing.T) {
	r := newConversionRig(t)
	stub := &stubLegStore{
		rampLegStore:    r.legStore,
		reserveExisting: &models.RampLeg{ID: uuid.New(), OrderID: r.order.ID, Kind: models.RampLegKindBridge, Attempt: 1},
	}
	svc := r.newOrchestrator(stub)

	if err := svc.Advance(context.Background(), r.order); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if r.lifi.quoteCalls() != 0 || r.broadcasts() != 0 {
		t.Fatalf("quotes=%d broadcasts=%d; the tick that lost the row must do nothing",
			r.lifi.quoteCalls(), r.broadcasts())
	}
}

// A pending deliver/refund leg has no oracle to ask, so the order stops for a
// human rather than guessing whether the transfer landed.
func TestOrchestrator_StopsWhenADeliverLegIsStalled(t *testing.T) {
	r := newConversionRig(t)
	r.seedLeg(t, &models.RampLeg{
		OrderID: r.order.ID, Kind: models.RampLegKindDeliver, Attempt: 1,
		ChainKey: "polygon", Token: "USDT", ToAddr: r.order.WalletAddress,
		Amount: "100000", Status: models.RampLegStatusPending, TxHash: "0xstalled",
	})
	r.dstTransfers.err = ErrTransferNotFound // the receipt is not readable

	mustAdvance(t, r)

	if r.broadcasts() != 0 || r.lifi.quoteCalls() != 0 {
		t.Fatalf("broadcasts=%d quotes=%d; a stalled leg must freeze the order", r.broadcasts(), r.lifi.quoteCalls())
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; want the order left where it was", got.Status)
	}
}

// I6: the kill switch is exactly the checkCaps failure the final review wave
// asked to be parked rather than bare-errored — Holder is already Treasury
// and zero legs exist yet, so a bare error here used to be a silent
// permanent stall (afterStep's own stall detector never trips on zero legs).
func TestOrchestrator_RefusesWhenTheTreasuryIsKilled(t *testing.T) {
	r := newConversionRig(t)
	r.treasury.SetKillSwitch(true)

	if err := r.svc.Advance(context.Background(), r.order); err != nil {
		t.Fatal(err)
	}
	if got := r.legsOfKind(t, models.RampLegKindBridge); len(got) != 0 {
		t.Fatalf("legs = %+v; a paused treasury must reserve nothing", got)
	}
	if r.broadcasts() != 0 || r.lifi.quoteCalls() != 0 {
		t.Fatalf("broadcasts=%d quotes=%d; want none while paused", r.broadcasts(), r.lifi.quoteCalls())
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("status = %s; a paused treasury must park the order, not silently stall it", got.Status)
	}
	if got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %s, want treasury (custody was never in question)", got.Holder)
	}
}

// ── taking custody ─────────────────────────────────────────────────────────

func TestOrchestrator_WaitsForTheDeliveryToConfirm(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.transfers.err = ErrTransferNotFound

	mustAdvance(t, r)

	if r.lifi.quoteCalls() != 0 {
		t.Fatalf("quotes = %d; nothing may be converted before the delivery is proven", r.lifi.quoteCalls())
	}
	if got := r.reload(t); got.Holder != models.HolderPaycrest || got.Status != models.PaycrestStatusConverting {
		t.Fatalf("order = %s/%s; want it still waiting on Paycrest", got.Status, got.Holder)
	}
}

// No delivery hash yet means Paycrest has not paid at all: nothing is even
// looked up, let alone converted.
func TestOrchestrator_WaitsWhenThereIsNoDeliveryHashYet(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Updates(map[string]any{"holder": models.HolderPaycrest, "tx_hash": ""})

	mustAdvance(t, r)

	if r.transfers.calls != 0 {
		t.Fatalf("verifier calls = %d; there is no transaction to verify yet", r.transfers.calls)
	}
	if got := r.reload(t); got.Holder != models.HolderPaycrest {
		t.Fatalf("holder = %s; want it still with Paycrest", got.Holder)
	}
}

func TestOrchestrator_TakesCustodyOnceTheDeliveryIsProven(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %s, want treasury once the transfer log proves it landed", got.Holder)
	}
	// Proving custody is one step; the conversion itself waits for the next tick.
	if r.lifi.quoteCalls() != 0 {
		t.Fatalf("quotes = %d, want 0 — taking custody is its own step", r.lifi.quoteCalls())
	}
}

// One verifier wraps one RPC client, so an order on a chain with no verifier
// could never be proven. Failing closed beats an order that silently never
// confirms.
func TestOrchestrator_RefusesWhenNoVerifierCoversTheChain(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	svc := NewConversionOrchestrator(r.db, r.treasury, r.legStore, r.lifi,
		NewLiFiStatusClient(r.status.srv.URL, ""),
		map[string]ERC20TransferVerifier{"polygon": r.transfers}, // nothing for base
		r.chains, 30*time.Minute, 0, 0, 0, func() time.Time { return r.clock })

	err := svc.Advance(context.Background(), r.reload(t))
	if !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("Advance = %v, want ErrSponsorNotAvailable", err)
	}
	if got := r.reload(t); got.Holder != models.HolderPaycrest {
		t.Fatalf("holder = %s; an unprovable delivery must not be taken into custody", got.Holder)
	}
}

// A short Paycrest delivery rewrites BOTH the amount and the promise: a
// promise computed from money that never arrived would owe more than we hold.
func TestOrchestrator_ShortDeliveryRepricesThePromise(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.transfers.amount = big.NewInt(99_000000) // 1 USDC short

	mustAdvance(t, r)

	got := r.reload(t)
	// TokenAmount is a plain human-readable decimal string (Paycrest's own
	// order.Amount, exactly as CreateBuy writes it) — the correction must be
	// recorded in that SAME convention, not as a base-units integer string.
	if got.TokenAmount != "99" {
		t.Fatalf("tokenAmount = %q, want what actually landed (\"99\", the same decimal convention it started in)", got.TokenAmount)
	}
	// 99_000000 less the order's own 50 bps markup (source and destination are
	// both 6dp here, so the rescale is the identity).
	if want := PromisedAmount(big.NewInt(99_000000), 50).String(); got.PromisedAmount != want {
		t.Fatalf("promisedAmount = %q, want %q recomputed from the delivery", got.PromisedAmount, want)
	}
	if got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %s, want treasury", got.Holder)
	}
}

// Final review fix wave, I1: VerifyTransfer sums EVERY matching Transfer log
// in the transaction, so a provider settling another one of our orders in
// the SAME tx (both rows would carry the same hash), or any unrelated
// transfer riding along, can make `received` exceed what THIS order's own
// TokenAmount ever promised. The bridge only ever sends order.TokenAmount
// (see startBridge) — promising off the inflated figure would guarantee a
// shortfall the treasury pays from margin or parks, for money that was
// never this order's to begin with.
func TestOrchestrator_OverDeliveryClampsThePromiseToWhatWasAsked(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.transfers.amount = big.NewInt(150_000000) // 150 USDC landed in the tx — 50 more than this order's 100

	mustAdvance(t, r)

	got := r.reload(t)
	// token_amount is UNCHANGED: the extra 50 USDC was never this order's to
	// claim, so it must not be folded into what gets converted.
	if got.TokenAmount != "100" {
		t.Fatalf("tokenAmount = %q, want it left at \"100\" — the extra 50 was never this order's", got.TokenAmount)
	}
	// The promise is computed off the CLAMPED 100000000, never the raw
	// 150000000 that actually showed up in the transaction.
	if want := PromisedAmount(big.NewInt(100_000000), 50).String(); got.PromisedAmount != want {
		t.Fatalf("promisedAmount = %q, want %q clamped to this order's own amount, not the over-delivered figure", got.PromisedAmount, want)
	}
	if got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %s, want treasury", got.Holder)
	}
}

// ── one delivery, one claim ────────────────────────────────────────────────

// seedClaimant stores another converted buy carrying the rig order's delivery
// hash, already in custody for 100 USDC unless mutate says otherwise.
func seedClaimant(t *testing.T, r *conversionRig, mutate func(*models.PaycrestOrderRecord)) {
	t.Helper()
	other := &models.PaycrestOrderRecord{
		ID: uuid.New(), UserID: uuid.New(),
		Direction: models.PaycrestDirectionBuy, Status: models.PaycrestStatusConverting,
		Reference: "conv-" + uuid.NewString(), FiatCode: "NGN", FiatAmount: "138000",
		Token: "USDC", ChainKey: "base", Network: "base",
		RouteKind: models.RouteKindConverted, RequestedToken: "USDT", RequestedChain: "polygon",
		TokenAmount: "100", Holder: models.HolderTreasury, WalletAddress: sponsorTestDeposit,
		TxHash: r.order.TxHash, CreatedAt: r.clock,
	}
	if mutate != nil {
		mutate(other)
	}
	if err := r.db.Create(other).Error; err != nil {
		t.Fatalf("seed claimant: %v", err)
	}
}

// A delivery another order already took custody of cannot fund this one too.
// Paycrest reporting one hash against two orders — a bug or a compromised
// account — would otherwise have the treasury bridge the second order out of
// its own float.
func TestOrchestrator_RefusesADeliveryAnotherOrderAlreadyClaimed(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	seedClaimant(t, r, nil) // the tx's whole 100 USDC is already spoken for

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Holder != models.HolderPaycrest {
		t.Fatalf("holder = %s; custody of an already-claimed delivery must not be recorded", got.Holder)
	}
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s; a reused delivery must stop for a human", got.Status)
	}
	mustAdvance(t, r)
	if r.lifi.quoteCalls() != 0 || r.broadcasts() != 0 {
		t.Fatalf("quotes=%d broadcasts=%d; nothing may move on a reused delivery", r.lifi.quoteCalls(), r.broadcasts())
	}
}

// One Paycrest transaction genuinely settling two of our orders must still
// fund both: each takes its own amount out of what the transaction delivered.
func TestOrchestrator_ASharedSettlementFundsEachOrderOnce(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.transfers.amount = big.NewInt(200_000000)
	seedClaimant(t, r, nil)

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Holder != models.HolderTreasury || got.TokenAmount != "100" {
		t.Fatalf("holder=%s tokenAmount=%q; want custody of this order's full 100", got.Holder, got.TokenAmount)
	}
}

// What is left after other orders' claims is all this order can have.
func TestOrchestrator_OnlyTheUnclaimedRemainderIsThisOrders(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.transfers.amount = big.NewInt(150_000000)
	seedClaimant(t, r, nil)

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Holder != models.HolderTreasury || got.TokenAmount != "50" {
		t.Fatalf("holder=%s tokenAmount=%q; want custody of only the unclaimed 50", got.Holder, got.TokenAmount)
	}
	if want := PromisedAmount(big.NewInt(50_000000), 50).String(); got.PromisedAmount != want {
		t.Fatalf("promisedAmount = %q, want %q from the remainder", got.PromisedAmount, want)
	}
}

// Only a converted buy on the same chain that has actually TAKEN custody has
// claimed anything: an order still waiting on Paycrest, a direct order (paid
// to the user's own wallet, never counted in the treasury's receipt), or the
// same hash on another chain claim nothing.
func TestOrchestrator_OnlyCustodyTakenOnTheSameChainCountsAsAClaim(t *testing.T) {
	for name, mutate := range map[string]func(*models.PaycrestOrderRecord){
		"still with paycrest": func(o *models.PaycrestOrderRecord) { o.Holder = models.HolderPaycrest },
		"direct order":        func(o *models.PaycrestOrderRecord) { o.RouteKind = models.RouteKindDirect; o.Holder = "" },
		"another chain":       func(o *models.PaycrestOrderRecord) { o.ChainKey = "polygon" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newConversionRig(t)
			r.db.Model(r.order).Update("holder", models.HolderPaycrest)
			seedClaimant(t, r, mutate)

			mustAdvance(t, r)

			if got := r.reload(t); got.Holder != models.HolderTreasury || got.TokenAmount != "100" {
				t.Fatalf("holder=%s tokenAmount=%q; that order claimed nothing", got.Holder, got.TokenAmount)
			}
		})
	}
}

// A delivered order hands custody on to the user; its claim on the
// transaction must survive that, or the delivery frees up for reuse.
func TestOrchestrator_ADeliveredOrdersClaimStillCounts(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	seedClaimant(t, r, func(o *models.PaycrestOrderRecord) {
		o.Holder = models.HolderUser
		o.Status = models.PaycrestStatusSettled
	})

	mustAdvance(t, r)

	if got := r.reload(t); got.Holder != models.HolderPaycrest {
		t.Fatalf("holder = %s; a settled order's delivery was reused", got.Holder)
	}
}

// A claim whose amount cannot be read still holds its share; guessing zero
// would hand that share to this order.
func TestOrchestrator_AnUnreadableClaimStopsForAHuman(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.transfers.amount = big.NewInt(200_000000)
	seedClaimant(t, r, func(o *models.PaycrestOrderRecord) { o.TokenAmount = "not-a-number" })

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Holder != models.HolderPaycrest || got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("holder=%s status=%s; want no custody, parked for a human", got.Holder, got.Status)
	}
}

// ── the promise is denominated in what the USER receives ───────────────────

// 6dp in, 18dp out. Comparing a 6dp promise against an 18dp landing would make
// every such route look catastrophically short.
func TestOrchestrator_RestatesThePromiseIntoAnEighteenDecimalDestination(t *testing.T) {
	r := newConversionRig(t)
	r.dst.native.decimalsByToken = map[string]int{strings.ToLower(sponsorTestBscUSDT): 18}
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)

	mustAdvance(t, r)

	// 100 USDC at 6dp becomes 100 * 10^18 at the destination, less 50 bps.
	want := PromisedAmount(mustBig(t, "100000000000000000000"), 50).String()
	if got := r.reload(t).PromisedAmount; got != want {
		t.Fatalf("promisedAmount = %q, want %q in the DESTINATION token's units", got, want)
	}
}

// 18dp in, 6dp out. Scaling down truncates, which keeps the promise one we can
// always honour.
//
// token_amount is seeded as "100" — the plain human-decimal convention (see
// TokenAmount's own doc comment on the model) — NOT "100000000000000000000".
// That base-units-looking string was a stale fixture from before the
// TokenAmount fix: under the corrected (decimal-string) reading it means 10^20
// TOKENS, which at 18dp is 10^38 base units — far more than the 10^20 base
// units actually verified as received, so the test was silently taking the
// SHORT-DELIVERY branch instead of exercising the full-delivery 18dp->6dp
// rescale its own name promises. It only ever passed because the promise
// assertion is computed from `received` either way.
func TestOrchestrator_RestatesThePromiseIntoASixDecimalDestination(t *testing.T) {
	r := newConversionRig(t)
	r.src.native.decimalsByToken = map[string]int{strings.ToLower(sponsorTestUSDC): 18}
	r.transfers.amount = mustBig(t, "100000000000000000000") // 100 tokens at 18dp, in base units
	r.db.Model(r.order).Updates(map[string]any{
		"holder": models.HolderPaycrest, "token_amount": "100", // 100 tokens, human-decimal
	})
	logs := captureLog(t)

	mustAdvance(t, r)

	// Proves the FULL-delivery path actually ran, not the short-delivery
	// correction: the stale ("100000000000000000000") fixture this replaced
	// happened to produce the identical FINAL TokenAmount ("100") through
	// EITHER branch — expected(10^38) > received(10^20) fired the
	// short-delivery correction, which then rewrote TokenAmount back down to
	// "100" anyway — so the final-value check alone cannot tell the two
	// branches apart. The log line can: it is only ever emitted on the
	// short-delivery branch.
	if strings.Contains(logs.String(), "delivery short") {
		t.Fatalf("a short-delivery correction fired; want the full-delivery path. logs:\n%s", logs.String())
	}
	got := r.reload(t)
	if got.TokenAmount != "100" {
		t.Fatalf("tokenAmount = %q, want it UNCHANGED at \"100\"", got.TokenAmount)
	}
	want := PromisedAmount(big.NewInt(100_000000), 50).String()
	if got.PromisedAmount != want {
		t.Fatalf("promisedAmount = %q, want %q in the DESTINATION token's units", got.PromisedAmount, want)
	}
}

// ── the retry window ───────────────────────────────────────────────────────

// The window measures how long we have been TRYING, not how long ago the order
// was created. A bank payment that took an hour to clear must still get its
// conversion attempted.
func TestOrchestrator_AttemptsABridgeEvenWhenTheOrderIsOlderThanTheWindow(t *testing.T) {
	r := newConversionRig(t)
	r.clock = r.clock.Add(90 * time.Minute) // custody taken long after creation

	mustAdvance(t, r)

	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("bridge legs = %+v; attempt 1 is always allowed however old the order is", legs)
	}
	if got := r.legsOfKind(t, models.RampLegKindRefund); len(got) != 0 {
		t.Fatalf("refund legs = %+v; an order that was never tried must not be refunded", got)
	}
}

// Only an attempt that never broadcast may be retried.
func TestOrchestrator_RetriesWithAFreshQuoteWhenTheAttemptNeverBroadcast(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.err = ErrLiFiNoRoute
	mustAdvance(t, r) // attempt 1: quote failed, nothing sent

	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusFailed || legs[0].TxHash != "" {
		t.Fatalf("bridge legs = %+v; want attempt 1 failed with no broadcast", legs)
	}

	r.lifi.err = nil
	r.clock = r.clock.Add(5 * time.Minute) // still inside the window
	mustAdvance(t, r)

	legs = r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 2 || legs[1].Attempt != 2 || legs[1].Status != models.RampLegStatusPending {
		t.Fatalf("bridge legs = %+v; want a second attempt", legs)
	}
	if r.lifi.quoteCalls() != 2 {
		t.Fatalf("quotes = %d, want 2 — every attempt is quoted fresh, never reused", r.lifi.quoteCalls())
	}
}

// LiFi reporting FAILED on a transaction that DID broadcast is not permission
// to send again: it reports FAILED for stalls where the funds can still land.
func TestOrchestrator_NeverRetriesABridgeThatAlreadyBroadcast(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r) // attempt 1 broadcasts
	sent := r.broadcasts()
	r.status.set(LiFiStatusFailed, "")

	mustAdvance(t, r) // observe the FAILED verdict

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || got.Fault != models.FaultPlatform {
		t.Fatalf("order = %s fault=%s; a broadcast bridge that LiFi calls failed needs a human",
			got.Status, got.Fault)
	}
	if !got.NeedsOperator() {
		t.Fatal("order must surface to the operator queue")
	}

	// And no later tick may quietly start attempt 2.
	r.clock = r.clock.Add(5 * time.Minute)
	mustAdvance(t, r)
	if legs := r.legsOfKind(t, models.RampLegKindBridge); len(legs) != 1 {
		t.Fatalf("bridge legs = %+v; want no second attempt, ever", legs)
	}
	if r.broadcasts() != sent {
		t.Fatalf("broadcasts = %d, want %d", r.broadcasts(), sent)
	}
}

// ── what may be settled on ─────────────────────────────────────────────────

// A route whose guaranteed minimum is under the promise cannot honour it, so
// it is not a route we may take. The attempt is freed and retried.
func TestOrchestrator_RefusesARouteThatGuaranteesLessThanThePromise(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t),
		ToAmount: big.NewInt(99_600000), ToAmountMin: big.NewInt(99_000000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}

	mustAdvance(t, r)

	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; a route that cannot meet the promise must not be executed", r.broadcasts())
	}
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("bridge legs = %+v; want the attempt freed for a retry", legs)
	}

	// A later quote that does meet it goes through.
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t),
		ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}
	r.clock = r.clock.Add(2 * time.Minute)
	mustAdvance(t, r)
	if r.broadcasts() == 0 {
		t.Fatal("a route that meets the promise must be executed")
	}
}

// A route to an allowlisted router can still name someone else as receiver.
// That must be caught from the calldata before signing — LiFi's status only
// reports the real landing once the treasury's funds are gone.
func TestOrchestrator_RefusesARoutePayingSomeoneElse(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: testBridgeCalldata(t, sponsorTestDeposit, 137, false),
		ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}

	mustAdvance(t, r)

	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; a route paying another wallet must not be signed", r.broadcasts())
	}
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("bridge legs = %+v; want the attempt freed for a retry", legs)
	}

	// An honest quote on the next tick goes through.
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t),
		ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}
	r.clock = r.clock.Add(2 * time.Minute)
	mustAdvance(t, r)
	if r.broadcasts() == 0 {
		t.Fatal("a route paying the order's wallet must be executed")
	}
}

// DONE is not a statement that the recipient was paid. Only DONE + COMPLETED is.
func TestOrchestrator_NeverSettlesAPartialDelivery(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.status.set(LiFiStatusDone, "99800000")
	r.status.setSubstatus(LiFiSubstatusPartial)

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s; a PARTIAL delivery must never settle", got.Status)
	}
}

func TestOrchestrator_NeverSettlesABridgeTheToolRefunded(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.status.set(LiFiStatusDone, "99800000")
	r.status.setSubstatus(LiFiSubstatusRefunded)

	mustAdvance(t, r)

	if got := r.reload(t); got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s; a REFUNDED bridge must never settle", got.Status)
	}
}

func TestOrchestrator_NeverSettlesAnUnqualifiedDone(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.status.set(LiFiStatusDone, "99800000")
	r.status.setSubstatus("") // LiFi said DONE and nothing else

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; a DONE we cannot qualify must leave the order open", got.Status)
	}
	if legs := r.legsOfKind(t, models.RampLegKindBridge); legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("bridge leg = %+v; want it left pending", legs[0])
	}
}

func TestOrchestrator_NeverSettlesADoneWithoutAnAmount(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "0")

	mustAdvance(t, r)

	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; a DONE with nothing received must not settle", got.Status)
	}
}

// An unstated recipient is an unknown recipient.
func TestOrchestrator_NeverSettlesWhenTheRecipientIsUnstated(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.status.setRecipient("")
	r.completeBridge(t, "99800000")

	mustAdvance(t, r)

	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; want it left open", got.Status)
	}
	if legs := r.legsOfKind(t, models.RampLegKindBridge); legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("bridge leg = %+v; want it left pending", legs[0])
	}
}

// LiFi claiming DONE to an address that is not the user's wallet is not a
// settlement. Nothing moves and the order stays open.
func TestOrchestrator_RefusesADoneReportToAnotherRecipient(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.status.setRecipient(sponsorTestDeposit)
	r.completeBridge(t, "99800000")

	if err := r.svc.Advance(context.Background(), r.reload(t)); err == nil {
		t.Fatal("Advance = nil; a DONE crediting someone else must not settle the order")
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s, want it left open", got.Status)
	}
	if legs := r.legsOfKind(t, models.RampLegKindBridge); legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("bridge leg = %+v; want it left pending", legs[0])
	}
}

// ── shortfalls: the user is always made whole ──────────────────────────────

// Beyond 20 bps is a margin loss finance must SEE — but the user is still paid
// what they were promised, because that is what a promise is.
func TestOrchestrator_TopsUpAShortfallBeyondTheToleranceAndFlagsIt(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "98000000") // 150 bps under the promise
	mustAdvance(t, r)

	legs := r.legsOfKind(t, models.RampLegKindDeliver)
	if len(legs) != 1 || legs[0].Amount != "1500000" {
		t.Fatalf("deliver legs = %+v; want the full 1.5 shortfall paid", legs)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s; the user is made whole, so the order settles", got.Status)
	}
	if got.OwedToUser != "0" {
		t.Fatalf("owedToUser = %q; nothing is owed once the top-up lands", got.OwedToUser)
	}
}

// Above the automatic ceiling, something is wrong in a way a top-up should not
// paper over: stop for a human with the balance recorded.
func TestOrchestrator_StopsForAHumanWhenTheShortfallExceedsTheCeiling(t *testing.T) {
	r := newConversionRig(t)
	r.svc.maxShortfallUSD = 1 // a dollar
	mustAdvance(t, r)
	r.completeBridge(t, "90000000") // 9.5 short, well over the ceiling
	mustAdvance(t, r)

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || got.Fault != models.FaultPlatform {
		t.Fatalf("order = %s fault=%s; want recovery_pending, our fault", got.Status, got.Fault)
	}
	if got.OwedToUser != "9500000" {
		t.Fatalf("owedToUser = %q, want the 9.5 still owed", got.OwedToUser)
	}
	if legs := r.legsOfKind(t, models.RampLegKindDeliver); len(legs) != 0 {
		t.Fatalf("deliver legs = %+v; a gap this size is not paid automatically", legs)
	}
	// The bridge did land, so the ledger must keep saying so.
	if legs := r.legsOfKind(t, models.RampLegKindBridge); legs[0].Status != models.RampLegStatusConfirmed {
		t.Fatalf("bridge leg = %+v; a leg that landed is never rewritten as failed", legs[0])
	}
}

// ── the money must not move twice if the final status write is lost ────────
//
// Both of these simulate the one gap a leg reservation cannot close on its
// own: the transfer broadcast and the leg row landed, but the order-status
// update that would have made the order terminal did not. The next tick sees
// an order that still looks unfinished, and must recognise from the LEG LEDGER
// that the user has already been paid.

func TestOrchestrator_NeverTopsUpTwiceIfTheSettlementWriteIsLost(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t), ToAmount: big.NewInt(99_600000), ToAmountMin: big.NewInt(99_500000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}
	mustAdvance(t, r)
	r.completeBridge(t, "99400000")
	mustAdvance(t, r) // tops up the 0.1 shortfall and settles
	sent := r.broadcasts()

	// The top-up landed, but the settle write was lost.
	if err := r.db.Model(r.order).Update("status", models.PaycrestStatusConverting).Error; err != nil {
		t.Fatal(err)
	}
	mustAdvance(t, r)

	if legs := r.legsOfKind(t, models.RampLegKindDeliver); len(legs) != 1 {
		t.Fatalf("deliver legs = %+v; the user must never be topped up twice", legs)
	}
	if r.broadcasts() != sent {
		t.Fatalf("broadcasts = %d, want %d — nothing may be sent a second time", r.broadcasts(), sent)
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s; the ledger already shows the promise met, so settle", got.Status)
	}
}

func TestOrchestrator_NeverRefundsTwiceIfTheFinalWriteIsLost(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.err = ErrLiFiNoRoute
	mustAdvance(t, r)
	r.clock = r.clock.Add(31 * time.Minute)
	mustAdvance(t, r) // refunds in full and marks the order refunded
	sent := r.broadcasts()

	// The refund transfer landed, but the "refunded" write was lost, so the
	// order still reads as mid-refund.
	if err := r.db.Model(r.order).Update("status", models.PaycrestStatusRefunding).Error; err != nil {
		t.Fatal(err)
	}
	mustAdvance(t, r)

	if legs := r.legsOfKind(t, models.RampLegKindRefund); len(legs) != 1 {
		t.Fatalf("refund legs = %+v; the user must never be refunded twice", legs)
	}
	if r.broadcasts() != sent {
		t.Fatalf("broadcasts = %d, want %d — the refund may not be re-sent", r.broadcasts(), sent)
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusRefunded {
		t.Fatalf("status = %s; a confirmed refund leg finishes the order", got.Status)
	}
}

// ── losing a race with another tick ────────────────────────────────────────

// Confirm finding the row already terminal means another tick got there first.
// That is not this tick's failure, and it must not be reported as one.
func TestOrchestrator_CarriesOnWhenAnotherTickAlreadyConfirmedTheLeg(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "99800000")

	stub := &stubLegStore{rampLegStore: r.legStore, confirmErr: ErrLegNotPending}
	if err := r.newOrchestrator(stub).Advance(context.Background(), r.reload(t)); err != nil {
		t.Fatalf("Advance = %v; losing the confirm race is not an error", err)
	}
	// The winning tick owns the settle, so this one must not have written it.
	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; the tick that lost must leave the settle to the winner", got.Status)
	}
}

// Fail finding the row already terminal is likewise another tick's work, not
// an error to propagate over the real cause.
func TestOrchestrator_CarriesOnWhenAnotherTickAlreadyFailedTheLeg(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.err = ErrLiFiNoRoute
	stub := &stubLegStore{rampLegStore: r.legStore, failErr: ErrLegNotPending}

	err := r.newOrchestrator(stub).Advance(context.Background(), r.reload(t))
	if !errors.Is(err, ErrLiFiNoRoute) {
		t.Fatalf("Advance = %v; want the quote failure, not the leg-store race", err)
	}
}

// ── caps and configuration ────────────────────────────────────────────────

func TestOrchestrator_CountsChainExposureAgainstTheCap(t *testing.T) {
	r := newConversionRig(t)
	// Another converted buy already in flight on base, just under the cap.
	other := *r.order
	other.ID, other.Reference, other.TokenAmount = uuid.New(), "conv-other", "9950" // 9,950 USDC (plain decimal, matching CreateBuy's convention)
	if err := r.db.Create(&other).Error; err != nil {
		t.Fatalf("seed second order: %v", err)
	}

	// I6: a cap breach must PARK, not bare-error — a bare error here, with
	// Holder already Treasury and zero legs, is invisible to the stall
	// detector forever (afterStep returns early on zero legs).
	if err := r.svc.Advance(context.Background(), r.order); err != nil {
		t.Fatal(err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("status = %s; a cap breach must park the order, not silently stall it", got.Status)
	}
	if r.broadcasts() != 0 || len(r.legsOfKind(t, models.RampLegKindBridge)) != 0 {
		t.Fatal("an order over the chain cap must reserve nothing and send nothing")
	}
}

// inFlightUSD's own doc comment: "a failure to read it fails CLOSED: an
// unknown exposure is not a zero one." An in-flight order whose token has no
// configured address on this lane must make the WHOLE caps check fail, not
// silently drop out of the exposure total and let a new bridge sail through
// underneath an exposure that is real but unmeasurable.
func TestOrchestrator_FailsClosedWhenAnInFlightOrdersTokenIsUnconfigured(t *testing.T) {
	r := newConversionRig(t)
	other := *r.order
	other.ID, other.Reference, other.Token, other.TokenAmount = uuid.New(), "conv-other", "GHOST", "100"
	if err := r.db.Create(&other).Error; err != nil {
		t.Fatalf("seed second order: %v", err)
	}

	// I6: the fail-closed refusal must PARK the order rather than bare-error
	// it, same reasoning as the cap-breach case above.
	if err := r.svc.Advance(context.Background(), r.order); err != nil {
		t.Fatal(err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("status = %s; a fail-closed caps check must park the order, not silently stall it", got.Status)
	}
	if r.broadcasts() != 0 || len(r.legsOfKind(t, models.RampLegKindBridge)) != 0 {
		t.Fatal("an unpriceable in-flight order must refuse the whole cap check, not silently skip it")
	}
}

// A refund returns money the treasury already holds: it creates no new
// exposure, so the cap must not be able to trap a user's funds.
func TestOrchestrator_RefundsEvenWhenTheChainCapIsExhausted(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.err = ErrLiFiNoRoute
	mustAdvance(t, r)

	other := *r.order
	other.ID, other.Reference, other.TokenAmount = uuid.New(), "conv-other", "9950"
	if err := r.db.Create(&other).Error; err != nil {
		t.Fatalf("seed second order: %v", err)
	}
	r.clock = r.clock.Add(31 * time.Minute)

	mustAdvance(t, r) // broadcasts the refund (no cap check on a refund)
	mustAdvance(t, r) // observeRefund verifies it on-chain and closes the order

	if got := r.reload(t); got.Status != models.PaycrestStatusRefunded {
		t.Fatalf("status = %s; a refund must not be blocked by the exposure cap", got.Status)
	}
}

// Lanes that disagree about their signer mean we do not know which address
// Paycrest paid. Refuse rather than guess.
func TestOrchestrator_RefusesWhenTheTreasuryAddressIsUnknown(t *testing.T) {
	r := newConversionRig(t)
	other, _ := newSponsorRig("polygon", "USDT")
	other.signer.addr = "0x1111111111111111111111111111111111111111"
	broken := NewTreasury(map[string]*TreasuryLane{
		"base":    {SponsorLane: r.src.lane, ChainID: 8453, Routers: treasuryTestRouters},
		"polygon": {SponsorLane: other.lane, ChainID: 137, Routers: treasuryTestRouters},
	}, TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 10000}, func() time.Time { return r.clock })
	r.svc.treasury = broken

	// I6: an unknown treasury address must PARK the order rather than
	// bare-error it — see the cap-breach tests above for why a bare error
	// here is a silent permanent stall.
	if err := r.svc.Advance(context.Background(), r.order); err != nil {
		t.Fatal(err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("status = %s; an unresolvable treasury address must park the order", got.Status)
	}
	if r.lifi.quoteCalls() != 0 || r.broadcasts() != 0 {
		t.Fatal("a misconfigured treasury must not quote or send")
	}
}

// I6: no lane for the order's OWN chain (e.g. removed from
// RAMP_TREASURY_CHAINS while orders are in flight) must also PARK, not
// bare-error — the treasury address itself resolves fine here (there is
// still a lane, just not on "base"), isolating this from the
// unknown-address case above.
func TestOrchestrator_ParksWhenNoLaneCoversTheOrdersChain(t *testing.T) {
	r := newConversionRig(t)
	dstOnly := NewTreasury(map[string]*TreasuryLane{
		"polygon": {SponsorLane: r.dst.lane, ChainID: 137, Routers: treasuryTestRouters},
	}, TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 10000}, func() time.Time { return r.clock })
	r.svc.treasury = dstOnly

	if err := r.svc.Advance(context.Background(), r.order); err != nil {
		t.Fatal(err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("status = %s; a missing lane for the order's own chain must park it", got.Status)
	}
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d, want 0", r.broadcasts())
	}
}

// An unroutable destination will never fix itself, so it stops immediately
// rather than burning the whole retry window rediscovering that.
func TestOrchestrator_StopsFastWhenTheDestinationTokenIsUnknown(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("requested_token", "NOPE")

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s; an unroutable destination needs a human now, not in 30 minutes", got.Status)
	}
	if r.lifi.quoteCalls() != 0 || r.broadcasts() != 0 {
		t.Fatalf("quotes=%d broadcasts=%d; want nothing attempted", r.lifi.quoteCalls(), r.broadcasts())
	}
}

func TestOrchestrator_TickOnlyPicksUpConvertedBuysInFlight(t *testing.T) {
	r := newConversionRig(t)

	// A direct buy and a settled converted buy must both be left alone.
	direct := *r.order
	direct.ID, direct.Reference, direct.RouteKind = uuid.New(), "direct-1", models.RouteKindDirect
	done := *r.order
	done.ID, done.Reference, done.Status = uuid.New(), "settled-1", models.PaycrestStatusSettled
	for _, row := range []*models.PaycrestOrderRecord{&direct, &done} {
		if err := r.db.Create(row).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if r.lifi.quoteCalls() != 1 {
		t.Fatalf("quotes = %d, want exactly one — only the live converted buy is due", r.lifi.quoteCalls())
	}
	for _, id := range []uuid.UUID{direct.ID, done.ID} {
		var legs []models.RampLeg
		if err := r.db.Where("order_id = ?", id).Find(&legs).Error; err != nil {
			t.Fatal(err)
		}
		if len(legs) != 0 {
			t.Fatalf("order %s got legs %+v; it was never due", id, legs)
		}
	}
}

// An order that cannot move must not hold the head of the queue: it is rotated
// to the back so the orders behind it still get their turn.
func TestOrchestrator_TickRotatesAnIdleOrderToTheBackOfTheQueue(t *testing.T) {
	r := newConversionRig(t)
	r.seedLeg(t, &models.RampLeg{
		OrderID: r.order.ID, Kind: models.RampLegKindDeliver, Attempt: 1,
		ChainKey: "polygon", Token: "USDT", ToAddr: r.order.WalletAddress,
		Amount: "100000", Status: models.RampLegStatusPending, TxHash: "0xstalled",
	})
	r.dstTransfers.err = ErrTransferNotFound
	// Put the row's clock on the orchestrator's clock: GORM stamps UpdatedAt
	// from the wall clock on create, and comparing that against an injected
	// clock would measure the gap between the two, not whether we rotated.
	if err := r.db.Model(r.order).UpdateColumn("updated_at", r.clock.UTC()).Error; err != nil {
		t.Fatal(err)
	}
	before := r.reload(t).UpdatedAt
	r.clock = r.clock.Add(time.Hour) // well inside the 6h stall window

	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := r.reload(t)
	if got.RampCheckedAt == nil || !got.RampCheckedAt.After(before) {
		t.Fatalf("rampCheckedAt = %v; a stalled order must rotate to the back of the queue", got.RampCheckedAt)
	}
	// And updated_at must NOT move: rotating is not progress, and forging it
	// here would mislead every other reader of the column. (The stall detector
	// itself reads the leg ledger, never this.)
	if !got.UpdatedAt.Equal(before) {
		t.Fatalf("updatedAt = %v, was %v; rotating must not forge progress", got.UpdatedAt, before)
	}
}

// An uncertain broadcast (the RPC failed after the transaction may already
// have been accepted) leaves the leg PENDING: the alternative is a second
// conversion for the same money.
func TestOrchestrator_KeepsTheLegPendingWhenABroadcastIsUncertain(t *testing.T) {
	r := newConversionRig(t)
	r.src.signer.err = fmt.Errorf("rpc timeout")

	mustAdvance(t, r)

	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("bridge legs = %+v; an uncertain broadcast must stay pending", legs)
	}
	// And nothing else may move while it is unresolved.
	mustAdvance(t, r)
	if legs := r.legsOfKind(t, models.RampLegKindBridge); len(legs) != 1 {
		t.Fatalf("bridge legs = %+v; want no second attempt", legs)
	}
}

// A refund that has not landed yet is a live liability, and reporting zero
// would hide it for exactly as long as it matters.
func TestOrchestrator_ReportsWhatIsOwedWhileARefundIsInFlight(t *testing.T) {
	r := newConversionRig(t)
	order := r.reload(t)
	order.Status = models.PaycrestStatusRefunding
	if got := owedFromLegs(order, nil); got != "99500000" {
		t.Fatalf("owed while refunding = %q, want the full promise still outstanding", got)
	}
}

// ── round 2: the gates that were not fail-closed ───────────────────────────

// Custody is selected by "NOT yet ours", not by "explicitly Paycrest's".
// A row with an unset Holder — the zero value — would otherwise skip both the
// delivery proof AND the rescale of the promise into destination units, after
// which a 6dp source against an 18dp destination settles about a trillion
// times early.
func TestOrchestrator_TakesCustodyWhenTheHolderIsUnset(t *testing.T) {
	r := newConversionRig(t)
	r.dst.native.decimalsByToken = map[string]int{strings.ToLower(sponsorTestBscUSDT): 18}
	if err := r.db.Model(r.order).UpdateColumn("holder", "").Error; err != nil {
		t.Fatal(err)
	}

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %q; an unrecognised holder must go through the delivery proof", got.Holder)
	}
	if r.transfers.calls == 0 {
		t.Fatal("the delivery was never verified")
	}
	want := PromisedAmount(mustBig(t, "100000000000000000000"), 50).String()
	if got.PromisedAmount != want {
		t.Fatalf("promisedAmount = %q, want %q — the rescale must not be skippable", got.PromisedAmount, want)
	}
	// And nothing was converted on this step: custody is its own step.
	if r.lifi.quoteCalls() != 0 {
		t.Fatalf("quotes = %d, want 0", r.lifi.quoteCalls())
	}
}

// Staleness is measured on the LEG LEDGER, not the order row. Nothing writes
// the order between custody and settlement, so an updated_at deadline would be
// a hard deadline on the whole conversion.
func TestOrchestrator_ParksAnOrderWhoseLedgerHasNotMoved(t *testing.T) {
	r := newConversionRig(t)
	// A pending deliver leg whose receipt never becomes readable: every tick is
	// a no-op, and no bridge is live.
	r.seedLeg(t, &models.RampLeg{
		OrderID: r.order.ID, Kind: models.RampLegKindDeliver, Attempt: 1,
		ChainKey: "polygon", Token: "USDT", ToAddr: r.order.WalletAddress,
		Amount: "100000", Status: models.RampLegStatusPending, TxHash: "0xstalled",
	})
	r.dstTransfers.err = ErrTransferNotFound

	// Inside the window it is left alone.
	r.clock = r.clock.Add(5 * time.Hour)
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; an order inside the stall window is still just waiting", got.Status)
	}

	// Past it, a human is asked to look.
	r.clock = r.clock.Add(2 * time.Hour)
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || got.Fault != models.FaultPlatform {
		t.Fatalf("order = %s fault=%s; a stalled order must park for an operator", got.Status, got.Fault)
	}
	if !got.NeedsOperator() {
		t.Fatal("a stalled order must reach the operator queue")
	}
}

// A bridge that is genuinely in flight is not a stall, however long it takes.
// Parking it would strand a user who may be about to be paid.
func TestOrchestrator_NeverParksWhileABridgeIsStillInFlight(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r) // broadcasts the bridge; LiFi still reports PENDING

	r.clock = r.clock.Add(7 * time.Hour) // well past the 6h stall window
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; a bridge still in flight is not a stall", got.Status)
	}
	if got.NeedsOperator() {
		t.Fatal("a live bridge must not be handed to an operator on a timer")
	}
	// A slow bridge eventually lands, and still settles.
	r.completeBridge(t, "99800000")
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s; a slow bridge must still be able to settle", got.Status)
	}
}

// Parking an order whose bridge broadcast must NOT report the full promise as
// owed: the user may already hold the money, and that figure is what a human
// would pay against.
func TestOrchestrator_ParkedWithABridgeInFlightDoesNotClaimTheFullPromise(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.status.set(LiFiStatusFailed, "") // parks, leg left pending WITH its hash

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending", got.Status)
	}
	if got.OwedToUser == got.PromisedAmount {
		t.Fatalf("owedToUser = %q; the full promise must not be claimed while %q is unresolved",
			got.OwedToUser, got.TxHash)
	}
	if got.OwedToUser != "" {
		t.Fatalf("owedToUser = %q; an in-flight bridge makes the liability undetermined, not a number",
			got.OwedToUser)
	}
	if got.FailureReason == "" {
		t.Fatal("the reason must name what is outstanding")
	}
}

// A refund stuck on a pending leg is just as silent as a stuck conversion.
func TestOrchestrator_ParksAStalledRefund(t *testing.T) {
	r := newConversionRig(t)
	if err := r.db.Model(r.order).Updates(map[string]any{
		"status": models.PaycrestStatusRefunding,
	}).Error; err != nil {
		t.Fatal(err)
	}
	r.seedLeg(t, &models.RampLeg{
		OrderID: r.order.ID, Kind: models.RampLegKindRefund, Attempt: 1,
		ChainKey: "base", Token: "USDC", ToAddr: r.order.WalletAddress,
		Amount: "100000000", Status: models.RampLegStatusPending, TxHash: "0xstuckrefund",
	})
	// Genuinely unverifiable — observeRefund now asks the chain on every
	// tick (C1), so without this the rig's default verifier would happily
	// confirm the "stuck" refund instead of ever letting it stall.
	r.transfers.errByTx = map[string]error{"0xstuckrefund": ErrTransferNotFound}

	r.clock = r.clock.Add(7 * time.Hour)
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("status = %s; a refunding order can stall too", got.Status)
	}
}

// The staleness signal must not depend on a column another service owns.
// PaycrestOrderService.Sync bumps updated_at on every poll of a non-terminal
// order, which would otherwise suppress the detector forever and leave
// ramp_checked_at NULL at the head of the queue.
func TestOrchestrator_StallDetectionSurvivesThePollerTouchingTheOrder(t *testing.T) {
	r := newConversionRig(t)
	r.seedLeg(t, &models.RampLeg{
		OrderID: r.order.ID, Kind: models.RampLegKindDeliver, Attempt: 1,
		ChainKey: "polygon", Token: "USDT", ToAddr: r.order.WalletAddress,
		Amount: "100000", Status: models.RampLegStatusPending, TxHash: "0xstalled",
	})
	r.dstTransfers.err = ErrTransferNotFound
	r.clock = r.clock.Add(7 * time.Hour)

	// The poller touches the row, exactly as Sync's Updates(...) would.
	if err := r.db.Model(r.order).UpdateColumn("updated_at", r.clock.UTC()).Error; err != nil {
		t.Fatal(err)
	}

	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s; a fresh updated_at must not hide a stalled ledger", got.Status)
	}
	if got.RampCheckedAt == nil {
		t.Fatal("rotation must still happen when updated_at was written by someone else")
	}
}

// ── round 2: the chain outranks LiFi ───────────────────────────────────────

// LiFi names the transaction; the ERC-20 Transfer log in it says what the user
// received. Settlement is on the verified figure.
func TestOrchestrator_SettlesOnTheVerifiedLandingNotLiFisNumber(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "99800000")
	// The chain disagrees with LiFi: slightly less actually arrived.
	r.dstTransfers.amount = big.NewInt(99_600000)

	mustAdvance(t, r)

	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if legs[0].AmountReceived != "99600000" {
		t.Fatalf("amountReceived = %q; the ledger records what the CHAIN says, not LiFi",
			legs[0].AmountReceived)
	}
	if r.dstTransfers.calls == 0 {
		t.Fatal("the landing was never verified on the destination chain")
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s; a verified landing over the promise settles", got.Status)
	}
}

// A DONE whose destination transfer cannot be read is not a settlement.
func TestOrchestrator_NeverSettlesALandingItCannotVerify(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "99800000")
	r.dstTransfers.err = ErrTransferNotFound

	mustAdvance(t, r)

	got := r.reload(t)
	if got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; an unverified landing must leave the order open", got.Status)
	}
	if legs := r.legsOfKind(t, models.RampLegKindBridge); legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("bridge leg = %+v; want it left pending for the next tick", legs[0])
	}

	// Once the destination transaction is readable, it settles.
	r.dstTransfers.err = nil
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s; want settled once the landing verifies", got.Status)
	}
}

// With no verifier for the destination chain there is nothing to check the
// bridge against, so nothing settles.
func TestOrchestrator_NeverSettlesWithNoVerifierForTheDestination(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "99800000")
	svc := NewConversionOrchestrator(r.db, r.treasury, r.legStore, r.lifi,
		NewLiFiStatusClient(r.status.srv.URL, ""),
		map[string]ERC20TransferVerifier{"base": r.transfers}, // nothing for polygon
		r.chains, 30*time.Minute, 0, 0, 0, func() time.Time { return r.clock })

	if err := svc.Advance(context.Background(), r.reload(t)); err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; want it left open", got.Status)
	}
}

// ── round 2: the ledger must count a deliver leg it wrote itself ───────────

// A deliver leg's Amount is already in DESTINATION units — we chose it — so a
// confirmed deliver row with no AmountReceived still counts. Skipping it would
// under-count what the user holds and top them up a second time. A bridge leg's
// Amount is source units and must NOT be counted.
func TestCreditedToUser_CountsADeliverLegByItsAmount(t *testing.T) {
	order := &models.PaycrestOrderRecord{WalletAddress: sponsorTestUser}
	legs := []models.RampLeg{
		{
			Kind: models.RampLegKindBridge, Status: models.RampLegStatusConfirmed,
			ToAddr: sponsorTestUser, Amount: "100000000", AmountReceived: "99400000",
		},
		{
			// Confirmed, but the amount_received write was lost.
			Kind: models.RampLegKindDeliver, Status: models.RampLegStatusConfirmed,
			ToAddr: sponsorTestUser, Amount: "100000", AmountReceived: "",
		},
	}
	if got := creditedToUser(order, legs).String(); got != "99500000" {
		t.Fatalf("credited = %s, want 99500000 — the deliver leg must count by its Amount", got)
	}
}

func TestCreditedToUser_NeverCountsABridgeLegBySourceAmount(t *testing.T) {
	order := &models.PaycrestOrderRecord{WalletAddress: sponsorTestUser}
	legs := []models.RampLeg{{
		Kind: models.RampLegKindBridge, Status: models.RampLegStatusConfirmed,
		ToAddr: sponsorTestUser, Amount: "100000000", AmountReceived: "",
	}}
	if got := creditedToUser(order, legs).String(); got != "0" {
		t.Fatalf("credited = %s, want 0 — a bridge Amount is source units and may never have landed", got)
	}
}

// ── round 3: the top-up is checked too ─────────────────────────────────────

// "Never settle on a number nobody checked" has to hold for the top-up as well
// as the bridge: a broadcast is not a delivery.
func TestOrchestrator_VerifiesTheTopUpOnChainBeforeSettling(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t), ToAmount: big.NewInt(99_600000), ToAmountMin: big.NewInt(99_500000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}
	mustAdvance(t, r)
	r.completeBridge(t, "99400000") // 0.1 short

	// The landing verifies (tx 0xdest); the top-up's own transaction
	// (tx 0xsponsored) is broadcast but not readable yet.
	r.dstTransfers.errByTx = map[string]error{"0xsponsored": ErrTransferNotFound}
	mustAdvance(t, r)

	legs := r.legsOfKind(t, models.RampLegKindDeliver)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("deliver legs = %+v; an unverified top-up must stay pending", legs)
	}
	if got := r.reload(t); got.Status == models.PaycrestStatusSettled {
		t.Fatal("an unverified top-up must not settle the order")
	}

	// Once the transfer is readable, the leg confirms and the order settles —
	// on the CHAIN's figure.
	r.dstTransfers.errByTx = nil
	r.dstTransfers.amountByTx = map[string]*big.Int{"0xsponsored": big.NewInt(100_000)}
	mustAdvance(t, r)

	legs = r.legsOfKind(t, models.RampLegKindDeliver)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusConfirmed || legs[0].AmountReceived != "100000" {
		t.Fatalf("deliver legs = %+v; want one confirmed on the verified amount", legs)
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s; want settled once the top-up is proven", got.Status)
	}
	// And it was never sent twice while it was unverified.
	if r.dst.signer.calls() != 1 {
		t.Fatalf("destination broadcasts = %d, want 1", r.dst.signer.calls())
	}
}

// ── round 3: a reverted transfer is a settled question with a bad answer ───

// Polling a reverted transaction until the stall deadline just costs hours
// before a human hears about it.
func TestOrchestrator_ParksAtOnceWhenTheLandingReverted(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "99800000")
	r.dstTransfers.err = ErrTransferReverted

	mustAdvance(t, r) // one tick, no waiting

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || got.Fault != models.FaultPlatform {
		t.Fatalf("order = %s fault=%s; a reverted landing must park immediately", got.Status, got.Fault)
	}
	if !got.NeedsOperator() {
		t.Fatal("a reverted landing must reach the operator queue")
	}
}

func TestOrchestrator_ParksAtOnceWhenTheLandingPaidSomeoneElse(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "99800000")
	r.dstTransfers.err = ErrTransferWrongRecipient

	mustAdvance(t, r)

	if got := r.reload(t); got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s; a transfer that paid someone else must park immediately", got.Status)
	}
}

// ── round 4: the outer ceiling, and never paying a top-up twice ────────────

// A bridge leg that is pending WITH a hash is never parked by the stall
// deadline, because it might still be in flight. That exemption has no floor of
// its own, so a DONE LiFi never qualifies would sit in converting forever: the
// order is not settled, not refunded, and reaches no operator queue. The outer
// ceiling is the backstop — measured from the pending leg's own CreatedAt, and
// blind to whatever LiFi last said.
func TestOrchestrator_ParksAnUnqualifiedDoneAtTheOuterCeiling(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r) // bridge#1 broadcast, pending with a hash
	r.status.set(LiFiStatusDone, "99800000")
	r.status.setSubstatus("") // DONE, but nothing says the user was paid

	// Inside the ceiling it is left alone: the bridge may still be live.
	r.clock = r.clock.Add(7 * time.Hour)
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; a bridge inside the ceiling is still just waiting", got.Status)
	}

	// Past it, a human is asked to look, whatever LiFi last answered.
	r.clock = r.clock.Add(18 * time.Hour)
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("order = %s; a leg pending past the outer ceiling must reach an operator", got.Status)
	}
	if got.FailureReason == "" {
		t.Fatal("the reason must name the leg that never resolved")
	}
	// The liability is still UNDETERMINED: the money may already be with the
	// user. Nobody may pay against a figure here.
	if got.OwedToUser != "" {
		t.Fatalf("owedToUser = %q; an in-flight bridge is undetermined, not a number", got.OwedToUser)
	}
	if legs := r.legsOfKind(t, models.RampLegKindBridge); len(legs) != 1 {
		t.Fatalf("bridge legs = %+v; parking must never start a second attempt", legs)
	}
}

// LiFi not knowing the transaction is not evidence the bridge is alive. The
// ceiling runs on the leg's own age, so this shape surfaces too.
func TestOrchestrator_ParksABridgeLiFiCannotFindAtTheOuterCeiling(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.status.set("NOT_FOUND", "")

	r.clock = r.clock.Add(25 * time.Hour)
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("order = %s; a bridge LiFi cannot find must not sit forever", got.Status)
	}
	if r.dst.signer.calls() != 0 || r.src.signer.calls() != 1 {
		t.Fatalf("broadcasts = src:%d dst:%d; parking must move no money",
			r.src.signer.calls(), r.dst.signer.calls())
	}
}

// A landing that can never be verified — a dead destination RPC, or a transfer
// that paid someone else and so reads as ErrTransferNotFound — leaves the leg
// pending with its hash on every tick. The ceiling is what ends it.
func TestOrchestrator_ParksALandingItCanNeverVerifyAtTheOuterCeiling(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "99800000")
	r.dstTransfers.err = ErrTransferNotFound // never readable

	r.clock = r.clock.Add(25 * time.Hour)
	if err := r.svc.Tick(context.Background(), 10); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || !got.NeedsOperator() {
		t.Fatalf("order = %s; an unverifiable landing must not sit forever", got.Status)
	}
	if legs := r.legsOfKind(t, models.RampLegKindBridge); legs[0].Status != models.RampLegStatusPending {
		t.Fatalf("bridge leg = %+v; parking must not forge an outcome for it", legs[0])
	}
}

// An order with NO legs has never been touched by this state machine. Its clock
// measures the user's bank payment and Paycrest's matching window — neither of
// which this orchestrator owns — so parking it would hand an operator an order
// on which the user may not even have paid us yet, and record the FULL promise
// as owed against it. Worse, the park is one-way: Advance early-returns on
// NeedsOperator(), so the delivery that arrives an hour later is never seen.
func TestOrchestrator_NeverParksAnOrderWithNoLegs(t *testing.T) {
	r := newConversionRig(t)
	if err := r.db.Model(r.order).Updates(map[string]any{
		"holder":  models.HolderPaycrest,
		"tx_hash": "", // Paycrest has not delivered
	}).Error; err != nil {
		t.Fatal(err)
	}

	for _, after := range []time.Duration{7 * time.Hour, 18 * time.Hour} {
		r.clock = r.clock.Add(after)
		if err := r.svc.Tick(context.Background(), 10); err != nil {
			t.Fatalf("Tick: %v", err)
		}
		got := r.reload(t)
		if got.Status != models.PaycrestStatusConverting || got.NeedsOperator() {
			t.Fatalf("order = %s after %s; a pre-custody order must never be parked", got.Status, after)
		}
		if got.OwedToUser == got.PromisedAmount {
			t.Fatalf("owedToUser = %q; we cannot owe the full promise on an order nobody has paid us for",
				got.OwedToUser)
		}
	}

	// And it still completes normally when Paycrest finally delivers.
	if err := r.db.Model(r.order).UpdateColumn("tx_hash", "0xpaycrestdelivery").Error; err != nil {
		t.Fatal(err)
	}
	mustAdvance(t, r)
	if got := r.reload(t); got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %q; a late delivery must still be taken into custody", got.Holder)
	}
}

// creditedToUser counts CONFIRMED legs only, and a top-up now stays pending
// across ticks while its receipt is unreadable. Recomputing the shortfall in
// that window would broadcast the same top-up a second time.
func TestOrchestrator_NeverStartsASecondTopUpWhileOneIsPending(t *testing.T) {
	r := newConversionRig(t)
	// A top-up already in flight, and a bridge attempt still pending. (The
	// attempt numbers are what sequence the two; the invariant under test is
	// about the deliver leg, however the pair came to exist.)
	r.seedLeg(t, &models.RampLeg{
		OrderID: r.order.ID, Kind: models.RampLegKindDeliver, Attempt: 1,
		ChainKey: "polygon", Token: "USDT", ToAddr: r.order.WalletAddress,
		Amount: "100000", Status: models.RampLegStatusPending, TxHash: "0xsponsored",
	})
	r.seedLeg(t, &models.RampLeg{
		OrderID: r.order.ID, Kind: models.RampLegKindBridge, Attempt: 2,
		ChainKey: "base", Token: "USDC", ToAddr: r.order.WalletAddress,
		Amount: "100000000", Status: models.RampLegStatusPending, TxHash: "0xbridge2",
	})
	r.completeBridge(t, "99400000") // lands 0.1 short of the promise

	mustAdvance(t, r)

	legs := r.legsOfKind(t, models.RampLegKindDeliver)
	if len(legs) != 1 {
		t.Fatalf("deliver legs = %+v; the user must never be topped up twice for one shortfall", legs)
	}
	if r.dst.signer.calls() != 0 {
		t.Fatalf("destination broadcasts = %d; nothing may be sent while a top-up is unresolved",
			r.dst.signer.calls())
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled && got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; want the order left open for the pending top-up", got.Status)
	}
}

// settleTopUp must ask the same question settleOrTopUp does: is the user whole?
// A verified credit that is merely POSITIVE is not the same as a sufficient
// one, and settling on it closes an order the user is still short on.
func TestOrchestrator_NeverSettlesOnAPartialTopUp(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t), ToAmount: big.NewInt(99_600000), ToAmountMin: big.NewInt(99_500000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}
	mustAdvance(t, r)
	r.completeBridge(t, "99400000") // 0.1 short; the top-up is for 100000
	// The top-up transaction credits only part of what it was sent for.
	r.dstTransfers.amountByTx = map[string]*big.Int{"0xsponsored": big.NewInt(40_000)}

	mustAdvance(t, r)

	legs := r.legsOfKind(t, models.RampLegKindDeliver)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusConfirmed || legs[0].AmountReceived != "40000" {
		t.Fatalf("deliver legs = %+v; want one confirmed on the verified 40000", legs)
	}
	got := r.reload(t)
	if got.Status == models.PaycrestStatusSettled {
		t.Fatalf("status = %s; 40000 of a 100000 shortfall does not make the user whole", got.Status)
	}
	if got.OwedToUser != "60000" {
		t.Fatalf("owedToUser = %q; the balance the user is still short must be on the record", got.OwedToUser)
	}

	// The next tick pays the REMAINING 60000 — not the whole shortfall again.
	r.dstTransfers.amountByTx = map[string]*big.Int{"0xsponsored": big.NewInt(60_000)}
	mustAdvance(t, r)

	legs = r.legsOfKind(t, models.RampLegKindDeliver)
	if len(legs) != 2 || legs[1].Amount != "60000" {
		t.Fatalf("deliver legs = %+v; want a second leg for the 60000 still owed", legs)
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s; want settled once the user actually holds the promise", got.Status)
	}
}

// rigRouteData is calldata for the rig's own conversion: a bridge to
// sponsorTestUser (the order's wallet) on polygon (137).
func rigRouteData(t *testing.T) string {
	t.Helper()
	return testBridgeCalldata(t, sponsorTestUser, 137, false)
}

// ── any-token destinations ─────────────────────────────────────────────────

// asNewVolatileBuy makes the rig order a post-change BNB-on-BSC buy: owed T,
// a native non-stable destination with no lane there, no promise yet.
func asNewVolatileBuy(t *testing.T, r *conversionRig) {
	t.Helper()
	if err := r.db.Model(r.order).Updates(map[string]any{
		"requested_chain": "bsc", "requested_token": "BNB", "requested_chain_id": 56,
		"requested_token_address": evmNativeAddress, "requested_token_decimals": 18,
		"destination_address": sponsorTestUser, "promised_amount": "", "quoted_amount": "99500000",
	}).Error; err != nil {
		t.Fatal(err)
	}
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: testBridgeCalldata(t, sponsorTestUser, 56, false),
		ToAmount: big.NewInt(1_000), ToAmountMin: big.NewInt(990), FromAmountUSD: 100, ToAmountUSD: 99.8,
		GasLimit: 1_000_000, Tool: "across"}
	r.status.setToken(evmNativeAddress)
	r.status.chainID = 56
}

func TestOrchestrator_BridgesWhatSheIsOwedAndKeepsTheRest(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	mustAdvance(t, r)
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	// T = 99.5 USDC, l = 20 bps: a = 99500000 × 10000/9980 = 99699398.
	if len(legs) != 1 || legs[0].Amount != "99699398" || legs[0].TxHash == "" {
		t.Fatalf("bridge leg = %+v", legs)
	}
	req := r.lifi.lastRequest()
	if req.ToToken != evmNativeAddress || req.ToChainID != 56 || req.FromAmount != "99699398" || req.ToAddress != sponsorTestUser {
		t.Fatalf("LiFi request = %+v", req)
	}
}

func TestOrchestrator_LegacyOrderStillBridgesTheFullAmount(t *testing.T) {
	r := newConversionRig(t) // QuotedAmount "" — written before this change
	mustAdvance(t, r)
	if legs := r.legsOfKind(t, models.RampLegKindBridge); len(legs) != 1 || legs[0].Amount != "100000000" {
		t.Fatalf("legacy bridge leg = %+v", legs)
	}
}

func TestOrchestrator_VolatileDestinationSettlesAtLiFisMinimum(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	mustAdvance(t, r)
	if got := r.reload(t).PromisedAmount; got != "990" {
		t.Fatalf("promise = %q, want LiFi's toAmountMin 990", got)
	}
	r.status.set(LiFiStatusDone, "995")
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s, want settled", got.Status)
	}
	if n := len(r.legsOfKind(t, models.RampLegKindDeliver)); n != 0 {
		t.Fatalf("deliver legs = %d; a volatile destination is never topped up", n)
	}
}

func TestOrchestrator_VolatileLandingBelowTheMinimumStopsForAHuman(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	mustAdvance(t, r)
	r.status.set(LiFiStatusDone, "900")
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending", got.Status)
	}
}

// LiFi names a native coin either as the zero address or as the 0xEeee…EEeE
// alias, and which one comes back varies by chain and by tool. Both mean the
// same asset, so both must settle — insisting on the spelling we happened to
// store would park every native buy on the chains that answer with the other.
func TestOrchestrator_NativeLandingReportedByItsAliasStillSettles(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	mustAdvance(t, r)
	r.status.setToken("0xEeeeeEeeeEeEeeEeEeEeeEEEeeeeEeeeeeeeEEeE")
	r.status.set(LiFiStatusDone, "995")
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s; the native alias names the same coin we asked for", got.Status)
	}
}

func TestOrchestrator_WrongReceivingTokenStopsForAHuman(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	mustAdvance(t, r)
	r.status.setToken("0x55d398326f99059fF775485246999027B3197955") // USDT, not BNB
	r.status.set(LiFiStatusDone, "995")
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s; a delivery in the wrong token must park", got.Status)
	}
}

// "Native" names the coin of every chain, and a native landing has no
// on-chain proof behind it — so a delivery LiFi reports on another chain is
// not hers to settle on, however right the token spelling looks.
func TestOrchestrator_NativeLandingOnAnotherChainStopsForAHuman(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	mustAdvance(t, r)
	r.status.chainID = 137 // Polygon's native coin, not the BNB she bought
	r.status.set(LiFiStatusDone, "995")
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s; a native landing on the wrong chain must park", got.Status)
	}
}

// At 80 bps, T/(1 − l) is 100302419 — more than the 100000000 that actually
// landed. The attempt is refused before the budget is ever consulted: the
// treasury can only send what it holds.
func TestOrchestrator_ARouteThatNoLongerFitsWhatLandedFailsTheAttempt(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	r.lifi.route.ToAmountUSD = 99.2 // 80 bps
	mustAdvance(t, r)
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d, want none", r.broadcasts())
	}
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("legs = %+v, want one failed attempt (the retry window starts)", legs)
	}
	if !strings.Contains(legs[0].Error, "no longer fits") {
		t.Fatalf("leg error = %q; this test pins the FIT refusal, not the budget one", legs[0].Error)
	}
}

// The admission rule is l + g ≤ B, and g is half of it. 40 bps of LiFi cost
// alone clears the 50 bps budget; it is the treasury's own gas — $0.15 on a
// 1M-gas route plus the 80k approve, 17 bps of a $100 order — that takes the
// round trip over. Sized so the route still FITS what landed (T/(1 − l) at 40
// bps is 99899598, inside the 100000000 delivered), so the refusal can only
// come from the budget line.
func TestOrchestrator_ARouteWhoseGasTakesItOverTheBudgetFailsTheAttempt(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	r.lifi.route.ToAmountUSD = 99.6 // l = 40 bps, on its own inside the 50 bps budget
	r.lifi.route.GasCostUSD = 0.15  // + 80k approve on a 1M-gas route = 17 bps
	mustAdvance(t, r)
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; 40 + 17 bps is over the 50 bps budget", r.broadcasts())
	}
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("legs = %+v, want one failed attempt (the retry window starts)", legs)
	}
	if !strings.Contains(legs[0].Error, "over the 50 bps budget") {
		t.Fatalf("leg error = %q; the gas half of l + g ≤ B must be what refused this route", legs[0].Error)
	}
}

// An order priced on a raised budget executes against THAT budget, not the
// global floor: the same 57 bps round trip refused above clears an order
// whose own B′ is 121.
func TestOrchestrator_AnOrdersOwnBudgetAdmitsItsRoute(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	r.db.Model(r.order).Update("markup_bps", 121)
	r.lifi.route.ToAmountUSD = 99.6
	r.lifi.route.GasCostUSD = 0.15
	mustAdvance(t, r)
	if r.broadcasts() != 1 {
		t.Fatalf("broadcasts = %d; 57 bps fits a 121 bps order budget", r.broadcasts())
	}
}

// A short Paycrest delivery scales what she's owed, and the bridge sizes
// from that — not from the original quote, which no longer fits R.
func TestOrchestrator_ShortDeliveryScalesWhatSheIsOwed(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.transfers.amount = big.NewInt(50_000000) // half of the expected 100 USDC
	mustAdvance(t, r)                          // custody
	if got := r.reload(t).QuotedAmount; got != "49750000" {
		t.Fatalf("quoted = %q, want 49750000 (99.5 × 50/100)", got)
	}
	mustAdvance(t, r) // bridge
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	// 49750000 × 10000/9980 = 49849699.
	if len(legs) != 1 || legs[0].Amount != "49849699" || legs[0].TxHash == "" {
		t.Fatalf("bridge leg = %+v", legs)
	}
}

// USDT priced a few bps above USDC must still clear a stablecoin promise.
func TestOrchestrator_StableDestinationPricedAboveUSDCStillBridges(t *testing.T) {
	r := newConversionRig(t) // polygon USDT, already in custody
	promise := StablePromise(big.NewInt(99_500000), 6, 6)
	if err := r.db.Model(r.order).Updates(map[string]any{
		"quoted_amount": "99500000", "promised_amount": promise.String(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: rigRouteData(t),
		ToAmount: big.NewInt(99_470000), ToAmountMin: big.NewInt(99_370000), // ≈ T × 0.9997 × 0.999
		FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: "across"}
	mustAdvance(t, r)
	if r.broadcasts() == 0 {
		t.Fatal("an honest stable route 3 bps off USDC was refused")
	}
}

func TestOrchestrator_OwedToUserOnANonStableDeliveryIsUndetermined(t *testing.T) {
	r := newConversionRig(t)
	asNewVolatileBuy(t, r)
	mustAdvance(t, r)
	r.status.set(LiFiStatusDone, "900")
	mustAdvance(t, r) // parks with a confirmed bridge leg
	if _, _, _, err := r.svc.OwedToUser(context.Background(), r.reload(t)); !errors.Is(err, ErrOwedUndetermined) {
		t.Fatalf("OwedToUser err = %v, want ErrOwedUndetermined (and no panic)", err)
	}
}

// ── a Solana destination ───────────────────────────────────────────────────

// fakeSolanaLanding stands in for the Solana chain client's ReceivedBy: the
// chain's own answer to "what did this transaction credit her", which is the
// only thing a Solana landing may settle on.
type fakeSolanaLanding struct {
	amount *big.Int
	err    error
	calls  int
	txHash string
	owner  string
	mint   string
}

func (f *fakeSolanaLanding) ReceivedBy(_ context.Context, txHash, owner, mint string) (*big.Int, error) {
	f.calls++
	f.txHash, f.owner, f.mint = txHash, owner, mint
	return f.amount, f.err
}

// asSolanaBuy makes the rig order a SOL buy delivered to the receiver of the
// captured native-SOL Task 10 fixture, with LiFi's minimum at 0.99 SOL.
//
// The fixture is named rather than "the first Solana one": loadLiFiFixtures
// returns a map, so scanning it would pick the SPL capture at random on some
// runs — a different delivery shape (a token account, not the owner) that this
// native-SOL order has no business being checked against.
func asSolanaBuy(t *testing.T, r *conversionRig, land solanaLanding, destination string) {
	t.Helper()
	fx := loadLiFiFixtures(t)["near_base_solana_sol"]
	if fx.Data == "" {
		t.Fatal("no native-SOL Solana fixture (Task 10)")
	}
	if destination == "" {
		destination = fx.Receiver
	}
	if err := r.db.Model(r.order).Updates(map[string]any{
		"requested_chain": "solana", "requested_token": "SOL", "requested_chain_id": solanaLiFiChainID,
		"requested_token_address": solanaNativeToken, "requested_token_decimals": 9,
		"destination_address": destination, "promised_amount": "", "quoted_amount": "99500000",
	}).Error; err != nil {
		t.Fatal(err)
	}
	r.lifi.route = &LiFiRoute{To: treasuryTestRouter, Data: fx.Data, ToAmount: big.NewInt(1_000_000_000),
		ToAmountMin: big.NewInt(990_000_000), FromAmountUSD: 100, ToAmountUSD: 99.8, GasLimit: 1_000_000, Tool: fx.Tool}
	r.status.toAddress = destination
	r.status.chainID = solanaLiFiChainID
	r.status.receivingToken = solanaNativeToken
	r.svc.SetSolanaLanding(land)
}

func TestOrchestrator_SolanaDestinationSettlesOnTheChainsProof(t *testing.T) {
	r := newConversionRig(t)
	land := &fakeSolanaLanding{amount: big.NewInt(1_000_000_000)}
	asSolanaBuy(t, r, land, "")
	mustAdvance(t, r)
	if r.broadcasts() == 0 {
		t.Fatal("the Solana bridge was not sent")
	}
	r.status.set(LiFiStatusDone, "1000000000")
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusSettled {
		t.Fatalf("status = %s, want settled", got.Status)
	}
	// The chain was asked about the destination LiFi named, for the owner the
	// order pays, in native SOL — not about the user's EVM wallet.
	if land.calls == 0 || land.txHash != "0xdest" || land.owner != r.reload(t).DestinationAddress || land.mint != "" {
		t.Fatalf("landing read = %+v; want the destination tx, the Solana recipient, native SOL", land)
	}
}

func TestOrchestrator_SolanaLandingShortParks(t *testing.T) {
	r := newConversionRig(t)
	asSolanaBuy(t, r, &fakeSolanaLanding{amount: big.NewInt(900_000_000)}, "")
	mustAdvance(t, r)
	r.status.set(LiFiStatusDone, "1000000000") // LiFi claims more than the chain shows
	mustAdvance(t, r)
	if got := r.reload(t); got.Status != models.PaycrestStatusRecoveryPending {
		t.Fatalf("status = %s, want recovery_pending", got.Status)
	}
}

func TestOrchestrator_SolanaRouteToSomeoneElseIsRefused(t *testing.T) {
	r := newConversionRig(t)
	asSolanaBuy(t, r, &fakeSolanaLanding{}, "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin")
	mustAdvance(t, r)
	if r.broadcasts() != 0 {
		t.Fatalf("broadcasts = %d; a route paying another Solana address must not be signed", r.broadcasts())
	}
	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("legs = %+v", legs)
	}
}

// Nothing settles on a landing the chain was never asked about: with no Solana
// client wired the order waits for a human rather than trusting LiFi's word.
func TestOrchestrator_SolanaWithoutAChainClientNeverSettles(t *testing.T) {
	r := newConversionRig(t)
	asSolanaBuy(t, r, nil, "")
	mustAdvance(t, r)
	r.status.set(LiFiStatusDone, "1000000000")
	mustAdvance(t, r)
	// Pinned to the exact status, not merely "not settled": waiting is the
	// only right answer here. Parking it for recovery would declare a
	// short-pay we have no proof of, and failing it would throw away an order
	// the bridge says is done — both are wrong in a way "!= settled" accepts.
	if got := r.reload(t); got.Status != models.PaycrestStatusConverting {
		t.Fatalf("status = %s; an unproven Solana landing must keep waiting", got.Status)
	}
}
