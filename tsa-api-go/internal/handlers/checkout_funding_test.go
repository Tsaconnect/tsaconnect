package handlers

import (
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// checkout_funding_test.go — the HTTP side of "pay with another token": a
// checkout funded by swap is created without the shortfall refusal and as one
// group, and every funding route requires her PROVEN wallet as the source.

type fakeFunding struct {
	startErr  error
	quotes    int
	lastGroup uuid.UUID
	lastSrc   services.FundingSource
	lastTotal *big.Int
	err       error
}

func (f *fakeFunding) Enabled() bool { return true }
func (f *fakeFunding) Quote(_ context.Context, _ *models.User, g uuid.UUID, src services.FundingSource) (*services.FundingQuote, error) {
	f.quotes++
	f.lastGroup, f.lastSrc = g, src
	return &services.FundingQuote{GroupID: g.String()}, f.err
}
func (f *fakeFunding) QuoteTotal(_ context.Context, _ *models.User, _ string, total *big.Int, _ int, src services.FundingSource) (*services.FundingQuote, error) {
	f.quotes++
	f.lastTotal, f.lastSrc = total, src
	return &services.FundingQuote{}, f.err
}
func (f *fakeFunding) CanStartCheckout(context.Context, uuid.UUID) error { return f.startErr }
func (f *fakeFunding) Start(context.Context, *models.User, uuid.UUID, services.FundingSource) (*services.FundingStart, error) {
	return nil, f.err
}
func (f *fakeFunding) Get(context.Context, uuid.UUID, uuid.UUID) (*models.RampSellIntent, error) {
	return nil, f.err
}
func (f *fakeFunding) Open(context.Context, uuid.UUID) (*models.RampSellIntent, error) {
	return nil, nil
}
func (f *fakeFunding) RecordSwap(context.Context, *models.User, uuid.UUID, string) error {
	return f.err
}
func (f *fakeFunding) Confirm(context.Context, *models.User, uuid.UUID) (*services.FundingStatus, error) {
	return nil, f.err
}
func (f *fakeFunding) Abandon(context.Context, *models.User, uuid.UUID) error { return f.err }
func (f *fakeFunding) SourceGasTopUp(context.Context, *models.User, uuid.UUID) (services.GasTopUpResult, error) {
	return services.GasTopUpResult{}, f.err
}
func (f *fakeFunding) SonicGasTopUp(context.Context, *models.User, uuid.UUID) (services.GasTopUpResult, error) {
	return services.GasTopUpResult{}, f.err
}

const fundingBuyerWallet = "0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce"

// ginTestContext is a POST with no body, as buyer, on /:id.
func ginTestContext(buyer *models.User, id string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	c.Params = gin.Params{{Key: "id", Value: id}}
	c.Request, _ = http.NewRequest("POST", "/", nil)
	return c, w
}

// fundingCheckout seeds a one-item USDC cart for a buyer whose wallet holds
// 0.02 USDC — short of the order — and returns the handler and buyer.
func fundingCheckout(t *testing.T) (*CheckoutHandler, *models.User) {
	t.Helper()
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	ch.walletBalances = fakeWalletBalances{balance: big.NewInt(20_000)}
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, fundingBuyerWallet)
	buyer.WalletAddress = fundingBuyerWallet
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 0.08)
	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 0.08, AddedAt: time.Now()},
	})
	return ch, buyer
}

var fundingCart = map[string]interface{}{"token": "USDC", "buyerCity": "Lagos", "buyerState": "Lagos", "buyerCountry": "Nigeria"}

