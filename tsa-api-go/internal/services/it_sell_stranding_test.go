package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// ─── fakes ──────────────────────────────────────────────────────────────────

// fakeSellChain stands in for the swap contract's operator debit path. Debits
// it accepts are recorded per ref exactly as the contract's `debits` mapping
// would record them, so reconciler tests read the same evidence production does.
type fakeSellChain struct {
	balance   *big.Int
	debitErr  error
	verifyErr map[string]error
	settleErr error
	reverse   error
	readErr   error
	records   map[[32]byte]DebitRecord

	debitCalls, settleCalls, reverseCalls int
}

func newFakeSellChain() *fakeSellChain {
	return &fakeSellChain{
		balance:   new(big.Int).Mul(big.NewInt(1_000_000), oneE18),
		verifyErr: map[string]error{},
		records:   map[[32]byte]DebitRecord{},
	}
}

func (f *fakeSellChain) PremiumBalance(string) (*big.Int, error) { return f.balance, nil }

func (f *fakeSellChain) OperatorDebitSpendable(ctx context.Context, user string, amount, _ *big.Int, ref [32]byte) (string, error) {
	f.debitCalls++
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if f.debitErr != nil {
		return "", f.debitErr
	}
	f.records[ref] = DebitRecord{User: common.HexToAddress(user), Amount: amount, State: DebitStatePending}
	return "0xdebit", nil
}

func (f *fakeSellChain) OperatorSettleDebit(ctx context.Context, ref [32]byte) (string, error) {
	f.settleCalls++
	if f.settleErr != nil {
		return "", f.settleErr
	}
	r := f.records[ref]
	r.State = DebitStateResolved
	f.records[ref] = r
	return "0xsettle", nil
}

func (f *fakeSellChain) OperatorReverseDebit(ctx context.Context, ref [32]byte) (string, error) {
	f.reverseCalls++
	if f.reverse != nil {
		return "", f.reverse
	}
	r := f.records[ref]
	r.State = DebitStateResolved
	f.records[ref] = r
	return "0xreverse", nil
}

func (f *fakeSellChain) VerifyOperatorEvent(event, _ string, _ common.Address, _ [32]byte) (*big.Int, error) {
	if err := f.verifyErr[event]; err != nil {
		return nil, err
	}
	return big.NewInt(1), nil
}

func (f *fakeSellChain) DebitRecord(_ context.Context, ref [32]byte) (DebitRecord, error) {
	if f.readErr != nil {
		return DebitRecord{}, f.readErr
	}
	if r, ok := f.records[ref]; ok {
		return r, nil
	}
	return DebitRecord{Amount: big.NewInt(0), State: DebitStateNone}, nil
}

type fakePayoutTransfers struct {
	byID    map[string]FWTransfer
	byRef   map[string]FWTransfer
	getErr  error
	findErr error

	getCalls, findCalls int
}

func newFakePayoutTransfers() *fakePayoutTransfers {
	return &fakePayoutTransfers{byID: map[string]FWTransfer{}, byRef: map[string]FWTransfer{}}
}

func (f *fakePayoutTransfers) GetTransfer(_ context.Context, id string) (FWTransfer, error) {
	f.getCalls++
	if f.getErr != nil {
		return FWTransfer{}, f.getErr
	}
	tr, ok := f.byID[id]
	if !ok {
		return FWTransfer{}, errors.New("flutterwave get-transfer: HTTP 404")
	}
	return tr, nil
}

func (f *fakePayoutTransfers) FindTransferByReference(_ context.Context, ref string, _, _ time.Time) (FWTransfer, bool, error) {
	f.findCalls++
	if f.findErr != nil {
		return FWTransfer{}, false, f.findErr
	}
	tr, ok := f.byRef[ref]
	return tr, ok, nil
}

// ─── fixtures ───────────────────────────────────────────────────────────────

const sellWallet = "0x00000000000000000000000000000000000000AB"

type sellFixture struct {
	s         *ITService
	chain     *fakeSellChain
	gw        *fakeNombaGateway
	transfers *fakePayoutTransfers
}

func newSellFixture(t *testing.T) *sellFixture {
	t.Helper()
	s := newITTestService(t)
	if _, err := s.SetRate(context.Background(), uuid.New(), 20000, 500); err != nil {
		t.Fatalf("set rate: %v", err)
	}
	f := &sellFixture{s: s, chain: newFakeSellChain(), gw: &fakeNombaGateway{payoutID: "trf_ok"}, transfers: newFakePayoutTransfers()}
	s.sellChain = f.chain
	s.SetPayoutGateway(f.gw)
	s.SetPayoutTransfers(f.transfers)
	return f
}

