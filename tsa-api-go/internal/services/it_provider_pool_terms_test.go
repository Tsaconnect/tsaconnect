package services

import (
	"context"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// isPoolRefusal: the pool's own refusal — a bad-state error naming the pool,
// never the unknown-provider park (the pool HAS terms now, plan C1 P3).
func isPoolRefusal(err error) bool {
	return errors.Is(err, ErrITBadState) && !errors.Is(err, ErrUnknownITProvider) && strings.Contains(err.Error(), "TSA pool")
}

// poolSites is every PR 0 site with what it must do for a tsa_pool intent.
var poolSites = []providerSite{
	{"ConvertStableBuy", models.ITStatusPaymentReceived, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		conv := &stubConverter{}
		s.SetStableConverter(conv, "m")
		return isPoolRefusal(s.ConvertStableBuy(context.Background(), i.ID)) && len(conv.converted) == 0
	}},
	{"DeliverStableBuy", models.ITStatusDelivering, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		d := &stubDeliverer{id: "x"}
		s.SetStableDeliverer(d)
		return isPoolRefusal(s.DeliverStableBuy(context.Background(), i.ID)) && d.calls == 0
	}},
	{"FinalizeBuyConversion", models.ITStatusConverting, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		s.db.Model(i).Update("conversion_id", "trf_c")
		ct := &countingTransfers{}
		s.SetStableTransfers(ct)
		return isPoolRefusal(s.FinalizeBuyConversion(context.Background(), i.ID)) && ct.calls == 0
	}},
	{"FinalizeBuyDelivery", models.ITStatusDelivering, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		s.db.Model(i).Update("delivery_id", "trf_d")
		ct := &countingTransfers{}
		s.SetStableTransfers(ct)
		return isPoolRefusal(s.FinalizeBuyDelivery(context.Background(), i.ID)) && ct.calls == 0
	}},
	{"PendingStableBuys", models.ITStatusPaymentReceived, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		a, b, c, d, err := s.PendingStableBuys(context.Background())
		return err == nil && !slices.Contains(slices.Concat(a, b, c, d), i.ID)
	}},
	{"RepriceParkedStableBuy", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		_, err := s.RepriceParkedStableBuy(context.Background(), i.ID)
		return isPoolRefusal(err)
	}},
	{"priceRoutedArrival/honoured", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		a, err := s.priceRoutedArrival(context.Background(), i, i.AmountMinor, time.Now(), PayInCentral)
		return err == nil && a.honoured && a.units.String() == i.AssetAmount
	}},
	{"quoteRoutedLiveFor", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		q, err := s.quoteRoutedLiveFor(context.Background(), i, big.NewInt(1_000_000), PayInCentral)
		return q == nil && isPoolRefusal(err)
	}},
	{"routedMetric", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		return routedMetric(i) == "pool"
	}},
	{"quoteForIntent", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		q, err := s.quoteForIntent(context.Background(), i, "1000000")
		return q == nil && isPoolRefusal(err)
	}},
	// An opening intent has no honour window yet: it must never fall to
	// Flutterwave's old endpoint either (review M1 of PR 0, now for the pool).
	{"quoteForIntent/non-routed", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		q, err := s.quoteForIntent(context.Background(), unrouted(t, s, i), "1000000")
		return q == nil && isPoolRefusal(err)
	}},
	{"routedDeliveryAmounts", models.ITStatusDelivering, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		send, hold := s.routedDeliveryAmounts(i, big.NewInt(100))
		return send.Int64() == 100 && hold.Int64() == 100
	}},
	// The generic refund never runs a pool buy's: S37's refundPoolBuy does (P3).
	{"MarkStableBuyRefunded", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		_, err := s.MarkStableBuyRefunded(context.Background(), uuid.New(), i.ID, "note")
		var got models.ITIntent
		s.db.First(&got, "id = ?", i.ID)
		return isPoolRefusal(err) && got.Status == models.ITStatusBuyAwaitingRate
	}},
	{"DeliverParkedTreasuryBuy", models.ITStatusBuyAwaitingRate, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		_, err := s.DeliverParkedTreasuryBuy(context.Background(), uuid.New(), i.ID, "note")
		return errors.Is(err, ErrITBadState)
	}},
	// Review C-2 (supersedes P12's "honoured is today's code"): an honoured
	// pool claim is decided under the pool lock over the order's slices. With
	// no desk it writes nothing and the central poller retries — never paid
	// without slices. The wired path: TestPoolHonoured_ClaimsWithoutTouchingSlices.
	{"claimAndPriceStableBuy/honoured-central", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		ok, err := s.claimAndPriceStableBuy(context.Background(), i, "sess_pool_1", i.AmountMinor, time.Now(), false, PayInCentral)
		var got models.ITIntent
		s.db.First(&got, "id = ?", i.ID)
		return !ok && err != nil && got.Status == models.ITStatusPendingPayment && got.PaymentRef == ""
	}},
	// Review M4: a non-routed pool row can never price, so its money parks in
	// the claiming write (deterministic) instead of being retried every 30 s.
	{"claimAndPriceStableBuy/non-routed-central", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, _ uuid.UUID, i *models.ITIntent) bool {
		ok, err := s.claimAndPriceStableBuy(context.Background(), unrouted(t, s, i), "sess_pool_2", i.AmountMinor, time.Now(), false, PayInCentral)
		var got models.ITIntent
		s.db.First(&got, "id = ?", i.ID)
		return ok && err == nil && got.Status == models.ITStatusBuyAwaitingRate && got.PaymentRef == "sess_pool_2" &&
			strings.Contains(got.FailureReason, "TSA pool")
	}},
	{"GetStableBuyForUser", models.ITStatusPendingPayment, func(t *testing.T, s *ITService, userID uuid.UUID, i *models.ITIntent) bool {
		got, err := s.GetStableBuyForUser(context.Background(), userID, i.ID, models.ITProviderPool)
		_, other := s.GetStableBuyForUser(context.Background(), userID, i.ID, models.ITProviderTreasury)
		return err == nil && got.ID == i.ID && errors.Is(other, ErrStableIntentNotFound)
	}},
}

