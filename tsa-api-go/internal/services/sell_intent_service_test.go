package services

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// sell_intent_service_test.go — Task 13: the durable record of a sale in
// progress, and what binds an on-chain swap to it (design §5.1–§5.3).
//
// Her tokens leave her wallet in a swap she signs. These tests exist to pin
// the two ways that can go wrong: a swap of hers that no intent will claim,
// and an intent that claims a swap which was never hers.

type fakeSellQuoter struct {
	quote *SellSwapQuote
	err   error
	calls int
}

func (f *fakeSellQuoter) Quote(_ context.Context, _ SellQuoteInput) (*SellSwapQuote, error) {
	f.calls++
	return f.quote, f.err
}

type fakeStatusReader struct {
	st  *LiFiTransferStatus
	err error
	// entered/gate pause the FIRST Status call when set, so a test can run
	// another caller to completion between a read of the row and its claim.
	calls   int
	entered chan struct{}
	gate    chan struct{}
}

func (f *fakeStatusReader) Status(_ context.Context, _ string) (*LiFiTransferStatus, error) {
	f.calls++
	if f.entered != nil && f.calls == 1 {
		f.entered <- struct{}{}
		<-f.gate
	}
	return f.st, f.err
}

type sellIntentRig struct {
	svc    *SellIntentService
	quoter *fakeSellQuoter
	status *fakeStatusReader
	base   *fakeTransferVerifier
	client *fakePaycrestConversionClient
	user   *models.User
}

const sellUSDC = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"

// Valid-shaped EVM hashes: RecordSwap rejects anything else for an EVM source.
var (
	testSwapHash  = "0x" + strings.Repeat("ab", 32)
	testSwapHash2 = "0x" + strings.Repeat("cd", 32)
)

// mustRecord records a swap in test setup and fails loudly, so a rejected
// hash can never make a later assertion pass for the wrong reason.
func (r *sellIntentRig) mustRecord(t *testing.T, id uuid.UUID, hash string) {
	t.Helper()
	if err := r.svc.RecordSwap(context.Background(), r.user.ID, id, hash); err != nil {
		t.Fatalf("RecordSwap: %v", err)
	}
}

func newSellIntentRig(t *testing.T) *sellIntentRig {
	t.Helper()
	o := newOrderServiceWithFakes(t)
	if err := o.db.AutoMigrate(&models.RampSellIntent{}); err != nil {
		t.Fatal(err)
	}
	r := &sellIntentRig{client: o.client, status: &fakeStatusReader{},
		base: &fakeTransferVerifier{amount: big.NewInt(9_975000)},
		quoter: &fakeSellQuoter{quote: &SellSwapQuote{
			Route: &LiFiRoute{TransactionID: "0xabc", GasLimit: 300_000}, LiFiQuote: json.RawMessage(`{}`),
			FeeBps: 10, USDCAmount: "9980000", FiatEstimate: "14895.15"}}}
	r.user = seedVerifiedUserModel(t, o.db)
	r.svc = NewSellIntentService(o.db, r.quoter, r.status, r.base, o.svc, sellUSDC, func() time.Time { return sponsorTestNow })
	return r
}

