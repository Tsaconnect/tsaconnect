package services

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// excludePoolAds keeps LP stock ads out of every public read (spec S29, §7.3):
// nothing on the pool chain, and nothing owned by an LP row wherever it sits,
// so an ad survives its chain being unconfigured still hidden. LP ids are read
// in Go (a handful of rows) to keep the browse query join-free. Without a PM
// service no tagged ad can be proven not to be an LP's, so all of them hide
// (fail closed; production always wires one).
//
// The chain clause is needed on its own: delistedAdsClause walks Chains(),
// which omits the pool chain (plan A P8), so it never filters pool-chain ads.
func (s *P2PTradeService) excludePoolAds(ctx context.Context, q *gorm.DB) (*gorm.DB, error) {
	if pool, ok := s.escrows.PoolChain(); ok {
		q = q.Where("chain_id <> ?", pool.ChainID)
	}
	if s.pm == nil {
		return q.Where("pm_id IS NULL"), nil
	}
	ids, err := s.pm.LPIDs(ctx)
	if err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		q = q.Where("(pm_id IS NULL OR pm_id NOT IN ?)", ids)
	}
	return q, nil
}

// isPoolAd: on the pool chain, or owned by an LP row. A tagged ad with no PM
// service to check it against counts as pool (fail closed, as excludePoolAds).
func (s *P2PTradeService) isPoolAd(ctx context.Context, ad *models.P2PAd) (bool, error) {
	if s.IsPoolChain(ad.ChainID) {
		return true, nil
	}
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

// GetAdForViewer is GetAd for a user-facing read: an LP stock ad is visible to
// its owner only — to anyone else it does not exist, so voucherSignature never
// leaves (S29, §9.4). pool tells the handler to skip the PM pay-to enrichment.
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
