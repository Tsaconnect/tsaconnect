package services

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// fakeVenueQuoter answers every ask with one raw rate, or an error.
type fakeVenueQuoter struct {
	rate string
	err  error
	asks []string
}

func (f *fakeVenueQuoter) VenueForFee(_ context.Context, _, _, fiatAmount, _, _ string, _ float64) (*PaycrestVenue, error) {
	f.asks = append(f.asks, fiatAmount)
	if f.err != nil {
		return nil, f.err
	}
	return &PaycrestVenue{RawRate: f.rate}, nil
}

func newPlanner(t *testing.T) (*SellerPayoutPlanner, *payoutRig, *fakeVenueQuoter, uuid.UUID) {
	t.Helper()
	r := newPayoutRig(t)
	r.treasury.Lane("sonic").CallTargets = treasuryTestRouters // any non-empty allowlist
	svc, _ := sellerPayoutSvc(t, stubPayoutBanks{name: "ADA OBI"})
	uid := seller(t, svc)
	if _, err := setWithCode(t, svc, svc.mailer.(*codeMailer), uid, SetSellerPayoutInput{InstitutionCode: "058", AccountNumber: "0123456789"}); err != nil {
		t.Fatal(err)
	}
	q := &fakeVenueQuoter{rate: "1500"}
	return NewSellerPayoutPlanner(svc, q, r.treasury, func() string { return "0.5" }, 40), r, q, uid
}

func TestPayoutGrossUp_CoversTheFeeAndTheBridge(t *testing.T) {
	net := big.NewInt(100_000000)
	// 100 / ((1-0.005)(1-0.004)) = 100.9, so the buyer adds about 0.906.
	got := payoutGrossUp(net, big.NewRat(5, 1000), 40)
	if got.String() != "906138" {
		t.Fatalf("gross-up = %s; want 906138 (0.906138 USDC)", got)
	}
	// What the treasury ends up with after the bridge and the fee covers net.
	received := new(big.Int).Add(net, got)
	afterBridge := new(big.Rat).Mul(new(big.Rat).SetInt(received), big.NewRat(9960, 10000))
	deposit := new(big.Rat).Quo(new(big.Rat).SetInt(net), big.NewRat(995, 1000))
	if afterBridge.Cmp(deposit) < 0 {
		t.Fatalf("after the bridge %s cannot fund the %s deposit", afterBridge.FloatString(2), deposit.FloatString(2))
	}
	if g := payoutGrossUp(net, big.NewRat(0, 1), 0); g.Sign() != 0 {
		t.Fatalf("no fee and a free bridge cost nothing, got %s", g)
	}
}

func TestPayoutPlan_PricesTheSellersNaira(t *testing.T) {
	p, _, q, uid := newPlanner(t)
	plan, err := p.Plan(context.Background(), uid, "usdc", big.NewInt(100_000000))
	if err != nil || plan == nil {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	// 100 USDC at 1,500 = ₦150,000.00, in kobo; the venue is asked at the real size.
	if plan.FiatTargetKobo != 15_000_000 || plan.GrossUp.String() != "906138" {
		t.Fatalf("plan = %+v", plan)
	}
	if len(q.asks) != 2 || q.asks[0] != "100000" || q.asks[1] != "150000" {
		t.Fatalf("venue asks = %v; want the reference size, then the real one", q.asks)
	}
}

func TestPayoutPlan_PaysTheWalletWhenAnythingIsOff(t *testing.T) {
	ctx := context.Background()
	net := big.NewInt(100_000000)
	cases := map[string]func(p *SellerPayoutPlanner, r *payoutRig, q *fakeVenueQuoter) (token string){
		"MCGP is never offramped": func(*SellerPayoutPlanner, *payoutRig, *fakeVenueQuoter) string { return "MCGP" },
		"no venue for the amount": func(_ *SellerPayoutPlanner, _ *payoutRig, q *fakeVenueQuoter) string {
			q.err = ErrPaycrestBelowMinimum
			return "USDC"
		},
		"an unusable rate": func(_ *SellerPayoutPlanner, _ *payoutRig, q *fakeVenueQuoter) string { q.rate = "0"; return "USDC" },
		"the treasury kill switch": func(_ *SellerPayoutPlanner, r *payoutRig, _ *fakeVenueQuoter) string {
			r.treasury.SetKillSwitch(true)
			return "USDC"
		},
		"no Base lane": func(p *SellerPayoutPlanner, r *payoutRig, _ *fakeVenueQuoter) string { p.treasury = nil; return "USDC" },
		"the escrow is not callable": func(_ *SellerPayoutPlanner, r *payoutRig, _ *fakeVenueQuoter) string {
			r.treasury.Lane("sonic").CallTargets = nil
			return "USDC"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			p, r, q, uid := newPlanner(t)
			token := setup(p, r, q)
			plan, err := p.Plan(ctx, uid, token, net)
			if err != nil || plan != nil {
				t.Fatalf("plan = %+v, err = %v; want the wallet", plan, err)
			}
		})
	}
	t.Run("a seller with no payout method", func(t *testing.T) {
		p, _, _, _ := newPlanner(t)
		if plan, err := p.Plan(ctx, uuid.New(), "USDC", net); err != nil || plan != nil {
			t.Fatalf("plan = %+v, err = %v", plan, err)
		}
	})
	t.Run("a nil planner", func(t *testing.T) {
		var p *SellerPayoutPlanner
		if plan, err := p.Plan(ctx, uuid.New(), "USDC", net); err != nil || plan != nil {
			t.Fatalf("plan = %+v, err = %v", plan, err)
		}
	})
	t.Run("a failed method lookup is an error, not a silent wallet", func(t *testing.T) {
		p, _, _, uid := newPlanner(t)
		p.methods.db.Exec(`DROP TABLE seller_payout_methods`)
		if _, err := p.Plan(ctx, uid, "USDC", net); err == nil {
			t.Fatal("an unreadable payout method must not be read as 'no method'")
		}
	})
}

