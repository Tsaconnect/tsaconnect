package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// secondRole gives userID a row of kind, created after (newer than) the
// user's first row, which is the order that broke "the newest row" reads.
func secondRole(t *testing.T, db *gorm.DB, userID uuid.UUID, kind, status, wallet string) *models.PaymentMerchant {
	t.Helper()
	pm := seedPMRow(t, db, kind, status, wallet)
	if err := db.Model(pm).Updates(map[string]any{"user_id": userID, "created_at": time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	pm.UserID = userID
	return pm
}

func TestGetByUserIDKind_ReturnsEachRoleOnly(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	m := secondRole(t, r.db, lp.UserID, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusPaused, "0x00000000000000000000000000000000000000a1")
	svc := NewPMService(r.db)
	if got, err := svc.GetByUserIDKind(ctx, lp.UserID, models.PaymentMerchantKindLP); err != nil || got.ID != lp.ID {
		t.Fatalf("lp = %v %v", got, err)
	}
	if got, err := svc.GetByUserIDKind(ctx, lp.UserID, models.PaymentMerchantKindMerchant); err != nil || got.ID != m.ID {
		t.Fatalf("merchant = %v %v", got, err)
	}
	if _, err := svc.GetByUserIDKind(ctx, uuid.New(), models.PaymentMerchantKindLP); err != ErrPMNotFound {
		t.Fatalf("stranger = %v, want ErrPMNotFound", err)
	}
}

func TestGetRolesByUserID_SplitsTheRowsByKind(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	m := secondRole(t, r.db, lp.UserID, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusPaused, "0x00000000000000000000000000000000000000a1")
	svc := NewPMService(r.db)
	if gotM, gotLP, err := svc.GetRolesByUserID(ctx, lp.UserID); err != nil || gotM == nil || gotM.ID != m.ID || gotLP == nil || gotLP.ID != lp.ID {
		t.Fatalf("roles = %v %v %v", gotM, gotLP, err)
	}
	if gotM, gotLP, err := svc.GetRolesByUserID(ctx, uuid.New()); err != nil || gotM != nil || gotLP != nil {
		t.Fatalf("stranger = %v %v %v, want none", gotM, gotLP, err)
	}
}

func TestChainsFor_DualUserWithNewerMerchantRowStillGetsPoolChain(t *testing.T) {
	r := newPoolRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	secondRole(t, r.db, lp.UserID, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000a1")
	for _, c := range r.svc.ChainsFor(context.Background(), lp.UserID) {
		if c.ChainID == poolTestChain {
			return
		}
	}
	t.Fatal("a dual user lost the pool chain to the newer merchant row")
}

func TestLiquidityMe_DualUserReadsTheLPRow(t *testing.T) {
	r, svc := newLiquidityRig(t)
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusApproved, lpWallet)
	secondRole(t, r.db, lp.UserID, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000a1")
	me, err := svc.Me(context.Background(), lp.UserID)
	if err != nil || me.Status != LiquidityStatusApproved || me.Wallet != lpWallet {
		t.Fatalf("me = %+v err = %v: a merchant row hid the LP", me, err)
	}
	if _, err := svc.Activity(context.Background(), lp.UserID, "payouts", "", 20); err != nil {
		t.Fatalf("activity for a dual user: %v", err)
	}
}

func TestDualRole_PausingLPLeavesMerchantAndItsAdsLive(t *testing.T) {
	r := newPoolRig(t)
	ctx := context.Background()
	lp := seedPMRow(t, r.db, models.PaymentMerchantKindLP, models.PaymentMerchantStatusActive, lpWallet)
	m := secondRole(t, r.db, lp.UserID, models.PaymentMerchantKindMerchant, models.PaymentMerchantStatusActive, "0x00000000000000000000000000000000000000a1")
	stockAd := seedActiveAd(t, r.db, lp.UserID, poolTestChain, &lp.ID, "1000", usdt18(500000).String())
	merchantAd := seedActiveAd(t, r.db, lp.UserID, 146, &m.ID, "1500", "100000000")
	if _, err := NewPMService(r.db).PausePM(ctx, lp.ID, uuid.New()); err != nil {
		t.Fatal(err)
	}
	var got models.PaymentMerchant
	r.db.First(&got, "id = ?", m.ID)
	if got.Status != models.PaymentMerchantStatusActive {
		t.Fatalf("merchant status = %s after pausing the LP", got.Status)
	}
	ads, err := r.svc.ListAds(ctx, AdFilter{Status: models.P2PAdStatusActive, ExcludeDelisted: true})
	if err != nil {
		t.Fatal(err)
	}
	if ids := adIDs(ads); !ids[merchantAd.ID] || ids[stockAd.ID] {
		t.Fatalf("browse = %v: want the merchant's Sonic ad and never the LP stock ad", ids)
	}
}
