package services

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// fakeCompanyPayer stands in for the central account.
type fakeCompanyPayer struct {
	mu       sync.Mutex
	balances []FWBalance
	balErr   error
	initID   string
	initErr  error
	inits    []PayoutRequest
	transfer FWTransfer
	getErr   error
	gets     int
}

func newFakeCompanyPayer() *fakeCompanyPayer {
	return &fakeCompanyPayer{
		balances: []FWBalance{{Currency: "NGN", AvailableBalance: 1_000_000}},
		initID:   "trf_1",
		transfer: FWTransfer{ID: "trf_1", Status: "PENDING"},
	}
}

func (f *fakeCompanyPayer) InitiatePayout(_ context.Context, req PayoutRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inits = append(f.inits, req)
	if f.initErr != nil {
		return "", f.initErr
	}
	f.transfer.Reference = req.Reference
	return f.initID, nil
}
func (f *fakeCompanyPayer) GetTransfer(context.Context, string) (FWTransfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	return f.transfer, f.getErr
}
func (f *fakeCompanyPayer) FetchBalances(context.Context) ([]FWBalance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.balances, f.balErr
}
func (f *fakeCompanyPayer) sent() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.inits) }
func (f *fakeCompanyPayer) setStatus(s string) {
	f.mu.Lock()
	f.transfer.Status = s
	f.mu.Unlock()
}

// companyRig is a payout rig whose payout runs the company route and has been
// released to the treasury (status received).
func companyRig(t *testing.T) (*payoutRig, *fakeCompanyPayer) {
	t.Helper()
	r := newPayoutRig(t)
	fp := newFakeCompanyPayer()
	r.svc.WithCompany(fp, 5000)
	if err := r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Update("venue", models.SellerPayoutVenueCompany).Error; err != nil {
		t.Fatal(err)
	}
	r.mustStep() // awaiting_release -> received
	r.wantStatus(models.SellerPayoutReceived)
	return r, fp
}