func (r *sellIntentRig) create(t *testing.T) *models.RampSellIntent {
	t.Helper()
	in, _, err := r.svc.Create(context.Background(), r.user, CreateSellIntentInput{
		SourceChainID: 56, SourceToken: evmNativeAddress, SourceAmount: "16000000000000000",
		SourceAddress: r.user.WalletAddress, FiatCode: "NGN", BankCode: "044", AccountNumber: "0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// landed is a LiFi status that matches the rig intent in every bound field.
func (r *sellIntentRig) landed(in *models.RampSellIntent) *LiFiTransferStatus {
	amt, _ := new(big.Int).SetString(in.SourceAmount, 10)
	return &LiFiTransferStatus{Status: LiFiStatusDone, Substatus: LiFiSubstatusCompleted, DestTxHash: "0xdest",
		DestChainID: 8453, ReceivedBaseUnits: big.NewInt(9_975000), ToAddress: in.WalletAddress, ReceivingToken: sellUSDC,
		SendingChainID: in.SourceChainID, SendingToken: in.SourceTokenAddress, SendingAmount: amt,
		FromAddress: in.SourceAddress, TransactionID: "0xabc"}
}

func TestSellIntent_CreateStoresTheServersOwnQuote(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	if r.quoter.calls != 1 || in.QuotedUSDC != "9980000" || in.FeeBps != 10 || in.LiFiTransactionID != "0xabc" ||
		in.QuotedGasLimit != 300_000 || in.Status != models.SellIntentSwapping || in.WalletAddress != r.user.WalletAddress {
		t.Fatalf("intent = %+v", in)
	}
}

func TestSellIntent_OneOpenIntentPerUser(t *testing.T) {
	r := newSellIntentRig(t)
	first := r.create(t)
	_, _, err := r.svc.Create(context.Background(), r.user, CreateSellIntentInput{SourceChainID: 56, SourceToken: evmNativeAddress,
		SourceAmount: "1", SourceAddress: r.user.WalletAddress, FiatCode: "NGN", BankCode: "044", AccountNumber: "0123456789"})
	if !errors.Is(err, ErrSellIntentOpen) {
		t.Fatalf("err = %v, want ErrSellIntentOpen", err)
	}
	if open, _ := r.svc.Open(context.Background(), r.user.ID); open == nil || open.ID != first.ID {
		t.Fatalf("open intent = %+v", open)
	}
}

func TestSellIntent_RecordSwapIsIdempotentAndSingleHash(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	if err := r.svc.RecordSwap(context.Background(), r.user.ID, in.ID, testSwapHash); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.RecordSwap(context.Background(), r.user.ID, in.ID, testSwapHash); err != nil {
		t.Fatalf("the same hash twice must be idempotent: %v", err)
	}
	if err := r.svc.RecordSwap(context.Background(), r.user.ID, in.ID, testSwapHash2); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("a second, different hash: err = %v", err)
	}
}

func TestSellIntent_ASwapHashBacksOneIntentOnly(t *testing.T) {
	r := newSellIntentRig(t)
	first := r.create(t)
	r.mustRecord(t, first.ID, testSwapHash)
	_ = r.svc.Abandon(context.Background(), r.user.ID, first.ID)
	second := r.create(t)
	if err := r.svc.RecordSwap(context.Background(), r.user.ID, second.ID, testSwapHash); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("reusing a swap hash: err = %v", err)
	}
}

func TestSellIntent_OpenOrderWaitsForLiFi(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = &LiFiTransferStatus{Status: LiFiStatusPending}
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); !errors.Is(err, ErrSwapNotLanded) {
		t.Fatalf("err = %v, want ErrSwapNotLanded", err)
	}
	if r.client.offrampCalls != 0 {
		t.Fatal("no Paycrest order before the swap lands")
	}
}

func TestSellIntent_OpenOrderSellsTheOnChainLandedAmountAtFMinusB(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	order, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.client.lastOfframp.AmountToken != "9.975" || r.client.lastOfframp.SenderFeePercent != "0.5" {
		t.Fatalf("offramp = %+v, want the chain's 9.975 USDC at fee 0.5", r.client.lastOfframp)
	}
	again, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
	if err != nil || again.ID != order.ID || r.client.offrampCalls != 1 {
		t.Fatalf("second OpenOrder: err %v, calls %d; must be idempotent", err, r.client.offrampCalls)
	}
}

func TestSellIntent_OpenOrderRefusesAnySwapThatIsNotThisIntents(t *testing.T) {
	for name, mutate := range map[string]func(*LiFiTransferStatus){
		"another recipient":    func(s *LiFiTransferStatus) { s.ToAddress = "0x000000000000000000000000000000000000dEaD" },
		"no stated recipient":  func(s *LiFiTransferStatus) { s.ToAddress = "" },
		"another token landed": func(s *LiFiTransferStatus) { s.ReceivingToken = "0x55d398326f99059fF775485246999027B3197955" },
		"no stated token":      func(s *LiFiTransferStatus) { s.ReceivingToken = "" },
		"another dest chain":   func(s *LiFiTransferStatus) { s.DestChainID = 1 },
		"another source chain": func(s *LiFiTransferStatus) { s.SendingChainID = 1 },
		"another source token": func(s *LiFiTransferStatus) { s.SendingToken = sellUSDC },
		"a different amount":   func(s *LiFiTransferStatus) { s.SendingAmount = big.NewInt(1) },
		"an unstated amount":   func(s *LiFiTransferStatus) { s.SendingAmount = nil },
		"another sender":       func(s *LiFiTransferStatus) { s.FromAddress = "0x000000000000000000000000000000000000bEEF" },
		"not our quote":        func(s *LiFiTransferStatus) { s.TransactionID = "0xdef" },
		"no quote id at all":   func(s *LiFiTransferStatus) { s.TransactionID = "" },
		"no stated substatus":  func(s *LiFiTransferStatus) { s.Substatus = "" },
		"an unknown substatus": func(s *LiFiTransferStatus) { s.Substatus = "NOT_PROCESSABLE_REFUND_NEEDED" },
		"no destination tx":    func(s *LiFiTransferStatus) { s.DestTxHash = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := newSellIntentRig(t)
			in := r.create(t)
			r.mustRecord(t, in.ID, testSwapHash)
			st := r.landed(in)
			mutate(st)
			r.status.st = st
			if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); !errors.Is(err, ErrSwapMismatch) {
				t.Fatalf("err = %v, want ErrSwapMismatch", err)
			}
			if r.client.offrampCalls != 0 {
				t.Fatal("a mismatched swap opened a Paycrest order")
			}
		})
	}
}

