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

// Restored from pool_expiry_postgres_test.go (deleted with the slice pool in
// 4cf9bb39): the legacy-scope races still guard today's ordinary P2P canceller.

// Money rule (legacy scope): a central-account credit and the canceller's
// decision interleave on the trade's row lock. Either the credit claims the
// trade and the canceller refuses, or the canceller decides and no credit lands.
func TestLegacyClaimVsExpiry_Race_Postgres(t *testing.T) {
	cancels := 0
	for round := 0; round < 20; round++ {
		r := restoredPMLegacyPG(t)
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
// arrives while a decision is in flight waits for it and then refuses.
func TestLegacyCreditWaitsForAnInFlightDecision_Postgres(t *testing.T) {
	r := restoredPMLegacyPG(t)
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

// restoredPMLegacyPG is a merchant buy trade settled through the central
// account, over a temp Postgres, 31 minutes past its expiry notice and
// Pending on-chain past its window.
func restoredPMLegacyPG(t *testing.T) *fwdRig {
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
