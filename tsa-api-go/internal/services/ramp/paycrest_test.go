package ramp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

type fakePcOrders struct {
	buyReq    services.PaycrestBuyRequest
	sellReq   services.PaycrestSellRequest
	createErr error
	row       *models.PaycrestOrderRecord
	synced    bool
	disabled  error
	openErr   error
}

func (f *fakePcOrders) CreateBuy(_ context.Context, r services.PaycrestBuyRequest) (*models.PaycrestOrderRecord, error) {
	f.buyReq = r
	return f.row, f.createErr
}
func (f *fakePcOrders) CreateSell(_ context.Context, r services.PaycrestSellRequest) (*models.PaycrestOrderRecord, error) {
	f.sellReq = r
	return f.row, f.createErr
}
func (f *fakePcOrders) Get(context.Context, uuid.UUID, uuid.UUID) (*models.PaycrestOrderRecord, error) {
	return f.row, nil
}
func (f *fakePcOrders) Sync(_ context.Context, r *models.PaycrestOrderRecord) (*models.PaycrestOrderRecord, error) {
	f.synced = true
	return r, nil
}
func (f *fakePcOrders) List(context.Context, uuid.UUID, int) ([]models.PaycrestOrderRecord, error) {
	return []models.PaycrestOrderRecord{*f.row}, nil
}
func (f *fakePcOrders) ClaimPayment(context.Context, uuid.UUID, uuid.UUID) (*models.PaycrestOrderRecord, error) {
	return f.row, nil
}
func (f *fakePcOrders) Cancel(context.Context, uuid.UUID, uuid.UUID) (*models.PaycrestOrderRecord, error) {
	return f.row, nil
}
func (f *fakePcOrders) AssertNoOpenOrder(context.Context, uuid.UUID, string) error { return f.openErr }
func (f *fakePcOrders) RequirePaycrestEnabled(string) error                        { return f.disabled }
func (f *fakePcOrders) ExplainMissingVenue(_ context.Context, _, _ string, err error) error {
	return err
}

type fakePcQuotes struct {
	res  *services.PaycrestQuoteResult
	err  error
	legs map[string]services.RampLegs
}

func (f *fakePcQuotes) Quote(context.Context, string, string, string) (*services.PaycrestQuoteResult, error) {
	return f.res, f.err
}
func (f *fakePcQuotes) RampLegs(context.Context) (map[string]services.RampLegs, error) {
	return f.legs, nil
}

type fakeReg map[string]string

func (f fakeReg) RampProvider(code string) string { return f[code] }

func (f fakeReg) All() []models.Currency {
	out := make([]models.Currency, 0, len(f))
	for code := range f {
		out = append(out, models.Currency{Code: code})
	}
	return out
}

func newTestPaycrest(o *fakePcOrders, q *fakePcQuotes, reg providerRegistry) *PaycrestProvider {
	return &PaycrestProvider{orders: o, quotes: q, reg: reg}
}