func withBody(extra map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for k, v := range fundingCart {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// A buyer funding by swap is short of the order token by design: the orders
// are created (as one group) and the funding is sized from them.
func TestCreateOrderFromCart_FundWithSwapSkipsTheShortfallAndGroups(t *testing.T) {
	ch, buyer := fundingCheckout(t)
	ch.WithFunding(&fakeFunding{})
	mustRecordWalletProof(t, config.DB, buyer.ID, fundingBuyerWallet)
	w, data := callOrderHandler(t, ch.CreateOrderFromCart, buyer, withBody(map[string]interface{}{"fundWithSwap": true}))
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	group, _ := data["checkoutGroupId"].(string)
	if _, err := uuid.Parse(group); err != nil {
		t.Fatalf("checkoutGroupId = %q", group)
	}
	var grouped int64
	config.DB.Model(&models.Order{}).Where("buyer_id = ? AND checkout_group_id = ? AND fund_with_swap = ?", buyer.ID, group, true).Count(&grouped)
	if grouped != 1 {
		t.Fatalf("funded orders in the group = %d, want 1", grouped)
	}
}

// Orders created to be funded by swap hold stock with no funds behind them,
// so the hold is earned: her PROVEN wallet, and at most one such checkout of
// hers awaiting payment (or an open funding) at a time.
func TestCreateOrderFromCart_FundWithSwapStockHoldLimits(t *testing.T) {
	t.Run("an unproven wallet is refused", func(t *testing.T) {
		ch, buyer := fundingCheckout(t)
		ch.WithFunding(&fakeFunding{})
		ensureWalletLinkChallenges(t, config.DB)
		w, _ := callOrderHandler(t, ch.CreateOrderFromCart, buyer, withBody(map[string]interface{}{"fundWithSwap": true}))
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), WalletProofRequiredCode) {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("a second funded checkout awaiting payment is refused", func(t *testing.T) {
		ch, buyer := fundingCheckout(t)
		ch.WithFunding(&fakeFunding{})
		mustRecordWalletProof(t, config.DB, buyer.ID, fundingBuyerWallet)
		if err := config.DB.Exec(`INSERT INTO orders (id, buyer_id, seller_id, product_id, token, product_amount, total_amount, status, fund_with_swap)
			VALUES (?, ?, ?, ?, 'USDC', '1', '1', 'pending_payment', 1)`, uuid.NewString(), buyer.ID, uuid.NewString(), uuid.NewString()).Error; err != nil {
			t.Fatal(err)
		}
		w, _ := callOrderHandler(t, ch.CreateOrderFromCart, buyer, withBody(map[string]interface{}{"fundWithSwap": true}))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "funding_in_progress") {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
		var orders int64
		config.DB.Model(&models.Order{}).Where("buyer_id = ?", buyer.ID).Count(&orders)
		if orders != 1 {
			t.Fatalf("orders = %d: a refused checkout must take no stock", orders)
		}
	})
	t.Run("an open funding is refused before any order exists", func(t *testing.T) {
		ch, buyer := fundingCheckout(t)
		ch.WithFunding(&fakeFunding{startErr: services.ErrFundingInProgress})
		mustRecordWalletProof(t, config.DB, buyer.ID, fundingBuyerWallet)
		w, _ := callOrderHandler(t, ch.CreateOrderFromCart, buyer, withBody(map[string]interface{}{"fundWithSwap": true}))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "funding_in_progress") {
			t.Fatalf("got %d %s", w.Code, w.Body.String())
		}
	})
}

// Without the funding wired, the flag changes nothing: a short wallet is
// still refused before any order takes stock.
func TestCreateOrderFromCart_FundWithSwapNeedsTheFunding(t *testing.T) {
	ch, buyer := fundingCheckout(t)
	w, _ := callOrderHandler(t, ch.CreateOrderFromCart, buyer, withBody(map[string]interface{}{"fundWithSwap": true}))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"insufficient_balance"`) {
		t.Fatalf("expected 400 insufficient_balance, got %d: %s", w.Code, w.Body.String())
	}
}

// Every funding route pays from her PROVEN wallet: an unproven one is refused
// with the code the app answers by proving it.
func TestFundingRoutes_RequireTheProvenWallet(t *testing.T) {
	ch, buyer := fundingCheckout(t)
	f := &fakeFunding{}
	ch.WithFunding(f)
	ensureWalletLinkChallenges(t, config.DB)
	body := map[string]interface{}{"groupId": uuid.NewString(), "sourceChainId": 56, "sourceToken": "0x55d398326f99059fF775485246999027B3197955"}
	w, _ := callOrderHandler(t, ch.FundingQuote, buyer, body)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), WalletProofRequiredCode) {
		t.Fatalf("unproven wallet: %d %s", w.Code, w.Body.String())
	}
	w, _ = callOrderHandler(t, ch.FundingCartQuote, buyer, withBody(map[string]interface{}{"sourceChainId": 56, "sourceToken": "0x55d398326f99059fF775485246999027B3197955"}))
	if w.Code != http.StatusForbidden {
		t.Fatalf("unproven wallet (cart quote): %d %s", w.Code, w.Body.String())
	}
	if f.quotes != 0 {
		t.Fatal("nothing may be quoted for an unproven wallet")
	}

	mustRecordWalletProof(t, config.DB, buyer.ID, fundingBuyerWallet)
	w, _ = callOrderHandler(t, ch.FundingQuote, buyer, body)
	if w.Code != http.StatusOK || f.lastSrc.Address != fundingBuyerWallet || f.lastSrc.ChainID != 56 {
		t.Fatalf("proven wallet: %d %s src=%+v", w.Code, w.Body.String(), f.lastSrc)
	}
	// A source address that is not her wallet is refused, whatever else holds.
	body["sourceAddress"] = "0x0000000000000000000000000000000000000009"
	if w, _ = callOrderHandler(t, ch.FundingQuote, buyer, body); w.Code != http.StatusForbidden {
		t.Fatalf("another source address: %d %s", w.Code, w.Body.String())
	}
}

// The cart quote sizes the funding from the SERVER's price of the cart — the
// same total order creation would charge — never from a client figure.
func TestFundingCartQuote_TotalIsTheServersCartPrice(t *testing.T) {
	ch, buyer := fundingCheckout(t)
	f := &fakeFunding{}
	ch.WithFunding(f)
	mustRecordWalletProof(t, config.DB, buyer.ID, fundingBuyerWallet)
	_, quote := callOrderHandler(t, ch.QuoteCart, buyer, fundingCart)
	totals, _ := quote["totals"].(map[string]interface{})
	w, _ := callOrderHandler(t, ch.FundingCartQuote, buyer, withBody(map[string]interface{}{
		"sourceChainId": 56, "sourceToken": "0x55d398326f99059fF775485246999027B3197955", "orderTotal": "1",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("cart quote: %d %s", w.Code, w.Body.String())
	}
	if f.lastTotal == nil || f.lastTotal.String() != totals["total"] {
		t.Fatalf("funding sized from %v, want the cart quote's total %v", f.lastTotal, totals["total"])
	}
}

func TestFundingError_MapsTheCheckoutsWords(t *testing.T) {
	for err, want := range map[error]struct {
		code int
		key  string
	}{
		services.ErrFundingTooCostly:    {http.StatusUnprocessableEntity, "funding_too_costly"},
		services.ErrFundingNeedsGas:     {http.StatusUnprocessableEntity, "funding_needs_gas"},
		services.ErrFundingNotNeeded:    {http.StatusConflict, "funding_not_needed"},
		services.ErrFundingGroup:        {http.StatusConflict, "order_not_payable"},
		services.ErrSwapNotLanded:       {http.StatusConflict, sellCodeSwapPending},
		services.ErrFundingUnavailable:  {http.StatusServiceUnavailable, "funding_unavailable"},
		services.ErrFundingInProgress:   {http.StatusConflict, "funding_in_progress"},
		services.ErrFundingKYC:          {http.StatusForbidden, services.KYCVerificationRequiredCode},
		services.ErrFundingShortBalance: {http.StatusUnprocessableEntity, "funding_short_balance"},
		services.ErrFundingSourceGas:    {http.StatusUnprocessableEntity, "funding_needs_source_gas"},
		services.ErrFundingKeepS:        {http.StatusUnprocessableEntity, "funding_keep_s"},
		&services.SwapEndedShortError{Status: services.LiFiStatusDone, Substatus: services.LiFiSubstatusRefunded}: {http.StatusConflict, sellCodeSwapRefunded},
	} {
		ch, buyer := fundingCheckout(t)
		mustRecordWalletProof(t, config.DB, buyer.ID, fundingBuyerWallet)
		ch.WithFunding(&fakeFunding{err: err})
		w, _ := callOrderHandler(t, ch.FundingQuote, buyer, map[string]interface{}{
			"groupId": uuid.NewString(), "sourceChainId": 56, "sourceToken": "0x55d398326f99059fF775485246999027B3197955"})
		if w.Code != want.code || !strings.Contains(w.Body.String(), want.key) {
			t.Fatalf("%v: %d %s, want %d %s", err, w.Code, w.Body.String(), want.code, want.key)
		}
	}
	// A Sonic top-up we cannot send says so, with its own code.
	ch, buyer := fundingCheckout(t)
	ch.WithFunding(&fakeFunding{err: services.ErrDestGasUnavailable})
	c, w := ginTestContext(buyer, uuid.NewString())
	ch.FundingSonicGas(c)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "gas_unavailable") {
		t.Fatalf("sonic gas: %d %s", w.Code, w.Body.String())
	}
}