var hundredMCGP = new(big.Int).Mul(big.NewInt(100), oneE18).String()

func sellBank() *ITSellBank {
	return &ITSellBank{Code: "044", Number: "0690000031", Name: "Ada Obi"}
}

// seedSell writes an MCGP sell directly, as CreateSellIntent would have left it.
func (f *sellFixture) seedSell(t *testing.T, mutate func(*models.ITIntent)) *models.ITIntent {
	t.Helper()
	in := &models.ITIntent{
		ID: uuid.New(), UserID: uuid.New(), WalletAddress: sellWallet,
		Direction: models.ITDirectionSell, Status: models.ITStatusDebited, Asset: models.ITAssetMCGP,
		Currency: "NGN", McgpAmount: hundredMCGP, NgnKobo: "150000", NetNgnKobo: "150000", RateKobo: "1500",
		BankCode: "044", AccountNumber: "0690000031", AccountName: "Ada Obi",
	}
	ref := itRef("IT:debit:", in.ID)
	in.DebitRef = common.BytesToHash(ref[:]).Hex()
	if mutate != nil {
		mutate(in)
	}
	if err := f.s.db.Create(in).Error; err != nil {
		t.Fatalf("seed sell: %v", err)
	}
	if in.Status == models.ITStatusDebited || in.Status == models.ITStatusSettled {
		f.chain.records[ref] = DebitRecord{User: common.HexToAddress(sellWallet), Amount: new(big.Int).Mul(big.NewInt(100), oneE18), State: DebitStatePending}
	}
	return in
}

// age backdates an intent so the reconciler's grace windows treat it as stale.
func (f *sellFixture) age(t *testing.T, id uuid.UUID, by time.Duration) {
	t.Helper()
	ts := time.Now().Add(-by)
	if err := f.s.db.Model(&models.ITIntent{}).Where("id = ?", id).
		UpdateColumns(map[string]any{"created_at": ts, "updated_at": ts}).Error; err != nil {
		t.Fatalf("age intent: %v", err)
	}
}

func (f *sellFixture) reload(t *testing.T, id uuid.UUID) *models.ITIntent {
	t.Helper()
	in, err := f.s.GetIntent(context.Background(), id)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	return in
}

func (f *sellFixture) reconcile(t *testing.T) {
	t.Helper()
	if _, err := f.s.ReconcileStuckSells(context.Background(), 5*time.Minute); err != nil {
		t.Fatalf("ReconcileStuckSells: %v", err)
	}
}

func rejected(msg string) error {
	return &ProviderRejectedError{Err: errors.New(msg)}
}

// ─── CreateSellIntent ───────────────────────────────────────────────────────

func TestCreateSell_PayoutRejectedReversesDebit(t *testing.T) {
	f := newSellFixture(t)
	f.gw.err = rejected("flutterwave direct-transfer: HTTP 400: Insufficient balance")

	intent, err := f.s.CreateSellIntent(context.Background(), uuid.New(), sellWallet, hundredMCGP, sellBank())
	if !errors.Is(err, ErrITSellPayoutRejected) {
		t.Fatalf("err = %v, want ErrITSellPayoutRejected so the client is told the truth", err)
	}
	if intent == nil {
		t.Fatal("the intent must still be returned so the caller can show its final state")
	}
	got := f.reload(t, intent.ID)
	if got.Status != models.ITStatusReversed || f.chain.reverseCalls != 1 || f.chain.settleCalls != 0 {
		t.Fatalf("status=%s reverse=%d settle=%d, want reversed once", got.Status, f.chain.reverseCalls, f.chain.settleCalls)
	}
}

func TestCreateSell_PayoutRejectedReverseFailureLeavesItMarked(t *testing.T) {
	f := newSellFixture(t)
	f.gw.err = rejected("HTTP 422")
	f.chain.reverse = errors.New("rpc down")

	intent, err := f.s.CreateSellIntent(context.Background(), uuid.New(), sellWallet, hundredMCGP, sellBank())
	if !errors.Is(err, ErrITSellPayoutRejected) {
		t.Fatalf("err = %v, want ErrITSellPayoutRejected", err)
	}
	got := f.reload(t, intent.ID)
	if got.Status != models.ITStatusDebited || !got.PayoutRejected {
		t.Fatalf("status=%s rejected=%v, want debited + marked rejected for the reconciler", got.Status, got.PayoutRejected)
	}
}

