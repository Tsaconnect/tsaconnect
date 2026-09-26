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
	quote  *SellSwapQuote
	err    error
	calls  int
	lastIn SellQuoteInput
}

func (f *fakeSellQuoter) Quote(_ context.Context, in SellQuoteInput) (*SellSwapQuote, error) {
	f.calls++
	f.lastIn = in
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); !errors.Is(err, ErrSwapNotLanded) {
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
	order, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.client.lastOfframp.AmountToken != "9.975" || r.client.lastOfframp.SenderFeePercent != "0.5" {
		t.Fatalf("offramp = %+v, want the chain's 9.975 USDC at fee 0.5", r.client.lastOfframp)
	}
	again, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
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
			if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); !errors.Is(err, ErrSwapMismatch) {
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err == nil {
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err != nil {
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

// fakeFeeEnqueuer books like the real engine: one fee per (source type, source
// id) — its guard row — so a replayed booking is recorded as a call, not as a
// second fee.
type fakeFeeEnqueuer struct {
	events []FeeEvent
	calls  int
}

func (f *fakeFeeEnqueuer) EnqueueFeeSettled(e FeeEvent) {
	f.calls++
	for _, got := range f.events {
		if got.SourceType == e.SourceType && got.SourceID == e.SourceID {
			return
		}
	}
	f.events = append(f.events, e)
}

func TestSellIntent_BooksOurFeeOnceThroughTheFeeEngine(t *testing.T) {
	r := newSellIntentRig(t)
	fees := &fakeFeeEnqueuer{}
	r.svc.SetFeeService(fees)
	r.quoter.quote.Route.FromAmountUSD = 10
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	for i := 0; i < 2; i++ {
		if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err != nil {
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
	if _, err := r.svc.OpenOrder(context.Background(), &stranger, in.ID, false); err == nil {
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); !errors.Is(err, ErrSellIntentState) {
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
		o, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
		first <- result{o, err}
	}()
	<-r.client.offrampEntered // the first caller is now inside Paycrest

	second, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
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
	again, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
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
		o, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
		slow <- result{o, err}
	}()
	<-r.status.entered // the slow caller has read the row as swapped

	winner, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); !errors.Is(err, ErrSellIntentOrdering) {
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err != nil {
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err == nil {
		t.Fatal("want the Paycrest error")
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped || got.PaycrestOrderID != nil {
		t.Fatalf("after a failed order the intent is %q; want it back to swapped", got.Status)
	}
	r.client.offrampErr = nil
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err != nil {
		t.Fatalf("retry after release: %v", err)
	}
}

// Finding 2: the one-live-sell rule is checked when the order is OPENED, not
// only when the intent was. A direct sell opened in between must win.
func TestSellIntent_OpenOrderRespectsADirectSellOpenedMeanwhile(t *testing.T) {
	r, in := landedRig(t)
	seedOrder(t, r.svc.db, r.user.ID, models.PaycrestDirectionSell, models.PaycrestStatusAwaitingDeposit)
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); !errors.Is(err, ErrPaycrestOrderOpen) {
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
			_, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
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
		o, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
		first <- result{o, err}
	}()
	<-r.status.entered // the first caller has read swapped and is paused in LiFi
	go func() {
		o, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
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
		_, err := r.svc.OpenOrder(ctx, r.user, in.ID, false)
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
		_, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
		slow <- err
	}()
	<-r.status.entered // the slow caller read the stale ordering claim

	// Another caller takes the stale claim over, fails at Paycrest, releases.
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err == nil {
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err != nil {
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
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err != nil {
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
	order, err := r.svc.OpenOrder(ctx, r.user, in.ID, false)
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

// ─── Bank-detail retention ─────────────────────────────────────────────────

// giveAccountName sets the optional account name on the stored row, so the
// clearing below is proven for all three bank columns, not just the two the
// rig's Create fills.
func (r *sellIntentRig) giveAccountName(t *testing.T, id uuid.UUID) {
	t.Helper()
	if err := r.svc.db.Model(&models.RampSellIntent{}).Where("id = ?", id).
		Update("account_name", "ADA OBI").Error; err != nil {
		t.Fatal(err)
	}
}

func assertBankDetailsCleared(t *testing.T, in *models.RampSellIntent) {
	t.Helper()
	if in.BankCode != "" || in.AccountNumber != "" || in.AccountName != "" {
		t.Fatalf("bank details survived the %s transition: code=%q number=%q name=%q",
			in.Status, in.BankCode, in.AccountNumber, in.AccountName)
	}
}

// An abandoned sale will never pay anyone: her bank account goes with it, in
// the same write that fails the intent.
func TestSellIntent_AbandonClearsBankDetails(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	r.giveAccountName(t, in.ID)
	if err := r.svc.Abandon(context.Background(), r.user.ID, in.ID); err != nil {
		t.Fatal(err)
	}
	got := r.reload(t, in.ID)
	if got.Status != models.SellIntentFailed {
		t.Fatalf("status = %q, want failed", got.Status)
	}
	assertBankDetailsCleared(t, got)
}

// Once the order is open it carries its own copy of the payout destination,
// so the intent's copy is dropped as it moves to ordered — and the order,
// which still has to pay her, keeps its own.
func TestSellIntent_OpeningTheOrderClearsTheIntentsBankDetails(t *testing.T) {
	r, in := landedRig(t)
	r.giveAccountName(t, in.ID)
	order, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	got := r.reload(t, in.ID)
	if got.Status != models.SellIntentOrdered {
		t.Fatalf("status = %q, want ordered", got.Status)
	}
	assertBankDetailsCleared(t, got)

	var stored models.PaycrestOrderRecord
	if err := r.svc.db.First(&stored, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.PayoutAccount != "0123456789" || stored.PayoutBankCode != "044" || stored.PayoutAccountName != "ADA OBI" {
		t.Fatalf("the order lost the destination it must still pay: bank=%q account=%q name=%q",
			stored.PayoutBankCode, stored.PayoutAccount, stored.PayoutAccountName)
	}
	// Idempotent replay still answers from the order, not the cleared intent.
	again, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
	if err != nil || again.ID != order.ID {
		t.Fatalf("replay after clearing: order %v err %v", again, err)
	}
}

// A failed attempt to open the order hands the claim back: she can retry, so
// the destination she typed must still be there.
func TestSellIntent_AReleasedClaimKeepsBankDetails(t *testing.T) {
	r, in := landedRig(t)
	r.client.offrampErr = errors.New("paycrest down")
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false); err == nil {
		t.Fatal("want the Paycrest failure")
	}
	got := r.reload(t, in.ID)
	if got.Status != models.SellIntentSwapped || got.BankCode != "044" || got.AccountNumber != "0123456789" {
		t.Fatalf("a retryable sale lost its destination: %+v", got)
	}
}

// ─── Pay intents: a sale that pays SOMEONE ELSE an exact naira amount ──────

// payRig is the sale rig with the quoter answering an exact-output route: the
// quote's own fromAmount is what she signs, so it is what the intent binds to.
func payRig(t *testing.T) *sellIntentRig {
	t.Helper()
	r := newSellIntentRig(t)
	r.quoter.quote.Route.FromAmount = "17000000000000000"
	// LiFi answers the exact-output ask for 33.9 USDC with what it will
	// deliver (toAmount) and its slippage floor (toAmountMin).
	r.quoter.quote.Route.ToAmount = big.NewInt(33_900000)
	r.quoter.quote.Route.ToAmountMin = big.NewInt(33_561000)
	return r
}

func payIntentInput(pid uuid.UUID, u *models.User) CreateSellIntentInput {
	return CreateSellIntentInput{SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: u.WalletAddress,
		FiatCode: "NGN", BankCode: "044", AccountNumber: "9999999999", PayPaymentID: &pid, PayoutFiat: "50000.00", ToAmount: "33900000"}
}

func balanceOf(usdc int64) func(context.Context, string) (*big.Int, error) {
	return func(context.Context, string) (*big.Int, error) { return big.NewInt(usdc), nil }
}

func TestSellIntent_PayIntentOpensAnExactNairaOrderToTheMerchantFromTheLandedAmount(t *testing.T) {
	r := payRig(t)
	r.base.amount = big.NewInt(34_000000) // landed: enough
	r.svc.SetUSDCBalanceReader(balanceOf(500_000000))
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	order, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	off := r.client.lastOfframp
	if off.Recipient.AccountIdentifier != "9999999999" || off.SenderFeePercent != "0.5" {
		t.Fatalf("offramp = %+v", off)
	}
	dep, _ := r.svc.orders.SellDepositFor(context.Background(), "NGN", "50000.00", "base", "USDC", true)
	if off.AmountToken != BaseUnitsToDecimal(dep, 6) {
		t.Fatalf("order amount %s — must be the deposit for ₦50,000 (%s), never the wallet balance", off.AmountToken, dep)
	}
	if order.FiatAmount != "50000.00" {
		t.Fatalf("order owes %s, want the stated 50000.00", order.FiatAmount)
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentOrdered || got.PaycrestOrderID == nil {
		t.Fatalf("intent = %+v", got)
	}
}

// The masked responses are only a view: the order the offramp is opened for is
// built from the intent's own row, and it is marked as a pay attempt so the
// customer's routes know to mask it — including after the payment lets it go.
func TestSellIntent_APayIntentsOrderIsMarkedAsAPaymentsAndStillPaysTheRealAccount(t *testing.T) {
	r := payRig(t)
	r.base.amount = big.NewInt(34_000000)
	r.svc.SetUSDCBalanceReader(balanceOf(500_000000))
	pid := uuid.New()
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(pid, r.user))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	order, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if r.client.lastOfframp.Recipient.AccountIdentifier != "9999999999" {
		t.Fatalf("the offramp was opened for %+v", r.client.lastOfframp.Recipient)
	}
	var row models.PaycrestOrderRecord
	if err := r.svc.db.First(&row, "id = ?", order.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.PayPaymentID == nil || *row.PayPaymentID != pid || row.PayoutAccount != "9999999999" {
		t.Fatalf("stored order = %+v", row)
	}
}

func TestSellIntent_PayIntentShortfallNeedsConsentToUseTheWallet(t *testing.T) {
	r := payRig(t)
	r.base.amount = big.NewInt(9_975000) // landed short
	r.svc.SetUSDCBalanceReader(balanceOf(500_000000))
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	_, err = r.svc.OpenOrder(context.Background(), r.user, in.ID, false)
	var short *SellShortfallError
	if !errors.As(err, &short) || r.client.offrampCalls != 0 {
		t.Fatalf("without consent: err %v calls %d — her other USDC must not be spent silently", err, r.client.offrampCalls)
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped {
		t.Fatalf("status after a shortfall = %q, want the claim given back (swapped) so she can consent and retry", got.Status)
	}
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, true); err != nil {
		t.Fatalf("with consent: %v", err)
	}
}

func TestSellIntent_PayIntentWalletConsentWithoutABalanceReaderIsRefused(t *testing.T) {
	r := payRig(t)
	r.base.amount = big.NewInt(9_975000)
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, true); !errors.Is(err, ErrSellUnavailable) || r.client.offrampCalls != 0 {
		t.Fatalf("err %v calls %d — an unreadable balance must not be assumed sufficient", err, r.client.offrampCalls)
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentSwapped {
		t.Fatalf("status = %q: refusing must not leave the sale claimed", got.Status)
	}
}

// The flag is meaningless for an ordinary sale: it always sells exactly what
// landed, so a client cannot widen it.
func TestSellIntent_AnOrdinarySaleIgnoresTheWalletBalanceFlag(t *testing.T) {
	r := newSellIntentRig(t)
	r.svc.SetUSDCBalanceReader(balanceOf(500_000000))
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, true); err != nil {
		t.Fatal(err)
	}
	if r.client.lastOfframp.AmountToken != "9.975" {
		t.Fatalf("offramp amount = %q, want the landed 9.975", r.client.lastOfframp.AmountToken)
	}
}

func TestSellIntent_ExactOutputStoresTheRoutesFromAmount(t *testing.T) {
	r := payRig(t)
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
	if err != nil || in.SourceAmount != "17000000000000000" || in.PayoutFiat != "50000.00" || in.PayPaymentID == nil {
		t.Fatalf("err %v intent %+v", err, in)
	}
	if r.quoter.lastIn.ToAmount != "33900000" || r.quoter.lastIn.FromAmount != "" {
		t.Fatalf("quoted %+v, want an exact-output ask", r.quoter.lastIn)
	}
}

// An exact-output route that reports no fromAmount leaves nothing to bind the
// swap to: refused before a row exists rather than stored with an empty amount
// that boundToIntent could never match.
func TestSellIntent_ExactOutputWithoutARouteFromAmountIsRefused(t *testing.T) {
	r := newSellIntentRig(t)
	r.quoter.quote.Route.ToAmount, r.quoter.quote.Route.ToAmountMin = big.NewInt(33_900000), big.NewInt(33_561000)
	if _, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user)); !errors.Is(err, ErrSellUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if open, _ := r.svc.Open(context.Background(), r.user.ID); open != nil {
		t.Fatalf("an intent was stored: %+v", open)
	}
}

func TestSellIntent_ACustomerCannotAbandonAPayIntentWhoseSwapWasSent(t *testing.T) {
	r := payRig(t)
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	if err := r.svc.Abandon(context.Background(), r.user.ID, in.ID); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("err = %v", err)
	}
	if err := r.svc.AbandonForPay(context.Background(), in.ID, r.reload(t, in.ID).SwapTxHash); err != nil {
		t.Fatalf("the reconciler's path: %v", err)
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentFailed || got.AccountNumber != "" {
		t.Fatalf("intent = %+v, want failed with the bank details erased", got)
	}
	if open, _ := r.svc.Open(context.Background(), r.user.ID); open != nil {
		t.Fatal("the failed pay intent must free the one-open-sale index")
	}
}

// Before any swap was sent there is nothing that can still deliver, so she may
// walk away from a pay intent like any other sale.
func TestSellIntent_ACustomerCanAbandonAPayIntentBeforeSwapping(t *testing.T) {
	r := payRig(t)
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Abandon(context.Background(), r.user.ID, in.ID); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestSellIntent_SwapStatusReadsLiFiForTheRecordedHash(t *testing.T) {
	r := payRig(t)
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	if st, err := r.svc.SwapStatus(context.Background(), in.ID); st != nil || err != nil || r.status.calls != 0 {
		t.Fatalf("no swap yet: %+v %v (status calls %d) — must be nil, nil without asking LiFi", st, err, r.status.calls)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	st, err := r.svc.SwapStatus(context.Background(), in.ID)
	if err != nil || st == nil || st.Status != LiFiStatusDone {
		t.Fatalf("status %+v err %v", st, err)
	}
	if _, err := r.svc.SwapStatus(context.Background(), uuid.New()); !errors.Is(err, ErrSellIntentNotFound) {
		t.Fatalf("unknown intent: err = %v", err)
	}
}

// ─── I2: an exact-output route is checked against what was asked ───────────

func TestSellIntent_ExactOutputRouteMustDeliverWhatWasAsked(t *testing.T) {
	cases := map[string]func(rt *LiFiRoute){
		"a smaller toAmount than asked": func(rt *LiFiRoute) { rt.ToAmount = big.NewInt(33_899999) },
		"a missing toAmount":            func(rt *LiFiRoute) { rt.ToAmount = nil },
		"a zero toAmount":               func(rt *LiFiRoute) { rt.ToAmount = big.NewInt(0) },
		"a missing toAmountMin":         func(rt *LiFiRoute) { rt.ToAmountMin = nil },
		"a zero toAmountMin":            func(rt *LiFiRoute) { rt.ToAmountMin = big.NewInt(0) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := payRig(t)
			mutate(r.quoter.quote.Route)
			_, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
			if !errors.Is(err, ErrSellUnavailable) {
				t.Fatalf("err = %v, want ErrSellUnavailable", err)
			}
			if open, _ := r.svc.Open(context.Background(), r.user.ID); open != nil {
				t.Fatalf("an intent was stored for a route that under-delivers: %+v", open)
			}
		})
	}
}

// The caller pads the ask (Task 13: 1% over the deposit; Task 15: 50 bps so
// toAmountMin still covers what is owed), so a route may deliver MORE than was
// asked and its toAmountMin may sit BELOW the ask. Neither is refused: the
// consented-shortfall path is the backstop for slippage, not this check.
func TestSellIntent_ExactOutputAPaddedRouteIsAccepted(t *testing.T) {
	r := payRig(t)
	r.quoter.quote.Route.ToAmount = big.NewInt(34_239000) // 1% over the ask
	r.quoter.quote.Route.ToAmountMin = big.NewInt(33_800000)
	if _, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user)); err != nil {
		t.Fatal(err)
	}
}

// ─── S1: the pay fields travel together ────────────────────────────────────

func TestSellIntent_PartialPayFieldsAreRefusedBeforeAnythingIsQuoted(t *testing.T) {
	pid := uuid.New()
	cases := map[string]func(in *CreateSellIntentInput){
		"payout without a payment id or amount": func(in *CreateSellIntentInput) { in.PayPaymentID, in.ToAmount = nil, "" },
		"payout and amount without a payment":   func(in *CreateSellIntentInput) { in.PayPaymentID = nil },
		"payout and payment without an amount":  func(in *CreateSellIntentInput) { in.ToAmount = "" },
		"payment id alone":                      func(in *CreateSellIntentInput) { in.PayoutFiat, in.ToAmount = "", "" },
		"amount alone":                          func(in *CreateSellIntentInput) { in.PayoutFiat, in.PayPaymentID = "", nil },
		"payment id and amount without payout":  func(in *CreateSellIntentInput) { in.PayoutFiat = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := payRig(t)
			in := payIntentInput(pid, r.user)
			mutate(&in)
			_, _, err := r.svc.Create(context.Background(), r.user, in)
			if !errors.Is(err, ErrSellIntentState) || r.quoter.calls != 0 {
				t.Fatalf("err %v, quotes %d — a half-set pay intent must be refused up front", err, r.quoter.calls)
			}
			if open, _ := r.svc.Open(context.Background(), r.user.ID); open != nil {
				t.Fatalf("an intent was stored: %+v", open)
			}
		})
	}
}

// ─── S2: consent widens the cap, it never narrows it ───────────────────────

func TestSellIntent_PayIntentConsentNeverCapsBelowWhatLanded(t *testing.T) {
	r := payRig(t)
	r.base.amount = big.NewInt(34_000000) // landed covers the ₦50,000 deposit
	r.svc.SetUSDCBalanceReader(balanceOf(5_000000))
	in, _, err := r.svc.Create(context.Background(), r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	r.status.st = r.landed(in)
	// The swap's USDC is in her wallet, so a balance below it can only mean a
	// stale read: it must not turn a covered payment into pay_shortfall.
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, true); err != nil {
		t.Fatalf("err = %v: a balance under the proven landed amount produced a shortfall", err)
	}
}

// The reconciler reads an intent, decides its swap is dead, then abandons: a
// swap recorded in between must not be ended by that stale decision.
func TestSellIntent_AbandonForPayEndsOnlyTheSwapTheCallerRead(t *testing.T) {
	ctx := context.Background()
	r := payRig(t)
	in, _, err := r.svc.Create(ctx, r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	other := "0x" + strings.Repeat("cd", 32)
	if err := r.svc.AbandonForPay(ctx, in.ID, &other); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("a hash that was never recorded: err = %v", err)
	}
	r.mustRecord(t, in.ID, testSwapHash) // her swap lands after the reconciler's read
	if err := r.svc.AbandonForPay(ctx, in.ID, nil); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("'no swap' abandon after a swap was recorded: err = %v", err)
	}
	if err := r.svc.AbandonForPay(ctx, in.ID, &other); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("another swap's hash: err = %v", err)
	}
	if got := r.reload(t, in.ID); got.Status == models.SellIntentFailed || got.AccountNumber == "" {
		t.Fatalf("a guarded abandon ended the sale anyway: %+v", got)
	}
	hash := testSwapHash
	if err := r.svc.AbandonForPay(ctx, in.ID, &hash); err != nil {
		t.Fatalf("abandon guarded on the swap actually recorded: %v", err)
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentFailed {
		t.Fatalf("intent = %+v", got)
	}
}

func TestSellIntent_AbandonForPayWithNoSwapEndsAnUnsignedIntent(t *testing.T) {
	ctx := context.Background()
	r := payRig(t)
	in, _, err := r.svc.Create(ctx, r.user, payIntentInput(uuid.New(), r.user))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.svc.AbandonForPay(ctx, in.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := r.reload(t, in.ID); got.Status != models.SellIntentFailed {
		t.Fatalf("intent = %+v", got)
	}
}

// ─── S4: AbandonForPay only ever ends a pay intent ─────────────────────────

func TestSellIntent_AbandonForPayWillNotEndAnOrdinarySale(t *testing.T) {
	r := newSellIntentRig(t)
	in := r.create(t)
	r.mustRecord(t, in.ID, testSwapHash)
	if err := r.svc.AbandonForPay(context.Background(), in.ID, r.reload(t, in.ID).SwapTxHash); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("err = %v, want ErrSellIntentState", err)
	}
	if got := r.reload(t, in.ID); got.Status == models.SellIntentFailed || got.AccountNumber == "" {
		t.Fatalf("the reconciler's path ended an ordinary sale: %+v", got)
	}
}

// sell_intent_delivery_test.go — a sale that DELIVERS a token to a merchant's
// wallet instead of opening an offramp (TSA Pay's crypto route).
//
// What these pin is the one question the money hangs on: was the merchant's
// wallet actually credited what it is owed? ConfirmDelivery answers it, and it
// must answer no to everything short of "yes, in full, there".

const (
	deliveryOwed    = "33323336332433603252"
	deliveryToken   = payMerchantToken
	deliveryMerch   = payMerchantWallet
	deliveryDestTx  = "0xdesttx"
	deliveryChain   = payMerchantChainID
	deliveryChainKy = "optimism"
)

// recordingVerifier is an ERC20TransferVerifier that says what it was asked.
type recordingVerifier struct {
	amount                 *big.Int
	err                    error
	calls                  int
	gotTx, gotToken, gotTo string
}

func (v *recordingVerifier) VerifyTransfer(_ context.Context, txHash, token, to string) (*big.Int, error) {
	v.calls++
	v.gotTx, v.gotToken, v.gotTo = txHash, token, to
	return v.amount, v.err
}

func deliveryChainName(id int64) (string, bool) {
	if id == deliveryChain {
		return deliveryChainKy, true
	}
	return "", false
}

func deliveryInput(r *sellIntentRig) CreateSellIntentInput {
	pid := uuid.New()
	return CreateSellIntentInput{
		SourceChainID: 56, SourceToken: evmNativeAddress, SourceAddress: r.user.WalletAddress,
		PayPaymentID: &pid, ToAmount: "33500000000000000000", BudgetBps: 100,
		DestChainID: deliveryChain, DestTokenAddress: deliveryToken, DestAddress: deliveryMerch, DestAmount: deliveryOwed,
	}
}

// deliveryRig is the ramp rig with a delivery quote and a destination verifier.
func newDeliveryRig(t *testing.T) (*sellIntentRig, *recordingVerifier, *fakeFeeEnqueuer) {
	t.Helper()
	r := newSellIntentRig(t)
	// An exact-output route as LiFi answers one: the requested ToAmount, with
	// toAmountMin under it by the slippage the caller padded for.
	r.quoter.quote = &SellSwapQuote{Route: &LiFiRoute{TransactionID: "0xabc", GasLimit: 300_000,
		FromAmount: "17000000000000000", FromAmountUSD: 10,
		ToAmount: mustBig(t, "33500000000000000000"), ToAmountMin: mustBig(t, "33332500000000000000")}, FeeBps: 80}
	v := &recordingVerifier{amount: mustBig(t, deliveryOwed)}
	r.svc.SetDestVerifiers(map[string]ERC20TransferVerifier{deliveryChainKy: v}, deliveryChainName)
	fees := &fakeFeeEnqueuer{}
	r.svc.SetFeeService(fees)
	return r, v, fees
}

// createDelivery opens a delivery sale and records its swap: the state a
// reconciler finds it in.
func (r *sellIntentRig) createDelivery(t *testing.T) *models.RampSellIntent {
	t.Helper()
	in, _, err := r.svc.Create(context.Background(), r.user, deliveryInput(r))
	if err != nil {
		t.Fatal(err)
	}
	r.mustRecord(t, in.ID, testSwapHash)
	return in
}

// deliveredStatus is a LiFi status that matches the delivery intent in every
// bound field: the merchant's token, on the merchant's chain, to the merchant.
func (r *sellIntentRig) deliveredStatus(in *models.RampSellIntent) *LiFiTransferStatus {
	amt, _ := new(big.Int).SetString(in.SourceAmount, 10)
	return &LiFiTransferStatus{Status: LiFiStatusDone, Substatus: LiFiSubstatusCompleted, DestTxHash: deliveryDestTx,
		DestChainID: deliveryChain, ToAddress: deliveryMerch, ReceivingToken: deliveryToken,
		SendingChainID: in.SourceChainID, SendingToken: in.SourceTokenAddress, SendingAmount: amt,
		FromAddress: in.SourceAddress, TransactionID: "0xabc"}
}

func TestSellIntent_DestDefaultsToUSDCOnBaseInHerWallet(t *testing.T) {
	ordinary := &models.RampSellIntent{WalletAddress: "0xHer"}
	if c, tok, addr := ordinary.Dest(8453, sellUSDC); c != 8453 || tok != sellUSDC || addr != "0xHer" {
		t.Fatalf("Dest() = %d %s %s", c, tok, addr)
	}
	delivery := &models.RampSellIntent{WalletAddress: "0xHer", DestChainID: 10, DestTokenAddress: "0xTok", DestAddress: "0xMerchant"}
	if c, tok, addr := delivery.Dest(8453, sellUSDC); c != 10 || tok != "0xTok" || addr != "0xMerchant" {
		t.Fatalf("Dest() = %d %s %s", c, tok, addr)
	}
}

func TestSellIntent_CreateDeliveryAsksTheQuoterForTheMerchantAndNeedsNoBank(t *testing.T) {
	r, _, _ := newDeliveryRig(t)
	in, _, err := r.svc.Create(context.Background(), r.user, deliveryInput(r))
	if err != nil {
		t.Fatal(err)
	}
	got := r.quoter.lastIn
	if got.ToChainID != deliveryChain || got.ToToken != deliveryToken || got.ToAddress != deliveryMerch ||
		got.ToAmount != "33500000000000000000" || got.BudgetBps != 100 || got.FromAmount != "" {
		t.Fatalf("quoter asked %+v", got)
	}
	if in.DestChainID != deliveryChain || in.DestTokenAddress != deliveryToken || in.DestAddress != deliveryMerch ||
		in.DestAmount != deliveryOwed || in.SourceAmount != "17000000000000000" || in.FeeBps != 80 ||
		in.BankCode != "" || in.AccountNumber != "" || in.Status != models.SellIntentSwapping || in.PayPaymentID == nil {
		t.Fatalf("intent = %+v", in)
	}
	if r.client.offrampCalls != 0 {
		t.Fatal("a delivery has no Paycrest leg")
	}
}

func TestSellIntent_CreateDeliveryNeedsEveryFieldTheProofWillRead(t *testing.T) {
	for name, mutate := range map[string]func(*CreateSellIntentInput){
		"no dest chain":    func(in *CreateSellIntentInput) { in.DestChainID = 0 },
		"no dest token":    func(in *CreateSellIntentInput) { in.DestTokenAddress = "" },
		"no dest amount":   func(in *CreateSellIntentInput) { in.DestAmount = "" },
		"zero dest amount": func(in *CreateSellIntentInput) { in.DestAmount = "0" },
		"junk dest amount": func(in *CreateSellIntentInput) { in.DestAmount = "1.5" },
		"no exact output":  func(in *CreateSellIntentInput) { in.ToAmount = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r, _, _ := newDeliveryRig(t)
			in := deliveryInput(r)
			mutate(&in)
			if _, _, err := r.svc.Create(context.Background(), r.user, in); !errors.Is(err, ErrSellIntentState) {
				t.Fatalf("err = %v — a delivery that cannot be proven must not be quoted", err)
			}
			if r.quoter.calls != 0 {
				t.Fatal("quoted before the delivery was fully described")
			}
		})
	}
}

func TestSellIntent_ConfirmDeliveryAcceptsExactlyWhatIsOwedAndBooksOneFee(t *testing.T) {
	r, v, fees := newDeliveryRig(t)
	in := r.createDelivery(t)
	r.status.st = r.deliveredStatus(in)

	tx, err := r.svc.ConfirmDelivery(context.Background(), in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tx != deliveryDestTx {
		t.Fatalf("dest tx = %q", tx)
	}
	if v.gotTx != deliveryDestTx || v.gotToken != deliveryToken || v.gotTo != deliveryMerch {
		t.Fatalf("verified %s %s %s — must be asked about the merchant's token and wallet in the destination tx", v.gotTx, v.gotToken, v.gotTo)
	}
	after, _ := r.svc.Get(context.Background(), r.user.ID, in.ID)
	if after.Status != models.SellIntentDelivered {
		t.Fatalf("status = %s", after.Status)
	}
	// $10 × 80 bps = $0.08, keyed on the intent under tsa_pay.
	if len(fees.events) != 1 || fees.events[0].SourceType != "tsa_pay" || fees.events[0].SourceID != in.ID ||
		fees.events[0].UserID != r.user.ID || math.Abs(fees.events[0].FeeUSD-0.08) > 1e-9 {
		t.Fatalf("fee events = %+v", fees.events)
	}

	// A repeat (the reconciler died before it wrote the payment) answers the
	// same and books nothing more.
	again, err := r.svc.ConfirmDelivery(context.Background(), in.ID)
	if err != nil || again != deliveryDestTx || len(fees.events) != 1 {
		t.Fatalf("repeat: tx %q err %v fee events %d", again, err, len(fees.events))
	}
}

func TestSellIntent_ConfirmDeliveryRefusesOneBaseUnitShort(t *testing.T) {
	r, v, fees := newDeliveryRig(t)
	in := r.createDelivery(t)
	r.status.st = r.deliveredStatus(in)
	v.amount = new(big.Int).Sub(mustBig(t, deliveryOwed), big.NewInt(1))

	if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); !errors.Is(err, ErrSwapMismatch) {
		t.Fatalf("err = %v, want ErrSwapMismatch", err)
	}
	after, _ := r.svc.Get(context.Background(), r.user.ID, in.ID)
	if after.Status != models.SellIntentSwapped || len(fees.events) != 0 {
		t.Fatalf("a short delivery moved the sale to %s / booked %d fees", after.Status, len(fees.events))
	}
	// More than owed is fine: the merchant is whole.
	v.amount = new(big.Int).Add(mustBig(t, deliveryOwed), big.NewInt(1))
	if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); err != nil {
		t.Fatalf("an over-delivery: %v", err)
	}
}

// LiFi's status names where the swap actually delivered. One that paid the
// customer's own wallet (or anyone else) is the classic "reported paid, never
// reached the merchant" failure, whatever the chain says about that wallet.
func TestSellIntent_ConfirmDeliveryRefusesAStatusThatDidNotPayTheMerchant(t *testing.T) {
	for name, mutate := range map[string]func(*models.RampSellIntent, *LiFiTransferStatus){
		"the customer's own wallet": func(in *models.RampSellIntent, s *LiFiTransferStatus) { s.ToAddress = in.WalletAddress },
		"a stranger":                func(_ *models.RampSellIntent, s *LiFiTransferStatus) { s.ToAddress = sellOtherAddress },
		"no stated recipient":       func(_ *models.RampSellIntent, s *LiFiTransferStatus) { s.ToAddress = "" },
		"another token":             func(_ *models.RampSellIntent, s *LiFiTransferStatus) { s.ReceivingToken = sellUSDC },
		"another chain":             func(_ *models.RampSellIntent, s *LiFiTransferStatus) { s.DestChainID = rampSettlementChainID },
		"the ordinary Base USDC landing in her wallet": func(in *models.RampSellIntent, s *LiFiTransferStatus) {
			s.DestChainID, s.ReceivingToken, s.ToAddress = rampSettlementChainID, sellUSDC, in.WalletAddress
		},
		"another sender": func(_ *models.RampSellIntent, s *LiFiTransferStatus) { s.FromAddress = sellOtherAddress },
		"not our quote":  func(_ *models.RampSellIntent, s *LiFiTransferStatus) { s.TransactionID = "0xdef" },
		"a smaller swap": func(_ *models.RampSellIntent, s *LiFiTransferStatus) { s.SendingAmount = big.NewInt(1) },
		"no dest tx":     func(_ *models.RampSellIntent, s *LiFiTransferStatus) { s.DestTxHash = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r, v, fees := newDeliveryRig(t)
			in := r.createDelivery(t)
			st := r.deliveredStatus(in)
			mutate(in, st)
			r.status.st = st
			if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); !errors.Is(err, ErrSwapMismatch) {
				t.Fatalf("err = %v, want ErrSwapMismatch", err)
			}
			if v.calls != 0 || len(fees.events) != 0 {
				t.Fatalf("a swap that is not this sale's reached the chain proof (%d) or booked fees (%d)", v.calls, len(fees.events))
			}
			if after, _ := r.svc.Get(context.Background(), r.user.ID, in.ID); after.Status != models.SellIntentSwapped {
				t.Fatalf("status = %s", after.Status)
			}
		})
	}
}

func TestSellIntent_ConfirmDeliveryWaitsUntilLiFiSaysCompleted(t *testing.T) {
	for name, st := range map[string]*LiFiTransferStatus{
		"nothing yet":  nil,
		"pending":      {Status: LiFiStatusPending},
		"failed":       {Status: LiFiStatusFailed},
		"done, refund": {Status: LiFiStatusDone, Substatus: LiFiSubstatusRefunded},
		"done, part":   {Status: LiFiStatusDone, Substatus: LiFiSubstatusPartial},
	} {
		t.Run(name, func(t *testing.T) {
			r, v, fees := newDeliveryRig(t)
			in := r.createDelivery(t)
			r.status.st = st
			_, err := r.svc.ConfirmDelivery(context.Background(), in.ID)
			if !errors.Is(err, ErrSwapNotLanded) {
				t.Fatalf("err = %v, want ErrSwapNotLanded", err)
			}
			// The answer carries the LiFi status it was read from, so the pay
			// reconciler need not ask LiFi a second time.
			var nl *SwapNotLandedError
			if !errors.As(err, &nl) || nl.Status != st {
				t.Fatalf("err = %#v, want a SwapNotLandedError carrying %v", err, st)
			}
			if v.calls != 0 || len(fees.events) != 0 {
				t.Fatal("proof or fee before the swap completed")
			}
		})
	}
}

func TestSellIntent_ConfirmDeliveryFailsClosed(t *testing.T) {
	t.Run("the chain cannot be read", func(t *testing.T) {
		r, v, fees := newDeliveryRig(t)
		in := r.createDelivery(t)
		r.status.st = r.deliveredStatus(in)
		v.amount, v.err = nil, errors.New("rpc down")
		if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); err == nil || errors.Is(err, ErrSwapMismatch) {
			t.Fatalf("err = %v — an unreadable chain is a retry, not a verdict", err)
		}
		if after, _ := r.svc.Get(context.Background(), r.user.ID, in.ID); after.Status != models.SellIntentSwapped || len(fees.events) != 0 {
			t.Fatal("delivered without a proof")
		}
	})
	t.Run("nothing credited", func(t *testing.T) {
		r, v, _ := newDeliveryRig(t)
		in := r.createDelivery(t)
		r.status.st = r.deliveredStatus(in)
		v.amount = new(big.Int)
		if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); !errors.Is(err, ErrSwapMismatch) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no verifier for that chain", func(t *testing.T) {
		r, _, _ := newDeliveryRig(t)
		in := r.createDelivery(t)
		r.svc.SetDestVerifiers(map[string]ERC20TransferVerifier{}, deliveryChainName) // unwired after it was created
		r.status.st = r.deliveredStatus(in)
		if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); !errors.Is(err, ErrSellUnavailable) {
			t.Fatalf("err = %v — a chain nothing can prove on is never delivered", err)
		}
	})
	t.Run("verifiers never wired", func(t *testing.T) {
		r, _, _ := newDeliveryRig(t)
		in := r.createDelivery(t)
		r.svc.SetDestVerifiers(nil, nil)
		r.status.st = r.deliveredStatus(in)
		if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); !errors.Is(err, ErrSellUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestSellIntent_ConfirmDeliveryOnlyAppliesToAPaidDeliverySale(t *testing.T) {
	t.Run("an ordinary sale", func(t *testing.T) {
		r, _, _ := newDeliveryRig(t)
		in := r.create(t)
		r.mustRecord(t, in.ID, testSwapHash)
		r.status.st = r.landed(in)
		if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); !errors.Is(err, ErrSellIntentState) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no swap recorded", func(t *testing.T) {
		r, _, _ := newDeliveryRig(t)
		in, _, _ := r.svc.Create(context.Background(), r.user, deliveryInput(r))
		if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); !errors.Is(err, ErrSellIntentState) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("an abandoned sale", func(t *testing.T) {
		r, _, _ := newDeliveryRig(t)
		in := r.createDelivery(t)
		r.status.st = r.deliveredStatus(in)
		if err := r.svc.AbandonForPay(context.Background(), in.ID, r.reload(t, in.ID).SwapTxHash); err != nil {
			t.Fatal(err)
		}
		if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); !errors.Is(err, ErrSellIntentState) {
			t.Fatalf("err = %v — a failed sale is never delivered later", err)
		}
	})
	t.Run("no such sale", func(t *testing.T) {
		r, _, _ := newDeliveryRig(t)
		if _, err := r.svc.ConfirmDelivery(context.Background(), uuid.New()); !errors.Is(err, ErrSellIntentNotFound) {
			t.Fatalf("err = %v", err)
		}
	})
}

// The offramp path must not be able to act on a delivery sale: it would look
// for USDC in the customer's wallet and could open a naira order the sale
// never owed.
func TestSellIntent_OpenOrderRefusesADeliverySale(t *testing.T) {
	r, _, _ := newDeliveryRig(t)
	in := r.createDelivery(t)
	r.status.st = r.deliveredStatus(in)
	if _, err := r.svc.OpenOrder(context.Background(), r.user, in.ID, true); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("err = %v", err)
	}
	if r.client.offrampCalls != 0 || r.status.calls != 0 {
		t.Fatal("OpenOrder went past the state check on a delivery sale")
	}
}

