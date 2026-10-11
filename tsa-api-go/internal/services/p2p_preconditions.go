package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/models"
)

// The per-action gates shared by each Prepare* call and RequireP2PGasNeed, so
// a gas drip is only ever sent for a tx its Prepare* would build. Each returns
// nil when the action is the user's to take now. A refusal on the user or the
// trade/ad state comes back as a p2pRefusal wrapping the sentinel the Prepare*
// call answers with; any other error is a read that failed.

// p2pRefusal marks a gate refusal (as opposed to a failed read). It unwraps to
// the sentinel, so handlers map it exactly as before.
type p2pRefusal struct{ err error }

func (r p2pRefusal) Error() string { return r.err.Error() }
func (r p2pRefusal) Unwrap() error { return r.err }

func refuse(err error) error { return p2pRefusal{err: err} }

func isRefusal(err error) bool {
	var r p2pRefusal
	return errors.As(err, &r)
}

// openTradeStatuses are the trade states that still hold an ad's capacity.
var openTradeStatuses = []string{models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid,
	models.P2PTradeStatusReleasing, models.P2PTradeStatusDisputed}

// canMarkPaid: the buyer, while the trade waits for payment.
func canMarkPaid(userID uuid.UUID, t *models.P2PTrade) error {
	if t.BuyerID != userID {
		return refuse(ErrForbidden)
	}
	if t.Status != models.P2PTradeStatusPendingPayment {
		return refuse(fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, t.Status))
	}
	return nil
}

// canRelease: the seller, from pending_payment or paid, or releasing (a retry).
// Symmetric with the contract: the seller's signature is the gate, not the
// buyer's markPaid.
func canRelease(userID uuid.UUID, t *models.P2PTrade) error {
	if t.SellerID != userID {
		return refuse(ErrForbidden)
	}
	switch t.Status {
	case models.P2PTradeStatusPendingPayment, models.P2PTradeStatusPaid, models.P2PTradeStatusReleasing:
		return nil
	}
	return refuse(fmt.Errorf("%w: trade status=%s", ErrInvalidTransition, t.Status))
}

// canCancelByTimeout: either party, once a pending_payment trade is past its
// deadline (the contract reverts "Timeout not elapsed" before then).
func canCancelByTimeout(userID uuid.UUID, t *models.P2PTrade, now time.Time) error {
	if t.BuyerID != userID && t.SellerID != userID {
		return refuse(ErrForbidden)
	}
	if t.Status != models.P2PTradeStatusPendingPayment {
		return refuse(fmt.Errorf("%w: trade status=%s (only pending_payment can be timed out)",
			ErrInvalidTransition, t.Status))
	}
	if !now.After(t.ExpiresAt) {
		return refuse(fmt.Errorf("%w: trade has not yet expired (expires_at=%s)",
			ErrInvalidTransition, t.ExpiresAt.Format(time.RFC3339)))
	}
	return nil
}

// canDispute: either party, from pending_payment or paid (symmetric with the
// contract: a buyer who paid but never marked paid still needs a way in).
// Returns the raiser's role.
func canDispute(userID uuid.UUID, t *models.P2PTrade) (string, error) {
	var role string
	switch userID {
	case t.BuyerID:
		role = models.P2PDisputeRoleBuyer
	case t.SellerID:
		role = models.P2PDisputeRoleSeller
	default:
		return "", refuse(ErrForbidden)
	}
	if t.Status != models.P2PTradeStatusPaid && t.Status != models.P2PTradeStatusPendingPayment {
		return "", refuse(fmt.Errorf("%w: trade status=%s (only pending or paid trades can dispute)",
			ErrInvalidTransition, t.Status))
	}
	return role, nil
}

// openTradePreflight: the ad is open to this user now, and she clears the KYC
// tier for its side.
func (s *P2PTradeService) openTradePreflight(ctx context.Context, ad *models.P2PAd, userID uuid.UUID) error {
	// An LP stock ad is opened only by the pool's own order path (Plan C1),
	// never by a buyer at another price (spec §9.4).
	if pool, err := s.isPoolAd(ctx, ad); err != nil {
		return err
	} else if pool {
		return refuse(ErrAdNotActive)
	}
	if ad.Status != models.P2PAdStatusActive {
		return refuse(ErrAdNotActive)
	}
	if ad.SellerID == userID {
		return refuse(ErrSelfTrade)
	}
	// Browse hides a suspended seller's ads; a stale screen or deep link must
	// not open one.
	if blocked, err := s.sellerBlocked(ctx, ad.SellerID); err != nil {
		return err
	} else if blocked {
		return refuse(ErrAdNotActive)
	}
	// An ad that was live before its asset was delisted can't take new trades.
	if err := s.requireOpenable(ad.ChainID, ad.Token); err != nil {
		return refuse(err)
	}
	return s.openerKYCTier(ctx, ad, userID)
}

// openerKYCTier: on a sell-ad the opener is the buyer about to send fiat, so the
// stricter money-in gate (verified AND a legal name) applies. On a buy-ad she
// sells crypto and is paid by the ad's poster, so verified is enough; she has
// not deposited when this runs, so refusing traps nothing.
func (s *P2PTradeService) openerKYCTier(ctx context.Context, ad *models.P2PAd, userID uuid.UUID) error {
	var err error
	switch {
	case ad.Side != models.P2PAdSideBuy:
		err = requireVerifiedForMoneyIn(ctx, s.db, userID)
	case ad.PMID == nil:
		// Defence in depth for non-HTTP callers: every HTTP P2P route already
		// refuses an unverified user (P2PHandler.require).
		err = requireKYCVerified(ctx, s.db, userID)
	default:
		// PM buy-ads are carved out to keep their existing behaviour (PM is
		// out of scope here); over HTTP the route check above still applies.
		return nil
	}
	if errors.Is(err, ErrKYCVerificationRequired) {
		return refuse(err)
	}
	return err
}

// cancelAdPreflight: her own active or paused ad with no open trade. The side
// decides which cancel call applies; callers check it.
func (s *P2PTradeService) cancelAdPreflight(ctx context.Context, ad *models.P2PAd, userID uuid.UUID) error {
	if ad.SellerID != userID {
		return refuse(ErrForbidden)
	}
	if ad.Status != models.P2PAdStatusActive && ad.Status != models.P2PAdStatusPaused {
		return refuse(fmt.Errorf("%w: ad status=%s", ErrInvalidTransition, ad.Status))
	}
	open, err := s.adHasOpenTrades(ctx, ad.ID)
	if err != nil {
		return err
	}
	if open {
		return refuse(ErrAdHasOpenTrades)
	}
	return nil
}

// adHasOpenTrades: some trade on the ad still holds its capacity.
func (s *P2PTradeService) adHasOpenTrades(ctx context.Context, adID uuid.UUID) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&models.P2PTrade{}).
		Where("ad_id = ? AND status IN ?", adID, openTradeStatuses).Count(&n).Error
	return n > 0, err
}
