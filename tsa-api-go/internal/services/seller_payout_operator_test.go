package services

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// parkAs puts the rig's payout in needs_review the way the machine would.
func (r *payoutRig) parkAs(from string, received bool) {
	r.t.Helper()
	updates := map[string]any{"status": models.SellerPayoutNeedsReview, "parked_from": from, "retry_safe": false, "failure_reason": "stuck"}
	if received {
		updates["received_amount"] = "100000000"
	}
	if err := r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Updates(updates).Error; err != nil {
		r.t.Fatal(err)
	}
}

func TestReroute_ASmallOrderTheBridgeRefusedIsPaidByTheCompanyRoute(t *testing.T) {
	// The real failure: the bridge route costs too much for the size, every attempt
	// is refused before anything is broadcast, and the payout parks.
	r := newPayoutRig(t)
	fp := newFakeCompanyPayer()
	r.svc.WithCompany(fp, 5000)
	admin, _ := newAdmin(t, r)
	r.lifi.route.ToAmountUSD = 98 // 200 bps: over the cap
	r.mustStep()                  // received
	r.mustStep()                  // the attempt is refused
	r.clock = r.clock.Add(31 * time.Minute)
	r.mustStep()
	p := r.wantStatus(models.SellerPayoutNeedsReview)
	if p.Venue != models.SellerPayoutVenuePaycrest || r.broadcasts() != 0 {
		t.Fatalf("payout = %+v broadcasts=%d; want parked on Paycrest with nothing sent", p, r.broadcasts())
	}

	if err := admin.Reroute(context.Background(), r.payout.ID, uuid.New(), "too small to bridge", "company"); err != nil {
		t.Fatalf("reroute = %v", err)
	}
	p = r.wantStatus(models.SellerPayoutReceived)
	if p.Venue != models.SellerPayoutVenueCompany || p.FailureReason != "" || p.ParkedFrom != "" {
		t.Fatalf("payout = %+v", p)
	}
	r.mustStep()
	r.wantStatus(models.SellerPayoutPayingCompany)
	if fp.sent() != 1 || r.broadcasts() != 0 {
		t.Fatalf("sent=%d broadcasts=%d; the central account pays and no chain call is made", fp.sent(), r.broadcasts())
	}
}

func TestReroute_NeverMovesAPayoutSomethingWasSentFor(t *testing.T) {
	ctx := context.Background()

	t.Run("a bridge that broadcast", func(t *testing.T) {
		r := newPayoutRig(t)
		r.svc.WithCompany(newFakeCompanyPayer(), 5000)
		admin, _ := newAdmin(t, r)
		r.mustStep() // received
		r.mustStep() // bridge broadcast
		r.parkAs(models.SellerPayoutBridging, true)
		if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "try", "company"); !errors.Is(err, ErrPayoutRerouteUnsafe) {
			t.Fatalf("reroute = %v; a broadcast bridge may have moved the money", err)
		}
		if r.reload().Venue != models.SellerPayoutVenuePaycrest {
			t.Fatal("a refused reroute changes nothing")
		}
	})

	t.Run("a Paycrest order", func(t *testing.T) {
		r := newPayoutRig(t)
		r.svc.WithCompany(newFakeCompanyPayer(), 5000)
		admin, _ := newAdmin(t, r)
		r.parkAs(models.SellerPayoutSelling, true)
		o := uuid.New()
		r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Update("paycrest_order_id", o)
		if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "try", "company"); !errors.Is(err, ErrPayoutRerouteUnsafe) {
			t.Fatalf("reroute = %v", err)
		}
	})

	t.Run("a company transfer that may have left", func(t *testing.T) {
		r, fp := companyRig(t)
		admin, _ := newAdmin(t, r)
		fp.initErr = errors.New("context deadline exceeded")
		r.mustStep() // unknown outcome: parked unsafe
		r.wantStatus(models.SellerPayoutNeedsReview)
		if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "try", "paycrest"); !errors.Is(err, ErrPayoutRerouteUnsafe) {
			t.Fatalf("reroute = %v; the transfer may have paid the seller", err)
		}
	})
}

