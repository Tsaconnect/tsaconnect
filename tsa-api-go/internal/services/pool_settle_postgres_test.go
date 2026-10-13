package services

import (
	"context"
	"math/big"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// §24.3: two ticks forwarding the same proven slice at once — one forward
// row, one ledger credit, one transfer.
func TestPoolForward_ConcurrentReplay_Postgres(t *testing.T) {
	r := newPoolOpenRigPG(t)
	lp, ad := r.addLP(t, 50)
	r.db.Model(&models.PaymentMerchant{}).Where("id = ?", lp.ID).Updates(map[string]any{
		"settlement_bank_code": "058", "settlement_account_number": "0123456789", "settlement_account_name": "LP"})
	if err := r.db.AutoMigrate(&models.P2PSettlementForward{}, &models.PMBalance{}); err != nil {
		t.Fatal(err)
	}
	intentID := uuid.New()
	units := canonical(3)
	if err := r.db.Create(&models.ITIntent{ID: intentID, UserID: uuid.New(), Direction: models.ITDirectionBuy, Asset: "USDT", Currency: "NGN",
		Provider: models.ITProviderPool, Status: models.ITStatusDelivering, AmountMinor: "500000", ReceivedAmount: "500000",
		AssetAmount: units.String(), LockedRate: "135474", DisplayRate: "136151", NgnKobo: "500000", RateKobo: "1", RateMinor: "1",
		McgpAmount: "0", FeeNgnKobo: "0", NetNgnKobo: "500000", PlatformKeptKobo: "0"}).Error; err != nil {
		t.Fatal(err)
	}
	seedPoolOrder(t, r.db, intentID, 1, units)
	chain, _ := CanonicalToChainUnits(units, 18)
	sl := seedPoolSlice(t, r.db, sliceSeed{Intent: intentID, Ad: ad, Units: chain, Status: models.P2PTradeStatusReleased, Opened: true})
	r.db.Model(sl).Update("fiat_amount", FormatMinor(PoolLPPayoutKobo(units, big135474()).String()))
	gw := &fakeNombaGateway{payoutID: "trf_pg"}
	fwd := NewP2PSettlementForwarder(r.db, NewPMService(r.db), gw, nil, "TSA Connect")
	src := NewP2PTradeClaimSource(r.db, NewPMService(r.db), r.trades, nil)
	src.SetForwarder(fwd)
	r.it.SetPoolForwards(src)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	ids := map[uuid.UUID]bool{}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var cur models.P2PTrade
			r.db.First(&cur, "id = ?", sl.ID)
			f, err := r.it.forwardSlice(context.Background(), r.desk, &cur, poolForwardRef(cur.ID), false)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			} else {
				ids[f.ID] = true
			}
		}()
	}
	wg.Wait()
	// Every replay reads back the one winner (the unique-violation branch
	// works on Postgres, where a failed INSERT aborts the transaction).
	if len(errs) != 0 || len(ids) != 1 {
		t.Fatalf("errs=%v forwards returned=%d, want every caller to get the one forward", errs, len(ids))
	}
	var n int64
	r.db.Model(&models.P2PSettlementForward{}).Count(&n)
	var got models.PaymentMerchant
	r.db.First(&got, "id = ?", lp.ID)
	if n != 1 || !ngnEqual(got.AvailableNGN, FormatMinor(PoolLPPayoutKobo(units, big135474()).String())) || len(gw.calls) > 1 {
		t.Fatalf("forwards=%d available=%s calls=%d, want one of each", n, got.AvailableNGN, len(gw.calls))
	}
}

func big135474() *big.Int { return big.NewInt(135474) } // §10.2 L, kobo per token

// newPoolSettleRigPG is the settle rig over a temp Postgres: every table the
// open, the settle engine, the forwarder and the books touch.
func newPoolSettleRigPG(t *testing.T, freeWhole ...int64) *poolSettleRig {
	t.Helper()
	db := testutil.TempPostgres(t, "liq_c2_settle_", &models.User{}, &models.PaymentMerchant{}, &models.PMBalance{}, &models.P2PAd{},
		&models.P2PTrade{}, &models.P2PAuditLog{}, &models.ITIntent{}, &models.ITIntentAuditLog{}, &models.PoolOrder{},
		&models.PaycrestOrderRecord{}, &models.FiatVirtualAccount{}, &models.WalletAddress{}, &models.InboundCredit{},
		&models.JobRunMarker{})
	if err := config.MigratePoolSliceIndexes(db); err != nil {
		t.Fatal(err)
	}
	if err := config.MigrateOpenStableBuyIndex(db); err != nil {
		t.Fatal(err)
	}
	base := buildPoolOpenRig(t, db)
	u := &models.User{ID: uuid.New(), Name: "Ada Okafor", Username: "ada_" + uuid.NewString()[:8], Email: uuid.NewString() + "@test.com",
		Password: "x", WalletAddress: "0x000000000000000000000000000000000000dEaD",
		VerificationStatus: models.VerificationStatusVerified, VerifiedLegalName: "Ada Okafor"}
	if err := db.Create(u).Error; err != nil {
		t.Fatal(err)
	}
	base.userID = u.ID
	return poolSettleRigOn(t, base, freeWhole...)
}

