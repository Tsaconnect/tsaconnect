package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

func seedAdBy(t *testing.T, db *gorm.DB, seller uuid.UUID, side string) uuid.UUID {
	id := uuid.New()
	if err := db.Create(&models.P2PAd{ID: id, SellerID: seller, Side: side, Token: "USDT", ChainID: 146, Amount: "100000000",
		Remaining: "100000000", Reserved: "0", FiatCurrency: "NGN", Rate: "1300", RateType: "fixed", FloatAnchor: "bybit_sell",
		Status: models.P2PAdStatusActive, SellerAddress: "0x000000000000000000000000000000000000d00d",
		PaymentMethodIDs: stringifyUUIDs(nil)}).Error; err != nil {
		t.Fatal(err)
	}
	return id
}

func TestListAds_HidesSuspendedSellers(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	active := seedVerifiedUser(t, db)
	suspended := seedVerifiedUser(t, db)
	db.Exec(`UPDATE users SET account_status = 'suspended' WHERE id = ?`, suspended)
	seedAdBy(t, db, active, "sell")
	seedAdBy(t, db, suspended, "sell")
	rows, err := svc.ListAds(context.Background(), AdFilter{Status: models.P2PAdStatusActive, ExcludeSuspended: true})
	if err != nil || len(rows) != 1 || rows[0].SellerID != active {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	own, _ := svc.ListAds(context.Background(), AdFilter{SellerID: suspended, ViewerID: suspended})
	if len(own) != 1 {
		t.Fatal("own list unaffected")
	}
}

func TestOpenTrade_RefusesSuspendedSellersAd(t *testing.T) {
	db := setupTradeDB(t)
	svc, _, _ := newService(t, db)
	merchant := seedVerifiedUser(t, db)
	db.Exec(`UPDATE users SET account_status = 'suspended' WHERE id = ?`, merchant)
	adID := seedAdBy(t, db, merchant, "buy")
	opener := seedVerifiedUser(t, db)
	pm := seedPaymentMethod(t, db, opener, "NGN")
	_, _, err := svc.OpenTrade(context.Background(), adID, opener, "0x000000000000000000000000000000000000beef",
		OpenTradeParams{FiatAmount: "1300", PaymentMethodID: pm})
	if !errors.Is(err, ErrAdNotActive) {
		t.Fatalf("err = %v", err)
	}
}