func TestCreateSell_PayoutOutcomeUnknownStaysDebited(t *testing.T) {
	f := newSellFixture(t)
	f.gw.err = errors.New("flutterwave direct-transfer: Post: context deadline exceeded")

	intent, err := f.s.CreateSellIntent(context.Background(), uuid.New(), sellWallet, hundredMCGP, sellBank())
	if err != nil {
		t.Fatalf("an unknown payout outcome is still a submitted sell: %v", err)
	}
	got := f.reload(t, intent.ID)
	if got.Status != models.ITStatusDebited || got.PayoutRejected || got.PayoutAttempts != 1 {
		t.Fatalf("status=%s rejected=%v attempts=%d, want debited/unrejected/1", got.Status, got.PayoutRejected, got.PayoutAttempts)
	}
	if f.chain.reverseCalls != 0 {
		t.Fatal("must never reverse a payout that may have gone out")
	}
}

func TestCreateSell_VerifyErrorLeavesDebiting(t *testing.T) {
	f := newSellFixture(t)
	f.chain.verifyErr["PremiumDebitPending"] = errors.New("get receipt: timeout")

	intent, err := f.s.CreateSellIntent(context.Background(), uuid.New(), sellWallet, hundredMCGP, sellBank())
	if !errors.Is(err, ErrITSellDebitPending) {
		t.Fatalf("err = %v, want ErrITSellDebitPending", err)
	}
	got := f.reload(t, intent.ID)
	if got.Status != models.ITStatusDebiting || got.TxHash != "0xdebit" {
		t.Fatalf("status=%s tx=%q, want debiting with the broadcast hash kept as evidence", got.Status, got.TxHash)
	}
	if len(f.gw.calls) != 0 {
		t.Fatal("no payout may fire before the debit is confirmed")
	}
}

func TestCreateSell_SendErrorLeavesDebiting(t *testing.T) {
	f := newSellFixture(t)
	f.chain.debitErr = errors.New("send raw tx: i/o timeout")

	intent, err := f.s.CreateSellIntent(context.Background(), uuid.New(), sellWallet, hundredMCGP, sellBank())
	if !errors.Is(err, ErrITSellDebitPending) || intent == nil {
		t.Fatalf("intent=%v err=%v, want ErrITSellDebitPending with the intent", intent, err)
	}
	if got := f.reload(t, intent.ID); got.Status != models.ITStatusDebiting {
		t.Fatalf("status = %s, want debiting (the tx may have gone out)", got.Status)
	}
}

func TestCreateSell_PreSendErrorFails(t *testing.T) {
	f := newSellFixture(t)
	f.chain.debitErr = notBroadcast(errors.New("gas estimation failed"))

	userID := uuid.New()
	if _, err := f.s.CreateSellIntent(context.Background(), userID, sellWallet, hundredMCGP, sellBank()); err == nil || errors.Is(err, ErrITSellDebitPending) {
		t.Fatalf("err = %v, want a plain failure", err)
	}
	var rows []models.ITIntent
	f.s.db.Where("user_id = ?", userID).Find(&rows)
	if len(rows) != 1 || rows[0].Status != models.ITStatusFailed {
		t.Fatalf("rows = %+v, want one failed intent", rows)
	}
}

// REGRESSION: the sell ran on the HTTP request context, so a client that hung
// up mid-sell cancelled the debit or the payout halfway through.
func TestCreateSell_SurvivesCancelledRequestContext(t *testing.T) {
	f := newSellFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	intent, err := f.s.CreateSellIntent(ctx, uuid.New(), sellWallet, hundredMCGP, sellBank())
	if err != nil {
		t.Fatalf("CreateSellIntent with a cancelled request context: %v", err)
	}
	if got := f.reload(t, intent.ID); got.Status != models.ITStatusDebited || got.PayoutID != "trf_ok" {
		t.Fatalf("status=%s payout=%q, want debited with the payout recorded", got.Status, got.PayoutID)
	}
}

func TestCreateSell_DebitingBlocksAnotherSell(t *testing.T) {
	f := newSellFixture(t)
	userID := uuid.New()
	f.seedSell(t, func(in *models.ITIntent) { in.UserID = userID; in.Status = models.ITStatusDebiting })
	if _, err := f.s.CreateSellIntent(context.Background(), userID, sellWallet, hundredMCGP, sellBank()); err == nil {
		t.Fatal("a sell whose debit is unconfirmed must still count as in flight")
	}
}

