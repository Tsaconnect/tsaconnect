package services

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// poolRig is a trade service over Sonic (146, USDT 6 dp) with a PM service:
// the ordinary P2P rig the expiry and merge-review tests share.
type poolRig struct {
	db    *gorm.DB
	svc   *P2PTradeService
	sonic *mockEscrow
	bus   *mockBus
}

func newPoolRig(t *testing.T) *poolRig {
	t.Helper()
	db := setupTradeDB(t)
	createPaymentMerchantsTable(t, db)
	sonic := newMockEscrow()
	reg := NewP2PEscrowRegistryFromEntries(map[int64]P2PRegistryEntry{
		146: {Escrow: sonic, ChainKey: "sonic", Purpose: config.P2PPurposeP2P, Assets: []string{"USDT"}, Decimals: map[string]int{"USDT": 6}},
	}, "2")
	bus := &mockBus{}
	svc := NewP2PTradeService(db, reg, &mockRates{}, bus, 146).
		WithClock(fixedClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)})
	svc.SetTokenResolver(func(int64, string) string { return "0x29219dd400f2Bf60E5a23d13Be72B486D4038894" })
	svc.SetPMService(NewPMService(db))
	return &poolRig{db: db, svc: svc, sonic: sonic, bus: bus}
}

// seedActiveAd inserts an active sell ad directly.
func seedActiveAd(t *testing.T, db *gorm.DB, sellerID uuid.UUID, chainID int64, pmID *uuid.UUID, rate, remaining string) *models.P2PAd {
	t.Helper()
	ad := &models.P2PAd{
		ID: uuid.New(), SellerID: sellerID, Side: models.P2PAdSideSell, Token: "USDT", ChainID: chainID,
		Amount: remaining, Remaining: remaining, Reserved: "0", FiatCurrency: "NGN", Rate: rate,
		RateType: models.P2PAdRateTypeFixed, FloatAnchor: models.P2PAdFloatAnchorBybitSell,
		PaymentMethodIDs: stringifyUUIDs(nil), Status: models.P2PAdStatusActive, PMID: pmID,
		VoucherSignature: "0xdeadbeef",
	}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("seed ad: %v", err)
	}
	return ad
}

func releaseSender() *chainCountingSender {
	return &chainCountingSender{fakeReleaseSigner: fakeReleaseSigner{addr: "0x00000000000000000000000000000000000000a1",
		hash: "0x" + strings.Repeat("e1", 32)}}
}
