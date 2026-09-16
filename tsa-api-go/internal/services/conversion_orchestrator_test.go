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
}

func (f *fakeLiFiQuoter) Quote(_ context.Context, req LiFiQuoteRequest) (*LiFiRoute, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.last = req
	if f.err != nil {
		return nil, f.err
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
}

func (f *fakeTransferVerifier) VerifyTransfer(_ context.Context, _, _, _ string) (*big.Int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return new(big.Int).Set(f.amount), nil
}

// fakeLiFiStatus serves LiFi's /status shape so the orchestrator can be driven
// through the REAL LiFiStatusClient (the constructor takes the concrete type),
// parsing included.
type fakeLiFiStatus struct {
	mu        sync.Mutex
	status    string
	amount    string
	toAddress string
	srv       *httptest.Server
}

func newFakeLiFiStatus(t *testing.T, toAddress string) *fakeLiFiStatus {
	t.Helper()
	f := &fakeLiFiStatus{status: LiFiStatusPending, toAddress: toAddress}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		body := map[string]any{
			"status": f.status,
			"receiving": map[string]any{
				"txHash":    "0xdest",
				"chainId":   137,
				"amount":    f.amount,
				"toAddress": f.toAddress,
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

func (f *fakeLiFiStatus) setRecipient(addr string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.toAddress = addr
}

// ── rig ────────────────────────────────────────────────────────────────────

// conversionRig is one converted buy mid-flight: Paycrest has already paid the
// treasury 100 USDC on base (holder=treasury), the user asked for USDT on
// polygon, and the promise is 99.5 USDC-equivalent (100 USDC less 50 bps).
type conversionRig struct {
	t         *testing.T
	db        *gorm.DB
	svc       *ConversionOrchestrator
	legStore  *GormRampLegStore
	lifi      *fakeLiFiQuoter
	transfers *fakeTransferVerifier
	status    *fakeLiFiStatus
	treasury  *Treasury
	src       *sponsorRig // base: where Paycrest delivered
	dst       *sponsorRig // polygon: where the user is paid
	order     *models.PaycrestOrderRecord
	clock     time.Time
}

func newConversionRig(t *testing.T) *conversionRig {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.PaycrestOrderRecord{}, &models.RampLeg{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	src, _ := newSponsorRig("base", "USDC")
	dst, _ := newSponsorRig("polygon", "USDT")
	r := &conversionRig{t: t, db: db, clock: sponsorTestNow, src: src, dst: dst}

	r.treasury = NewTreasury(map[string]*TreasuryLane{
		"base":    {SponsorLane: src.lane, ChainID: 8453},
		"polygon": {SponsorLane: dst.lane, ChainID: 137},
	}, TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 10000},
		func() time.Time { return r.clock })

	r.lifi = &fakeLiFiQuoter{route: &LiFiRoute{
		To: "0xR", Data: "0x01", ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), Tool: "across",
	}}
	r.transfers = &fakeTransferVerifier{amount: big.NewInt(100_000000)}
	r.status = newFakeLiFiStatus(t, sponsorTestUser)
	r.legStore = &GormRampLegStore{DB: db}

	chains := &BlockchainService{cfg: &config.Config{Networks: map[string]config.NetworkConfig{
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
		MarkupBps: 50, TokenAmount: "100000000", PromisedAmount: "99500000",
		Holder: models.HolderTreasury, WalletAddress: sponsorTestUser,
		TxHash: "0xpaycrestdelivery", CreatedAt: r.clock,
	}
	if err := db.Create(r.order).Error; err != nil {
		t.Fatalf("seed order: %v", err)
	}

	r.svc = NewConversionOrchestrator(db, r.treasury, r.legStore, r.lifi,
		NewLiFiStatusClient(r.status.srv.URL, ""), r.transfers, chains,
		30*time.Minute, func() time.Time { return r.clock })
	return r
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

// completeBridge makes LiFi report the bridge as DONE with what landed.
func (r *conversionRig) completeBridge(t *testing.T, amount string) {
	t.Helper()
	r.status.set(LiFiStatusDone, amount)
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
	r.lifi.route = &LiFiRoute{To: "0xR", Data: "0x01", ToAmount: big.NewInt(99_800000), ToAmountMin: big.NewInt(99_700000), Tool: "across"}
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
	r.lifi.route = &LiFiRoute{To: "0xR", Data: "0x01", ToAmount: big.NewInt(99_600000), ToAmountMin: big.NewInt(99_400000), Tool: "across"}
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
	mustAdvance(t, r)
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

func TestOrchestrator_RefusesWhenTheTreasuryIsKilled(t *testing.T) {
	r := newConversionRig(t)
	r.treasury.SetKillSwitch(true)

	err := r.svc.Advance(context.Background(), r.order)
	if !errors.Is(err, ErrTreasuryKilled) {
		t.Fatalf("Advance = %v, want ErrTreasuryKilled", err)
	}
	if got := r.legsOfKind(t, models.RampLegKindBridge); len(got) != 0 {
		t.Fatalf("legs = %+v; a paused treasury must reserve nothing", got)
	}
	if r.broadcasts() != 0 || r.lifi.quoteCalls() != 0 {
		t.Fatalf("broadcasts=%d quotes=%d; want none while paused", r.broadcasts(), r.lifi.quoteCalls())
	}
	if got := r.reload(t); got.Status != models.PaycrestStatusConverting || got.Holder != models.HolderTreasury {
		t.Fatalf("order = %s/%s; want it untouched", got.Status, got.Holder)
	}
}

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

// A short Paycrest delivery rewrites BOTH the amount and the promise: a
// promise computed from money that never arrived would owe more than we hold.
func TestOrchestrator_ShortDeliveryRepricesThePromise(t *testing.T) {
	r := newConversionRig(t)
	r.db.Model(r.order).Update("holder", models.HolderPaycrest)
	r.transfers.amount = big.NewInt(99_000000) // 1 USDC short

	mustAdvance(t, r)

	got := r.reload(t)
	if got.TokenAmount != "99000000" {
		t.Fatalf("tokenAmount = %q, want what actually landed", got.TokenAmount)
	}
	// 99_000000 less the order's own 50 bps markup.
	if want := PromisedAmount(big.NewInt(99_000000), 50).String(); got.PromisedAmount != want {
		t.Fatalf("promisedAmount = %q, want %q recomputed from the delivery", got.PromisedAmount, want)
	}
	if got.Holder != models.HolderTreasury {
		t.Fatalf("holder = %s, want treasury", got.Holder)
	}
}

// ── failure paths ──────────────────────────────────────────────────────────

func TestOrchestrator_RetriesWithAFreshQuoteAfterAFailedBridge(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.status.set(LiFiStatusFailed, "")
	mustAdvance(t, r) // observe the failure

	legs := r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 1 || legs[0].Status != models.RampLegStatusFailed {
		t.Fatalf("bridge legs = %+v; want attempt 1 recorded as failed", legs)
	}

	r.status.set(LiFiStatusPending, "")
	r.clock = r.clock.Add(5 * time.Minute) // still inside the 30-minute window
	mustAdvance(t, r)

	legs = r.legsOfKind(t, models.RampLegKindBridge)
	if len(legs) != 2 || legs[1].Attempt != 2 || legs[1].Status != models.RampLegStatusPending {
		t.Fatalf("bridge legs = %+v; want a second attempt", legs)
	}
	if r.lifi.quoteCalls() != 2 {
		t.Fatalf("quotes = %d, want 2 — every attempt is quoted fresh, never reused", r.lifi.quoteCalls())
	}
}

// Landing further below the promise than margin covers is not something a
// retry can fix — the money already reached the user. The order stops for an
// operator with the shortfall recorded, rather than looping forever.
func TestOrchestrator_FlagsAMissBeyondTheToleranceForRecovery(t *testing.T) {
	r := newConversionRig(t)
	mustAdvance(t, r)
	r.completeBridge(t, "98000000") // 150 bps under the promise
	mustAdvance(t, r)

	got := r.reload(t)
	if got.Status != models.PaycrestStatusRecoveryPending || got.Fault != models.FaultPlatform {
		t.Fatalf("order = %s fault=%s; want recovery_pending, our fault", got.Status, got.Fault)
	}
	if got.OwedToUser != "1500000" {
		t.Fatalf("owedToUser = %q, want the 1.5 USDC still owed", got.OwedToUser)
	}
	if legs := r.legsOfKind(t, models.RampLegKindDeliver); len(legs) != 0 {
		t.Fatalf("deliver legs = %+v; a miss this size is not topped up from margin", legs)
	}
	// The bridge did land, so the ledger must keep saying so.
	if legs := r.legsOfKind(t, models.RampLegKindBridge); legs[0].Status != models.RampLegStatusConfirmed {
		t.Fatalf("bridge leg = %+v; a leg that landed is never rewritten as failed", legs[0])
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

// ── caps and configuration ────────────────────────────────────────────────

func TestOrchestrator_CountsChainExposureAgainstTheCap(t *testing.T) {
	r := newConversionRig(t)
	// Another converted buy already in flight on base, just under the cap.
	other := *r.order
	other.ID, other.Reference, other.TokenAmount = uuid.New(), "conv-other", "9950000000" // 9,950 USDC
	if err := r.db.Create(&other).Error; err != nil {
		t.Fatalf("seed second order: %v", err)
	}

	err := r.svc.Advance(context.Background(), r.order)
	if !errors.Is(err, ErrTreasuryCapExceeded) {
		t.Fatalf("Advance = %v, want ErrTreasuryCapExceeded", err)
	}
	if r.broadcasts() != 0 || len(r.legsOfKind(t, models.RampLegKindBridge)) != 0 {
		t.Fatal("an order over the chain cap must reserve nothing and send nothing")
	}
}

// Lanes that disagree about their signer mean we do not know which address
// Paycrest paid. Refuse rather than guess.
func TestOrchestrator_RefusesWhenTheTreasuryAddressIsUnknown(t *testing.T) {
	r := newConversionRig(t)
	other, _ := newSponsorRig("polygon", "USDT")
	other.signer.addr = "0x1111111111111111111111111111111111111111"
	broken := NewTreasury(map[string]*TreasuryLane{
		"base":    {SponsorLane: r.src.lane, ChainID: 8453},
		"polygon": {SponsorLane: other.lane, ChainID: 137},
	}, TreasuryCaps{MaxInFlightUSDPerOrder: 2000, MaxInFlightUSDPerChain: 10000}, func() time.Time { return r.clock })
	r.svc.treasury = broken

	if err := r.svc.Advance(context.Background(), r.order); !errors.Is(err, ErrSponsorNotAvailable) {
		t.Fatalf("Advance = %v, want ErrSponsorNotAvailable", err)
	}
	if r.lifi.quoteCalls() != 0 || r.broadcasts() != 0 {
		t.Fatal("a misconfigured treasury must not quote or send")
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

// ── the money must not move twice if the final status write is lost ────────
//
// Both of these simulate the one gap a leg reservation cannot close on its
// own: the transfer broadcast and the leg row landed, but the order-status
// update that would have made the order terminal did not. The next tick sees
// an order that still looks unfinished, and must recognise from the LEG LEDGER
// that the user has already been paid.

func TestOrchestrator_NeverTopsUpTwiceIfTheSettlementWriteIsLost(t *testing.T) {
	r := newConversionRig(t)
	r.lifi.route = &LiFiRoute{To: "0xR", Data: "0x01", ToAmount: big.NewInt(99_600000), ToAmountMin: big.NewInt(99_400000), Tool: "across"}
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
