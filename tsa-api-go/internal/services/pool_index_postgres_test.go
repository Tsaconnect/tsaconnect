package services

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/testutil"
)

// S11, P2: a fresh database gets the v2 definition (opening included) and no
// old index; a database that had the old index ends with only the new one.
func TestOpenBuyIndex_FreshDBHasNewDefinition_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "liq_c1_idx_", &models.ITIntent{})
	// An existing database: the old definition is present before the migration.
	if err := db.Exec(`CREATE UNIQUE INDEX uq_it_intents_open_stable_buy ON it_intents (user_id)
	    WHERE direction = 'buy' AND status = 'pending_payment' AND asset IN ('USDC','USDT')`).Error; err != nil {
		t.Fatalf("old index: %v", err)
	}
	for i := 0; i < 2; i++ { // idempotent: a second boot changes nothing
		if err := config.MigrateOpenStableBuyIndex(db); err != nil {
			t.Fatalf("migrate (pass %d): %v", i+1, err)
		}
	}
	var defs []struct{ Indexname, Indexdef string }
	db.Raw(`SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'it_intents' AND indexname LIKE 'uq_it_intents_open_stable_buy%'`).Scan(&defs)
	if len(defs) != 1 || defs[0].Indexname != config.OpenStableBuyIndexName || !strings.Contains(defs[0].Indexdef, "opening") {
		t.Fatalf("indexes = %+v, want only %s covering opening", defs, config.OpenStableBuyIndexName)
	}
}

// Roadmap risk 8: an opening pool order blocks a second open buy for the user.
func TestOpenBuyIndex_OpeningBlocksSecond_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "liq_c1_open_", &models.ITIntent{})
	if err := config.MigrateOpenStableBuyIndex(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	user := uuid.New()
	mk := func(status string) *models.ITIntent {
		return &models.ITIntent{ID: uuid.New(), UserID: user, Direction: models.ITDirectionBuy, Status: status,
			Provider: models.ITProviderPool, Asset: "USDT", AssetAmount: "1", Currency: "NGN", DestChain: "BSC",
			LockedRate: "1", DisplayRate: "1", NgnKobo: "1", AmountMinor: "1", RateKobo: "1", RateMinor: "1",
			McgpAmount: "0", FeeNgnKobo: "0", NetNgnKobo: "1", PlatformKeptKobo: "0", ReceivedAmount: "0"}
	}
	if err := db.Create(mk(models.ITStatusOpening)).Error; err != nil {
		t.Fatalf("opening: %v", err)
	}
	if err := db.Create(mk(models.ITStatusPendingPayment)).Error; err == nil || !isUniqueViolation(err) {
		t.Fatalf("second open buy = %v, want a unique violation", err)
	}
}

// §7.4: one live slice per LP per generation; a cancelled one frees the slot.
func TestPoolSliceIndexes_OneLiveSlicePerLPPerGeneration_Postgres(t *testing.T) {
	db := testutil.TempPostgres(t, "liq_c1_slice_", &models.P2PAd{}, &models.P2PTrade{})
	if err := config.MigratePoolSliceIndexes(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ad := &models.P2PAd{ID: uuid.New(), SellerID: uuid.New(), Side: models.P2PAdSideSell, Token: "USDT", ChainID: poolTestChain,
		Amount: "100", Remaining: "100", Reserved: "0", FiatCurrency: "NGN", Rate: "0",
		RateType: models.P2PAdRateTypeFixed, FloatAnchor: models.P2PAdFloatAnchorBybitSell, Status: models.P2PAdStatusActive}
	if err := db.Create(ad).Error; err != nil {
		t.Fatalf("ad: %v", err)
	}
	intent := uuid.New()
	first := seedPoolSlice(t, db, sliceSeed{Intent: intent, Ad: ad, Units: usdt18(2)})
	if err := db.Create(&models.P2PTrade{ID: uuid.New(), AdID: ad.ID, BuyerID: uuid.New(), SellerID: ad.SellerID,
		Token: "USDT", ChainID: poolTestChain, TokenAmount: "1", FiatAmount: "1354.74", FiatCurrency: "NGN", Rate: "1354.74",
		Status: models.P2PTradeStatusPendingPayment, ExpiresAt: time.Now(), ITIntentID: &intent, Generation: 1,
		OnChainTradeID: "0x" + strings.Repeat("ab", 32)}).Error; err == nil {
		t.Fatal("a second live slice for the same LP ad and generation was accepted")
	}
	db.Model(first).Update("status", models.P2PTradeStatusCancelled)
	seedPoolSlice(t, db, sliceSeed{Intent: intent, Ad: ad, Units: usdt18(2)}) // a cancelled slice frees the slot
}