func TestSellerPayoutCompany_PaysFromTheCentralAccountWithNoBridge(t *testing.T) {
	r, fp := companyRig(t)
	bus := events.NewBus()
	defer bus.Stop()
	got := make(chan events.Event, 2)
	bus.Subscribe(events.SellerPayoutPaid, func(e events.Event) { got <- e })
	r.svc.WithEvents(bus)

	r.mustStep() // claims and sends
	p := r.wantStatus(models.SellerPayoutPayingCompany)
	if fp.sent() != 1 || p.CompanyTransferID != "trf_1" || p.CompanyAttempts != 1 {
		t.Fatalf("sent=%d payout=%+v; want one transfer recorded", fp.sent(), p)
	}
	req := fp.inits[0]
	if req.Amount != "150000.00" || req.BankCode != "058" || req.AccountNumber != "0123456789" || req.AccountName != "ADA OBI" ||
		req.SourceCurrency != "NGN" || req.Reference != sellerPayoutRef(r.payout.ID, 0) || !strings.HasPrefix(req.Reference, SellerPayoutRefPrefix) {
		t.Fatalf("transfer request = %+v", req)
	}
	if len(req.Reference) > 42 {
		t.Fatalf("reference %q is over the provider's 42 characters", req.Reference)
	}

	r.mustStep() // provider still pending
	r.wantStatus(models.SellerPayoutPayingCompany)
	fp.setStatus("SUCCESSFUL")
	r.mustStep()
	r.wantStatus(models.SellerPayoutPaid)

	if fp.sent() != 1 || r.broadcasts() != 0 || len(r.legs(models.RampLegKindBridge)) != 0 || r.seller.calls != 0 {
		t.Fatalf("sent=%d broadcasts=%d: the company route sends one transfer and touches no chain, bridge or Paycrest", fp.sent(), r.broadcasts())
	}
	select {
	case e := <-got:
		if e.UserID != r.payout.SellerUserID || !strings.Contains(e.Message, "6789") {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the seller was not told the naira was sent")
	}
	r.mustStep() // a paid payout is left alone
	if fp.sent() != 1 {
		t.Fatal("a paid payout must never send again")
	}
}

func TestSellerPayoutCompany_AShortFloatParksSafelyAndSendsNothing(t *testing.T) {
	r, fp := companyRig(t)
	fp.balances = []FWBalance{{Currency: "NGN", AvailableBalance: 1000}}
	r.mustStep()
	p := r.wantStatus(models.SellerPayoutNeedsReview)
	if !p.RetrySafe || p.ParkedFrom != models.SellerPayoutReceived || !strings.Contains(p.FailureReason, "top it up") || fp.sent() != 0 {
		t.Fatalf("payout = %+v sent=%d; want a safe park from received and nothing sent", p, fp.sent())
	}

	// Topped up, the operator's retry lets it run.
	fp.balances = []FWBalance{{Currency: "NGN", AvailableBalance: 1_000_000}}
	admin, _ := newAdmin(t, r)
	if err := admin.Retry(context.Background(), r.payout.ID, uuid.New(), "topped up", false); err != nil {
		t.Fatal(err)
	}
	r.mustStep()
	r.wantStatus(models.SellerPayoutPayingCompany)
	if fp.sent() != 1 {
		t.Fatalf("sent = %d; want one", fp.sent())
	}
}

func TestSellerPayoutCompany_AnUnreadableBalanceIsNotAShortOne(t *testing.T) {
	r, fp := companyRig(t)
	fp.balErr = errors.New("flutterwave balances: 503")
	if err := r.step(); err == nil {
		t.Fatal("an unreadable balance must surface as an error to try again")
	}
	r.wantStatus(models.SellerPayoutReceived)
	if fp.sent() != 0 {
		t.Fatal("nothing may be sent on an unknown balance")
	}
}

func TestSellerPayoutCompany_ARefusalOnTheFirstAttemptIsSafeAndRetriesOnANewReference(t *testing.T) {
	r, fp := companyRig(t)
	fp.initErr = &ProviderRejectedError{Err: errors.New("invalid account")}
	r.mustStep()
	p := r.wantStatus(models.SellerPayoutNeedsReview)
	if !p.RetrySafe || p.ParkedFrom != models.SellerPayoutPayingCompany {
		t.Fatalf("payout = %+v; want a safe park from paying_company", p)
	}
	first := fp.inits[0].Reference

	fp.initErr = nil
	admin, _ := newAdmin(t, r)
	if err := admin.Retry(context.Background(), r.payout.ID, uuid.New(), "fixed the account", false); err != nil {
		t.Fatal(err)
	}
	got := r.wantStatus(models.SellerPayoutReceived)
	if got.CompanyRefGen != 1 || got.CompanyTransferID != "" {
		t.Fatalf("payout = %+v; want generation 1 and no transfer", got)
	}
	r.mustStep()
	r.wantStatus(models.SellerPayoutPayingCompany)
	if second := fp.inits[1].Reference; second == first || !strings.HasSuffix(second, "-1") || len(second) > 42 {
		t.Fatalf("references %q then %q; want a new, in-limit one", first, second)
	}
}

func TestSellerPayoutCompany_AnUnknownOutcomeNeverSendsAgain(t *testing.T) {
	r, fp := companyRig(t)
	fp.initErr = errors.New("context deadline exceeded")
	r.mustStep()
	p := r.wantStatus(models.SellerPayoutNeedsReview)
	if p.RetrySafe || p.ParkedFrom != models.SellerPayoutPayingCompany || !strings.Contains(p.FailureReason, p.CompanyRef) {
		t.Fatalf("payout = %+v; want an unsafe park naming the reference", p)
	}
	fp.initErr = nil
	for i := 0; i < 3; i++ {
		r.mustStep()
	}
	if fp.sent() != 1 {
		t.Fatalf("sent = %d; a transfer that may have left is never sent again", fp.sent())
	}

	// Retrying it needs force, and the wallet is never offered for it.
	admin, _ := newAdmin(t, r)
	if err := admin.Retry(context.Background(), r.payout.ID, uuid.New(), "checked", false); !errors.Is(err, ErrPayoutRetryUnsafe) {
		t.Fatalf("retry = %v; want retry_unsafe", err)
	}
	if err := r.svc.StartWalletPayment(context.Background(), r.reload()); !errors.Is(err, ErrWalletPaymentNotSafe) {
		t.Fatalf("pay wallet = %v; want not safe after a transfer may have left", err)
	}
}

func TestSellerPayoutCompany_AFailedTransferParksForAPerson(t *testing.T) {
	r, fp := companyRig(t)
	r.mustStep()
	fp.setStatus("FAILED")
	r.mustStep()
	p := r.wantStatus(models.SellerPayoutNeedsReview)
	if p.RetrySafe || p.ParkedFrom != models.SellerPayoutPayingCompany || !strings.Contains(p.FailureReason, "failed") {
		t.Fatalf("payout = %+v", p)
	}
}

func TestSellerPayoutCompany_AClaimWithNoRecordedTransferParksAfterAWhile(t *testing.T) {
	r, fp := companyRig(t)
	// The process died between the claim and the answer.
	r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).
		Updates(map[string]any{"status": models.SellerPayoutPayingCompany, "company_ref": sellerPayoutRef(r.payout.ID, 0), "company_attempts": 1, "updated_at": r.clock})
	r.mustStep()
	r.wantStatus(models.SellerPayoutPayingCompany) // not yet: the answer may still be recorded
	r.clock = r.clock.Add(sellerPayoutCompanyUncertainAfter + time.Second)
	r.mustStep()
	p := r.wantStatus(models.SellerPayoutNeedsReview)
	if p.RetrySafe || fp.sent() != 0 {
		t.Fatalf("payout = %+v sent=%d; want an unsafe park and no resend", p, fp.sent())
	}
}

