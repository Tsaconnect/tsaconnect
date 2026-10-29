package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
)

// listingPaid is what the buyer pays for the listing itself: the escrowed productAmount. OrderEscrow
// takes the platform (merchant) fee from the seller at release, so it is never added, and the
// amount must equal the listing price.
func listingPaid(t *testing.T, productAmount, platformFee interface{}) string {
	t.Helper()
	p, ok1 := new(big.Int).SetString(fmt.Sprint(productAmount), 10)
	f, ok2 := new(big.Int).SetString(fmt.Sprint(platformFee), 10)
	if !ok1 || !ok2 || f.Cmp(p) > 0 {
		t.Fatalf("amounts: %v / %v", productAmount, platformFee)
	}
	return p.String()
}

// callOrderHandler runs a cart-order handler for buyer with a JSON body and
// returns the recorder and the decoded `data` object.
func callOrderHandler(t *testing.T, h gin.HandlerFunc, buyer *models.User, body map[string]interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", buyer)
	raw, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	data, _ := resp["data"].(map[string]interface{})
	return w, data
}

// The confirm screen shows the quote; the wallet is asked for the order total
// plus the gas fee. Those must be the same number, and quoting must not create
// orders or move stock.
func TestQuoteCart_MatchesWhatTheOrderCharges(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0) // same_state shipping $5

	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 2, Price: 10.0, AddedAt: time.Now()},
	})
	body := map[string]interface{}{"token": "USDC", "buyerCity": "Lagos", "buyerState": "Lagos", "buyerCountry": "Nigeria"}

	w, quote := callOrderHandler(t, ch.QuoteCart, buyer, body)
	if w.Code != http.StatusOK {
		t.Fatalf("quote: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	totals := quote["totals"].(map[string]interface{})
	// $20 of product escrowed (the seller pays 2% of it, 0.40, at release), $10 shipping and the
	// buyer's $0.10 flat fee, at 6dp.
	want := map[string]string{
		"productAmount": "20000000", "shippingAmount": "10000000",
		"platformFee": "400000", "gasFee": "100000", "total": "30100000",
	}
	for k, v := range want {
		if totals[k] != v {
			t.Errorf("totals.%s = %v, want %s", k, totals[k], v)
		}
	}
	if quote["usdPerToken"] != 1.0 || quote["decimals"] != 6.0 {
		t.Errorf("usdPerToken/decimals = %v/%v, want 1/6", quote["usdPerToken"], quote["decimals"])
	}
	line := quote["lines"].([]interface{})[0].(map[string]interface{})
	if line["sellerName"] != seller.Name || line["shippingZone"] != "same_state" {
		t.Errorf("line seller/zone = %v/%v", line["sellerName"], line["shippingZone"])
	}

	var orderCount int64
	db.Model(&models.Order{}).Count(&orderCount)
	var stock int
	db.Model(&models.Product{}).Select("stock").Where("id = ?", product.ID).Scan(&stock)
	if orderCount != 0 || stock != 100 {
		t.Fatalf("quote wrote state: %d orders, stock %d", orderCount, stock)
	}

	w, created := callOrderHandler(t, ch.CreateOrderFromCart, buyer, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	order := created["orders"].([]interface{})[0].(map[string]interface{})
	// Order.TotalAmount excludes gas; the approve adds it back.
	if order["totalAmount"] != "30000000" {
		t.Errorf("order totalAmount = %v, want 30000000 (quote total minus gas)", order["totalAmount"])
	}
}

func TestQuoteCart_RejectsUnsupportedToken(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")

	w, _ := callOrderHandler(t, ch.QuoteCart, buyer, map[string]interface{}{"token": "DAI"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// Two cart lines for the same product are each priced against the starting
// stock; the decrement itself must refuse to oversell.
func TestCreateOrderFromCart_DuplicateLinesCannotOversell(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0) // stock 100

	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 60, Price: 10.0, AddedAt: time.Now()},
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 60, Price: 10.0, AddedAt: time.Now()},
	})

	w, _ := callOrderHandler(t, ch.CreateOrderFromCart, buyer, map[string]interface{}{"token": "USDC"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var stock int
	db.Model(&models.Product{}).Select("stock").Where("id = ?", product.ID).Scan(&stock)
	if stock != 100 {
		t.Errorf("stock = %d after a refused order, want 100 (rolled back)", stock)
	}
}

// The merchant fee is the contract's: product * bps / 10000, rounded down, charged to the seller.
func TestEscrowFeeOn(t *testing.T) {
	for _, c := range []struct {
		amount string
		bps    int
		want   string
	}{{"20000000", 200, "400000"}, {"20000000", 150, "300000"}, {"1", 200, "0"}, {"5000000000000000000", 0, "0"}} {
		amount, _ := new(big.Int).SetString(c.amount, 10)
		if got := escrowFeeOn(amount, c.bps).String(); got != c.want {
			t.Errorf("%s @%d = %s, want %s", c.amount, c.bps, got, c.want)
		}
	}
}

// fakeEscrowRates stands in for FeeConfig's order row and limit.
type fakeEscrowRates struct {
	bps      int
	gas      int64
	maxOrder int64 // 0: no limit to speak of
	feeErr   error
}

func (f fakeEscrowRates) PlatformFeeBPS(context.Context, string) (int, error) { return f.bps, f.feeErr }
func (f fakeEscrowRates) GasFeeWei(context.Context, string) (*big.Int, error) {
	return big.NewInt(f.gas), nil
}
func (f fakeEscrowRates) MaxOrderAmount(context.Context, string) (*big.Int, error) {
	if f.maxOrder == 0 {
		return new(big.Int).Lsh(big.NewInt(1), 127), nil
	}
	return big.NewInt(f.maxOrder), nil
}

type fakeMCGPPrice struct{ usd6 int64 } // USD (6dp) per 1 MCGP

func (f fakeMCGPPrice) GetBuyPricePerToken() (*big.Int, error) { return big.NewInt(f.usd6), nil }

// seedQuoteCart makes a buyer with one $10 x2 product line (same_state shipping $5).
func seedQuoteCart(t *testing.T) *models.User {
	t.Helper()
	db := setupCheckoutTestDB(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 10.0)
	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 2, Price: 10.0, AddedAt: time.Now()},
	})
	return buyer
}