func TestSellIntent_OpenOrderNeedsTheOnChainProof(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	r.base.err = errors.New("no transfer in that tx")
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err == nil {
		t.Fatal("an unproven landing opened an order")
	}
	if r.client.offrampCalls != 0 {
		t.Fatal("an unproven landing reached Paycrest")
	}
}

// The chain is the authority on how much landed, not LiFi. A status that
// claims more than the receipt proves must sell only what the receipt proves.
func TestSellIntent_OpenOrderSellsWhatTheReceiptProvesNotWhatLiFiClaims(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	st := r.landed(in)
	st.ReceivedBaseUnits = big.NewInt(99_000000) // LiFi claims ten times the truth
	r.status.st = st
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err != nil {
		t.Fatal(err)
	}
	if r.client.lastOfframp.AmountToken != "9.975" {
		t.Fatalf("offramp amount = %q, want the verified 9.975", r.client.lastOfframp.AmountToken)
	}
}

func TestSellIntent_CreateRejectsABankItCannotPay(t *testing.T) {
	r := newSellIntentRig(t)
	_, _, err := r.svc.Create(context.Background(), r.user, CreateSellIntentInput{SourceChainID: 56, SourceToken: evmNativeAddress,
		SourceAmount: "1", SourceAddress: r.user.WalletAddress, FiatCode: "NGN", BankCode: "999", AccountNumber: "0123456789"})
	if err == nil {
		t.Fatal("an unsupported bank must be refused before she swaps")
	}
	if r.quoter.calls != 0 {
		t.Fatal("no quote before the payout details are known good")
	}
}

func TestSellIntent_RecordSwapRejectsAMalformedHash(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	if err := r.svc.RecordSwap(context.Background(), r.user.ID, in.ID, "not-a-hash"); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("err = %v", err)
	}
}

type fakeFeeEnqueuer struct{ events []FeeEvent }

func (f *fakeFeeEnqueuer) EnqueueFeeSettled(e FeeEvent) { f.events = append(f.events, e) }

func TestSellIntent_BooksOurFeeOnceThroughTheFeeEngine(t *testing.T) {
	r := newSellIntentRig(t)
	fees := &fakeFeeEnqueuer{}
	r.svc.SetFeeService(fees)
	r.quoter.quote.Route.FromAmountUSD = 10
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	for i := 0; i < 2; i++ {
		if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err != nil {
			t.Fatal(err)
		}
	}
	// $10 × 10 bps = $0.01, booked once (the second OpenOrder returns the existing order).
	if len(fees.events) != 1 || math.Abs(fees.events[0].FeeUSD-0.01) > 1e-9 || fees.events[0].SourceType != "ramp_sell" || fees.events[0].SourceID != in.ID {
		t.Fatalf("fee events = %+v", fees.events)
	}
}

func TestSellIntent_OnlyTheOwnerCanDriveIt(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	stranger := *r.user
	stranger.ID = uuid.New()
	if err := r.svc.RecordSwap(context.Background(), stranger.ID, in.ID, testSwapHash); err == nil {
		t.Fatal("another user recorded a swap on this intent")
	}
}