func TestSellerPayoutCompany_TwoTicksSendOnce(t *testing.T) {
	r, fp := companyRig(t)
	a, b := r.reload(), r.reload()
	if err := r.svc.Advance(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Advance(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if fp.sent() != 1 {
		t.Fatalf("sent = %d; two ticks on one payout must send exactly one transfer", fp.sent())
	}
}

func TestSellerPayoutCompany_APausedMachineSendsNothing(t *testing.T) {
	r, fp := companyRig(t)
	r.svc.WithSwitch(&fakeSwitch{on: false})
	r.mustStep()
	r.wantStatus(models.SellerPayoutReceived)
	if fp.sent() != 0 {
		t.Fatal("a switched-off machine must not send")
	}
}

func TestSellerPayoutCompany_AWrongReferenceOnTheTransferParks(t *testing.T) {
	r, fp := companyRig(t)
	r.mustStep()
	fp.mu.Lock()
	fp.transfer.Reference = "SPO-someoneelse"
	fp.transfer.Status = "SUCCESSFUL"
	fp.mu.Unlock()
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
}

func TestSellerPayoutCompany_TheWebhookOnlyWakesTheMachine(t *testing.T) {
	r, _ := companyRig(t)
	r.mustStep()
	ref := r.reload().CompanyRef
	r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).UpdateColumn("checked_at", r.clock)
	if err := NudgeCompanyPayout(context.Background(), r.db, ref); err != nil {
		t.Fatal(err)
	}
	if r.reload().CheckedAt != nil {
		t.Fatal("the webhook must make the machine look at the payout on its next tick")
	}
	if r.reload().Status != models.SellerPayoutPayingCompany {
		t.Fatal("a webhook never decides the outcome")
	}
	if err := NudgeCompanyPayout(context.Background(), r.db, "SPO-unknown"); err != nil {
		t.Fatalf("an unknown reference is logged, not an error: %v", err)
	}
}

func TestSellerPayoutCompany_RebalanceAccountsForPaidPayoutsOnly(t *testing.T) {
	r, fp := companyRig(t)
	admin, _ := newAdmin(t, r)
	ctx := context.Background()

	owed, err := admin.CompanyOwed(ctx)
	if err != nil || len(owed.Payouts) != 0 {
		t.Fatalf("owed = %+v, %v; an unpaid payout is not owed yet", owed, err)
	}
	r.mustStep()
	fp.setStatus("SUCCESSFUL")
	r.mustStep()
	r.wantStatus(models.SellerPayoutPaid)
	r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Update("received_amount", "100000000")

	owed, err = admin.CompanyOwed(ctx)
	if err != nil || len(owed.Payouts) != 1 || owed.Totals["USDC"] != "100000000" {
		t.Fatalf("owed = %+v, %v; want 100 USDC", owed, err)
	}
	if _, err := admin.MarkRebalanced(ctx, []uuid.UUID{r.payout.ID}, uuid.New(), "", "ref"); !errors.Is(err, ErrPayoutNeedsNote) {
		t.Fatalf("no note = %v", err)
	}
	// An id that is not owed refuses the WHOLE batch.
	if _, err := admin.MarkRebalanced(ctx, []uuid.UUID{r.payout.ID, uuid.New()}, uuid.New(), "batch", "tx"); err == nil {
		t.Fatal("a batch with an ineligible id must be refused")
	}
	if r.reload().RebalancedAt != nil {
		t.Fatal("a refused batch must change nothing")
	}
	n, err := admin.MarkRebalanced(ctx, []uuid.UUID{r.payout.ID}, uuid.New(), "batch 1", "0xabc")
	if err != nil || n != 1 || r.reload().RebalancedAt == nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if owed, _ = admin.CompanyOwed(ctx); len(owed.Payouts) != 0 {
		t.Fatalf("owed after rebalancing = %+v", owed)
	}
	if _, err := admin.MarkRebalanced(ctx, []uuid.UUID{r.payout.ID}, uuid.New(), "again", "0xabc"); err == nil {
		t.Fatal("a rebalanced payout cannot be rebalanced twice")
	}
}

// The company payout still counts as USDC held while it is in flight.
func TestSellerPayoutCompany_InFlightFundsCountTowardTheCaps(t *testing.T) {
	r, _ := companyRig(t)
	r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).
		Updates(map[string]any{"status": models.SellerPayoutPayingCompany, "received_amount": "100000000"})
	usd, err := sellerPayoutExposureUSD(context.Background(), r.db, "sonic", uuid.New())
	if err != nil || usd != 100 {
		t.Fatalf("exposure = %v, %v; want the 100 USDC held on Sonic", usd, err)
	}
	_ = big.NewInt
}