func TestPayoutPlan_PersistSnapshotsTheDestination(t *testing.T) {
	p, r, _, uid := newPlanner(t)
	plan, _ := p.Plan(context.Background(), uid, "USDC", big.NewInt(100_000000))
	order := &models.Order{ID: uuid.New(), SellerID: uid, Token: "usdc"}
	expected := big.NewInt(100_906138)
	if err := p.Persist(r.db, order, "0xsellerwallet", plan, expected); err != nil {
		t.Fatal(err)
	}
	row, err := PayoutForOrder(context.Background(), r.db, order.ID)
	if err != nil || row == nil {
		t.Fatalf("row = %+v, %v", row, err)
	}
	if row.AccountNumber != "0123456789" || row.AccountName != "ADA OBI" || row.InstitutionCode != "058" ||
		row.ExpectedAmount != "100906138" || row.FiatTargetKobo != 15_000_000 || row.Token != "USDC" ||
		row.Status != models.SellerPayoutAwaitingRelease || row.SellerWallet != "0xsellerwallet" {
		t.Fatalf("row = %+v", row)
	}
	if none, err := PayoutForOrder(context.Background(), r.db, uuid.New()); err != nil || none != nil {
		t.Fatalf("an order with no payout: %+v, %v", none, err)
	}
	_ = errors.New
}

func TestPayoutPlan_AnOperatorSwitchStopsNewPayoutOrders(t *testing.T) {
	p, _, _, uid := newPlanner(t)
	sw := &fakeSwitch{on: false}
	p.WithSwitch(sw)
	for name, s := range map[string]*fakeSwitch{"off": {on: false}, "unreadable": {err: errors.New("db down")}} {
		p.WithSwitch(s)
		if plan, err := p.Plan(context.Background(), uid, "USDC", big.NewInt(100_000000)); err != nil || plan != nil {
			t.Fatalf("%s: plan = %+v, err = %v; the order is made the ordinary way", name, plan, err)
		}
	}
	p.WithSwitch(&fakeSwitch{on: true})
	if plan, err := p.Plan(context.Background(), uid, "USDC", big.NewInt(100_000000)); err != nil || plan == nil {
		t.Fatalf("on: plan = %+v, err = %v", plan, err)
	}
}

func TestSellerPayoutSwitch_PersistsAndTakesEffectAtOnce(t *testing.T) {
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.SellerPayoutSetting{}); err != nil {
		t.Fatal(err)
	}
	sw := NewSellerPayoutSwitch(db)
	ctx := context.Background()
	if on, err := sw.Enabled(ctx); err != nil || !on {
		t.Fatalf("no row means on: %v, %v", on, err)
	}
	admin := uuid.New()
	if err := sw.Set(ctx, false, admin, "incident"); err != nil {
		t.Fatal(err)
	}
	if on, _ := sw.Enabled(ctx); on {
		t.Fatal("this process must see the change at once, not after the cache expires")
	}
	// Another process reads the same row.
	if on, _ := NewSellerPayoutSwitch(db).Enabled(ctx); on {
		t.Fatal("the switch is persisted")
	}
	if err := sw.Set(ctx, true, admin, "all clear"); err != nil {
		t.Fatal(err)
	}
	if on, _ := sw.Enabled(ctx); !on {
		t.Fatal("back on")
	}
	var row models.SellerPayoutSetting
	db.First(&row, "key = ?", models.SellerPayoutSettingKey)
	if row.Note != "all clear" || row.UpdatedBy == nil || *row.UpdatedBy != admin {
		t.Fatalf("row = %+v", row)
	}
	db.Exec(`DROP TABLE seller_payout_settings`)
	sw2 := NewSellerPayoutSwitch(db)
	if _, err := sw2.Enabled(ctx); err == nil {
		t.Fatal("an unreadable switch is an error, never 'on'")
	}
}