// A stranger who knows the intent id must not be able to open its order
// either — the payout would still go to the owner's bank, but the swap that
// funds it is claimed against a row she never opened.
func TestSellIntent_OnlyTheOwnerCanOpenTheOrder(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	stranger := *r.user
	stranger.ID = uuid.New()
	if _, err := r.svc.OpenOrder(context.Background(), &stranger, in.ID); err == nil {
		t.Fatal("another user opened this intent's order")
	}
	if r.client.offrampCalls != 0 {
		t.Fatal("a stranger's OpenOrder reached Paycrest")
	}
}

// A terminal intent is done: nothing may reopen it, and Open() must not
// report it, or the one-open-intent rule would lock her out of selling again.
func TestSellIntent_TerminalIntentsAreNotOpenAndTakeNoMoreSteps(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	if err := r.svc.Abandon(context.Background(), r.user.ID, in.ID); err != nil {
		t.Fatal(err)
	}
	if open, _ := r.svc.Open(context.Background(), r.user.ID); open != nil {
		t.Fatalf("an abandoned intent still reads as open: %+v", open)
	}
	if err := r.svc.RecordSwap(context.Background(), r.user.ID, in.ID, testSwapHash); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("recording a swap on an abandoned intent: err = %v", err)
	}
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("opening an order on an abandoned intent: err = %v", err)
	}
	// And she can start again.
	if _, _, err := r.svc.Create(context.Background(), r.user, CreateSellIntentInput{SourceChainID: 56,
		SourceToken: evmNativeAddress, SourceAmount: "1", SourceAddress: r.user.WalletAddress,
		FiatCode: "NGN", BankCode: "044", AccountNumber: "0123456789"}); err != nil {
		t.Fatalf("a new sale after abandoning: %v", err)
	}
}

// THE ACCEPTANCE POINT (brief, "USD vs wei"), as the whole-branch fix
// restated it. The quote budgets a source-chain gas top-up out of the round
// trip in DOLLARS; whatever later SENDS it must send that same top-up, in wei.
// The quote carries the wei it priced (SellSwapQuote.TopUpWei) and the intent
// stores exactly that — never a figure re-derived from the route afterwards,
// which is how a top-up nobody budgeted used to reach the row.
func TestSellIntent_StoresTheTopUpTheQuoteBudgeted(t *testing.T) {
	r := newSellIntentRig(t)
	r.quoter.quote.GasTopUp, r.quoter.quote.TopUpWei = true, "7600000"
	r.quoter.quote.Route.GasCostWei = big.NewInt(1) // re-deriving from the route would give 2
	in := r.create(t)
	if in.QuotedTopUpWei != "7600000" || !in.TopUpBudgeted {
		t.Fatalf("quotedTopUpWei = %q budgeted = %v, want the quote's own 7600000", in.QuotedTopUpWei, in.TopUpBudgeted)
	}
}

// A quote that judged her funded budgeted nothing, so the row says so — even
// when the route quoted its gas in wei and a figure COULD be derived from it.
func TestSellIntent_AQuoteThatBudgetedNoTopUpStoresNone(t *testing.T) {
	r := newSellIntentRig(t)
	r.quoter.quote.Route.GasCostWei = big.NewInt(3_000_000)
	in := r.create(t)
	if in.QuotedTopUpWei != "" || in.TopUpBudgeted {
		t.Fatalf("quotedTopUpWei = %q budgeted = %v, want none", in.QuotedTopUpWei, in.TopUpBudgeted)
	}
}

// Budgeted with no wei (the route quoted none) is kept apart from "not
// budgeted": the first may be priced live at send time, the second never sent.
func TestSellIntent_ABudgetedTopUpWithNoWeiStaysBudgeted(t *testing.T) {
	r := newSellIntentRig(t)
	r.quoter.quote.GasTopUp = true
	in := r.create(t)
	if in.QuotedTopUpWei != "" || !in.TopUpBudgeted {
		t.Fatalf("quotedTopUpWei = %q budgeted = %v, want budgeted with no wei", in.QuotedTopUpWei, in.TopUpBudgeted)
	}
}

// ─── Fix round 1: opening the order exactly once ───────────────────────────

// landedRig is a rig with one intent whose swap has landed and binds.
func landedRig(t *testing.T) (*sellIntentRig, *models.RampSellIntent) {
	t.Helper()
	r := newSellIntentRig(t)
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	return r, in
}

