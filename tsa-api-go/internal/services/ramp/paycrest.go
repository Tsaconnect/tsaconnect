package ramp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// The slices of the Paycrest services the adapter uses — interfaces so the
// adapter is testable without a database or the Paycrest API.
type paycrestOrders interface {
	CreateBuy(context.Context, services.PaycrestBuyRequest) (*models.PaycrestOrderRecord, error)
	CreateSell(context.Context, services.PaycrestSellRequest) (*models.PaycrestOrderRecord, error)
	Get(ctx context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error)
	Sync(context.Context, *models.PaycrestOrderRecord) (*models.PaycrestOrderRecord, error)
	List(ctx context.Context, userID uuid.UUID, limit int) ([]models.PaycrestOrderRecord, error)
	ClaimPayment(ctx context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error)
	Cancel(ctx context.Context, userID, orderID uuid.UUID) (*models.PaycrestOrderRecord, error)
	AssertNoOpenOrder(ctx context.Context, userID uuid.UUID, direction string) error
	RequirePaycrestEnabled(fiat string) error
	ExplainMissingVenue(ctx context.Context, side, fiat string, err error) error
}

type paycrestQuotes interface {
	Quote(ctx context.Context, side, fiat, amount string) (*services.PaycrestQuoteResult, error)
	RampLegs(ctx context.Context) (map[string]services.RampLegs, error)
}

type providerRegistry interface{ RampProvider(code string) string }

// PaycrestProvider adapts the existing Paycrest services. No logic lives
// here beyond mapping shapes and classifying errors.
type PaycrestProvider struct {
	orders paycrestOrders
	quotes paycrestQuotes
	reg    providerRegistry // nil: no kill-switch filter on legs
}

// NewPaycrestProvider returns a nil Provider (not a typed nil, which
// NewRouter would count as live) when either service is missing.
func NewPaycrestProvider(orders *services.PaycrestOrderService, quotes *services.PaycrestQuoteService, reg *services.CurrencyRegistry) Provider {
	if orders == nil || quotes == nil {
		return nil
	}
	p := &PaycrestProvider{orders: orders, quotes: quotes}
	if reg != nil { // a typed nil would read as a registry
		p.reg = reg
	}
	return p
}

func (p *PaycrestProvider) Name() string { return ProviderPaycrest }

func (p *PaycrestProvider) RampLegs(ctx context.Context) (map[string]services.RampLegs, error) {
	legs, err := p.quotes.RampLegs(ctx)
	if err != nil || p.reg == nil {
		return legs, err
	}
	out := make(map[string]services.RampLegs, len(legs))
	for fiat, l := range legs {
		if p.reg.RampProvider(strings.ToUpper(fiat)) == services.RampProviderPaycrest {
			out[fiat] = l
		}
	}
	return out, nil
}

func (p *PaycrestProvider) Quote(ctx context.Context, r Request) (*ProviderQuote, error) {
	if err := p.orders.RequirePaycrestEnabled(r.FiatCode); err != nil {
		return nil, fmt.Errorf("paycrest disabled for %q: %w", r.FiatCode, err)
	}
	res, err := p.quotes.Quote(ctx, string(r.Side), r.FiatCode, r.FiatAmount)
	if err != nil {
		err = p.orders.ExplainMissingVenue(ctx, string(r.Side), r.FiatCode, err)
		switch {
		case errors.Is(err, services.ErrPaycrestLegClosed):
			return nil, fmt.Errorf("%w: %w", ErrLegClosed, err)
		case errors.Is(err, services.ErrPaycrestBelowMinimum):
			return nil, fmt.Errorf("%w: %w", ErrBelowMinimum, err)
		case errors.Is(err, services.ErrPaycrestNoVenue), errors.Is(err, services.ErrPaycrestNoProvider):
			return nil, fmt.Errorf("%w: %w", ErrNoVenue, err)
		default:
			return nil, err
		}
	}
	out := &ProviderQuote{MarketRate: res.MarketRate}
	for _, v := range res.Venues {
		if v.Kind != "" && v.Kind != models.RouteKindDirect {
			continue // converted venues are out of v1 scope
		}
		out.Venues = append(out.Venues, Venue{
			Handle:        EncodeHandle(ProviderPaycrest, VenueKey{ChainKey: v.ChainKey, Token: v.Token}),
			Provider:      ProviderPaycrest,
			ChainKey:      v.ChainKey,
			Token:         v.Token,
			Rate:          v.Rate,
			SpreadPercent: v.SpreadPercent,
			TokenAmount:   v.TokenAmount,
			Kind:          models.RouteKindDirect,
		})
	}
	return out, nil
}

