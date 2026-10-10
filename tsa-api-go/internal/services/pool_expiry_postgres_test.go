package services

import (
	"context"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
	"gorm.io/gorm/clause"
)

// Roadmap risk 6: a late claim and the canceller's decision interleave on the
// intent lock; a paid order never has a slice the canceller decided to cancel.
func TestPoolClaimVsExpiry_Race_Postgres(t *testing.T) { poolClaimVsExpiryRace(t, false) }

// Review C-2: the honoured claim takes the same lock and coverage check, so
// the canceller's decision and an exact, on-time payment cannot both win.
func TestPoolHonouredClaimVsExpiry_Race_Postgres(t *testing.T) { poolClaimVsExpiryRace(t, true) }

// poolClaimVsExpiryRace races the claim against the canceller over 20 rounds.
// Every round one side must win (review M-5: never vacuous): the claim pays
// the order on slices nobody decided, or the canceller's decision makes it
// park pool_slices_short. Both outcomes are counted.
func poolClaimVsExpiryRace(t *testing.T, honoured bool) {
	paidRounds, parkedRounds := 0, 0
	for round := 0; round < 20; round++ {
		r := newPoolOpenRigPG(t)
		r.addLP(t, 50)
		r.addLP(t, 50)
		r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
		p := r.price(t, "5000")
		intent := poolIntentFor(p)
		var slices []models.P2PTrade
		if err := r.desk.insertOrder(context.Background(), intent, p, nil, &slices); err != nil {
			t.Fatal(err)
		}
		expires := time.Now().Add(-40 * time.Minute) // late: priced live
		decideAt := time.Now()
		if honoured {
			expires = time.Now().Add(10 * time.Minute) // on time: honoured
			decideAt = expires.Add(r.it.rampTerms().honourGrace + expiryPendingMargin + time.Minute)
		}
		r.db.Model(intent).Updates(map[string]any{"status": models.ITStatusPendingPayment, "rate_expires_at": expires})
		r.db.Model(&models.P2PTrade{}).Where("it_intent_id = ?", intent.ID).Update("opened_at", time.Now())
		intent.Status, intent.RateExpiresAt = models.ITStatusPendingPayment, &expires
		var wg sync.WaitGroup
		decided := make([]bool, len(slices))
		var claimed bool
		var claimErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			claimed, claimErr = r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_race", intent.AmountMinor, time.Now(), false, PayInCentral)
		}()
		go func() {
			defer wg.Done()
			<-start
			for t0 := time.Now(); time.Since(t0) < time.Duration(round)*50*time.Microsecond; {
			} // staggered so both orders occur
			for i := range slices {
				decided[i], _ = r.it.decideSliceCancel(context.Background(), &slices[i], decideAt)
			}
		}()
		close(start)
		wg.Wait()
		if !claimed || claimErr != nil {
			t.Fatalf("round %d: the claim did not run: %v %v", round, claimed, claimErr)
		}
		final := r.intent(t, intent.ID)
		switch {
		case final.Status == models.ITStatusPaymentReceived:
			for i, d := range decided {
				if d {
					t.Fatalf("round %d: order paid but slice %s was decided cancellable", round, slices[i].ID)
				}
			}
			if honoured && final.Repriced {
				t.Fatalf("round %d: an honoured claim was re-priced", round)
			}
			paidRounds++
		case final.Status == models.ITStatusBuyAwaitingRate && strings.HasPrefix(final.FailureReason, models.ITFailurePoolSlicesShort):
			parkedRounds++
		default:
			t.Fatalf("round %d: neither side won: %s %q", round, final.Status, final.FailureReason)
		}
	}
	if paidRounds == 0 {
		t.Fatalf("the claim never won a round (parked %d): the race proves nothing", parkedRounds)
	}
	t.Logf("claim paid %d of 20 rounds, the canceller's decision parked %d", paidRounds, parkedRounds)
}

// Money rule (legacy scope): a central-account credit and the canceller's
// decision interleave on the trade's row lock. Either the credit claims the
// trade and the canceller refuses, or the canceller decides and no credit lands.
func TestLegacyClaimVsExpiry_Race_Postgres(t *testing.T) {
	cancels := 0
	for round := 0; round < 20; round++ {
		r := pmLegacyPG(t)
		ctx := context.Background()
		now := time.Now()
		var wg sync.WaitGroup
		var acceptErr error
		var decided bool
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			acceptErr = r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("99900000"+uuid.NewString()[:4], "1500.00"))
		}()
		go func() {
			defer wg.Done()
			<-start
			// Staggered so both orders occur: Accept reads the merchant and its
			// gates before it reaches the credit's lock.
			for t0 := time.Now(); time.Since(t0) < time.Duration(round)*10*time.Microsecond; {
			} // a spin: time.Sleep cannot wait less than about a millisecond
			decided, _, _ = r.src.trades.decideLegacyCancelLeg(ctx, r.trade, now)
		}()
		close(start)
		wg.Wait()
		var fwd int64
		r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Count(&fwd)
		var got models.P2PTrade
		r.db.First(&got, "id = ?", r.trade.ID)
		if decided && (fwd != 0 || acceptErr == nil || got.Status != models.P2PTradeStatusPendingPayment) {
			t.Fatalf("round %d: cancel decided but the credit landed (forwards=%d accept=%v status=%s)", round, fwd, acceptErr, got.Status)
		}
		if !decided && acceptErr != nil {
			t.Fatalf("round %d: neither side won: accept=%v", round, acceptErr)
		}
		if decided {
			cancels++
		}
	}
	t.Logf("canceller won %d of 20 rounds, the credit %d", cancels, 20-cancels)
}

