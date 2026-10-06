package handlers

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"github.com/ojimcy/tsa-api-go/internal/services/ramp"
)

// legsProvider is a ramp.Provider that only answers RampLegs.
type legsProvider struct {
	name string
	legs map[string]services.RampLegs
}

func (p legsProvider) Name() string { return p.name }
func (p legsProvider) RampLegs(context.Context) (map[string]services.RampLegs, error) {
	return p.legs, nil
}
func (legsProvider) Quote(context.Context, ramp.Request) (*ramp.ProviderQuote, error) {
	return nil, ramp.ErrNoVenue
}
func (legsProvider) Open(context.Context, ramp.VenueKey, ramp.Request, ramp.OrderInput) (*ramp.Order, error) {
	return nil, ramp.ErrVenueUnavailable
}
func (legsProvider) CheckNoOpenOrder(context.Context, uuid.UUID, ramp.Side) error { return nil }
func (legsProvider) Get(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return nil, ramp.ErrOrderNotFound
}
func (legsProvider) List(context.Context, uuid.UUID, int) ([]ramp.Order, error) { return nil, nil }
func (legsProvider) ClaimPayment(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return nil, ramp.ErrOrderNotFound
}
func (legsProvider) Cancel(context.Context, uuid.UUID, string) (*ramp.Order, error) {
	return nil, ramp.ErrOrderNotFound
}

// §4.5 / §11: with Paycrest's naira buy book empty, Buy reopens for NGN when
// only the company wallet holds stock.
func TestCurrencies_CompanyWalletReopensNGNBuy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newCurrencyHandler(t)
	h.SetRampLegs(ramp.NewRouter(ramp.Config{ToleranceBps: 50},
		legsProvider{name: ramp.ProviderTreasury, legs: map[string]services.RampLegs{"NGN": {Buy: true}}},
		legsProvider{name: ramp.ProviderPaycrest, legs: map[string]services.RampLegs{"NGN": {Sell: true}}},
	))
	rows := getCurrencyRows(t, h)
	if !rows["NGN"].RampBuy || !rows["NGN"].RampSell {
		t.Fatalf("NGN = %+v, want Buy (company wallet) and Sell (Paycrest)", rows["NGN"])
	}
}
