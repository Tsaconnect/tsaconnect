package services

import (
	"context"
	"math/big"
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
func TestPoolClaimVsExpiry_Race_Postgres(t *testing.T) {
	for round := 0; round < 20; round++ {
		r := newPoolOpenRigPG(t)
		r.addLP(t, 50)
		r.it.SetTreasuryMarketRate(&fixedTreasuryRate{rate: poolMid})
		p := r.price(t, "5000")
		intent := poolIntentFor(p)
		var slices []models.P2PTrade
		if err := r.desk.insertOrder(context.Background(), intent, p, nil, &slices); err != nil {
			t.Fatal(err)
		}
		expired := time.Now().Add(-40 * time.Minute)
		r.db.Model(intent).Updates(map[string]any{"status": models.ITStatusPendingPayment, "rate_expires_at": expired})
		r.db.Model(&models.P2PTrade{}).Where("it_intent_id = ?", intent.ID).Update("opened_at", time.Now())
		intent.Status, intent.RateExpiresAt = models.ITStatusPendingPayment, &expired
		var wg sync.WaitGroup
		decided := make([]bool, len(slices))
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = r.it.claimAndPriceStableBuy(context.Background(), intent, "sess_race", intent.AmountMinor, time.Now(), false, PayInCentral)
		}()
		go func() {
			defer wg.Done()
			for i := range slices {
				decided[i], _ = r.it.decideSliceCancel(context.Background(), &slices[i], time.Now())
			}
		}()
		wg.Wait()
		final := r.intent(t, intent.ID)
		for i, d := range decided {
			if d && final.Status == models.ITStatusPaymentReceived {
				t.Fatalf("round %d: order paid but slice %s was decided cancellable", round, slices[i].ID)
			}
		}
	}
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
			decided, _ = r.src.trades.decideLegacyCancel(ctx, r.trade, now)
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