func (r *sellIntentRig) reload(t *testing.T, id uuid.UUID) *models.RampSellIntent {
	t.Helper()
	var in models.RampSellIntent
	if err := r.svc.db.First(&in, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return &in
}

// Two OpenOrder calls racing on one intent. The first is held INSIDE its
// Paycrest request — the exact window in which the old code let a second
// caller through (both had read "no order yet") — and the second runs to
// completion in that window. It must not reach Paycrest, and must say so.
func TestSellIntent_ConcurrentOpenOrderOpensOneOfframp(t *testing.T) {
	r, in := landedRig(t)
	if sqlDB, err := r.svc.db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1) // one :memory: database shared by both goroutines
	}
	r.client.offrampEntered = make(chan struct{})
	r.client.offrampGate = make(chan struct{})

	type result struct {
		order *models.PaycrestOrderRecord
		err   error
	}
	first := make(chan result, 1)
	go func() {
		o, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
		first <- result{o, err}
	}()
	<-r.client.offrampEntered // the first caller is now inside Paycrest

	second, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
	if !errors.Is(err, ErrSellIntentOrdering) || second != nil {
		close(r.client.offrampGate)
		t.Fatalf("second OpenOrder mid-flight: order %v, err %v; want ErrSellIntentOrdering", second, err)
	}

	close(r.client.offrampGate)
	got := <-first
	if got.err != nil || got.order == nil {
		t.Fatalf("first OpenOrder: %v", got.err)
	}
	if r.client.offrampCalls != 1 {
		t.Fatalf("offramp calls = %d, want exactly 1", r.client.offrampCalls)
	}
	var n int64
	r.svc.db.Model(&models.PaycrestOrderRecord{}).Where("user_id = ?", r.user.ID).Count(&n)
	if n != 1 {
		t.Fatalf("paycrest order rows = %d, want 1", n)
	}
	// Once the winner has finished, a retry is the idempotent read-back.
	again, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
	if err != nil || again.ID != got.order.ID || r.client.offrampCalls != 1 {
		t.Fatalf("retry after the race: err %v, calls %d", err, r.client.offrampCalls)
	}
}

// The other interleaving: both callers read the row as swapped (neither has
// claimed yet). The first is paused after its read; the second runs to the
// end and opens the order; then the first resumes and tries to claim a row
// that is no longer what it read. It must lose the claim and hand back the
// winner's order — never open its own.
func TestSellIntent_ALoserThatReadBeforeTheClaimGetsTheWinnersOrder(t *testing.T) {
	r, in := landedRig(t)
	if sqlDB, err := r.svc.db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	r.status.entered = make(chan struct{})
	r.status.gate = make(chan struct{})

	type result struct {
		order *models.PaycrestOrderRecord
		err   error
	}
	slow := make(chan result, 1)
	go func() {
		o, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
		slow <- result{o, err}
	}()
	<-r.status.entered // the slow caller has read the row as swapped

	winner, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
	if err != nil {
		close(r.status.gate)
		t.Fatalf("winner: %v", err)
	}
	close(r.status.gate)
	got := <-slow
	if got.err != nil || got.order == nil || got.order.ID != winner.ID {
		t.Fatalf("loser: order %v, err %v; want the winner's order %s", got.order, got.err, winner.ID)
	}
	if r.client.offrampCalls != 1 {
		t.Fatalf("offramp calls = %d, want exactly 1", r.client.offrampCalls)
	}
}

// While claimed, the intent is still her sale in progress: no second intent,
// and no abandoning it out from under the order being opened.
func TestSellIntent_AClaimedIntentIsStillHerOpenSale(t *testing.T) {
	r, in := landedRig(t)
	r.svc.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).
		Updates(map[string]any{"status": models.SellIntentOrdering, "updated_at": sponsorTestNow})
	if open, _ := r.svc.Open(context.Background(), r.user.ID); open == nil || open.ID != in.ID {
		t.Fatalf("a claimed intent must read as open, got %+v", open)
	}
	if err := r.svc.Abandon(context.Background(), r.user.ID, in.ID); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("abandon mid-order: err = %v, want ErrSellIntentState", err)
	}
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); !errors.Is(err, ErrSellIntentOrdering) {
		t.Fatalf("OpenOrder on a fresh claim: err = %v", err)
	}
	if r.client.offrampCalls != 0 {
		t.Fatal("a claimed intent reached Paycrest a second time")
	}
}

