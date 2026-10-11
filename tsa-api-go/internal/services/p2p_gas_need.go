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
// caller really has this tx to sign on the Sonic escrow, now. It runs the same
// gate as the Prepare* call that builds that tx (p2p_preconditions.go), so a
// drip is only ever backed by real trade or ad state.
//
//   - approve / deposit: no state beyond the KYC every top-up already
//     requires (any verified user may fund the pool to post ads or open a
//     buy-ad trade); the per-user cap and the unused-drip rule bound it.
//   - withdraw: a non-zero available pool balance in some listed asset.
//   - cancel_ad: adId of her own active/paused sell ad with no open trade.
//   - open_trade: adId of an active, openable ad that is not hers, and she
//     clears OpenTrade's KYC tier for its side (else that KYC refusal itself).
//   - mark_paid: tradeId; she is the buyer, it is pending_payment and its
//     open is on chain.
//   - release: tradeId; she is the seller and it is pending_payment, paid or
//     releasing (a retry), as PrepareRelease allows.
//   - cancel_trade: tradeId; she is a party, pending_payment, past expiry.
//   - dispute: tradeId; she is a party, pending_payment or paid, open on
//     chain, and no live counterparty prepare or on-chain dispute exists.
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
		return s.gasNeedTradeTurn(ctx, user.ID, req.Action, trade)
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

// gasNeedTradeTurn applies the same gate as the action's Prepare* call; mark
// paid and dispute also need the open on chain (requireOpenOnChain), as theirs do.
func (s *P2PTradeService) gasNeedTradeTurn(ctx context.Context, userID uuid.UUID, action P2PGasAction, t *models.P2PTrade) error {
	var err error
	switch action {
	case P2PGasMarkPaid:
		err = canMarkPaid(userID, t)
	case P2PGasRelease:
		err = canRelease(userID, t)
	case P2PGasCancelTrade:
		err = canCancelByTimeout(userID, t, s.clock.Now())
	case P2PGasDispute:
		_, err = canDispute(userID, t)
	}
	if err == nil && (action == P2PGasMarkPaid || action == P2PGasDispute) {
		err = s.gasNeedOpenOnChain(ctx, t)
	}
	if err == nil && action == P2PGasDispute {
		err = s.gasNeedDisputeRow(ctx, userID, t)
	}
	return gasNotNeeded(err, fmt.Sprintf("%s on trade %s (status %s)", action, t.ID, t.Status))
}

// gasNeedDisputeRow applies PrepareRaiseDispute's existing-row gate
// (replaceableDisputeRow): no gas for a prepare that would be refused.
func (s *P2PTradeService) gasNeedDisputeRow(ctx context.Context, userID uuid.UUID, t *models.P2PTrade) error {
	escrow, _, _, err := s.resolve(t.ChainID)
	if err != nil {
		return err
	}
	_, err = s.replaceableDisputeRow(ctx, t, userID, escrow)
	return err
}

// gasNeedOpenOnChain is requireOpenOnChain for the gas check: "not opened" (or
// an on-chain trade that is not ours) is a refusal, a failed read is not.
func (s *P2PTradeService) gasNeedOpenOnChain(ctx context.Context, t *models.P2PTrade) error {
	escrow, _, _, err := s.resolve(t.ChainID)
	if err != nil {
		return err
	}
	err = s.requireOpenOnChain(ctx, t, escrow)
	if errors.Is(err, ErrInvalidTransition) {
		return refuse(err)
	}
	return err
}

// gasNotNeeded turns a gate refusal into ErrP2PGasNotNeeded and passes a
// failed read through unchanged. A KYC refusal is returned as itself (403 with
// its code): it is the real blocker, and "nothing to sign" would leave the app
// telling her to add S she cannot use.
func gasNotNeeded(err error, what string) error {
	var r p2pRefusal
	switch {
	case !errors.As(err, &r):
		return err
	case errors.Is(r.err, ErrKYCVerificationRequired):
		return r.err
	default:
		return fmt.Errorf("%w: %s: %v", ErrP2PGasNotNeeded, what, err)
	}
}

func (s *P2PTradeService) gasNeedCancelAd(ctx context.Context, userID uuid.UUID, ad *models.P2PAd) error {
	// A buy-ad has no voucher on-chain: cancelling it is DB-only (CancelBuyAd).
	if ad.Side == models.P2PAdSideBuy {
		return fmt.Errorf("%w: cancel_ad on buy ad %s", ErrP2PGasNotNeeded, ad.ID)
	}
	return gasNotNeeded(s.cancelAdPreflight(ctx, ad, userID), fmt.Sprintf("cancel_ad on ad %s", ad.ID))
}

func (s *P2PTradeService) gasNeedOpenTrade(ctx context.Context, userID uuid.UUID, ad *models.P2PAd) error {
	return gasNotNeeded(s.openTradePreflight(ctx, ad, userID), fmt.Sprintf("open_trade on ad %s", ad.ID))
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