func TestReroute_ARefusedCompanyTransferCanGoToPaycrestAndBack(t *testing.T) {
	r, fp := companyRig(t)
	admin, _ := newAdmin(t, r)
	ctx := context.Background()
	fp.initErr = &ProviderRejectedError{Err: errors.New("invalid account")}
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview) // safe park: the refusal proves no transfer exists

	if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "give Paycrest a go", "paycrest"); err != nil {
		t.Fatalf("reroute = %v", err)
	}
	p := r.wantStatus(models.SellerPayoutReceived)
	if p.Venue != models.SellerPayoutVenuePaycrest || p.CompanyRefGen != 1 {
		t.Fatalf("payout = %+v; want Paycrest with the company reference generation moved on", p)
	}
	r.mustStep()
	r.wantStatus(models.SellerPayoutBridging)
	if fp.sent() != 1 {
		t.Fatalf("sent = %d; moving to Paycrest sends no company transfer", fp.sent())
	}
}

func TestReroute_ReturnsAnUnprovenReleaseToAwaitingIt(t *testing.T) {
	r := newPayoutRig(t)
	r.svc.WithCompany(newFakeCompanyPayer(), 5000)
	admin, _ := newAdmin(t, r)
	r.parkAs(models.SellerPayoutAwaitingRelease, false)
	if err := admin.Reroute(context.Background(), r.payout.ID, uuid.New(), "route", "company"); err != nil {
		t.Fatal(err)
	}
	r.wantStatus(models.SellerPayoutAwaitingRelease) // the release is still to be proven
}

func TestReroute_RefusesWhatItShould(t *testing.T) {
	ctx := context.Background()
	r := newPayoutRig(t)
	r.svc.WithCompany(newFakeCompanyPayer(), 5000)
	admin, _ := newAdmin(t, r)

	if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "x", "company"); !errors.Is(err, ErrPayoutNotParked) {
		t.Fatalf("a payout that is not parked = %v", err)
	}
	r.parkAs(models.SellerPayoutReceived, true)
	for name, c := range map[string]struct {
		note, to string
		want     error
	}{
		"no note":        {"", "company", ErrPayoutNeedsNote},
		"not a route":    {"x", "wire", ErrPayoutBadRoute},
		"the same route": {"x", "paycrest", ErrPayoutBadRoute},
	} {
		if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), c.note, c.to); !errors.Is(err, c.want) {
			t.Fatalf("%s: reroute = %v; want %v", name, err, c.want)
		}
	}
	r.svc.company = nil
	if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "x", "company"); !errors.Is(err, ErrPayoutRouteUnavailable) {
		t.Fatalf("no central account = %v", err)
	}
}

func TestHealth_ShowsWhatAnOperatorNeeds(t *testing.T) {
	r, fp := companyRig(t)
	admin, _ := newAdmin(t, r)
	ctx := context.Background()
	r.mustStep() // paying_company, ₦150,000 promised

	h, err := admin.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.Counts[models.SellerPayoutPayingCompany] != 1 {
		t.Fatalf("counts = %v", h.Counts)
	}
	// ₦1,000,000 available, ₦150,000 promised, one payout's ₦50 fee held back.
	if h.Central == nil || h.Central.AvailableKobo != 100_000_000 || h.Central.PromisedKobo != 15_000_000 ||
		h.Central.OpenPayouts != 1 || h.Central.FreeKobo != 100_000_000-15_000_000-5000 {
		t.Fatalf("central = %+v", h.Central)
	}
	if h.OldestParkedAt != nil {
		t.Fatal("nothing is parked")
	}

	// Parked payouts show how long they have waited; an unreadable balance is
	// reported as a problem, never shown as zero.
	r.parkAs(models.SellerPayoutPayingCompany, true)
	fp.balErr = errors.New("503")
	h, err = admin.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if h.OldestParkedAt == nil || h.Central != nil {
		t.Fatalf("oldest=%v central=%+v; want a parked age and no invented balance", h.OldestParkedAt, h.Central)
	}
	found := false
	for _, p := range h.Problems {
		found = found || strings.Contains(p, "central account balance")
	}
	if !found {
		t.Fatalf("problems = %v; the unreadable balance must be named", h.Problems)
	}
}

func addLeg(t *testing.T, r *payoutRig, l models.RampLeg) {
	t.Helper()
	l.OrderID, l.ChainKey, l.Token = r.payout.ID, "sonic", "USDC"
	l.FromAddr, l.ToAddr = r.treasury.Address(), r.treasury.Address()
	if l.Kind == "" {
		l.Kind = models.RampLegKindBridge
	}
	if l.Attempt == 0 {
		l.Attempt = 1
	}
	if err := r.db.Create(&l).Error; err != nil {
		t.Fatal(err)
	}
}