// ─── AutoPayout classification ──────────────────────────────────────────────

func TestAutoPayout_PreflightFailureOnFirstAttemptIsRejection(t *testing.T) {
	f := newSellFixture(t)
	allowQuoteCurrency(t, "KES")
	in := f.seedSell(t, func(in *models.ITIntent) { in.DestinationCurrency = "KES" }) // no quoter wired

	err := f.s.AutoPayout(context.Background(), in)
	if !errors.Is(err, ErrITSellPayoutRejected) {
		t.Fatalf("err = %v, want a rejection (nothing was ever sent)", err)
	}
	if got := f.reload(t, in.ID); !got.PayoutRejected || got.PayoutAttempts != 0 || len(f.gw.calls) != 0 {
		t.Fatalf("rejected=%v attempts=%d calls=%d", got.PayoutRejected, got.PayoutAttempts, len(f.gw.calls))
	}
}

// On a retry, a refusal can mean "that reference already exists" — i.e. the
// first attempt went out. Only a replayed answer describes the first attempt.
func TestAutoPayout_RetryRefusalIsOnlyRejectionWhenReplayed(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.gw.err = rejected("HTTP 400: reference already exists")
	if err := f.s.AutoPayout(context.Background(), in); errors.Is(err, ErrITSellPayoutRejected) {
		t.Fatal("a fresh refusal of a retried reference is not evidence the payout failed")
	}
	if got := f.reload(t, in.ID); got.PayoutRejected {
		t.Fatal("must not mark rejected")
	}

	in2 := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.gw.err = &ProviderRejectedError{Err: errors.New("HTTP 400: Insufficient balance"), Replayed: true}
	if err := f.s.AutoPayout(context.Background(), in2); !errors.Is(err, ErrITSellPayoutRejected) {
		t.Fatalf("err = %v, want a rejection for a replayed refusal", err)
	}
}

// Legacy rows were submitted before attempts were counted; their failure
// reason is the only trace of it, and it must stop a retry being read as first.
func TestAutoPayout_LegacyAttemptIsNotFirst(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.FailureReason = "auto-payout: flutterwave direct-transfer: timeout" })
	f.gw.err = rejected("HTTP 400")
	if err := f.s.AutoPayout(context.Background(), in); errors.Is(err, ErrITSellPayoutRejected) {
		t.Fatal("a legacy row's earlier submission must keep a fresh refusal ambiguous")
	}
}

// ─── Reconciler: in-flight debits ───────────────────────────────────────────

func TestReconcileSells_PromotesDebitingWhenChainShowsPending(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusDebiting })
	ref := itRef("IT:debit:", in.ID)
	f.chain.records[ref] = DebitRecord{User: common.HexToAddress(sellWallet), Amount: new(big.Int).Mul(big.NewInt(100), oneE18), State: DebitStatePending}
	f.age(t, in.ID, 10*time.Minute)

	f.reconcile(t)
	if got := f.reload(t, in.ID); got.Status != models.ITStatusDebited {
		t.Fatalf("status = %s, want debited", got.Status)
	}
}

func TestReconcileSells_DoesNotPromoteDebitForAnotherWallet(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusDebiting })
	f.chain.records[itRef("IT:debit:", in.ID)] = DebitRecord{User: common.HexToAddress("0x00000000000000000000000000000000000000CD"), Amount: big.NewInt(1), State: DebitStatePending}
	f.age(t, in.ID, 10*time.Minute)

	f.reconcile(t)
	if got := f.reload(t, in.ID); got.Status != models.ITStatusDebiting {
		t.Fatalf("status = %s, want debiting left for an operator", got.Status)
	}
}

func TestReconcileSells_FailsInflightOnlyAfterAbandonWindow(t *testing.T) {
	f := newSellFixture(t)
	fresh := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusProcessing })
	f.age(t, fresh.ID, 10*time.Minute)
	old := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusDebiting })
	f.age(t, old.ID, time.Hour)

	f.reconcile(t)
	if got := f.reload(t, fresh.ID); got.Status != models.ITStatusProcessing {
		t.Fatalf("fresh status = %s, want untouched while a broadcast could still mine", got.Status)
	}
	if got := f.reload(t, old.ID); got.Status != models.ITStatusFailed {
		t.Fatalf("old status = %s, want failed (no debit on-chain)", got.Status)
	}
}

