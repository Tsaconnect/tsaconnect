package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

const (
	// offerValiditySlack keeps the signed window inside the Paycrest order's
	// own expiry, so a relay that lands late still lands before the order dies.
	offerValiditySlack = 30 * time.Second
	// offerMaxValidity bounds how long a signed authorization is replayable.
	offerMaxValidity = 25 * time.Minute
)

type SponsorshipOffer struct {
	Mode         SponsorMode   `json:"mode"`
	Chain        string        `json:"chain"`
	Token        string        `json:"token"`
	TokenAddress string        `json:"tokenAddress,omitempty"`
	Decimals     int           `json:"decimals,omitempty"`
	From         string        `json:"from,omitempty"`
	To           string        `json:"to,omitempty"`
	Value        string        `json:"value,omitempty"` // token base units
	ValidBefore  uint64        `json:"validBefore,omitempty"`
	Domain       *EIP712Domain `json:"domain,omitempty"`
}

type SponsorFloat struct {
	Chain      string
	Address    string
	BalanceWei *big.Int
	FloorWei   *big.Int
}

// RampSponsorship is the one surface the HTTP layer and the float monitor
// see. It decides HOW an order is sponsored; the relayer and top-up services
// each re-validate before spending anything, so a stale offer cannot be used
// to push a relay past their checks.
type RampSponsorship struct {
	orders  sponsorOrderReader
	lanes   map[string]*SponsorLane
	relayer *AuthorizationRelayer
	topUp   *GasTopUp
	now     func() time.Time
}

func NewRampSponsorship(orders sponsorOrderReader, store sponsorshipStore, lanes map[string]*SponsorLane, topUpUnusedCap int, receiptTimeout time.Duration, now func() time.Time) *RampSponsorship {
	if now == nil {
		now = time.Now
	}
	return &RampSponsorship{
		orders:  orders,
		lanes:   lanes,
		relayer: NewAuthorizationRelayer(orders, store, lanes, now),
		topUp:   NewGasTopUp(orders, store, lanes, topUpUnusedCap, receiptTimeout, now),
		now:     now,
	}
}

func (s *RampSponsorship) Enabled() bool { return s != nil && len(s.lanes) > 0 }

// Routes lists what this caller would be sponsored on, so the sell screen can
// stop warning about gas BEFORE an order exists. Top-up routes are omitted
// for unverified users: they would be refused at send time.
func (s *RampSponsorship) Routes(user *models.User) []SponsorRoute {
	if !s.Enabled() {
		return nil
	}
	verified := user != nil && user.VerificationStatus == models.VerificationStatusVerified
	out := []SponsorRoute{}
	for _, r := range RampSponsorRoutes() {
		lane := s.lanes[strings.ToLower(r.Chain)]
		if lane == nil || lane.Guard.Killed() || lane.tokenAddress(r.Token) == "" {
			continue
		}
		if r.Mode == SponsorModeTopUp && !verified {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Chain+out[i].Token < out[j].Chain+out[j].Token })
	return out
}

func (s *RampSponsorship) Offer(ctx context.Context, user *models.User, orderID uuid.UUID) (SponsorshipOffer, error) {
	if user == nil {
		return SponsorshipOffer{}, ErrSponsorWallet
	}
	order, err := s.orders.Get(ctx, user.ID, orderID)
	if err != nil {
		return SponsorshipOffer{}, err
	}
	none := SponsorshipOffer{Mode: SponsorModeNone, Chain: order.ChainKey, Token: order.Token}
	mode := RampSponsorMode(order.ChainKey, order.Token)
	lane := s.lanes[strings.ToLower(order.ChainKey)]
	if !s.Enabled() || mode == SponsorModeNone || lane == nil || lane.Guard.Killed() || lane.tokenAddress(order.Token) == "" {
		return none, nil
	}
	now := s.now()
	if err := sponsorableOrder(order, user.WalletAddress, now); err != nil {
		return SponsorshipOffer{}, err
	}
	mode = lane.orderMode(ctx, order)

	if mode == SponsorModeTopUp {
		if user.VerificationStatus != models.VerificationStatusVerified {
			return none, nil
		}
		return SponsorshipOffer{Mode: mode, Chain: lane.ChainKey, Token: strings.ToUpper(order.Token)}, nil
	}

	validBefore := order.ValidUntil.Add(-offerValiditySlack)
	if latest := now.Add(offerMaxValidity); validBefore.After(latest) {
		validBefore = latest
	}
	if validBefore.Before(now.Add(2 * minAuthorizationWindow)) {
		return none, nil // not enough time to sign and relay; self-pay is safer
	}

	token := lane.tokenAddress(order.Token)
	domain, err := s.relayer.Domain(ctx, lane.ChainKey, order.Token)
	if err != nil {
		log.Printf("[ramp-sponsor] ALERT ramp_sponsor_domain_unverified_total=1 chain=%s token=%s err=%v", lane.ChainKey, order.Token, err)
		return none, nil
	}
	decimals, err := lane.Native.GetTokenDecimals(token)
	if err != nil {
		return SponsorshipOffer{}, fmt.Errorf("sponsor: token decimals: %w", err)
	}
	value, err := DecimalToBaseUnits(order.TokenAmount, decimals)
	if err != nil {
		return SponsorshipOffer{}, fmt.Errorf("sponsor: order amount: %w", err)
	}
	return SponsorshipOffer{
		Mode:         mode,
		Chain:        lane.ChainKey,
		Token:        strings.ToUpper(order.Token),
		TokenAddress: common.HexToAddress(token).Hex(),
		Decimals:     decimals,
		From:         common.HexToAddress(order.WalletAddress).Hex(),
		To:           common.HexToAddress(order.DepositAddress).Hex(),
		Value:        value.String(),
		ValidBefore:  uint64(validBefore.Unix()),
		Domain:       &domain,
	}, nil
}

func (s *RampSponsorship) RelayAuthorization(ctx context.Context, user *models.User, orderID uuid.UUID, auth TransferAuthorization) (AuthorizationRelayResult, error) {
	if user == nil {
		return AuthorizationRelayResult{}, ErrSponsorWallet
	}
	return s.relayer.Relay(ctx, user.ID, user.WalletAddress, orderID, auth)
}

func (s *RampSponsorship) TopUp(ctx context.Context, user *models.User, orderID uuid.UUID) (GasTopUpResult, error) {
	return s.topUp.TopUp(ctx, user, orderID)
}

// FloatBalances reads every lane, carrying on past a failed chain so one bad
// RPC does not hide another chain running dry.
func (s *RampSponsorship) FloatBalances(ctx context.Context) ([]SponsorFloat, error) {
	var out []SponsorFloat
	var errs []error
	for chain, lane := range s.lanes {
		addr := lane.Signer.Address()
		bal, err := lane.Chain.BalanceAt(ctx, common.HexToAddress(addr), nil)
		if err != nil {
			errs = append(errs, fmt.Errorf("sponsor float %s: %w", chain, err))
			continue
		}
		out = append(out, SponsorFloat{Chain: chain, Address: addr, BalanceWei: bal, FloorWei: lane.Guard.Floor()})
	}
	return out, errors.Join(errs...)
}
