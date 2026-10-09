package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// RequireP2PGasNeed is the P2PGasNeed the gas top-up asks before it sends: the
// caller really has this tx to sign on the Sonic escrow, now. It mirrors the
// gates of the Prepare* call that builds that tx (party, status, expiry), so
// a drip is only ever backed by real trade or ad state.
//
//   - approve / deposit: no state beyond the KYC every top-up already
//     requires (any verified user may fund the pool to post ads or open a
//     buy-ad trade); the per-user cap and the unused-drip rule bound it.
//   - withdraw: a non-zero available pool balance in some listed asset.
//   - cancel_ad: adId of her own active/paused sell ad with no open trade.
//   - open_trade: adId of an active, openable ad that is not hers.
//   - mark_paid: tradeId; she is the buyer and it is pending_payment.
//   - release: tradeId; she is the seller and it is pending_payment, paid or
//     releasing (a retry), as PrepareRelease allows.
//   - cancel_trade: tradeId; she is a party, pending_payment, past expiry.
//   - dispute: tradeId; she is a party, pending_payment or paid.
//
// Returns ErrInvalidParams for a missing or unknown id, ErrP2PGasNotNeeded
// when it is not her turn (or not on Sonic), and any other error unwrapped
// (a DB or chain read that failed).
func (s *P2PTradeService) RequireP2PGasNeed(ctx context.Context, user *models.User, wallet string, req P2PGasRequest) error {
	switch req.Action {
	case P2PGasApprove, P2PGasDeposit:
		return nil
	case P2PGasWithdraw:
		return s.gasNeedWithdraw(ctx, wallet)
	case P2PGasCancelAd, P2PGasOpenTrade:
		ad, err := s.gasNeedAd(ctx, req.AdID)
		if err != nil {
			return err
		}
		if req.Action == P2PGasCancelAd {
			return s.gasNeedCancelAd(ctx, user.ID, ad)
		}
		return s.gasNeedOpenTrade(ctx, user.ID, ad)
	case P2PGasMarkPaid, P2PGasRelease, P2PGasCancelTrade, P2PGasDispute:
		trade, err := s.gasNeedTrade(ctx, req.TradeID)
		if err != nil {
			return err
		}
		return s.gasNeedTradeTurn(user.ID, req.Action, trade)
	}
	return fmt.Errorf("%w: unknown gas action %q", ErrInvalidParams, req.Action)
}

func (s *P2PTradeService) gasNeedAd(ctx context.Context, id uuid.UUID) (*models.P2PAd, error) {
	if id == uuid.Nil {
		return nil, fmt.Errorf("%w: adId is required", ErrInvalidParams)
	}
	var ad models.P2PAd
	if err := s.db.WithContext(ctx).First(&ad, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: unknown adId", ErrInvalidParams)
		}
		return nil, err
	}
	if ad.ChainID != s.defaultChainID {
		return nil, fmt.Errorf("%w: ad is not on the sponsored chain", ErrP2PGasNotNeeded)
	}
	return &ad, nil
}

func (s *P2PTradeService) gasNeedTrade(ctx context.Context, id uuid.UUID) (*models.P2PTrade, error) {
	if id == uuid.Nil {
		return nil, fmt.Errorf("%w: tradeId is required", ErrInvalidParams)
	}
	var trade models.P2PTrade
	if err := s.db.WithContext(ctx).First(&trade, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: unknown tradeId", ErrInvalidParams)
		}
		return nil, err
	}
	if trade.ChainID != s.defaultChainID {
		return nil, fmt.Errorf("%w: trade is not on the sponsored chain", ErrP2PGasNotNeeded)
	}
	return &trade, nil
}

func (s *P2PTradeService) gasNeedTradeTurn(userID uuid.UUID, action P2PGasAction, t *models.P2PTrade) error {
	party := userID == t.BuyerID || userID == t.SellerID
	ok := false
	switch action {
	case P2PGasMarkPaid:
		ok = userID == t.BuyerID && t.Status == models.P2PTradeStatusPendingPayment
	case P2PGasRelease:
		ok = userID == t.SellerID && (t.Status == models.P2PTradeStatusPendingPayment ||
			t.Status == models.P2PTradeStatusPaid || t.Status == models.P2PTradeStatusReleasing)
	case P2PGasCancelTrade:
		ok = party && t.Status == models.P2PTradeStatusPendingPayment && s.clock.Now().After(t.ExpiresAt)
	case P2PGasDispute:
		ok = party && (t.Status == models.P2PTradeStatusPendingPayment || t.Status == models.P2PTradeStatusPaid)
	}
	if !ok {
		return fmt.Errorf("%w: %s on trade %s (status %s)", ErrP2PGasNotNeeded, action, t.ID, t.Status)
	}
	return nil
}

func (s *P2PTradeService) gasNeedCancelAd(ctx context.Context, userID uuid.UUID, ad *models.P2PAd) error {
	// A buy-ad has no voucher on-chain: cancelling it is DB-only (PrepareCancelAd).
	if ad.SellerID != userID || ad.Side == models.P2PAdSideBuy ||
		(ad.Status != models.P2PAdStatusActive && ad.Status != models.P2PAdStatusPaused) {
		return fmt.Errorf("%w: cancel_ad on ad %s", ErrP2PGasNotNeeded, ad.ID)
	}
	var open int64
	if err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("ad_id = ? AND status IN ?", ad.ID, []string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid,
			models.P2PTradeStatusReleasing, models.P2PTradeStatusDisputed}).
		Count(&open).Error; err != nil {
		return err
	}
	if open > 0 {
		return fmt.Errorf("%w: ad %s has open trades", ErrP2PGasNotNeeded, ad.ID)
	}
	return nil
}

func (s *P2PTradeService) gasNeedOpenTrade(ctx context.Context, userID uuid.UUID, ad *models.P2PAd) error {
	if ad.Status != models.P2PAdStatusActive || ad.SellerID == userID {
		return fmt.Errorf("%w: open_trade on ad %s", ErrP2PGasNotNeeded, ad.ID)
	}
	// The same refusals OpenTrade makes before building the tx.
	if pool, err := s.isPoolAd(ctx, ad); err != nil {
		return err
	} else if pool {
		return fmt.Errorf("%w: pool ad %s", ErrP2PGasNotNeeded, ad.ID)
	}
	if blocked, err := s.sellerBlocked(ctx, ad.SellerID); err != nil {
		return err
	} else if blocked {
		return fmt.Errorf("%w: ad %s seller is blocked", ErrP2PGasNotNeeded, ad.ID)
	}
	if err := s.requireOpenable(ad.ChainID, ad.Token); err != nil {
		return fmt.Errorf("%w: %v", ErrP2PGasNotNeeded, err)
	}
	return nil
}

// gasNeedWithdraw: something in her pool to withdraw, in any asset listed on
// the sponsored chain (one balance read per asset, stopping at the first).
func (s *P2PTradeService) gasNeedWithdraw(ctx context.Context, wallet string) error {
	for _, c := range s.EnabledChains() {
		if c.ChainID != s.defaultChainID {
			continue
		}
		for _, sym := range c.Assets {
			bal, err := s.GetPoolBalance(ctx, wallet, sym, c.ChainID)
			if errors.Is(err, ErrUnsupportedToken) {
				continue
			}
			if err != nil {
				return err
			}
			if bal.Available != nil && bal.Available.Sign() > 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("%w: nothing available to withdraw", ErrP2PGasNotNeeded)
}