func (p *PaycrestProvider) Open(ctx context.Context, k VenueKey, r Request, in OrderInput) (*Order, error) {
	var row *models.PaycrestOrderRecord
	var err error
	if r.Side == Sell {
		row, err = p.orders.CreateSell(ctx, services.PaycrestSellRequest{
			UserID: in.UserID, WalletAddress: in.WalletAddress, FiatCode: r.FiatCode, FiatAmount: r.FiatAmount,
			ChainKey: k.ChainKey, Token: k.Token,
			BankCode: in.BankCode, AccountNo: in.AccountNo, AccountName: in.AccountName,
			MinRate: in.LimitRate, PriceGuard: in.Guard,
		})
	} else {
		row, err = p.orders.CreateBuy(ctx, services.PaycrestBuyRequest{
			UserID: in.UserID, WalletAddress: in.WalletAddress, FiatCode: r.FiatCode, FiatAmount: r.FiatAmount,
			ChainKey: k.ChainKey, Token: k.Token,
			// A direct buy: what she gets is exactly what Paycrest fills.
			RequestedChain: k.ChainKey, RequestedToken: k.Token,
			RefundBankCode: in.RefundBankCode, RefundAccount: in.RefundAccount, RefundAccountName: in.RefundAccountName,
			MaxRate: in.LimitRate, PriceGuard: in.Guard,
		})
	}
	if err != nil {
		return nil, classifyOpenErr(err)
	}
	return &Order{Provider: ProviderPaycrest, Paycrest: row}, nil
}

// classifyOpenErr wraps a CreateBuy/CreateSell error in its outcome class
// (spec §4.3). The cause always survives for paycrestError's wording.
func classifyOpenErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, services.ErrOrderRequestSent),
		errors.Is(err, services.ErrBuyAttemptMayExist),
		errors.Is(err, services.ErrSellAttemptMayExist):
		return WrapClass(ErrOutcomeUnknown, err)
	case errors.Is(err, services.ErrPriceMovedPastQuote):
		return err
	case errors.Is(err, services.ErrKYCVerificationRequired),
		errors.Is(err, services.ErrPaycrestOrderOpen), // also matches ErrPaycrestPayOrderOpen
		errors.Is(err, services.ErrPaycrestPayOrderOpen),
		errors.Is(err, services.ErrBuyNeedsWallet),
		errors.Is(err, services.ErrBuyNeedsRefundAccount),
		errors.Is(err, services.ErrSellNeedsWallet),
		errors.Is(err, services.ErrSellNeedsPayoutAccount),
		errors.Is(err, services.ErrPaycrestAccountShape):
		return WrapClass(ErrRefusedForUser, err)
	case errors.Is(err, services.ErrPaycrestDisabled),
		errors.Is(err, services.ErrPaycrestBelowMinimum),
		errors.Is(err, services.ErrPaycrestLegClosed),
		errors.Is(err, services.ErrPaycrestNoVenue),
		errors.Is(err, services.ErrPaycrestNoProvider),
		errors.Is(err, services.ErrPaycrestBankUnsupported),
		errors.Is(err, services.ErrPaycrestOrderRejected): // a 400/422: nothing opened
		return WrapClass(ErrVenueUnavailable, err)
	default:
		// Infrastructure (e.g. the KYC read failing) and pre-create provider
		// errors with no sentinel (a /v2/rates transport error in VenueFor, a
		// bank-directory failure): v1 deliberately does not fail over on
		// these — with one provider there is nowhere to go. The Flutterwave
		// adapter must not land until VenueFor/directory errors carry a
		// "nothing sent" sentinel (spec §9).
		return err
	}
}

func (p *PaycrestProvider) CheckNoOpenOrder(ctx context.Context, userID uuid.UUID, side Side) error {
	dir := models.PaycrestDirectionBuy
	if side == Sell {
		dir = models.PaycrestDirectionSell
	}
	err := p.orders.AssertNoOpenOrder(ctx, userID, dir)
	if errors.Is(err, services.ErrPaycrestOrderOpen) { // also ErrPaycrestPayOrderOpen
		return WrapClass(ErrRefusedForUser, err)
	}
	return err
}

func parseNative(id string) (uuid.UUID, error) {
	u, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil {
		return uuid.Nil, ErrOrderNotFound
	}
	return u, nil
}

func (p *PaycrestProvider) Get(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error) {
	id, err := parseNative(nativeID)
	if err != nil {
		return nil, err
	}
	row, err := p.orders.Get(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	// As GET /paycrest/orders/:id: refresh, but a sync failure never hides
	// the order we already have.
	if fresh, serr := p.orders.Sync(ctx, row); serr == nil && fresh != nil {
		row = fresh
	}
	return &Order{Provider: ProviderPaycrest, Paycrest: row}, nil
}

func (p *PaycrestProvider) List(ctx context.Context, userID uuid.UUID, limit int) ([]Order, error) {
	rows, err := p.orders.List(ctx, userID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]Order, len(rows))
	for i := range rows {
		out[i] = Order{Provider: ProviderPaycrest, Paycrest: &rows[i]}
	}
	return out, nil
}

func (p *PaycrestProvider) ClaimPayment(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error) {
	id, err := parseNative(nativeID)
	if err != nil {
		return nil, err
	}
	row, err := p.orders.ClaimPayment(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return &Order{Provider: ProviderPaycrest, Paycrest: row}, nil
}

func (p *PaycrestProvider) Cancel(ctx context.Context, userID uuid.UUID, nativeID string) (*Order, error) {
	id, err := parseNative(nativeID)
	if err != nil {
		return nil, err
	}
	row, err := p.orders.Cancel(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	return &Order{Provider: ProviderPaycrest, Paycrest: row}, nil
}
