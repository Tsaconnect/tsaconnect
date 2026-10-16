package services

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// Audience gate (direct P2P on BSC, decisions 2026-10-12). An escrow's
// audience says who may start NEW activity on it:
//
//   - all: anyone (Sonic until the switch, plan API-11).
//   - pm: Payment Merchants only — direct user-to-user P2P has moved to BSC.
//     Non-PM users may not post, sign, resume or fund there, nor open a
//     non-PM ad.
//   - direct: anyone but a PM (PM automation needs a release key that must
//     never be an admin on the direct escrow); AutoRelease refuses it outright.
//
// Refusals come back as p2pRefusal (the gas need tells them from a failed
// read). Exits are never gated: release, cancel, dispute, resolve, withdraw,
// confirm of a tx already on chain, reconcile, and cancelling an ad's voucher.

// Machine codes of the audience refusals (both 409).
const (
	P2PDirectMovedCode = "p2p_direct_moved"
	PMNotOnDirectCode  = "p2p_pm_not_on_direct"
)

// ErrP2PDirectMoved: new direct P2P activity on an escrow now kept for
// Payment Merchants.
var ErrP2PDirectMoved = errors.New("Direct P2P has moved to BNB Chain")

// ErrPMNotOnDirect: a Payment Merchant posting on the direct BSC escrow, where
// its automation cannot settle (no release-key admin there).
var ErrPMNotOnDirect = errors.New("p2p: payment merchant ads are not available on this chain")

// sellerAudience gates posting, signing and resuming an ad on chainID by its
// escrow's audience.
func (s *P2PTradeService) sellerAudience(ctx context.Context, sellerID uuid.UUID, chainID int64) error {
	if err := s.unverifiedChain(chainID); err != nil {
		return err
	}
	audience := s.escrows.Audience(chainID)
	if audience == config.P2PAudienceAll {
		return nil
	}
	pm, err := s.lookupActivePM(ctx, sellerID)
	if err != nil {
		return err
	}
	switch {
	case audience == config.P2PAudiencePM && pm == nil:
		return refuse(ErrP2PDirectMoved)
	case audience == config.P2PAudienceDirect && pm != nil:
		return refuse(ErrPMNotOnDirect)
	}
	return nil
}

// depositAudience gates funding a balance on chainID: a pm escrow takes
// deposits from its PMs only; anyone may fund the others.
func (s *P2PTradeService) depositAudience(ctx context.Context, userID uuid.UUID, chainID int64) error {
	if err := s.unverifiedChain(chainID); err != nil {
		return err
	}
	if s.escrows.Audience(chainID) != config.P2PAudiencePM {
		return nil
	}
	pm, err := s.lookupActivePM(ctx, userID)
	if err != nil {
		return err
	}
	if pm == nil {
		return refuse(ErrP2PDirectMoved)
	}
	return nil
}

// openAudience gates opening a trade on an ad: on a pm escrow only a PM ad
// opens (an LP ad never reaches here: isPoolAd refuses it first).
func (s *P2PTradeService) openAudience(ad *models.P2PAd) error {
	if err := s.unverifiedChain(ad.ChainID); err != nil {
		return err
	}
	if s.escrows.Audience(ad.ChainID) == config.P2PAudiencePM && ad.PMID == nil {
		return refuse(ErrP2PDirectMoved)
	}
	return nil
}

// unverifiedChain refuses new activity on an escrow whose boot gates could
// not be read yet (503: try again shortly). Exits never call it.
func (s *P2PTradeService) unverifiedChain(chainID int64) error {
	if s.escrows.Unverified(chainID) {
		return refuse(fmt.Errorf("%w: P2P on chain %d is being verified", ErrChainUnavailable, chainID))
	}
	return nil
}

// ChainsFor is GET /p2p/chains for one caller: every enabled chain with its
// live fees and adsOpen — whether she may post an ad there now.
func (s *P2PTradeService) ChainsFor(ctx context.Context, userID uuid.UUID) []P2PChainInfo {
	chains := s.LiveChains(ctx)
	for i := range chains {
		open := s.sellerAudience(ctx, userID, chains[i].ChainID) == nil
		chains[i].AdsOpen = &open
	}
	return chains
}

// hideMovedAds keeps non-PM ads on a pm escrow out of browse: they can no
// longer be opened. Their owners still see them in their own list.
func (s *P2PTradeService) hideMovedAds(q *gorm.DB) *gorm.DB {
	chains := s.escrows.ChainsWithAudience(config.P2PAudiencePM)
	if len(chains) == 0 {
		return q
	}
	return q.Where("NOT (chain_id IN ? AND pm_id IS NULL)", chains)
}

// refuseDirectAutoRelease is AutoRelease's guard: PM automation never settles
// on a direct-audience escrow (defence in depth — no PM ad can be posted
// there, and the release key is not an admin on it). A trade that gets here
// is an ALERT.
func (s *P2PTradeService) refuseDirectAutoRelease(t *models.P2PTrade) error {
	if s.escrows.Audience(t.ChainID) != config.P2PAudienceDirect {
		return nil
	}
	log.Printf("[p2p] ALERT p2p_auto_release_direct_refused_total=1 trade=%s chain=%d", t.ID, t.ChainID)
	return fmt.Errorf("%w: automated release is not available on chain %d", ErrForbidden, t.ChainID)
}