// lockedGateway / lockedBus make the forwarder's fakes safe to share between
// concurrent settle passes.
type lockedGateway struct {
	mu    sync.Mutex
	calls []PayoutRequest
}

func (g *lockedGateway) InitiatePayout(_ context.Context, req PayoutRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, req)
	return "trf_pg_" + req.Reference, nil
}

type lockedBus struct {
	mu        sync.Mutex
	published []events.Event
}

func (b *lockedBus) Publish(ev events.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.published = append(b.published, ev)
}

// Task 7 left this out: two settle passes racing on one paid order produce
// exactly one delivery to her and one forward (one credit, one transfer) per
// LP, and the books settle once.
func TestPoolSettle_ConcurrentPassesDeliverAndForwardOnce_Postgres(t *testing.T) {
	r := newPoolSettleRigPG(t, 60, 45)
	gw := &lockedGateway{}
	fwd := NewP2PSettlementForwarder(r.db, NewPMService(r.db), gw, nil, "TSA Connect")
	src := NewP2PTradeClaimSource(r.db, NewPMService(r.db), r.trades, r.sender)
	src.SetForwarder(fwd)
	r.it.SetPoolForwards(src)
	bus := &lockedBus{}
	r.it.rateNotifier = bus
	intent := r.openPaid(t, "100000")

	pass := func() {
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_ = r.it.AdvancePoolBuy(context.Background(), intent.ID)
			}()
		}
		close(start)
		wg.Wait()
	}
	for i := 0; i < 3; i++ {
		pass()
	}
	legs := r.legs(t, intent.ID)
	if len(legs) != 1 || r.saleSigner.broadcasts() != 1 {
		t.Fatalf("sale legs=%d broadcasts=%d, want one delivery", len(legs), r.saleSigner.broadcasts())
	}
	want, _ := new(big.Int).SetString(legs[0].Amount, 10)
	r.saleVerifier.err, r.saleVerifier.credited = nil, want
	r.db.Model(&models.RampLeg{}).Where("id = ?", legs[0].ID).Update("tx_hash", "0xsale")
	for i := 0; i < 2; i++ {
		pass()
	}

	if got := r.intent(t, intent.ID); got.Status != models.ITStatusSettled || r.saleSigner.broadcasts() != 1 {
		t.Fatalf("status=%s broadcasts=%d, want settled on one delivery", got.Status, r.saleSigner.broadcasts())
	}
	var fws []models.P2PSettlementForward
	r.db.Find(&fws)
	perLP := map[uuid.UUID]int{}
	for _, f := range fws {
		perLP[f.PMID]++
	}
	gw.mu.Lock()
	calls := len(gw.calls)
	gw.mu.Unlock()
	if len(fws) != 2 || perLP[r.lps[0].ID] != 1 || perLP[r.lps[1].ID] != 1 || calls != 2 {
		t.Fatalf("forwards=%d perLP=%v transfers=%d, want one forward and one transfer per LP", len(fws), perLP, calls)
	}
	for _, f := range fws {
		var lp models.PaymentMerchant
		r.db.First(&lp, "id = ?", f.PMID)
		if !ngnEqual(lp.AvailableNGN, f.Amount) {
			t.Fatalf("LP %s available=%s, want credited once with %s", f.PMID, lp.AvailableNGN, f.Amount)
		}
	}
	fills := 0
	bus.mu.Lock()
	for _, ev := range bus.published {
		if ev.Type == events.LiquidityFill {
			fills++
		}
	}
	bus.mu.Unlock()
	if b := r.book(t, intent.ID); fills != 2 || b.SettledAt == nil || b.LPTotalKobo == nil || *b.LPTotalKobo != "9793465" {
		t.Fatalf("fills=%d books=%+v, want two fills and the order booked once (LP total ₦97,934.65)", fills, b)
	}
}
