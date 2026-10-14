package services

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// The slice pool (spec 2026-10-08) is retired: the shared pool (spec
// 2026-10-14) holds no P2P trades or ads. These guards keep any row it left
// behind — a slice (it_intent_id set) or an LP stock ad (pm_id of an LP) —
// off every user P2P path; an operator settles leftovers by hand.

// notPoolSlice keeps a p2p_trades query to ordinary trades (spec 2026-10-08
// §23, plan C1 P13). Every P2P/PM path that lists trades for a user, a
// watchdog nudge, a payment match or an automated release takes it: a slice
// is pool money, settled only by the pool's own paths.
func notPoolSlice(db *gorm.DB) *gorm.DB { return db.Where("p2p_trades.it_intent_id IS NULL") }

// hideSlice answers a pool slice as not found on every single-trade user
// path, so no user (the LP included) can read or act on one through the API.
func hideSlice(t *models.P2PTrade) error {
	if t.IsPoolSlice() {
		return ErrTradeNotFound
	}
	return nil
}

// isPoolAd: owned by an LP row. A tagged ad with no PM service to check it
// against counts as one (fail closed, as excludePoolAds).
func (s *P2PTradeService) isPoolAd(ctx context.Context, ad *models.P2PAd) (bool, error) {
	if ad.PMID == nil {
		return false, nil
	}
	if s.pm == nil {
		return true, nil
	}
	pm, err := s.pm.GetByID(ctx, *ad.PMID)
	if errors.Is(err, ErrPMNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return pm.IsLP(), nil
}

// excludePoolAds keeps retired pool stock ads (owned by an LP row, any status)
// out of every public read. A correlated NOT EXISTS on the LP row's primary
// key, not an id list: with instant activation the LP rows grow with every
// user, and a list would be re-read and re-sent on every browse. Every LP row
// counts, vault-activated or not: a slice-era row re-activated on the vault
// keeps its id (and its leftover stock ads). Without a PM service every
// tagged ad hides.
func (s *P2PTradeService) excludePoolAds(_ context.Context, q *gorm.DB) (*gorm.DB, error) {
	if s.pm == nil {
		return q.Where("pm_id IS NULL"), nil
	}
	return q.Where("(pm_id IS NULL OR NOT EXISTS (SELECT 1 FROM payment_merchants pool_lp WHERE pool_lp.id = p2p_ads.pm_id AND pool_lp.kind = ?))",
		models.PaymentMerchantKindLP), nil
}

// GetAdForViewer is GetAd that answers a retired pool stock ad as not found to
// everyone but its own seller; pool reports whether it is one.
func (s *P2PTradeService) GetAdForViewer(ctx context.Context, adID, viewerID uuid.UUID) (*models.P2PAd, bool, error) {
	ad, err := s.GetAd(ctx, adID)
	if err != nil {
		return nil, false, err
	}
	pool, err := s.isPoolAd(ctx, ad)
	if err != nil {
		return nil, false, err
	}
	if pool && ad.SellerID != viewerID {
		return nil, false, ErrAdNotFound
	}
	return ad, pool, nil
}