func TestReconcileSells_ChainReadErrorChangesNothing(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusDebiting })
	f.age(t, in.ID, time.Hour)
	f.chain.readErr = errors.New("rpc down")

	f.reconcile(t)
	if got := f.reload(t, in.ID); got.Status != models.ITStatusDebiting {
		t.Fatalf("status = %s, want debiting", got.Status)
	}
}

// A debit marked failed can still mine later. The user was told it failed and
// no payout was ever attempted, so the only safe correction is to restore it.
func TestReconcileSells_ReversesLateMinedDebitOnFailedIntent(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) {
		in.Status = models.ITStatusFailed
		in.FailureReason = "debit verify: get receipt: timeout"
	})
	f.chain.records[itRef("IT:debit:", in.ID)] = DebitRecord{User: common.HexToAddress(sellWallet), Amount: new(big.Int).Mul(big.NewInt(100), oneE18), State: DebitStatePending}
	f.age(t, in.ID, time.Hour)

	f.reconcile(t)
	if got := f.reload(t, in.ID); got.Status != models.ITStatusReversed || f.chain.reverseCalls != 1 {
		t.Fatalf("status=%s reverse=%d, want reversed once", got.Status, f.chain.reverseCalls)
	}
	if len(f.gw.calls) != 0 {
		t.Fatal("a sell the user was told failed must not be paid out")
	}
}

// ─── Reconciler: debited sells ──────────────────────────────────────────────

func TestReconcileSells_ReversesRecordedRejection(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutRejected = true; in.PayoutAttempts = 1 })
	f.age(t, in.ID, 10*time.Minute)

	f.reconcile(t)
	if got := f.reload(t, in.ID); got.Status != models.ITStatusReversed {
		t.Fatalf("status = %s, want reversed", got.Status)
	}
}

func TestReconcileSells_ResolvesByTransferStatus(t *testing.T) {
	f := newSellFixture(t)
	ok := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_s"; in.PayoutAttempts = 1 })
	bad := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_f"; in.PayoutAttempts = 1 })
	pend := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_p"; in.PayoutAttempts = 1 })
	f.transfers.byID["trf_s"] = FWTransfer{ID: "trf_s", Status: "SUCCESSFUL"}
	f.transfers.byID["trf_f"] = FWTransfer{ID: "trf_f", Status: "FAILED"}
	f.transfers.byID["trf_p"] = FWTransfer{ID: "trf_p", Status: "PENDING"}
	for _, in := range []*models.ITIntent{ok, bad, pend} {
		f.age(t, in.ID, 30*time.Hour)
	}

	f.reconcile(t)
	if got := f.reload(t, ok.ID); got.Status != models.ITStatusSettled {
		t.Errorf("SUCCESSFUL transfer: status = %s, want settled", got.Status)
	}
	if got := f.reload(t, bad.ID); got.Status != models.ITStatusReversed {
		t.Errorf("FAILED transfer: status = %s, want reversed", got.Status)
	}
	if got := f.reload(t, pend.ID); got.Status != models.ITStatusDebited {
		t.Errorf("PENDING transfer: status = %s, want debited (alert only)", got.Status)
	}
	if f.chain.settleCalls != 1 || f.chain.reverseCalls != 1 {
		t.Fatalf("settle=%d reverse=%d, want exactly one of each", f.chain.settleCalls, f.chain.reverseCalls)
	}
}

func TestReconcileSells_TransferReadErrorChangesNothing(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_x"; in.PayoutAttempts = 1 })
	f.transfers.getErr = errors.New("flutterwave get-transfer: HTTP 503")
	f.age(t, in.ID, time.Hour)

	f.reconcile(t)
	if got := f.reload(t, in.ID); got.Status != models.ITStatusDebited || f.chain.reverseCalls+f.chain.settleCalls != 0 {
		t.Fatalf("status=%s, want untouched on a failed provider read", got.Status)
	}
}

func TestReconcileSells_NeverSubmittedPayoutIsSent(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, nil) // crash between `debited` and the payout call
	f.age(t, in.ID, 10*time.Minute)

	f.reconcile(t)
	if len(f.gw.calls) != 1 || f.gw.calls[0].Reference != itPayoutRef(in.ID) {
		t.Fatalf("calls = %+v, want one payout with the intent's reference", f.gw.calls)
	}
	if f.transfers.findCalls != 0 {
		t.Fatal("a never-submitted payout needs no provider lookup")
	}
	if got := f.reload(t, in.ID); got.PayoutID != "trf_ok" {
		t.Fatalf("payout id = %q", got.PayoutID)
	}
}