func TestReroute_AnyTraceOfASendBlocksIt(t *testing.T) {
	for name, leg := range map[string]models.RampLeg{
		"an approve with no route":   {Status: models.RampLegStatusFailed, ApproveTxHash: "0xapprove"},
		"a pending leg with no hash": {Status: models.RampLegStatusPending},
		"a confirmed leg":            {Status: models.RampLegStatusConfirmed},
		"a failed leg that had a tx": {Status: models.RampLegStatusFailed, TxHash: "0xroute"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newPayoutRig(t)
			r.svc.WithCompany(newFakeCompanyPayer(), 5000)
			admin, _ := newAdmin(t, r)
			r.parkAs(models.SellerPayoutBridging, true)
			addLeg(t, r, leg)
			if err := admin.Reroute(context.Background(), r.payout.ID, uuid.New(), "try", "company"); !errors.Is(err, ErrPayoutRerouteUnsafe) {
				t.Fatalf("reroute = %v", err)
			}
		})
	}
}

// A refusal on a LATER reference says nothing about an earlier one that may have landed.
func TestReroute_ASecondRefusalDoesNotProveTheFirstTransferAbsent(t *testing.T) {
	r, fp := companyRig(t)
	admin, _ := newAdmin(t, r)
	ctx := context.Background()
	fp.initErr = errors.New("context deadline exceeded") // attempt 1: outcome unknown
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)
	fp.mu.Lock()
	fp.transfer.Status = "FAILED" // the operator proved it dead to be allowed to retry
	fp.mu.Unlock()
	if err := admin.Retry(ctx, r.payout.ID, uuid.New(), "checked", true); err != nil {
		t.Fatal(err)
	}
	fp.initErr = &ProviderRejectedError{Err: errors.New("refused")} // attempt 2 is refused: a safe park
	r.mustStep()
	p := r.wantStatus(models.SellerPayoutNeedsReview)
	if !p.RetrySafe || p.CompanyAttempts != 2 {
		t.Fatalf("payout = %+v; want a safe park on the second reference", p)
	}
	if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "try", "paycrest"); !errors.Is(err, ErrPayoutRerouteUnsafe) {
		t.Fatalf("reroute = %v; the first reference may have paid", err)
	}
	if err := r.svc.StartWalletPayment(ctx, r.reload()); !errors.Is(err, ErrWalletPaymentNotSafe) {
		t.Fatalf("pay wallet = %v; same rule", err)
	}
}

func TestReroute_AStaleReadCannotWriteOverANewerState(t *testing.T) {
	r := newPayoutRig(t)
	r.svc.WithCompany(newFakeCompanyPayer(), 5000)
	admin, _ := newAdmin(t, r)
	r.parkAs(models.SellerPayoutReceived, true)
	stale := r.reload()
	// Someone else acts in between: the row changes (here, a re-park with a new reason).
	r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).
		Updates(map[string]any{"failure_reason": "parked again", "updated_at": r.clock.Add(time.Minute)})
	if err := admin.rerouteLoaded(context.Background(), stale, uuid.New(), "route", models.SellerPayoutVenueCompany); !errors.Is(err, ErrPayoutNotParked) {
		t.Fatalf("reroute on a stale row = %v; want it refused", err)
	}
	if got := r.reload(); got.Venue != models.SellerPayoutVenuePaycrest || got.Status != models.SellerPayoutNeedsReview {
		t.Fatalf("payout = %+v; a stale decision must change nothing", got)
	}
}

func TestReroute_TheCompanyRouteKeepsItsOwnLimits(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*payoutRig, *fakeCompanyPayer, *SellerPayoutAdmin) {
		r := newPayoutRig(t)
		fp := newFakeCompanyPayer()
		r.svc.WithCompany(fp, 5000).WithCompanyMax(50_000_000) // ₦500,000
		admin, _ := newAdmin(t, r)
		r.parkAs(models.SellerPayoutReceived, true)
		return r, fp, admin
	}
	for name, mod := range map[string]func(r *payoutRig, fp *fakeCompanyPayer){
		"over the route's maximum": func(r *payoutRig, _ *fakeCompanyPayer) {
			r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Update("fiat_target_kobo", 80_000_000)
		},
		"under the transfer floor": func(r *payoutRig, _ *fakeCompanyPayer) {
			r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).Update("fiat_target_kobo", 5000)
		},
		"more than the float can pay": func(_ *payoutRig, fp *fakeCompanyPayer) {
			fp.balances = []FWBalance{{Currency: "NGN", AvailableBalance: 1000}}
		},
		"an unreadable balance": func(_ *payoutRig, fp *fakeCompanyPayer) { fp.balErr = errors.New("503") },
	} {
		t.Run(name, func(t *testing.T) {
			r, fp, admin := setup(t)
			mod(r, fp)
			if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "route", "company"); err == nil {
				t.Fatal("the company route must refuse what it would refuse at checkout")
			} else if name != "an unreadable balance" && !errors.Is(err, ErrPayoutRouteUnavailable) {
				t.Fatalf("err = %v; want route_unavailable", err)
			}
			if r.reload().Venue != models.SellerPayoutVenuePaycrest {
				t.Fatal("a refused reroute changes nothing")
			}
		})
	}
}