// The credit takes the row lock the canceller decides under, so a credit that
// arrives while a decision is in flight waits for it and then refuses (the
// race test above rarely lands inside that window; this pins it).
func TestLegacyCreditWaitsForAnInFlightDecision_Postgres(t *testing.T) {
	r := pmLegacyPG(t)
	ctx := context.Background()
	tx := r.db.Begin() // what decideLegacyCancel does, held open
	var cur models.P2PTrade
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, "id = ?", r.trade.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := tx.Model(&models.P2PTrade{}).Where("id = ?", cur.ID).Update("dropped_at", time.Now()).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.src.Accept(ctx, r.trade.ID, p2pInboundCredit("999000000099", "1500.00")) }()
	select {
	case err := <-done:
		tx.Rollback()
		t.Fatalf("the credit did not wait for the in-flight decision: %v", err)
	case <-time.After(500 * time.Millisecond):
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	err := <-done
	var fwd int64
	r.db.Model(&models.P2PSettlementForward{}).Where("trade_id = ?", r.trade.ID).Count(&fwd)
	if err == nil || fwd != 0 {
		t.Fatalf("accept=%v forwards=%d, want the credit refused after the decision", err, fwd)
	}
}

// pmLegacyPG is pmLegacy over a temp Postgres.
func pmLegacyPG(t *testing.T) *fwdRig {
	t.Helper()
	db := testutil.TempPostgres(t, "liq_c1_legacy_", &models.PaymentMerchant{}, &models.PMBalance{}, &models.P2PAd{},
		&models.P2PTrade{}, &models.P2PAuditLog{}, &models.P2PSettlementForward{}, &models.P2PMessage{})
	trades, esc, _ := newService(t, db)
	pm := seedActivePM(t, db, "1234567890", 0)
	ad := seedPMSellAd(t, db, pm.ID, uuid.New())
	trade := seedBuyTradeOnAd(t, db, ad, uuid.New(), "1500.00")
	setReleasedEvent(esc, trade)
	notified := time.Now().Add(-31 * time.Minute)
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("expired_notified_at", notified)
	trade.ExpiredNotifiedAt = &notified
	esc.onChainTrade = &OnChainP2PTrade{Exists: true, Status: OnChainP2PStatusPending,
		CreatedAt: big.NewInt(time.Now().Add(-2 * time.Hour).Unix())}
	signer := &fakeReleaseSigner{addr: "0x1111111111111111111111111111111111111111", hash: "0xrelease"}
	src := NewP2PTradeClaimSource(db, NewPMService(db), trades, signer)
	return &fwdRig{db: db, pm: pm, trade: trade, src: src, signer: signer, esc: esc}
}

// Review C-1: the payout commits its started marker under the row lock the
// canceller decides under before the gateway is asked, so a decision taken
// while the money moves sees it — never a refund after the naira left.
func TestLegacyCancelWaitsForAnInFlightPayout_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "liq_c1_payout_", &models.PaymentMerchant{}, &models.PMBalance{}, &models.P2PAd{},
		&models.P2PTrade{}, &models.P2PAuditLog{}, &models.P2PSettlementForward{})
	svc, _, _ := newService(t, db)
	trade := seedSellTradeOnAd(t, db, uuid.New(), uuid.New(), "1500.00")
	notified := time.Now().Add(-31 * time.Minute)
	db.Model(&models.P2PTrade{}).Where("id = ?", trade.ID).Update("expired_notified_at", notified)
	trade.ExpiredNotifiedAt = &notified
	decided := make(chan bool, 1)
	gw := &fakeNombaGateway{payoutID: "po_race"}
	gw.onCall = func() {
		go func() {
			ok, _, err := svc.decideLegacyCancelLeg(context.Background(), trade, time.Now())
			if err != nil {
				t.Errorf("decide: %v", err)
			}
			decided <- ok
		}()
		time.Sleep(300 * time.Millisecond) // the decision is taken while the money moves
	}
	auto, _ := newPMAutomationServiceWithGateway(t, db, svc, gw, nameMap(nil))
	if _, err := auto.InitiateSellPayout(context.Background(), trade.ID); err != nil {
		t.Fatal(err)
	}
	var got models.P2PTrade
	if <-decided {
		db.First(&got, "id = ?", trade.ID)
		t.Fatalf("the canceller decided a trade whose payout was in flight (dropped=%v payout=%q)", got.DroppedAt, got.NombaPayoutID)
	}
}
