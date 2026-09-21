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

// listingPaid is what the buyer pays for the listing itself: the escrowed
// productAmount plus the platform fee the contract adds on top of it. Because
// the seller absorbs the fee, it must equal the listing price.
func listingPaid(t *testing.T, productAmount, platformFee interface{}) string {
	t.Helper()
	p, ok1 := new(big.Int).SetString(fmt.Sprint(productAmount), 10)
	f, ok2 := new(big.Int).SetString(fmt.Sprint(platformFee), 10)
	if !ok1 || !ok2 {
		t.Fatalf("non-numeric amounts: %v / %v", productAmount, platformFee)
	}
	return new(big.Int).Add(p, f).String()
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
	// $20 of product, 2% of it absorbed by the seller: 19.607844 escrowed for the
	// seller + 0.392156 fee = exactly $20. Then $10 shipping + $0.10 gas, at 6dp.
	want := map[string]string{
		"productAmount": "19607844", "shippingAmount": "10000000",
		"platformFee": "392156", "gasFee": "100000", "total": "30100000",
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

// The buyer pays the listing price and no more; the seller nets price - fee.
func TestSplitSellerFee(t *testing.T) {
	cases := []struct {
		price                string
		bps                  int
		wantProduct, wantFee string
	}{
		{"20000000", 200, "19607844", "392156"},                // $20: sums exactly
		{"10000000", 200, "9803922", "196078"},                 // $10: sums exactly
		{"60000", 200, "58824", "1176"},                        // $0.06: sums exactly
		{"1", 200, "1", "0"},                                   // dust: fee rounds to 0
		{"20000000", 150, "19704434", "295566"},                // an admin-set 1.5%
		{"5000000000000000000", 0, "5000000000000000000", "0"}, // MCGP / exempt
	}
	for _, c := range cases {
		price, _ := new(big.Int).SetString(c.price, 10)
		p, f := splitSellerFee(price, c.bps)
		if p.String() != c.wantProduct || f.String() != c.wantFee {
			t.Errorf("%s @%d: got product %s fee %s, want %s / %s", c.price, c.bps, p, f, c.wantProduct, c.wantFee)
		}
		if sum := new(big.Int).Add(p, f); sum.Cmp(price) > 0 {
			t.Errorf("%s @%d: buyer would pay %s, above the price", c.price, c.bps, sum)
		}
		// The fee the contract computes on the escrowed amount is the one recorded.
		if escrowFeeOn(p, c.bps).Cmp(f) != 0 {
			t.Errorf("%s @%d: recorded fee %s != contract fee %s", c.price, c.bps, f, escrowFeeOn(p, c.bps))
		}
	}
}

// Prices that do not divide evenly by 1.02 leave the buyer at most one base
// unit under the price, never over.
func TestSplitSellerFee_NeverOverchargesAcrossPrices(t *testing.T) {
	setupCheckoutTestDB(t)
	for v := int64(1); v <= 5000; v += 7 {
		price := big.NewInt(v * 997)
		p, f := splitSellerFee(price, 200)
		diff := new(big.Int).Sub(price, new(big.Int).Add(p, f))
		if diff.Sign() < 0 || diff.Cmp(big.NewInt(1)) > 0 {
			t.Fatalf("price %s: product %s + fee %s is off by %s", price, p, f, diff)
		}
	}
}

// fakeEscrowRates stands in for the escrow's on-chain fee and gas.
type fakeEscrowRates struct {
	bps    int
	gas    int64
	feeErr error
}

func (f fakeEscrowRates) PlatformFeeBPS(context.Context, string) (int, error) { return f.bps, f.feeErr }
func (f fakeEscrowRates) GasFeeWei(context.Context, string) (*big.Int, error) {
	return big.NewInt(f.gas), nil
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

// The quote uses the escrow's rates, not fee_config: a 1.5% on-chain fee and a
// $0.05 on-chain gas fee show up in the amounts.
func TestQuoteCart_UsesTheEscrowsRates(t *testing.T) {
	buyer := seedQuoteCart(t)
	ch := setupCheckoutHandler(t)
	ch.feeRates = fakeEscrowRates{bps: 150, gas: 50000}

	w, quote := callOrderHandler(t, ch.QuoteCart, buyer, withToken("USDC", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("quote: %d %s", w.Code, w.Body.String())
	}
	totals := quote["totals"].(map[string]interface{})
	// $20 at 1.5% seller-paid: 19.704434 + 0.295566; $10 shipping; $0.05 gas.
	want := map[string]string{"productAmount": "19704434", "platformFee": "295566", "gasFee": "50000", "total": "30050000"}
	for k, v := range want {
		if totals[k] != v {
			t.Errorf("totals.%s = %v, want %s", k, totals[k], v)
		}
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
	// A $20 service recorded at 0.5% on top: 20 + 0.1 fee, $5 shipping.
	db.Model(order).Updates(map[string]interface{}{
		"product_amount": "20000000", "platform_fee": "100000", "shipping_amount": "5000000", "total_amount": "25100000",
	})
	order.ProductAmount, order.PlatformFee, order.ShippingAmount, order.TotalAmount = "20000000", "100000", "5000000", "25100000"

	if err := repriceToEscrowFee(db, order, 200); err != nil {
		t.Fatal(err)
	}
	var got models.Order
	db.First(&got, "id = ?", order.ID)
	// 20.1 re-split at 2%: 19.705883 + 0.394117; the buyer's 25.1 is unchanged.
	if got.ProductAmount != "19705883" || got.PlatformFee != "394117" || got.TotalAmount != "25100000" {
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