// The planner asks LiFi for the real Sonic -> Base route at the amount the
// treasury will hold, offers a payout only when that route fits the machine's
// cap with slack to spare, and charges the buyer what it costs.
func TestPayoutPlan_PricesTheRealBridge(t *testing.T) {
	ctx := context.Background()
	net := big.NewInt(100_000000)

	t.Run("a route inside the cap is priced at its cost plus the slack", func(t *testing.T) {
		p, r, _, uid := newPlanner(t)
		p.WithBridge(r.lifi)
		plan, err := p.Plan(ctx, uid, "USDC", net)
		if err != nil || plan == nil {
			t.Fatalf("plan = %+v, %v", plan, err)
		}
		l, _ := LiFiCostBps(r.lifi.route)
		want := int64(l+TreasuryGasBps(r.lifi.route, treasuryERC20Gas)) + sellerPayoutBridgeSlackBps
		if want < 40 {
			want = 40
		}
		fee, _ := feeFraction("0.5")
		if exp := payoutGrossUp(net, fee, want); plan.GrossUp.Cmp(exp) != 0 {
			t.Fatalf("gross-up = %s; want %s (bridge priced at %d bps)", plan.GrossUp, exp, want)
		}
		held := new(big.Int).Add(net, payoutGrossUp(net, fee, 40))
		if r.lifi.calls != 1 || r.lifi.last.FromAmount != held.String() || r.lifi.last.FromChainID != 146 || r.lifi.last.ToChainID != 8453 {
			t.Fatalf("quoted %d times, last %+v; want one Sonic -> Base quote for %s", r.lifi.calls, r.lifi.last, held)
		}
	})

	for name, setup := range map[string]func(r *payoutRig){
		"a route over the cap (a small order's fixed cost)": func(r *payoutRig) { r.lifi.route.ToAmountUSD = 98 },
		"a route with no slack under the cap":               func(r *payoutRig) { r.lifi.route.ToAmountUSD = 99.1 }, // 90 bps: inside the 100 cap, past the 20 slack
		"a route with no price":                             func(r *payoutRig) { r.lifi.route.FromAmountUSD = 0 },
		"no route":                                          func(r *payoutRig) { r.lifi.err = errors.New("no route") },
	} {
		t.Run(name+" pays the wallet", func(t *testing.T) {
			p, r, _, uid := newPlanner(t)
			p.WithBridge(r.lifi)
			setup(r)
			if plan, err := p.Plan(ctx, uid, "USDC", net); err != nil || plan != nil {
				t.Fatalf("plan = %+v, err = %v; want the wallet", plan, err)
			}
		})
	}
}

