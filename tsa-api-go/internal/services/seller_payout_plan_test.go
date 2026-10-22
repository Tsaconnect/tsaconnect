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