func TestReroute_PaycrestNeedsTheWholeBridgePath(t *testing.T) {
	r, _ := companyRig(t)
	admin, _ := newAdmin(t, r)
	r.parkAs(models.SellerPayoutReceived, true)
	r.svc.lifi = nil
	if err := admin.Reroute(context.Background(), r.payout.ID, uuid.New(), "route", "paycrest"); !errors.Is(err, ErrPayoutRouteUnavailable) {
		t.Fatalf("no LiFi quoter = %v", err)
	}
}

func TestReroute_ResetsTheClocksAuditsAndCanGoBack(t *testing.T) {
	r, fp := companyRig(t)
	admin, _ := newAdmin(t, r)
	ctx := context.Background()
	fp.initErr = &ProviderRejectedError{Err: errors.New("invalid account")}
	r.mustStep()
	r.wantStatus(models.SellerPayoutNeedsReview)

	if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "go to paycrest", "paycrest"); err != nil {
		t.Fatal(err)
	}
	p := r.wantStatus(models.SellerPayoutReceived)
	if p.CompanyAttempts != 0 || p.CompanyRefGen != 1 || p.AttemptStartedAt == nil || p.NextAttemptAt != nil {
		t.Fatalf("payout = %+v; want the counter reset, a new generation and fresh clocks", p)
	}
	var audit models.P2PAuditLog
	if err := r.db.Where("action = ?", "seller_payout.reroute").First(&audit).Error; err != nil ||
		!strings.Contains(string(audit.Metadata), `"fromVenue":"company"`) || !strings.Contains(string(audit.Metadata), `"to":"paycrest"`) {
		t.Fatalf("audit = %s, %v; want the old and new venue recorded", audit.Metadata, err)
	}

	// Stuck again on the Paycrest side with nothing sent: it can go back to the company route.
	r.parkAs(models.SellerPayoutBridging, true)
	fp.initErr = nil
	if err := admin.Reroute(ctx, r.payout.ID, uuid.New(), "and back", "company"); err != nil {
		t.Fatalf("back to company = %v", err)
	}
	r.mustStep()
	r.wantStatus(models.SellerPayoutPayingCompany)
	if got := r.reload(); got.CompanyRef != sellerPayoutRef(r.payout.ID, 1) {
		t.Fatalf("ref = %s; want the next reference generation", got.CompanyRef)
	}
}

func TestHealth_ArithmeticAndTheTreasury(t *testing.T) {
	r, _ := companyRig(t)
	admin, _ := newAdmin(t, r)
	r.mustStep() // ₦150,000 paying
	second := &models.SellerPayoutOrder{
		ID: uuid.New(), MarketplaceOrderID: uuid.New(), SellerUserID: uuid.New(), Currency: "NGN",
		InstitutionCode: "058", InstitutionName: "GTBank", AccountNumber: "0123456789", AccountName: "A",
		ChainKey: "sonic", Token: "USDC", ExpectedAmount: "1", FiatTargetKobo: 10_000_000,
		Status: models.SellerPayoutReceived, Venue: models.SellerPayoutVenueCompany,
	}
	if err := r.db.Create(second).Error; err != nil {
		t.Fatal(err)
	}
	h, err := admin.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Central == nil || h.Central.PromisedKobo != 25_000_000 || h.Central.OpenPayouts != 2 ||
		h.Central.FreeKobo != 100_000_000-25_000_000-2*5000 {
		t.Fatalf("central = %+v; want ₦250,000 promised across two payouts and a fee held back for each", h.Central)
	}
	for _, c := range h.Treasury {
		if c.Chain == "" || c.NativeWei == "" {
			t.Fatalf("treasury entry = %+v", c)
		}
	}
}