// delivered is terminal: it must not read as an open sale (or the customer is
// locked out of selling), and nothing may reopen, fail or abandon it.
func TestSellIntent_DeliveredIsTerminal(t *testing.T) {
	r, _, _ := newDeliveryRig(t)
	in := r.createDelivery(t)
	r.status.st = r.deliveredStatus(in)
	if _, err := r.svc.ConfirmDelivery(context.Background(), in.ID); err != nil {
		t.Fatal(err)
	}
	if open, _ := r.svc.Open(context.Background(), r.user.ID); open != nil {
		t.Fatalf("a delivered sale is still open: %+v", open)
	}
	if res, _ := r.svc.Resumable(context.Background(), r.user.ID); res != nil {
		t.Fatalf("a delivered sale is resumable: %+v", res)
	}
	if err := r.svc.Abandon(context.Background(), r.user.ID, in.ID); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("Abandon: %v", err)
	}
	if err := r.svc.AbandonForPay(context.Background(), in.ID, r.reload(t, in.ID).SwapTxHash); !errors.Is(err, ErrSellIntentState) {
		t.Fatalf("AbandonForPay: %v — a paid sale must not be failed", err)
	}
	// She is free to sell again.
	if _, _, err := r.svc.Create(context.Background(), r.user, deliveryInput(r)); err != nil {
		t.Fatalf("a new sale after a delivered one: %v", err)
	}
	if after, _ := r.svc.Get(context.Background(), r.user.ID, in.ID); after.BankCode != "" || after.AccountNumber != "" {
		t.Fatalf("bank details kept on a delivered sale: %+v", after)
	}
}