// A claim whose holder died (crash between claim and write-back) must not
// strand her sale forever: past the stale window the next caller takes over.
func TestSellIntent_AStaleClaimIsTakenOver(t *testing.T) {
	r, in := landedRig(t)
	r.svc.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).
		Updates(map[string]any{"status": models.SellIntentOrdering,
			"updated_at": sponsorTestNow.Add(-sellIntentClaimStale - time.Second)})
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err != nil {
		t.Fatalf("stale claim: %v", err)
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentOrdered || r.client.offrampCalls != 1 {
		t.Fatalf("status %q, calls %d", got.Status, r.client.offrampCalls)
	}
}

// A failed Paycrest request gives the claim back, so she can simply retry.
func TestSellIntent_OpenOrderReleasesTheClaimOnFailure(t *testing.T) {
	r, in := landedRig(t)
	r.client.offrampErr = errors.New("paycrest down")
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err == nil {
		t.Fatal("want the Paycrest error")
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped || got.PaycrestOrderID != nil {
		t.Fatalf("after a failed order the intent is %q; want it back to swapped", got.Status)
	}
	r.client.offrampErr = nil
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err != nil {
		t.Fatalf("retry after release: %v", err)
	}
}

// Finding 2: the one-live-sell rule is checked when the order is OPENED, not
// only when the intent was. A direct sell opened in between must win.
func TestSellIntent_OpenOrderRespectsADirectSellOpenedMeanwhile(t *testing.T) {
	r, in := landedRig(t)
	seedOrder(t, r.svc.db, r.user.ID, models.PaycrestDirectionSell, models.PaycrestStatusAwaitingDeposit)
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); !errors.Is(err, ErrPaycrestOrderOpen) {
		t.Fatalf("err = %v, want ErrPaycrestOrderOpen", err)
	}
	if r.client.offrampCalls != 0 {
		t.Fatal("a second live sell order reached Paycrest")
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped {
		t.Fatalf("intent left %q; the claim must be released", got.Status)
	}
}

func TestPaycrestCreateSellForToken_OneLiveSellPerUser(t *testing.T) {
	r := newSellIntentRig(t)
	seedOrder(t, r.svc.db, r.user.ID, models.PaycrestDirectionSell, models.PaycrestStatusAwaitingDeposit)
	_, err := r.svc.orders.CreateSellForToken(context.Background(), PaycrestSellRequest{
		UserID: r.user.ID, WalletAddress: r.user.WalletAddress, FiatCode: "NGN",
		ChainKey: rampSettlementChainKey, Token: rampSettlementToken,
		BankCode: "044", AccountNo: "0123456789"}, "9.975", true)
	if !errors.Is(err, ErrPaycrestOrderOpen) || r.client.offrampCalls != 0 {
		t.Fatalf("err = %v, calls %d; want ErrPaycrestOrderOpen and no Paycrest call", err, r.client.offrampCalls)
	}
}

// What she is owed is computed the way her quote was — exactly, never through
// a float64. 1.01 USDC at 1492.50 is ₦1507.425: the quote showed her 1507.43,
// and float64 arithmetic lands just under the half and says 1507.42.
func TestPaycrestCreateSellForToken_OwesTheKoboHerQuoteShowed(t *testing.T) {
	r := newSellIntentRig(t)
	order, err := r.svc.orders.CreateSellForToken(context.Background(), PaycrestSellRequest{
		UserID: r.user.ID, WalletAddress: r.user.WalletAddress, FiatCode: "NGN",
		ChainKey: rampSettlementChainKey, Token: rampSettlementToken,
		BankCode: "044", AccountNo: "0123456789"}, "1.01", true)
	if err != nil {
		t.Fatal(err)
	}
	quoted := new(big.Rat).Mul(usdcRat(big.NewInt(1_010000)), big.NewRat(149250, 100)).FloatString(2)
	if order.FiatAmount != "1507.43" || order.FiatAmount != quoted {
		t.Fatalf("fiat = %s, want %s — the figure her quote was priced at", order.FiatAmount, quoted)
	}
}

// §5.5: a swap that failed, or was refunded or only partly delivered, is a
// terminal answer with LiFi's substatus — not "still swapping" forever, and
// not a mismatch alarm.
func TestSellIntent_OpenOrderSurfacesAFailedSwap(t *testing.T) {
	for name, set := range map[string]func(*LiFiTransferStatus){
		"failed":   func(s *LiFiTransferStatus) { s.Status, s.Substatus = LiFiStatusFailed, "" },
		"refunded": func(s *LiFiTransferStatus) { s.Substatus = LiFiSubstatusRefunded },
		"partial":  func(s *LiFiTransferStatus) { s.Substatus = LiFiSubstatusPartial },
	} {
		t.Run(name, func(t *testing.T) {
			r, in := landedRig(t)
			set(r.status.st)
			_, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
			if !errors.Is(err, ErrSwapFailed) {
				t.Fatalf("err = %v, want ErrSwapFailed", err)
			}
			// The outcome travels as data, not only inside the message.
			var ended *SwapEndedShortError
			if !errors.As(err, &ended) || ended.Status != r.status.st.Status || ended.Substatus != r.status.st.Substatus {
				t.Fatalf("err = %#v, want SwapEndedShortError{%s, %s}", err, r.status.st.Status, r.status.st.Substatus)
			}
			if r.client.offrampCalls != 0 {
				t.Fatal("a failed swap reached Paycrest")
			}
			if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped {
				t.Fatalf("intent %q; a failed swap must leave it for her to abandon", got.Status)
			}
		})
	}
}

// ─── Fix round 2: the claim's status clause, and giving a claim back ──────

// The interleaving the status clause exists for: both callers read the row as
// swapped; the second claims it and is held inside Paycrest; only then does
// the first try to claim. The row still has no order recorded, so without
// "status = what I read" in the claim the first caller would win too and open
// a second offramp. It must lose and be told to retry.
func TestSellIntent_BothReadSwappedAndTheLaterClaimLoses(t *testing.T) {
	r, in := landedRig(t)
	if sqlDB, err := r.svc.db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	r.status.entered = make(chan struct{})
	r.status.gate = make(chan struct{})
	r.client.offrampEntered = make(chan struct{})
	r.client.offrampGate = make(chan struct{})

	type result struct {
		order *models.PaycrestOrderRecord
		err   error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() {
		o, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
		first <- result{o, err}
	}()
	<-r.status.entered // the first caller has read swapped and is paused in LiFi
	go func() {
		o, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
		second <- result{o, err}
	}()
	<-r.client.offrampEntered // the second caller has claimed and is inside Paycrest

	close(r.status.gate)
	lost := <-first // its claim runs while the winner is still inside Paycrest
	close(r.client.offrampGate)
	won := <-second

	if !errors.Is(lost.err, ErrSellIntentOrdering) || lost.order != nil {
		t.Fatalf("late claimer: order %v, err %v; want ErrSellIntentOrdering", lost.order, lost.err)
	}
	if won.err != nil || won.order == nil {
		t.Fatalf("winner: %v", won.err)
	}
	if r.client.offrampCalls != 1 {
		t.Fatalf("offramp calls = %d, want exactly 1", r.client.offrampCalls)
	}
}

// She closed the app (the request context was cancelled) while Paycrest was
// failing. The claim must still be given back: otherwise her sale sits in
// ordering for the whole stale window, answering "being opened" to every retry.
func TestSellIntent_AReleaseSurvivesACancelledRequest(t *testing.T) {
	r, in := landedRig(t)
	r.client.offrampEntered = make(chan struct{})
	r.client.offrampGate = make(chan struct{})
	r.client.offrampErr = errors.New("client went away")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.svc.OpenOrder(ctx, r.user, in.ID)
		done <- err
	}()
	<-r.client.offrampEntered // claimed, inside Paycrest
	cancel()
	close(r.client.offrampGate)
	if err := <-done; err == nil {
		t.Fatal("want the Paycrest error")
	}

	if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped || got.PaycrestOrderID != nil {
		t.Fatalf("after a cancelled, failed order the intent is %q; want it back to swapped", got.Status)
	}
}

// A caller can lose its claim because the row was claimed AND given back in
// between: it read a stale ordering claim, someone else took that claim over,
// failed at Paycrest and released it to swapped. Nothing is being opened and
// nothing is dead — the sale is retryable, so the loser must hear "try again",
// not "this sale can't take that step".
func TestSellIntent_ALoserAfterAReleaseIsToldToRetry(t *testing.T) {
	r, in := landedRig(t)
	if sqlDB, err := r.svc.db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	r.svc.db.Model(&models.RampSellIntent{}).Where("id = ?", in.ID).
		Updates(map[string]any{"status": models.SellIntentOrdering,
			"updated_at": sponsorTestNow.Add(-sellIntentClaimStale - time.Second)})
	r.status.entered = make(chan struct{})
	r.status.gate = make(chan struct{})
	r.client.offrampErr = errors.New("paycrest down")

	slow := make(chan error, 1)
	go func() {
		_, err := r.svc.OpenOrder(context.Background(), r.user, in.ID)
		slow <- err
	}()
	<-r.status.entered // the slow caller read the stale ordering claim

	// Another caller takes the stale claim over, fails at Paycrest, releases.
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err == nil {
		close(r.status.gate)
		t.Fatal("takeover: want the Paycrest error")
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped {
		close(r.status.gate)
		t.Fatalf("after the takeover failed the intent is %q; want swapped", got.Status)
	}

	close(r.status.gate)
	if err := <-slow; !errors.Is(err, ErrSellIntentOrdering) {
		t.Fatalf("loser after a release: err = %v, want ErrSellIntentOrdering", err)
	}
	if r.client.offrampCalls != 1 {
		t.Fatalf("offramp calls = %d, want 1 (the takeover's)", r.client.offrampCalls)
	}

	r.client.offrampErr = nil
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err != nil {
		t.Fatalf("retry after the release: %v", err)
	}
}

// ─── Whole-branch fix: no cashback on a fee we cannot show was collected ───

// A Solana route carries no transactionId, so nothing binds her swap to OUR
// fee-carrying quote: an equal-amount route she assembled without our fee
// passes every other clause. Booking the fee anyway would pay buyer and upline
// cashback — real money — out of a fee that may never have been collected.
func TestSellIntent_NoFeeBookedWhenNoTransactionIdBoundTheSwap(t *testing.T) {
	const solOwner = "9xQeWvG816bUx9EPjHmaT23yvVM2ZWbrrpZb9PusVFin"
	const solMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	sig := strings.Repeat("5", 88)
	r := newSellIntentRig(t)
	fees := &fakeFeeEnqueuer{}
	r.svc.SetFeeService(fees)
	r.quoter.quote.Route.TransactionID = "" // a Solana quote: nothing to bind
	r.quoter.quote.Route.FromAmountUSD = 10
	in, _, err := r.svc.Create(context.Background(), r.user, CreateSellIntentInput{
		SourceChainID: solanaLiFiChainID, SourceToken: solMint, SourceAmount: "10000000",
		SourceAddress: solOwner, FiatCode: "NGN", BankCode: "044", AccountNumber: "0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, sig)
	st := r.landed(in)
	st.TransactionID = ""
	r.status.st = st
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID); err != nil {
		t.Fatalf("OpenOrder: %v — the sale itself still completes", err)
	}
	if len(fees.events) != 0 {
		t.Fatalf("fee events = %+v; no cashback may be paid on an unbound Solana sale", fees.events)
	}
}

// ─── Whole-branch fix: an ordered sale is still resumable ──────────────────

// A sale killed after its payout opened but before the USDC was sent into it
// is ordered — not open, by design — yet "Finish your sale" has to find it
// while its order still awaits the deposit, and only then.
func TestSellIntent_ResumableFindsAnOrderedSaleAwaitingItsDeposit(t *testing.T) {
	r, in := landedRig(t)
	ctx := context.Background()
	if got, err := r.svc.Resumable(ctx, r.user.ID); err != nil || got == nil || got.ID != in.ID {
		t.Fatalf("an open sale: got %+v err %v", got, err)
	}
	order, err := r.svc.OpenOrder(ctx, r.user, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if open, _ := r.svc.Open(ctx, r.user.ID); open != nil {
		t.Fatalf("ordered must stay out of Open (the one-open-sale rule): %+v", open)
	}
	got, err := r.svc.Resumable(ctx, r.user.ID)
	if err != nil || got == nil || got.ID != in.ID || got.Status != models.SellIntentOrdered {
		t.Fatalf("an ordered sale awaiting its deposit: got %+v err %v", got, err)
	}
	// Another user never sees it.
	if other, _ := r.svc.Resumable(ctx, uuid.New()); other != nil {
		t.Fatalf("a stranger resumed %+v", other)
	}
	// Once the order stops waiting for her deposit there is nothing to finish.
	if err := r.svc.db.Model(&models.PaycrestOrderRecord{}).Where("id = ?", order.ID).
		Update("status", models.PaycrestStatusSettled).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := r.svc.Resumable(ctx, r.user.ID); err != nil || got != nil {
		t.Fatalf("a settled order: got %+v err %v, want nothing to resume", got, err)
	}
}
