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

	"github.com/ojimcy/tsa-api-go/internal/models"
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
	setupCheckoutTestDB(t)
	cases := []struct {
		price, token, wantProduct, wantFee string
	}{
		{"20000000", "USDC", "19607844", "392156"},                  // $20: sums exactly
		{"10000000", "USDT", "9803922", "196078"},                   // $10: sums exactly
		{"60000", "USDC", "58824", "1176"},                          // $0.06: sums exactly
		{"1", "USDC", "1", "0"},                                     // dust: fee rounds to 0
		{"5000000000000000000", "MCGP", "5000000000000000000", "0"}, // MCGP exempt
	}
	for _, c := range cases {
		price, _ := new(big.Int).SetString(c.price, 10)
		p, f := splitSellerFee(price, setupCheckoutHandler(t).escrowFeeBPS(context.Background(), c.token))
		if p.String() != c.wantProduct || f.String() != c.wantFee {
			t.Errorf("%s %s: got product %s fee %s, want %s / %s", c.price, c.token, p, f, c.wantProduct, c.wantFee)
		}
		sum := new(big.Int).Add(p, f)
		if sum.Cmp(price) > 0 {
			t.Errorf("%s %s: buyer would pay %s, above the price", c.price, c.token, sum)
		}
		// The fee the contract will compute on the escrowed amount is the one recorded.
		if CalculatePlatformFee(p, c.token).Cmp(f) != 0 {
			t.Errorf("%s %s: recorded fee %s != contract fee %s", c.price, c.token, f, CalculatePlatformFee(p, c.token))
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