var lagos = map[string]interface{}{"buyerCity": "Lagos", "buyerState": "Lagos", "buyerCountry": "Nigeria"}

func withToken(token string, extra map[string]interface{}) map[string]interface{} {
	body := map[string]interface{}{"token": token}
	for k, v := range lagos {
		body[k] = v
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// The quote uses FeeConfig's rates: a 1.5% merchant fee (the seller's, not added to the total)
// and a $0.05 flat fee show up in the amounts.
func TestQuoteCart_UsesTheEscrowsRates(t *testing.T) {
	buyer := seedQuoteCart(t)
	ch := setupCheckoutHandler(t)
	ch.feeRates = fakeEscrowRates{bps: 150, gas: 50000}

	w, quote := callOrderHandler(t, ch.QuoteCart, buyer, withToken("USDC", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("quote: %d %s", w.Code, w.Body.String())
	}
	totals := quote["totals"].(map[string]interface{})
	// $20 listing (the seller pays 1.5% of it at release); $10 shipping; $0.05 flat fee.
	want := map[string]string{"productAmount": "20000000", "platformFee": "300000", "gasFee": "50000", "total": "30050000"}
	for k, v := range want {
		if totals[k] != v {
			t.Errorf("totals.%s = %v, want %s", k, totals[k], v)
		}
	}
}

// An order above FeeConfig's maxPerOrder is refused before the buyer signs, and a token FeeConfig
// does not price cannot be checked out in.
func TestQuoteCart_RefusesWhatCreateOrderWouldRevert(t *testing.T) {
	buyer := seedQuoteCart(t)
	ch := setupCheckoutHandler(t)
	ch.feeRates = fakeEscrowRates{bps: 200, gas: 50000, maxOrder: 29_000000} // $20 + $10 shipping is $30
	if w, _ := callOrderHandler(t, ch.QuoteCart, buyer, withToken("USDC", nil)); w.Code != http.StatusBadRequest {
		t.Fatalf("over the order limit: %d %s", w.Code, w.Body.String())
	}
	ch.feeRates = fakeEscrowRates{feeErr: services.ErrOrderFeeNotConfigured}
	if w, _ := callOrderHandler(t, ch.QuoteCart, buyer, withToken("USDC", nil)); w.Code != http.StatusBadRequest {
		t.Fatalf("token not priced: %d %s", w.Code, w.Body.String())
	}
	ch.feeRates = nil
	if w, _ := callOrderHandler(t, ch.QuoteCart, buyer, withToken("USDC", nil)); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("no FeeConfig reader: %d %s", w.Code, w.Body.String())
	}
}

// A rate the escrow would refuse must stop checkout, not quote an order that
// can never be escrowed.
func TestQuoteCart_RefusesARateTheEscrowWouldRevert(t *testing.T) {
	buyer := seedQuoteCart(t)
	ch := setupCheckoutHandler(t)
	ch.feeRates = fakeEscrowRates{feeErr: services.ErrEscrowFeeAboveMax}

	w, _ := callOrderHandler(t, ch.QuoteCart, buyer, withToken("USDC", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// MCGP is priced through the OTC rate read once, and the quote reports that
// same rate for the app's local-currency estimate.
func TestQuoteCart_MCGP(t *testing.T) {
	buyer := seedQuoteCart(t)
	ch := setupCheckoutHandler(t)
	ch.otcService = fakeMCGPPrice{usd6: 250000} // $0.25 per MCGP
	ch.spendable = &fakeSpendablePayer{}        // MCGP checkout needs the Spendable MCGP path wired

	w, quote := callOrderHandler(t, ch.QuoteCart, buyer, withToken("MCGP", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("quote: %d %s", w.Code, w.Body.String())
	}
	totals := quote["totals"].(map[string]interface{})
	// $20 → 80 MCGP, $10 → 40 MCGP; no fee, no gas for MCGP.
	want := map[string]string{"productAmount": "80000000000000000000", "shippingAmount": "40000000000000000000", "platformFee": "0", "gasFee": "0", "total": "120000000000000000000"}
	for k, v := range want {
		if totals[k] != v {
			t.Errorf("totals.%s = %v, want %s", k, totals[k], v)
		}
	}
	if quote["usdPerToken"] != 0.25 || quote["decimals"] != 18.0 {
		t.Errorf("usdPerToken/decimals = %v/%v, want 0.25/18", quote["usdPerToken"], quote["decimals"])
	}
}

// Order creation refuses a total other than the one the buyer confirmed, and
// creates nothing.
func TestCreateOrderFromCart_RefusesAChangedTotal(t *testing.T) {
	buyer := seedQuoteCart(t)
	ch := setupCheckoutHandler(t)

	w, _ := callOrderHandler(t, ch.CreateOrderFromCart, buyer, withToken("USDC", map[string]interface{}{"expectedTotal": "30099999"}))
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["code"] != "price_changed" {
		t.Errorf("code = %v, want price_changed", resp["code"])
	}
	var orders int64
	config.DB.Model(&models.Order{}).Count(&orders)
	if orders != 0 {
		t.Fatalf("%d orders created on a refused total", orders)
	}

	w, _ = callOrderHandler(t, ch.CreateOrderFromCart, buyer, withToken("USDC", map[string]interface{}{"expectedTotal": "30100000"}))
	if w.Code != http.StatusCreated {
		t.Fatalf("matching total: expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

// A pending order priced at an older rate is re-split at the escrow's current
// rate before approval: the buyer's total is unchanged, only the split moves.
// This is also what makes pre-seller-pays orders (0.5% service fee) payable.
func TestRepriceToEscrowFee(t *testing.T) {
	db := setupCheckoutTestDB(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 20.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusPendingPayment, "USDC")
	// A $20 order recorded at a 0.5% merchant fee, $5 shipping.
	db.Model(order).Updates(map[string]interface{}{
		"product_amount": "20000000", "platform_fee": "100000", "shipping_amount": "5000000", "total_amount": "25000000",
	})
	order.ProductAmount, order.PlatformFee, order.ShippingAmount, order.TotalAmount = "20000000", "100000", "5000000", "25000000"

	if err := repriceToEscrowFee(db, order, 200); err != nil {
		t.Fatal(err)
	}
	var got models.Order
	db.First(&got, "id = ?", order.ID)
	// At 2% the seller's fee is 0.4; what the buyer pays (product + shipping) is unchanged.
	if got.ProductAmount != "20000000" || got.PlatformFee != "400000" || got.TotalAmount != "25000000" {
		t.Fatalf("re-priced to %s + %s = %s", got.ProductAmount, got.PlatformFee, got.TotalAmount)
	}

	// Already at the current rate: untouched.
	if err := repriceToEscrowFee(db, &got, 200); err != nil {
		t.Fatal(err)
	}
	var again models.Order
	db.First(&again, "id = ?", order.ID)
	if again.ProductAmount != got.ProductAmount || again.UpdatedAt != got.UpdatedAt {
		t.Fatal("an order already at the current rate was rewritten")
	}
}

// A pending order priced before the switch carried the fee inside its total (V2's seller-fee
// split): it is re-priced to escrow the listing price whole, and the buyer still pays the same.
func TestRepriceToEscrowFee_ConvertsAV2SplitOrder(t *testing.T) {
	db := setupCheckoutTestDB(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 20.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusPendingPayment, "USDC")
	// $20 listing split at 2%: 19.607844 + 0.392156, $5 shipping, total 25.
	db.Model(order).Updates(map[string]interface{}{
		"product_amount": "19607844", "platform_fee": "392156", "shipping_amount": "5000000", "total_amount": "25000000",
	})
	order.ProductAmount, order.PlatformFee, order.ShippingAmount, order.TotalAmount = "19607844", "392156", "5000000", "25000000"
	if err := repriceToEscrowFee(db, order, 200); err != nil {
		t.Fatal(err)
	}
	var got models.Order
	db.First(&got, "id = ?", order.ID)
	if got.ProductAmount != "20000000" || got.PlatformFee != "400000" || got.TotalAmount != "25000000" {
		t.Fatalf("re-priced to product %s fee %s total %s", got.ProductAmount, got.PlatformFee, got.TotalAmount)
	}
}

func TestRepriceToEscrowFee_LeavesEscrowedOrdersAlone(t *testing.T) {
	db := setupCheckoutTestDB(t)
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, models.OrderStatusEscrowed, "USDC")

	if err := repriceToEscrowFee(db, order, 150); err != nil {
		t.Fatal(err)
	}
	var got models.Order
	db.First(&got, "id = ?", order.ID)
	if got.ProductAmount != "10000000" || got.PlatformFee != "1000000" {
		t.Fatalf("escrowed order changed: %s / %s", got.ProductAmount, got.PlatformFee)
	}
}