func TestReconcileSells_UnknownSubmissionAdoptsExistingTransfer(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.FailureReason = "auto-payout unknown: timeout" })
	f.transfers.byRef[itPayoutRef(in.ID)] = FWTransfer{ID: "trf_found", Reference: itPayoutRef(in.ID), Status: "SUCCESSFUL"}
	f.age(t, in.ID, 10*time.Minute)

	f.reconcile(t)
	got := f.reload(t, in.ID)
	if got.PayoutID != "trf_found" || got.Status != models.ITStatusSettled {
		t.Fatalf("payout=%q status=%s, want the found transfer adopted and settled", got.PayoutID, got.Status)
	}
	if len(f.gw.calls) != 0 {
		t.Fatal("a transfer that exists must never be re-sent")
	}
}

func TestReconcileSells_UnknownSubmissionAbsentRetriesSameReference(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.FailureReason = "auto-payout unknown: timeout" })
	f.age(t, in.ID, 10*time.Minute)

	f.reconcile(t)
	if len(f.gw.calls) != 1 || f.gw.calls[0].Reference != itPayoutRef(in.ID) {
		t.Fatalf("calls = %+v, want one retry reusing the reference (the provider's idempotency key)", f.gw.calls)
	}
	if got := f.reload(t, in.ID); got.PayoutAttempts != 2 {
		t.Fatalf("attempts = %d, want 2", got.PayoutAttempts)
	}
}

func TestReconcileSells_UnknownSubmissionLookupErrorChangesNothing(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
	f.transfers.findErr = errors.New("scan incomplete")
	f.age(t, in.ID, 10*time.Minute)

	f.reconcile(t)
	if len(f.gw.calls) != 0 || f.chain.reverseCalls != 0 {
		t.Fatalf("calls=%d reverse=%d, want no action without evidence", len(f.gw.calls), f.chain.reverseCalls)
	}
}

func TestReconcileSells_ExhaustedOrOldUnknownOnlyAlerts(t *testing.T) {
	f := newSellFixture(t)
	exhausted := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = itPayoutMaxAttempts })
	f.age(t, exhausted.ID, 10*time.Minute)
	old := f.seedSell(t, func(in *models.ITIntent) { in.FailureReason = "auto-payout: HTTP 400" })
	f.age(t, old.ID, 48*time.Hour)

	f.reconcile(t)
	if len(f.gw.calls) != 0 || f.chain.reverseCalls != 0 || f.chain.settleCalls != 0 {
		t.Fatalf("calls=%d reverse=%d settle=%d, want alert-only", len(f.gw.calls), f.chain.reverseCalls, f.chain.settleCalls)
	}
}

func TestReconcileSells_IgnoresStablecoinSells(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.Asset = models.ITAssetUSDC; in.PayoutRejected = true })
	f.age(t, in.ID, time.Hour)

	f.reconcile(t)
	if f.chain.reverseCalls != 0 || f.reload(t, in.ID).Status != models.ITStatusDebited {
		t.Fatal("a stablecoin sell has no swap debit to reverse")
	}
}

// ─── Admin ──────────────────────────────────────────────────────────────────