// The company route is preferred: the central account pays the naira, so a payout
// is offered with no bridge and no sale, priced at the reference rate, and the
// buyer's gross-up carries the transfer fee and the company's take.
func TestPayoutPlan_PrefersTheCompanyRoute(t *testing.T) {
	ctx := context.Background()
	net := big.NewInt(100_000000)
	const takeBps, feeKobo, maxKobo = 50, 5000, 50_000_000

	setup := func(t *testing.T) (*SellerPayoutPlanner, *payoutRig, *fakeCompanyPayer, *fakeVenueQuoter, uuid.UUID) {
		p, r, q, uid := newPlanner(t)
		fp := newFakeCompanyPayer()
		p.WithCompany(fp, r.db, takeBps, feeKobo, maxKobo)
		return p, r, fp, q, uid
	}

	t.Run("offered at the reference rate with the fee and the take on the buyer", func(t *testing.T) {
		p, _, _, q, uid := setup(t)
		plan, err := p.Plan(ctx, uid, "USDC", net)
		if err != nil || plan == nil || plan.Venue != models.SellerPayoutVenueCompany {
			t.Fatalf("plan = %+v, %v; want the company route", plan, err)
		}
		if plan.FiatTargetKobo != 15_000_000 {
			t.Fatalf("target = %d kobo; want ₦150,000.00", plan.FiatTargetKobo)
		}
		// Treasury must receive R with R(1 - 0.5%) = 100 USDC + ₦50 at 1500 per USDC.
		need := new(big.Rat).Add(big.NewRat(100_000000, 1), big.NewRat(50*1_000_000, 1500))
		gross := new(big.Rat).Quo(need, big.NewRat(9950, 10000))
		want := new(big.Int).Quo(gross.Num(), gross.Denom())
		if new(big.Int).Mul(want, gross.Denom()).Cmp(gross.Num()) != 0 {
			want.Add(want, big.NewInt(1))
		}
		want.Sub(want, net)
		if plan.GrossUp.Cmp(want) != 0 {
			t.Fatalf("gross-up = %s; want %s", plan.GrossUp, want)
		}
		if len(q.asks) != 1 || q.asks[0] != "100000" {
			t.Fatalf("venue asks = %v; the company route needs only the reference rate", q.asks)
		}
	})

	for name, setup2 := range map[string]func(p *SellerPayoutPlanner, r *payoutRig, fp *fakeCompanyPayer){
		"a short float": func(_ *SellerPayoutPlanner, _ *payoutRig, fp *fakeCompanyPayer) {
			fp.balances = []FWBalance{{Currency: "NGN", AvailableBalance: 100_000}}
		},
		"an unreadable float": func(_ *SellerPayoutPlanner, _ *payoutRig, fp *fakeCompanyPayer) {
			fp.balErr = errors.New("503")
		},
		"no NGN balance": func(_ *SellerPayoutPlanner, _ *payoutRig, fp *fakeCompanyPayer) {
			fp.balances = []FWBalance{{Currency: "USD", AvailableBalance: 9e9}}
		},
		"a payout over the route's maximum": func(p *SellerPayoutPlanner, _ *payoutRig, _ *fakeCompanyPayer) {
			p.companyMaxKobo = 10_000_000
		},
		"the route switched off": func(p *SellerPayoutPlanner, _ *payoutRig, _ *fakeCompanyPayer) {
			p.companyMaxKobo = 0
		},
		"payouts already promised the float to": func(_ *SellerPayoutPlanner, r *payoutRig, _ *fakeCompanyPayer) {
			r.db.Model(&models.SellerPayoutOrder{}).Where("id = ?", r.payout.ID).
				Updates(map[string]any{"venue": models.SellerPayoutVenueCompany, "fiat_target_kobo": 99_000_000})
		},
	} {
		t.Run(name+" falls back to Paycrest", func(t *testing.T) {
			p, r, fp, _, uid := setup(t)
			setup2(p, r, fp)
			plan, err := p.Plan(ctx, uid, "USDC", net)
			if err != nil || plan == nil || plan.Venue == models.SellerPayoutVenueCompany {
				t.Fatalf("plan = %+v, %v; want the Paycrest route", plan, err)
			}
		})
	}

	t.Run("a payout under the provider's floor is not a company payout", func(t *testing.T) {
		p, _, _, _, uid := setup(t)
		plan, _ := p.Plan(ctx, uid, "USDC", big.NewInt(50_000)) // 0.05 USDC = ₦75
		if plan != nil && plan.Venue == models.SellerPayoutVenueCompany {
			t.Fatalf("plan = %+v; ₦75 is under the transfer floor", plan)
		}
	})

	t.Run("without the company route nothing changes", func(t *testing.T) {
		p, _, _, uid := newPlanner(t)
		plan, err := p.Plan(ctx, uid, "USDC", net)
		if err != nil || plan == nil || plan.Venue != "" {
			t.Fatalf("plan = %+v, %v; want the old Paycrest plan", plan, err)
		}
	})

	t.Run("the venue is stored with the payout", func(t *testing.T) {
		p, r, _, _, uid := setup(t)
		plan, _ := p.Plan(ctx, uid, "USDC", net)
		if plan == nil {
			t.Fatal("no plan")
		}
		order := &models.Order{ID: uuid.New(), SellerID: uid, Token: "USDC"}
		if err := p.Persist(r.db, order, sponsorTestUser, plan, net); err != nil {
			t.Fatal(err)
		}
		var row models.SellerPayoutOrder
		if err := r.db.First(&row, "marketplace_order_id = ?", order.ID).Error; err != nil || row.Venue != models.SellerPayoutVenueCompany {
			t.Fatalf("row = %+v, %v; want venue company", row, err)
		}
	})
}