func TestPaycrestQuote_DirectVenuesOnlyWithHandles(t *testing.T) {
	q := &fakePcQuotes{res: &services.PaycrestQuoteResult{MarketRate: 1380, Venues: []services.PaycrestVenue{
		{ChainKey: "base", Token: "USDC", Rate: "1397.59", TokenAmount: 0.7, SpreadPercent: 1.2},
		{ChainKey: "polygon", Token: "USDT", Rate: "1399", Kind: models.RouteKindConverted},
	}}}
	pq, err := newTestPaycrest(&fakePcOrders{}, q, nil).Quote(context.Background(), Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000"})
	if err != nil || len(pq.Venues) != 1 || pq.MarketRate != 1380 {
		t.Fatalf("%+v %v", pq, err)
	}
	v := pq.Venues[0]
	if v.Provider != ProviderPaycrest || v.Rate != "1397.59" || v.TokenAmount != 0.7 || v.Handle != EncodeHandle(ProviderPaycrest, VenueKey{"base", "USDC"}) {
		t.Fatalf("venue = %+v", v)
	}
}

func TestPaycrestQuote_KillSwitchAndErrorMapping(t *testing.T) {
	ctx := context.Background()
	req := Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000"}
	if _, err := newTestPaycrest(&fakePcOrders{disabled: services.ErrPaycrestDisabled}, &fakePcQuotes{}, nil).Quote(ctx, req); err == nil ||
		errors.Is(err, ErrNoVenue) || errors.Is(err, ErrLegClosed) {
		t.Fatalf("kill switch must read as the provider being unavailable: %v", err)
	}
	for in, want := range map[error]error{
		services.ErrPaycrestLegClosed:    ErrLegClosed,
		services.ErrPaycrestBelowMinimum: ErrBelowMinimum,
		services.ErrPaycrestNoVenue:      ErrNoVenue,
		services.ErrPaycrestNoProvider:   ErrNoVenue,
	} {
		_, err := newTestPaycrest(&fakePcOrders{}, &fakePcQuotes{err: in}, nil).Quote(ctx, req)
		if !errors.Is(err, want) || !errors.Is(err, in) {
			t.Errorf("%v → %v, want %v", in, err, want)
		}
	}
}

func TestPaycrestOpen_ForwardsTheGuard(t *testing.T) {
	o := &fakePcOrders{row: &models.PaycrestOrderRecord{}}
	p := newTestPaycrest(o, &fakePcQuotes{}, nil)
	in := OrderInput{UserID: uuid.New(), WalletAddress: "0xabc", LimitRate: "1407", Guard: services.PriceGuardLog,
		RefundBankCode: "044", RefundAccount: "0123", BankCode: "058", AccountNo: "0456"}
	if _, err := p.Open(context.Background(), VenueKey{"base", "USDC"}, Request{Side: Buy, FiatCode: "NGN", FiatAmount: "1000"}, in); err != nil {
		t.Fatal(err)
	}
	if o.buyReq.MaxRate != "1407" || o.buyReq.PriceGuard != services.PriceGuardLog || o.buyReq.ChainKey != "base" ||
		o.buyReq.RequestedChain != "base" || o.buyReq.RequestedToken != "USDC" || o.buyReq.RefundAccount != "0123" {
		t.Fatalf("buy request = %+v", o.buyReq)
	}
	if _, err := p.Open(context.Background(), VenueKey{"base", "USDC"}, Request{Side: Sell, FiatCode: "NGN", FiatAmount: "1000"}, in); err != nil {
		t.Fatal(err)
	}
	if o.sellReq.MinRate != "1407" || o.sellReq.PriceGuard != services.PriceGuardLog || o.sellReq.AccountNo != "0456" {
		t.Fatalf("sell request = %+v", o.sellReq)
	}
}

func TestPaycrestOpen_Classification(t *testing.T) {
	cases := map[string]struct {
		err   error
		class error // nil = returned as-is (infrastructure)
	}{
		"kyc":            {services.ErrKYCVerificationRequired, ErrRefusedForUser},
		"open order":     {services.ErrPaycrestOrderOpen, ErrRefusedForUser},
		"pay order open": {services.ErrPaycrestPayOrderOpen, ErrRefusedForUser},
		"no wallet":      {services.ErrBuyNeedsWallet, ErrRefusedForUser},
		"no refund":      {services.ErrBuyNeedsRefundAccount, ErrRefusedForUser},
		"no payout":      {services.ErrSellNeedsPayoutAccount, ErrRefusedForUser},
		"account shape":  {services.ErrPaycrestAccountShape, ErrRefusedForUser},
		"request sent":   {fmt.Errorf("%w: %w", services.ErrOrderRequestSent, errors.New("timeout")), ErrOutcomeUnknown},
		"nothing sent":   {fmt.Errorf("%w: %w", services.ErrPaycrestNothingSent, errors.New("rates timeout")), ErrVenueUnavailable},
		"rejected 4xx":   {fmt.Errorf("%w: %w", services.ErrPaycrestOrderRejected, errors.New("HTTP 422")), ErrVenueUnavailable},
		"buy persist":    {services.ErrBuyAttemptMayExist, ErrOutcomeUnknown},
		"sell persist":   {services.ErrSellAttemptMayExist, ErrOutcomeUnknown},
		"disabled":       {services.ErrPaycrestDisabled, ErrVenueUnavailable},
		"below minimum":  {services.ErrPaycrestBelowMinimum, ErrVenueUnavailable},
		"leg closed":     {services.ErrPaycrestLegClosed, ErrVenueUnavailable},
		"no venue":       {services.ErrPaycrestNoVenue, ErrVenueUnavailable},
		"bank":           {services.ErrPaycrestBankUnsupported, ErrVenueUnavailable},
		"price moved":    {services.ErrPriceMovedPastQuote, services.ErrPriceMovedPastQuote},
		"db down":        {errors.New("db connection refused"), nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := classifyOpenErr(tc.err)
			if !errors.Is(got, tc.err) {
				t.Fatalf("the cause must survive: %v", got)
			}
			// paycrestError shows some causes verbatim (KYC): never prefixed.
			if got.Error() != tc.err.Error() {
				t.Fatalf("message changed: %q → %q", tc.err.Error(), got.Error())
			}
			if tc.class == nil {
				for _, c := range []error{ErrRefusedForUser, ErrOutcomeUnknown, ErrVenueUnavailable} {
					if errors.Is(got, c) {
						t.Fatalf("infrastructure error classified as %v", c)
					}
				}
				return
			}
			if !errors.Is(got, tc.class) {
				t.Fatalf("%v → %v, want class %v", tc.err, got, tc.class)
			}
		})
	}
}

