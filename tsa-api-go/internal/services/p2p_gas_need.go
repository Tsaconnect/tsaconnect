package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/gorm"
)

// RequireP2PGasNeed is the P2PGasNeed the gas top-up asks before it sends: the
// caller really has this tx to sign on a P2P escrow, now — and on which chain.
// It runs the same gate as the Prepare* call that builds that tx
// (p2p_preconditions.go), so a drip is only ever backed by real trade or ad
// state.
//
// The chain is the entity's (ad or trade) for the actions that name one, and
// req.ChainID for approve / deposit / withdraw — 0 (older apps) is the
// default chain (Sonic), never BSC. A req.ChainID that differs from the
// entity's, or a chain with no registered escrow, is gas_not_needed.
//
//   - approve / deposit: the KYC every top-up already requires; on a
//     PM-only escrow only a PM (approve: or an opener of adId, a PM buy-ad's
//     JIT deposit, whose chain is the ad's). The per-user cap and the
//     unused-drip rule bound it.
//   - withdraw: a non-zero available pool balance in some listed asset.
//   - cancel_ad: adId of her own active/paused sell ad with no open trade.
//   - open_trade: adId of an active, openable ad that is not hers, and she
//     clears OpenTrade's KYC tier for its side (else that KYC refusal itself).
//   - mark_paid: tradeId; she is the buyer, it is pending_payment and its
//     open is on chain.
//   - release: tradeId; she is the seller and it is pending_payment, paid or
//     releasing (a retry), as PrepareRelease allows.
//   - cancel_trade: tradeId; she is a party, pending_payment, past expiry.
//   - dispute: tradeId; she is a party, paid, open on chain, and no live
//     counterparty prepare or on-chain dispute exists.
//   - set_releaser: she is an active payment merchant whose maker wallet (the
//     proven wallet the drip goes to) has not yet named the release key on the
//     chain (req.ChainID, 0 = default).
//
// Returns the chain the tx is on, ErrInvalidParams for a missing or unknown
// id, ErrP2PGasNotNeeded when it is not her turn (or not on that chain), and
// any other error unwrapped (a DB or chain read that failed).
func (s *P2PTradeService) RequireP2PGasNeed(ctx context.Context, user *models.User, wallet string, req P2PGasRequest) (int64, error) {
	switch req.Action {
	case P2PGasApprove, P2PGasDeposit, P2PGasWithdraw:
		if req.Action == P2PGasApprove && req.AdID != uuid.Nil {
			return s.gasNeedApproveForAd(ctx, user.ID, req)
		}
		chainID, err := s.gasNeedChain(req.ChainID)
		if err != nil {
			return 0, err
		}
		switch req.Action {
		case P2PGasWithdraw:
			return chainID, s.gasNeedWithdraw(ctx, wallet, chainID)
		default:
			return chainID, gasNotNeeded(s.depositAudience(ctx, user.ID, chainID), fmt.Sprintf("%s on chain %d", req.Action, chainID))
		}
	case P2PGasCancelAd, P2PGasOpenTrade:
		ad, err := s.gasNeedAd(ctx, req.AdID, req.ChainID)
		if err != nil {
			return 0, err
		}
		if req.Action == P2PGasCancelAd {
			return ad.ChainID, s.gasNeedCancelAd(ctx, user.ID, ad)
		}
		return ad.ChainID, s.gasNeedOpenTrade(ctx, user.ID, ad)
	case P2PGasSetReleaser:
		st, err := s.PMReleaser(ctx, user.ID, req.ChainID)
		if errors.Is(err, ErrForbidden) {
			return 0, gasNotNeeded(refuse(err), "set_releaser: not a payment merchant")
		}
		if err != nil {
			return 0, err
		}
		if st.Set {
			return 0, gasNotNeeded(refuse(ErrInvalidTransition), fmt.Sprintf("set_releaser on chain %d: already set", st.ChainID))
		}
		// setReleaser is signed by the maker wallet: gas sent anywhere else cannot pay for it.
		if !strings.EqualFold(wallet, st.Wallet) {
			return 0, gasNotNeeded(refuse(ErrInvalidTransition), "set_releaser: the proven wallet is not the maker wallet")
		}
		return st.ChainID, nil
	case P2PGasMarkPaid, P2PGasRelease, P2PGasCancelTrade, P2PGasDispute:
		trade, err := s.gasNeedTrade(ctx, req.TradeID, req.ChainID)
		if err != nil {
			return 0, err
		}
		return trade.ChainID, s.gasNeedTradeTurn(ctx, user.ID, req.Action, trade)
	}
	return 0, fmt.Errorf("%w: unknown gas action %q", ErrInvalidParams, req.Action)
}

