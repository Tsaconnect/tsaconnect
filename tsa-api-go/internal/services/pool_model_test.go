package services

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// sliceSeed describes one pool slice (a p2p_trades row with it_intent_id).
// Units are CHAIN units (18 dp on the pool chain), as every trade stores them.
type sliceSeed struct {
	Intent    uuid.UUID
	Ad        *models.P2PAd
	Units     *big.Int
	Status    string // default pending_payment
	Gen       int    // default 1
	Opened    bool   // opened_at = CreatedAt (proven on-chain)
	Dropped   bool   // dropped_at = now
	Nonce     *int64
	CreatedAt time.Time // default now
	BuyerID   uuid.UUID // the company PM user (S10); default a fresh id
}

// seedPoolSlice writes a slice directly (no chain): the shared fixture every
// later C1 task reads.
func seedPoolSlice(t *testing.T, db *gorm.DB, s sliceSeed) *models.P2PTrade {
	t.Helper()
	if s.Status == "" {
		s.Status = models.P2PTradeStatusPendingPayment
	}
	if s.Gen == 0 {
		s.Gen = 1
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	if s.BuyerID == uuid.Nil {
		s.BuyerID = uuid.New()
	}
	intent := s.Intent
	tr := &models.P2PTrade{
		ID: uuid.New(), AdID: s.Ad.ID, BuyerID: s.BuyerID, SellerID: s.Ad.SellerID,
		Token: s.Ad.Token, ChainID: s.Ad.ChainID, Funding: models.P2PTradeFundingPooled,
		// A realistic LP payout (re-review N1): a zero amount once hid a slice
		// cancel that released an NGN reservation never taken.
		TokenAmount: s.Units.String(), FiatAmount: "4877.06", FiatCurrency: "NGN", Rate: "1354.74",
		PaymentMethodID: uuid.Nil, Status: s.Status, ExpiresAt: s.CreatedAt.Add(60 * time.Minute),
		ITIntentID: &intent, Generation: s.Gen, OpenNonce: s.Nonce,
		CreatedAt: s.CreatedAt, UpdatedAt: s.CreatedAt,
	}
	tr.OnChainTradeID = HashIDHex(tr.ID)
	if s.Opened {
		at := s.CreatedAt
		tr.OpenedAt = &at
	}
	if s.Dropped {
		now := time.Now().UTC()
		tr.DroppedAt = &now
	}
	if err := db.Create(tr).Error; err != nil {
		t.Fatalf("seed slice: %v", err)
	}
	return tr
}

// seedPoolOrder writes one pool_orders snapshot at the §10.2 worked example's
// rates (M ₦1,348.00, spread and take 50 bps, central funding 140 bps).
func seedPoolOrder(t *testing.T, db *gorm.DB, intentID uuid.UUID, gen int, units *big.Int) *models.PoolOrder {
	t.Helper()
	o := &models.PoolOrder{
		IntentID: intentID, Generation: gen, ChainID: poolTestChain, Token: "USDT", TokenDecimals: 18,
		MidMinor: "134800", PoolSpreadBps: 50, PoolTakeBps: 50, FundingBps: 140, BandBps: 50,
		LPRateMinor: "135474", DisplayRateMinor: "136151", SliceCount: 1, OpenedUnits: units.String(),
		SurplusUnits: "0",
	}
	if err := db.Create(o).Error; err != nil {
		t.Fatalf("seed pool order: %v", err)
	}
	return o
}

func TestPoolSliceColumns_DefaultToAnOrdinaryTrade(t *testing.T) {
	db := setupTradeDB(t)
	ad := seedActiveAd(t, db, uuid.New(), poolTestChain, nil, "0", "1000")
	plain := &models.P2PTrade{ID: uuid.New(), AdID: ad.ID, BuyerID: uuid.New(), SellerID: ad.SellerID,
		Token: "USDT", ChainID: 146, TokenAmount: "1", FiatAmount: "1", FiatCurrency: "NGN", Rate: "1",
		Status: models.P2PTradeStatusPendingPayment, ExpiresAt: time.Now().Add(time.Hour)}
	if err := db.Create(plain).Error; err != nil {
		t.Fatalf("create ordinary trade: %v", err)
	}
	var got models.P2PTrade
	db.First(&got, "id = ?", plain.ID)
	if got.IsPoolSlice() || got.Generation != 1 || got.OpenedAt != nil || got.OpenNonce != nil || got.DroppedAt != nil {
		t.Fatalf("ordinary trade = %+v, want no slice fields and generation 1", got)
	}
	nonce := int64(41)
	sl := seedPoolSlice(t, db, sliceSeed{Intent: uuid.New(), Ad: ad, Units: usdt18(3), Opened: true, Nonce: &nonce})
	var slice models.P2PTrade // fresh: First on a struct with a primary key also filters by it
	db.First(&slice, "id = ?", sl.ID)
	if !slice.IsPoolSlice() || slice.OpenedAt == nil || slice.OpenNonce == nil || *slice.OpenNonce != 41 || slice.Generation != 1 {
		t.Fatalf("slice = %+v", slice)
	}
}

func TestPoolOrder_OneRowPerGeneration(t *testing.T) {
	db := setupTradeDB(t)
	if err := db.AutoMigrate(&models.PoolOrder{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	id := uuid.New()
	seedPoolOrder(t, db, id, 1, big.NewInt(72_419_593))
	seedPoolOrder(t, db, id, 2, big.NewInt(1_000_000))
	dup := &models.PoolOrder{IntentID: id, Generation: 1, ChainID: poolTestChain, Token: "USDT", TokenDecimals: 18,
		MidMinor: "1", LPRateMinor: "1", DisplayRateMinor: "1", OpenedUnits: "1", SurplusUnits: "0"}
	if err := db.Create(dup).Error; err == nil {
		t.Fatal("a second generation-1 snapshot was accepted; snapshots are never overwritten (S41)")
	}
}

// S11: one open buy per user covers `opening` too.
func TestPoolOpen_OneOpenBuyIncludesOpening(t *testing.T) {
	s, userID := newRoutedBuyService(t)
	opening := seedProviderIntent(t, s, userID, models.ITProviderPool, models.ITStatusOpening)
	got, err := s.findOpenStableBuy(context.Background(), userID)
	if err != nil || got == nil || got.ID != opening.ID {
		t.Fatalf("findOpenStableBuy = %v %v, want the opening pool order", got, err)
	}
}