func TestAdminReverseSell_RequiresEvidenceThePayoutDidNotHappen(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*models.ITIntent)
		setup   func(*sellFixture, *models.ITIntent)
		allowed bool
	}{
		{"successful transfer", func(in *models.ITIntent) { in.PayoutID = "trf_1"; in.PayoutAttempts = 1 },
			func(f *sellFixture, _ *models.ITIntent) {
				f.transfers.byID["trf_1"] = FWTransfer{ID: "trf_1", Status: "SUCCESSFUL"}
			}, false},
		{"pending transfer", func(in *models.ITIntent) { in.PayoutID = "trf_1"; in.PayoutAttempts = 1 },
			func(f *sellFixture, _ *models.ITIntent) {
				f.transfers.byID["trf_1"] = FWTransfer{ID: "trf_1", Status: "PENDING"}
			}, false},
		{"failed transfer", func(in *models.ITIntent) { in.PayoutID = "trf_1"; in.PayoutAttempts = 1 },
			func(f *sellFixture, _ *models.ITIntent) {
				f.transfers.byID["trf_1"] = FWTransfer{ID: "trf_1", Status: "FAILED"}
			}, true},
		{"provider unreachable", func(in *models.ITIntent) { in.PayoutID = "trf_1"; in.PayoutAttempts = 1 },
			func(f *sellFixture, _ *models.ITIntent) { f.transfers.getErr = errors.New("HTTP 503") }, false},
		{"unknown submission found at provider", func(in *models.ITIntent) { in.PayoutAttempts = 1 },
			func(f *sellFixture, in *models.ITIntent) {
				f.transfers.byRef[itPayoutRef(in.ID)] = FWTransfer{ID: "trf_2", Status: "SUCCESSFUL"}
			}, false},
		{"unknown submission absent at provider", func(in *models.ITIntent) { in.PayoutAttempts = 1 }, nil, true},
		{"reference scan failed", func(in *models.ITIntent) { in.PayoutAttempts = 1 },
			func(f *sellFixture, _ *models.ITIntent) { f.transfers.findErr = errors.New("scan incomplete") }, false},
		{"recorded rejection", func(in *models.ITIntent) { in.PayoutRejected = true }, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSellFixture(t)
			in := f.seedSell(t, tc.mutate)
			f.age(t, in.ID, time.Hour)
			if tc.setup != nil {
				tc.setup(f, in)
			}
			_, err := f.s.AdminReverseSell(context.Background(), in.ID)
			if tc.allowed {
				if err != nil || f.chain.reverseCalls != 1 {
					t.Fatalf("err=%v reverse=%d, want reversed", err, f.chain.reverseCalls)
				}
				return
			}
			if err == nil || f.chain.reverseCalls != 0 {
				t.Fatalf("err=%v reverse=%d, want refusal and no reverse", err, f.chain.reverseCalls)
			}
		})
	}
}

func TestAdminReverseSell_ManualPayoutModeUnchanged(t *testing.T) {
	f := newSellFixture(t)
	f.s.payout = nil
	f.s.payoutTransfers = nil
	in := f.seedSell(t, func(in *models.ITIntent) { in.BankCode = ""; in.AccountNumber = "" })
	if _, err := f.s.AdminReverseSell(context.Background(), in.ID); err != nil {
		t.Fatalf("manual-payout deployments keep the operator reverse: %v", err)
	}
}

func TestAdminSettleSell_RefusesTransferThatDidNotSucceed(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutID = "trf_1"; in.PayoutAttempts = 1 })
	f.transfers.byID["trf_1"] = FWTransfer{ID: "trf_1", Status: "FAILED"}
	if _, err := f.s.AdminSettleSell(context.Background(), in.ID); err == nil || f.chain.settleCalls != 0 {
		t.Fatalf("err=%v settle=%d, want refusal: settling a failed payout takes the user's MCGP", err, f.chain.settleCalls)
	}
	f.transfers.byID["trf_1"] = FWTransfer{ID: "trf_1", Status: "SUCCESSFUL"}
	if _, err := f.s.AdminSettleSell(context.Background(), in.ID); err != nil {
		t.Fatalf("successful transfer should settle: %v", err)
	}
}

func TestRetrySellPayout(t *testing.T) {
	t.Run("adopts a transfer that already exists", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
		f.transfers.byRef[itPayoutRef(in.ID)] = FWTransfer{ID: "trf_x", Status: "PENDING"}
		got, err := f.s.RetrySellPayout(context.Background(), in.ID)
		if err != nil || got.PayoutID != "trf_x" || len(f.gw.calls) != 0 {
			t.Fatalf("err=%v payout=%q calls=%d", err, got.PayoutID, len(f.gw.calls))
		}
	})
	t.Run("sends when no transfer exists", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
		got, err := f.s.RetrySellPayout(context.Background(), in.ID)
		if err != nil || got.PayoutID != "trf_ok" || len(f.gw.calls) != 1 {
			t.Fatalf("err=%v payout=%q calls=%d", err, got.PayoutID, len(f.gw.calls))
		}
	})
	t.Run("refuses a recorded rejection", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutRejected = true })
		if _, err := f.s.RetrySellPayout(context.Background(), in.ID); err == nil || len(f.gw.calls) != 0 {
			t.Fatalf("err=%v calls=%d, want refusal (the provider replays the refusal for this reference)", err, len(f.gw.calls))
		}
	})
	t.Run("refuses when the provider cannot be searched", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1 })
		f.transfers.findErr = errors.New("scan incomplete")
		if _, err := f.s.RetrySellPayout(context.Background(), in.ID); err == nil || len(f.gw.calls) != 0 {
			t.Fatalf("err=%v calls=%d", err, len(f.gw.calls))
		}
	})
	t.Run("rejection on retry reverses", func(t *testing.T) {
		f := newSellFixture(t)
		in := f.seedSell(t, nil)
		f.gw.err = rejected("HTTP 400")
		_, err := f.s.RetrySellPayout(context.Background(), in.ID)
		if !errors.Is(err, ErrITSellPayoutRejected) || f.reload(t, in.ID).Status != models.ITStatusReversed {
			t.Fatalf("err=%v status=%s", err, f.reload(t, in.ID).Status)
		}
	})
}