func TestPaycrestGet_SyncsAndRejectsNonUUID(t *testing.T) {
	o := &fakePcOrders{row: &models.PaycrestOrderRecord{}}
	p := newTestPaycrest(o, &fakePcQuotes{}, nil)
	if _, err := p.Get(context.Background(), uuid.New(), uuid.NewString()); err != nil || !o.synced {
		t.Fatalf("err=%v synced=%v", err, o.synced)
	}
	if _, err := p.Get(context.Background(), uuid.New(), "nope"); !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("non-uuid: %v", err)
	}
}

func TestPaycrestLegs_KillSwitchFilter(t *testing.T) {
	q := &fakePcQuotes{legs: map[string]services.RampLegs{"NGN": {Sell: true}, "GHS": {Buy: true}}}
	legs, err := newTestPaycrest(&fakePcOrders{}, q, fakeReg{"NGN": services.RampProviderPaycrest, "GHS": "none"}).RampLegs(context.Background())
	if err != nil || len(legs) != 1 || !legs["NGN"].Sell {
		t.Fatalf("%+v %v", legs, err)
	}
	legs, _ = newTestPaycrest(&fakePcOrders{}, q, nil).RampLegs(context.Background())
	if len(legs) != 2 {
		t.Fatalf("no registry means no filter: %+v", legs)
	}
}

func TestPaycrestCheckNoOpenOrder_KeepsItsOwnRefusal(t *testing.T) {
	p := newTestPaycrest(&fakePcOrders{openErr: services.ErrPaycrestPayOrderOpen}, &fakePcQuotes{}, nil)
	err := p.CheckNoOpenOrder(context.Background(), uuid.New(), Buy)
	if !errors.Is(err, ErrRefusedForUser) || !errors.Is(err, services.ErrPaycrestPayOrderOpen) ||
		err.Error() != services.ErrPaycrestPayOrderOpen.Error() {
		t.Fatalf("err = %v", err)
	}
	dbDown := errors.New("db down")
	err = newTestPaycrest(&fakePcOrders{openErr: dbDown}, &fakePcQuotes{}, nil).CheckNoOpenOrder(context.Background(), uuid.New(), Sell)
	if !errors.Is(err, dbDown) || errors.Is(err, ErrRefusedForUser) {
		t.Fatalf("infrastructure must not read as a refusal: %v", err)
	}
	if err := newTestPaycrest(&fakePcOrders{}, &fakePcQuotes{}, nil).CheckNoOpenOrder(context.Background(), uuid.New(), Buy); err != nil {
		t.Fatalf("nothing open: %v", err)
	}
}

// MaxLegs is both sides of every Paycrest-routed currency, buys only while
// they are not switched off.
func TestPaycrestMaxLegs(t *testing.T) {
	p := newTestPaycrest(&fakePcOrders{}, &fakePcQuotes{}, fakeReg{"NGN": services.RampProviderPaycrest, "GHS": "none"})
	got := p.MaxLegs()
	want := services.RampLegs{Buy: !services.PaycrestBuysOff, Sell: true}
	if got["NGN"] != want {
		t.Fatalf("NGN = %+v, want %+v", got["NGN"], want)
	}
	if _, ok := got["GHS"]; ok {
		t.Fatal("a currency not routed to Paycrest must not appear")
	}
}