// A forced retry must first prove the old transfer dead: the operator's word
// that it "may have been sent" is not enough to send another.
func TestSellerPayoutCompany_AForcedRetryNeedsTheOldTransferProvenDead(t *testing.T) {
	r, fp := companyRig(t)
	r.mustStep() // sent: trf_1, pending
	// The provider's read comes back wrong once, parking it with a transfer on record.
	fp.mu.Lock()
	fp.transfer.Reference = "SPO-someoneelse"
	fp.mu.Unlock()
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)

	admin, _ := newAdmin(t, r)
	fp.mu.Lock()
	fp.transfer.Reference = sellerPayoutRef(r.payout.ID, 0)
	fp.transfer.Status = "SUCCESSFUL"
	fp.mu.Unlock()
	if err := admin.Retry(context.Background(), r.payout.ID, uuid.New(), "it said it may have been sent", true); !errors.Is(err, ErrPayoutStillLive) {
		t.Fatalf("retry = %v; a transfer that succeeded must refuse a second one", err)
	}
	fp.setStatus("PENDING")
	if err := admin.Retry(context.Background(), r.payout.ID, uuid.New(), "still pending", true); !errors.Is(err, ErrPayoutStillLive) {
		t.Fatalf("retry = %v; a pending transfer must refuse a second one", err)
	}
	fp.setStatus("FAILED")
	if err := admin.Retry(context.Background(), r.payout.ID, uuid.New(), "failed at the provider", true); err != nil {
		t.Fatalf("retry of a provably failed transfer = %v", err)
	}
	r.wantStatus(models.SellerPayoutReceived)
}