// gasNeedChain is the chain an approve / deposit / withdraw names: 0 is the
// default chain; anything must have a registered escrow.
func (s *P2PTradeService) gasNeedChain(chainID int64) (int64, error) {
	if chainID == 0 {
		chainID = s.defaultChainID
	}
	if !s.escrows.HasChain(chainID) {
		return 0, fmt.Errorf("%w: chain %d has no P2P escrow", ErrP2PGasNotNeeded, chainID)
	}
	return chainID, nil
}

// gasNeedApproveForAd: an approve for opening adID (a buy-ad's JIT deposit)
// is on the ad's chain and needs the open itself to pass — so a non-PM on a
// PM-only escrow still gets one for a PM buy-ad she may open.
func (s *P2PTradeService) gasNeedApproveForAd(ctx context.Context, userID uuid.UUID, req P2PGasRequest) (int64, error) {
	ad, err := s.gasNeedAd(ctx, req.AdID, req.ChainID)
	if err != nil {
		return 0, err
	}
	return ad.ChainID, gasNotNeeded(s.openTradePreflight(ctx, ad, userID), fmt.Sprintf("approve for ad %s", ad.ID))
}

// gasNeedOnChain refuses an entity on a chain other than the one the request
// named (0 names none), or on a chain with no registered escrow.
func (s *P2PTradeService) gasNeedOnChain(what string, entityChain, requested int64) error {
	if requested != 0 && requested != entityChain {
		return fmt.Errorf("%w: %s is on chain %d, not %d", ErrP2PGasNotNeeded, what, entityChain, requested)
	}
	if !s.escrows.HasChain(entityChain) {
		return fmt.Errorf("%w: %s is on chain %d, which has no P2P escrow", ErrP2PGasNotNeeded, what, entityChain)
	}
	return nil
}

func (s *P2PTradeService) gasNeedAd(ctx context.Context, id uuid.UUID, requested int64) (*models.P2PAd, error) {
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
	if err := s.gasNeedOnChain("ad", ad.ChainID, requested); err != nil {
		return nil, err
	}
	return &ad, nil
}

func (s *P2PTradeService) gasNeedTrade(ctx context.Context, id uuid.UUID, requested int64) (*models.P2PTrade, error) {
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
	// A pool slice answers as missing, as on every user P2P path (hideSlice).
	if trade.IsPoolSlice() {
		return nil, fmt.Errorf("%w: unknown tradeId", ErrInvalidParams)
	}
	if err := s.gasNeedOnChain("trade", trade.ChainID, requested); err != nil {
		return nil, err
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
		// The expiry canceller decided it (dropped_at): the release key is
		// already sending cancelByTimeout, so hers would revert NotPending.
		// PrepareCancel itself stays permissionless.
		if err == nil && t.DroppedAt != nil {
			err = refuse(fmt.Errorf("%w: trade is already being cancelled", ErrInvalidTransition))
		}
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
	escrow, _, err := s.tradeEscrow(t)
	if err != nil {
		return err
	}
	_, err = s.replaceableDisputeRow(ctx, t, userID, escrow)
	return err
}

// gasNeedOpenOnChain is requireOpenOnChain for the gas check: "not opened" (or
// an on-chain trade that is not ours) is a refusal, a failed read is not.
func (s *P2PTradeService) gasNeedOpenOnChain(ctx context.Context, t *models.P2PTrade) error {
	escrow, _, err := s.tradeEscrow(t)
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
// chainID (one balance read per asset, stopping at the first).
func (s *P2PTradeService) gasNeedWithdraw(ctx context.Context, wallet string, chainID int64) error {
	for _, c := range s.EnabledChains() {
		if c.ChainID != chainID {
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
