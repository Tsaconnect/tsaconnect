package services

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// P2PGasAction names the user-signed P2P transaction a top-up pays for.
type P2PGasAction string

const (
	P2PGasApprove     P2PGasAction = "approve"
	P2PGasDeposit     P2PGasAction = "deposit"
	P2PGasWithdraw    P2PGasAction = "withdraw"
	P2PGasCancelAd    P2PGasAction = "cancel_ad"
	P2PGasOpenTrade   P2PGasAction = "open_trade"
	P2PGasMarkPaid    P2PGasAction = "mark_paid"
	P2PGasRelease     P2PGasAction = "release"
	P2PGasCancelTrade P2PGasAction = "cancel_trade"
	P2PGasDispute     P2PGasAction = "dispute"
)

// p2pActionGas is a gas-unit ceiling per action on Sonic P2PEscrowV3. Every
// figure sits above the hardhat gas report's max (2026-10-09: approve 46k,
// deposit 116k, withdraw 60k, cancelVoucher 62k, openTrade 251k, markPaid 47k,
// release 106k, cancelByTimeout 59k, raiseDispute 43k) — adjust upward only:
// an action priced short leaves her a top-up that cannot pay for the tx.
var p2pActionGas = map[P2PGasAction]uint64{
	P2PGasApprove:     70_000,
	P2PGasDeposit:     150_000,
	P2PGasWithdraw:    120_000,
	P2PGasCancelAd:    90_000,
	P2PGasOpenTrade:   350_000,
	P2PGasMarkPaid:    80_000,
	P2PGasRelease:     250_000,
	P2PGasCancelTrade: 200_000,
	P2PGasDispute:     90_000,
}

// ParseP2PGasAction reads an action name from a request, ignoring case and
// surrounding space.
func ParseP2PGasAction(s string) (P2PGasAction, bool) {
	a := P2PGasAction(strings.ToLower(strings.TrimSpace(s)))
	_, ok := p2pActionGas[a]
	return a, ok
}

// ErrP2PGasUnavailable: there is no live Sonic sponsor lane, or it refused
// for reasons of ours (float, ceiling, budget). She can still add S herself.
var ErrP2PGasUnavailable = errors.New("we can't cover the network fee right now — add a little S to your wallet, or try again shortly")

// p2pGasDefaultPerDay is P2P_GAS_TOPUPS_PER_DAY's default.
const p2pGasDefaultPerDay = 6

// P2PGasSponsor drips S to a P2P user's own wallet before a tx she signs on
// Sonic. It sends through the shared Sonic sponsor lane (the same one the
// marketplace funding uses, so one key never runs two nonce sequences) with
// GasTopUp's send: only the shortfall, skipped when she already holds it, the
// lane's ceiling, float floor and daily gas budget. Its own control is a
// per-user daily count of sends, on top of the KYC the P2P routes require.
type P2PGasSponsor struct {
	sponsor *RampSponsorship
	perUser *SponsorGuard
}

// NewP2PGasSponsor is nil when sponsor has no lane, so a dormant sponsor is a
// nil pointer whose TopUp refuses with ErrP2PGasUnavailable.
func NewP2PGasSponsor(sponsor *RampSponsorship, perDay int, now func() time.Time) *P2PGasSponsor {
	if !sponsor.Enabled() || sponsor.topUp == nil {
		return nil
	}
	if perDay <= 0 {
		perDay = p2pGasDefaultPerDay
	}
	return &P2PGasSponsor{
		sponsor: sponsor,
		perUser: NewSponsorGuard("p2p_gas_user", SponsorLimits{PerUserPerWindow: perDay, Window: 24 * time.Hour}, now),
	}
}

// TopUp sends `wallet` the S the action needs at today's gas price, times
// topUpGasBuffer, minus what it already holds. wallet must be the user's own
// linked wallet (the handler passes the proven one).
func (p *P2PGasSponsor) TopUp(ctx context.Context, user *models.User, wallet string, action P2PGasAction) (GasTopUpResult, error) {
	if p == nil || p.sponsor == nil || p.sponsor.topUp == nil {
		return GasTopUpResult{}, ErrP2PGasUnavailable
	}
	if err := requireTopUpUser(user); err != nil {
		return GasTopUpResult{}, err
	}
	units, ok := p2pActionGas[action]
	if !ok {
		return GasTopUpResult{}, fmt.Errorf("%w: unknown gas action %q", ErrInvalidParams, action)
	}
	if !common.IsHexAddress(strings.TrimSpace(wallet)) || !sameAddress(wallet, user.WalletAddress) {
		return GasTopUpResult{}, ErrSponsorWallet
	}
	g := p.sponsor.topUp
	lane := g.liveLane("sonic")
	if lane == nil {
		return GasTopUpResult{}, ErrP2PGasUnavailable
	}
	need, err := lane.gasCost(units)
	if err != nil {
		return GasTopUpResult{}, fmt.Errorf("sponsor: %w", err)
	}
	need.Mul(need, big.NewInt(topUpGasBuffer))

	var slot SponsorReservation
	res, err := g.sendTopUp(ctx, topUpSend{
		lane: lane, wallet: wallet, need: need,
		ref: "p2p=" + string(action) + " user=" + user.ID.String(),
		admit: func() error {
			s, err := p.perUser.ReserveUser("p2p:" + user.ID.String())
			if errors.Is(err, ErrRateLimited) {
				return ErrTopUpDailyCap
			}
			if err != nil {
				return err
			}
			slot = s
			return nil
		},
		release: func() { p.perUser.ReleaseUser(slot) },
	})
	// Refusals of ours mean the same thing to her: add S yourself or wait.
	if errors.Is(err, ErrTopUpTooExpensive) || errors.Is(err, ErrRelayerUnderfunded) || errors.Is(err, ErrDailyCapExceeded) {
		return res, fmt.Errorf("%w (%w)", ErrP2PGasUnavailable, err)
	}
	return res, err
}
