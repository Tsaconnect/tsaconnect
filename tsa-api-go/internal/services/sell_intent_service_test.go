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
}

func (f *fakeStatusReader) Status(_ context.Context, _ string) (*LiFiTransferStatus, error) {
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
		"partial delivery":     func(s *LiFiTransferStatus) { s.Substatus = LiFiSubstatusPartial },
		"refunded to her":      func(s *LiFiTransferStatus) { s.Substatus = LiFiSubstatusRefunded },
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

// THE ACCEPTANCE POINT (brief, "USD vs wei"). The quote budgets a
// source-chain gas top-up out of the round trip in DOLLARS, from the route's
// own quoted gas (TopUpUSDForRoute = RouteGasUSD(route, treasuryERC20Gas) ×
// topUpGasBuffer). Whatever later SENDS that top-up has to send the same
// figure, in the coin it is paid in — so the wei twin of it, RouteGasWei on
// the same units, is stored on the intent at quote time.
//
// Derive it from a live gas price instead and the two are only equal by
// coincidence: the quote priced LiFi's gas price and the send would pay
// whatever the chain costs minutes later, with the difference coming out of a
// budget the user was already priced against.
func TestSellIntent_StoresTheTopUpTheQuoteBudgeted(t *testing.T) {
	r := newSellIntentRig(t)
	r.quoter.quote.Route.GasCostWei = big.NewInt(3_000_000)
	in := r.create(t)
	// 3,000,000 wei for 300k gas, rescaled to 380k (the swap plus an approve),
	// doubled by topUpGasBuffer.
	if in.QuotedTopUpWei != "7600000" {
		t.Fatalf("quotedTopUpWei = %q, want 7600000 — the same figure the quote budgeted", in.QuotedTopUpWei)
	}
}

// A route that quoted no gas in wei leaves the field empty rather than "0": a
// top-up of zero is not the same claim as "we do not know what this costs",
// and only one of them may be sent.
func TestSellIntent_AnUnpricedRouteStoresNoTopUp(t *testing.T) {
	r := newSellIntentRig(t)
	r.quoter.quote.Route.GasCostWei = nil
	in := r.create(t)
	if in.QuotedTopUpWei != "" {
		t.Fatalf("quotedTopUpWei = %q, want empty", in.QuotedTopUpWei)
	}
}