// S45 / plan C1 P3: the pool has its own terms at every PR 0 site.
func TestProviderBranches_PoolTerms(t *testing.T) {
	for _, site := range poolSites {
		t.Run(site.name, func(t *testing.T) {
			s, userID := newRoutedBuyService(t)
			if err := s.db.AutoMigrate(&models.ITIntentAuditLog{}); err != nil {
				t.Fatalf("migrate audit: %v", err)
			}
			intent := seedProviderIntent(t, s, userID, models.ITProviderPool, site.status)
			if !site.refuses(t, s, userID, intent) {
				t.Fatalf("%s did the wrong thing with a tsa_pool intent", site.name)
			}
		})
	}
	if got := poolProviders(); len(got) != 1 || got[0] != models.ITProviderPool {
		t.Fatalf("poolProviders = %v", got)
	}
	if slices.Contains(flutterwaveFulfilledProviders(), models.ITProviderPool) {
		t.Fatal("Flutterwave's watcher would list pool intents")
	}
}

// The lint lets a positive pool filter through, and only from the terms.
func TestProviderLint_AllowsPoolProvidersIN(t *testing.T) {
	allowed := `
func poolProviders() []string { return nil }
func p(db *gorm.DB) { var rows []models.ITIntent; db.Where("provider IN ?", poolProviders()).Find(&rows) }
`
	if got := lintSynthetic(t, allowed); len(got) != 0 {
		t.Fatalf("guard flagged the pool filter: %v", got)
	}
	if got := lintSynthetic(t, `func p(db *gorm.DB) { var rows []models.ITIntent; db.Where("provider IN ?", []string{"tsa_pool"}).Find(&rows) }`); len(got) == 0 {
		t.Fatal("a hand-written pool list must still be flagged")
	}
}