func TestListStuckMCGPSells(t *testing.T) {
	f := newSellFixture(t)
	stuck := f.seedSell(t, func(in *models.ITIntent) { in.PayoutAttempts = 1; in.FailureReason = "auto-payout unknown: timeout" })
	f.age(t, stuck.ID, 2*time.Hour)
	debiting := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusDebiting })
	f.age(t, debiting.ID, 2*time.Hour)
	fresh := f.seedSell(t, nil)
	settled := f.seedSell(t, func(in *models.ITIntent) { in.Status = models.ITStatusSettled })
	f.age(t, settled.ID, 2*time.Hour)
	stable := f.seedSell(t, func(in *models.ITIntent) { in.Asset = models.ITAssetUSDT })
	f.age(t, stable.ID, 2*time.Hour)

	rows, err := f.s.ListStuckMCGPSells(context.Background(), 30*time.Minute)
	if err != nil {
		t.Fatalf("ListStuckMCGPSells: %v", err)
	}
	got := map[uuid.UUID]StuckMCGPSell{}
	for _, r := range rows {
		got[r.IntentID] = r
	}
	if len(rows) != 2 || got[stuck.ID].IntentID == uuid.Nil || got[debiting.ID].IntentID == uuid.Nil {
		t.Fatalf("rows = %+v, want exactly the stuck debited and debiting MCGP sells", rows)
	}
	if _, ok := got[fresh.ID]; ok {
		t.Fatal("a fresh sell is not stuck")
	}
	if r := got[stuck.ID]; r.PayoutAttempts != 1 || !strings.Contains(r.FailureReason, "unknown") || r.DebitRef == "" {
		t.Fatalf("row lacks operator evidence: %+v", r)
	}
}

// ─── Webhook: stablecoin sells ──────────────────────────────────────────────

// REGRESSION: a FAILED payout webhook for a USDC/USDT sell called
// reverseDebit on a ref that was never debited; the revert failed the webhook
// and the provider redelivered it forever.
func TestResolvePayoutWebhook_StableFailedDoesNotTouchSwap(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) {
		in.Asset = models.ITAssetUSDC
		in.PayoutID = "trf_s1"
		in.PayoutAttempts = 1
	})

	if err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "failed"); err != nil {
		t.Fatalf("webhook must be acknowledged: %v", err)
	}
	got := f.reload(t, in.ID)
	if f.chain.reverseCalls != 0 || got.Status != models.ITStatusDebited || !got.PayoutRejected {
		t.Fatalf("reverse=%d status=%s rejected=%v, want parked for an operator without a swap call", f.chain.reverseCalls, got.Status, got.PayoutRejected)
	}
	// Parked means parked: the stable watcher must not fire the same
	// reference again, which the provider would only answer with the failure.
	if err := f.s.PayoutStableSell(context.Background(), in.ID); err != nil || len(f.gw.calls) != 0 {
		t.Fatalf("err=%v calls=%d, want no re-send", err, len(f.gw.calls))
	}
}

func TestResolvePayoutWebhook_StableSuccessSettlesWithoutSwap(t *testing.T) {
	f := newSellFixture(t)
	in := f.seedSell(t, func(in *models.ITIntent) {
		in.Asset = models.ITAssetUSDT
		in.PayoutID = "trf_s2"
		in.PayoutAttempts = 1
	})

	if err := f.s.ResolvePayoutFromWebhook(context.Background(), itPayoutRef(in.ID), "success"); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	if got := f.reload(t, in.ID); f.chain.settleCalls != 0 || got.Status != models.ITStatusSettled {
		t.Fatalf("settle=%d status=%s, want settled without a swap call", f.chain.settleCalls, got.Status)
	}
}