func TestSellerPayoutCompany_AReversedOrUnknownTransferStatusParks(t *testing.T) {
	for _, status := range []string{"REVERSED", "SOMETHING_NEW"} {
		r, fp := companyRig(t)
		r.mustStep()
		fp.setStatus(status)
		r.mustStep()
		p := r.wantStatus(models.SellerPayoutNeedsReview)
		if p.RetrySafe {
			t.Fatalf("%s: an unrecognised outcome is never a safe retry", status)
		}
	}
}

func TestSellerPayoutCompany_AReversalFlagsAPaidPayout(t *testing.T) {
	r, fp := companyRig(t)
	r.mustStep()
	fp.setStatus("SUCCESSFUL")
	r.mustStep()
	r.wantStatus(models.SellerPayoutPaid)
	if err := FlagCompanyReversal(context.Background(), r.db, r.reload().CompanyRef); err != nil {
		t.Fatal(err)
	}
	p := r.wantStatus(models.SellerPayoutNeedsReview)
	if p.RetrySafe || p.ParkedFrom != models.SellerPayoutPayingCompany || !strings.Contains(p.FailureReason, "reversed") {
		t.Fatalf("payout = %+v", p)
	}
	// An unrelated reference changes nothing.
	if err := FlagCompanyReversal(context.Background(), r.db, "SPO-unknown"); err != nil {
		t.Fatal(err)
	}
}

func TestSellerPayoutCompany_ATransferInFlightStaysObservableWhateverTheTreasurySays(t *testing.T) {
	r, fp := companyRig(t)
	r.mustStep() // sent
	r.svc.treasury = nil
	fp.setStatus("SUCCESSFUL")
	r.mustStep()
	r.wantStatus(models.SellerPayoutPaid)
}

func TestSellerPayoutCompany_RebalanceIgnoresHandPaidAndDedupesIds(t *testing.T) {
	r, fp := companyRig(t)
	admin, _ := newAdmin(t, r)
	ctx := context.Background()
	r.mustStep()
	fp.setStatus("SUCCESSFUL")
	r.mustStep()
	r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Update("received_amount", "100000000")

	// The same id twice is one payout, not a refused batch.
	if n, err := admin.MarkRebalanced(ctx, []uuid.UUID{r.payout.ID, r.payout.ID}, uuid.New(), "batch", "0xabc"); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	// A payout marked paid by hand never debited the central account: not owed.
	handPaid := &models.SellerPayoutOrder{
		ID: uuid.New(), MarketplaceOrderID: uuid.New(), SellerUserID: uuid.New(), Currency: "NGN",
		InstitutionCode: "058", InstitutionName: "GTBank", AccountNumber: "0123456789", AccountName: "A",
		ChainKey: "sonic", Token: "USDC", ExpectedAmount: "1", FiatTargetKobo: 1, ReceivedAmount: "50000000",
		Status: models.SellerPayoutPaid, Venue: models.SellerPayoutVenueCompany,
	}
	if err := r.db.Create(handPaid).Error; err != nil {
		t.Fatal(err)
	}
	owed, err := admin.CompanyOwed(ctx)
	if err != nil || len(owed.Payouts) != 0 {
		t.Fatalf("owed = %+v, %v; a hand-paid payout owes the central account nothing", owed, err)
	}
	if _, err := admin.MarkRebalanced(ctx, []uuid.UUID{handPaid.ID}, uuid.New(), "batch", "0xdef"); err == nil {
		t.Fatal("a hand-paid payout cannot be marked rebalanced")
	}
}

func TestSellerPayoutCompany_ReservesNoBridgeGas(t *testing.T) {
	r, _ := companyRig(t)
	c := NewSellerPayoutCommitments(r.db, "sonic")
	got, err := c.Units(context.Background())
	if err != nil || got != 0 {
		t.Fatalf("units = %d, %v; a company payout needs no bridge so reserves no Sonic gas", got, err)
	}
}
