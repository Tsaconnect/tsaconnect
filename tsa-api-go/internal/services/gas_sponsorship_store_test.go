package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

func newSponsorshipStore(t *testing.T) *GormSponsorshipStore {
	t.Helper()
	db := newCurrencyTestDB(t)
	if err := db.AutoMigrate(&models.GasSponsorship{}, &models.PaycrestOrderRecord{}); err != nil {
		t.Fatalf("migrate gas_sponsorships, paycrest_order_records: %v", err)
	}
	return &GormSponsorshipStore{DB: db}
}

func sponsorshipRow(user, order uuid.UUID, kind string) *models.GasSponsorship {
	return &models.GasSponsorship{UserID: user, OrderID: order, Kind: kind, ChainKey: "bsc", Token: "USDT", Wallet: "0xabc"}
}

func TestGormSponsorshipStore_ReserveIsOncePerOrderKind(t *testing.T) {
	s := newSponsorshipStore(t)
	ctx := context.Background()
	user, order := uuid.New(), uuid.New()

	existing, err := s.Reserve(ctx, sponsorshipRow(user, order, models.GasSponsorshipKindTopUp))
	if err != nil || existing != nil {
		t.Fatalf("first reserve: existing=%v err=%v", existing, err)
	}
	existing, err = s.Reserve(ctx, sponsorshipRow(user, order, models.GasSponsorshipKindTopUp))
	if err != nil || existing == nil {
		t.Fatalf("second reserve must return the existing row: existing=%v err=%v", existing, err)
	}
	// A different kind on the same order is a separate slot.
	existing, err = s.Reserve(ctx, sponsorshipRow(user, order, models.GasSponsorshipKindAuthorization))
	if err != nil || existing != nil {
		t.Fatalf("other kind: existing=%v err=%v", existing, err)
	}
}

func TestGormSponsorshipStore_ReleaseFreesTheSlot(t *testing.T) {
	s := newSponsorshipStore(t)
	ctx := context.Background()
	row := sponsorshipRow(uuid.New(), uuid.New(), models.GasSponsorshipKindTopUp)
	if _, err := s.Reserve(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	existing, err := s.Reserve(ctx, sponsorshipRow(row.UserID, row.OrderID, models.GasSponsorshipKindTopUp))
	if err != nil || existing != nil {
		t.Fatalf("reserve after release: existing=%v err=%v", existing, err)
	}
}

func TestGormSponsorshipStore_CountUnusedTopUpsSince(t *testing.T) {
	s := newSponsorshipStore(t)
	ctx := context.Background()
	user := uuid.New()
	since := time.Now().Add(-24 * time.Hour)

	order := func(status, amountPaid string) uuid.UUID {
		t.Helper()
		o := &models.PaycrestOrderRecord{
			UserID: user, Direction: models.PaycrestDirectionSell, Status: status, AmountPaid: amountPaid,
			Reference: uuid.NewString(), FiatCode: "NGN", FiatAmount: "50000", Token: "USDT",
			ChainKey: "bsc", Network: "bnb-smart-chain", WalletAddress: "0xabc",
		}
		if err := s.DB.Create(o).Error; err != nil {
			t.Fatalf("create order: %v", err)
		}
		return o.ID
	}
	reserve := func(row *models.GasSponsorship) {
		t.Helper()
		if existing, err := s.Reserve(ctx, row); err != nil || existing != nil {
			t.Fatalf("reserve: existing=%v err=%v", existing, err)
		}
	}

	reserve(sponsorshipRow(user, order(models.PaycrestStatusExpired, "0"), models.GasSponsorshipKindTopUp))        // unused
	reserve(sponsorshipRow(user, order(models.PaycrestStatusSettled, "10"), models.GasSponsorshipKindTopUp))       // used
	reserve(sponsorshipRow(user, order(models.PaycrestStatusAwaitingDeposit, ""), models.GasSponsorshipKindTopUp)) // undecided
	reserve(sponsorshipRow(user, order(models.PaycrestStatusExpired, "0"), models.GasSponsorshipKindAuthorization))
	reserve(sponsorshipRow(uuid.New(), order(models.PaycrestStatusExpired, "0"), models.GasSponsorshipKindTopUp)) // another user

	stale := sponsorshipRow(user, order(models.PaycrestStatusExpired, "0"), models.GasSponsorshipKindTopUp)
	stale.CreatedAt = since.Add(-time.Hour)
	reserve(stale)

	n, err := s.CountUnusedTopUpsSince(ctx, user, since)
	if err != nil || n != 1 {
		t.Fatalf("CountUnusedTopUpsSince = %d, %v; want 1 (only the recent expired top-up)", n, err)
	}
}
