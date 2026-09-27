package handlers

import (
	"errors"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ojimcy/tsa-api-go/internal/config"
	"github.com/ojimcy/tsa-api-go/internal/models"
)

// fakeWalletBalances answers the buyer's balance read.
type fakeWalletBalances struct {
	balance *big.Int
	err     error
}

func (f fakeWalletBalances) DepositBalance(_, _, _ string) (*big.Int, error) {
	return f.balance, f.err
}

// checkoutOneItem places a one-item USDC cart for a buyer holding the given
// wallet balance and returns the response code, body and the product.
func checkoutOneItem(t *testing.T, balances walletBalanceReader) (int, string, *models.Product, *models.User) {
	t.Helper()
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	ch.walletBalances = balances

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, "0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce")
	seller := createTestUserWithLocation(t, db, "Ikeja", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, "0x0000000000000000000000000000000000000002")
	product := createTestProduct(t, db, seller.ID, 0.08)
	createTestCart(t, db, buyer.ID, []models.CartItem{
		{ID: uuid.New(), Product: product.ID, Seller: seller.ID, Quantity: 1, Price: 0.08, AddedAt: time.Now()},
	})

	w, _ := callOrderHandler(t, ch.CreateOrderFromCart, buyer, map[string]interface{}{
		"token": "USDC", "buyerCity": "Lagos", "buyerState": "Lagos", "buyerCountry": "Nigeria",
	})
	return w.Code, w.Body.String(), product, buyer
}

// The escrow pulls the whole total and reverts on a shortfall. A short wallet
// must be refused before any order takes stock or the buyer pays for an approve.
func TestCreateOrderFromCart_RefusesAWalletThatCannotPay(t *testing.T) {
	code, body, product, buyer := checkoutOneItem(t, fakeWalletBalances{balance: big.NewInt(20000)}) // 0.02 USDC

	if code != http.StatusBadRequest || !strings.Contains(body, `"insufficient_balance"`) {
		t.Fatalf("expected 400 insufficient_balance, got %d: %s", code, body)
	}
	if !strings.Contains(body, "your wallet holds 0.02 USDC") {
		t.Errorf("refusal should say what the wallet holds: %s", body)
	}

	var orders int64
	config.DB.Model(&models.Order{}).Where("buyer_id = ?", buyer.ID).Count(&orders)
	var stock int
	config.DB.Model(&models.Product{}).Where("id = ?", product.ID).Select("stock").Scan(&stock)
	var cartStatus string
	config.DB.Model(&models.Cart{}).Where("user_id = ?", buyer.ID).Select("status").Scan(&cartStatus)
	if orders != 0 || stock != product.Stock || cartStatus != "active" {
		t.Fatalf("a refused checkout must change nothing: orders=%d stock=%d cart=%s", orders, stock, cartStatus)
	}
}

func TestCreateOrderFromCart_PaysWhenTheWalletCovers(t *testing.T) {
	code, body, _, _ := checkoutOneItem(t, fakeWalletBalances{balance: big.NewInt(1_000_000_000)})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", code, body)
	}
}

// An unreadable balance is not a refusal: the chain still decides at escrow.
func TestCreateOrderFromCart_UnreadableBalanceDoesNotBlock(t *testing.T) {
	code, body, _, _ := checkoutOneItem(t, fakeWalletBalances{err: errors.New("rpc down")})
	if code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", code, body)
	}
}

// The single-service "Pay now" path pulls from the wallet the same way, and a
// booking would also reserve its slot: a short wallet is refused before either.
func TestCreateServiceOrder_RefusesAWalletThatCannotPay(t *testing.T) {
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	ch.walletBalances = fakeWalletBalances{balance: big.NewInt(20000)} // 0.02 USDC
	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, "0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce")
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	cat := createTypedServiceCategory(t, db, models.ServiceTypeOrder)
	svc := createTestService(t, db, seller.ID, &cat.ID, 0, 0)
	item := addMenuItem(t, db, svc.ID, "Combo", 15, true)

	w := postServiceOrder(t, ch, buyer, map[string]interface{}{
		"productId":      svc.ID.String(),
		"menuSelections": []map[string]interface{}{{"itemId": item.ID.String(), "quantity": 1}},
	})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"insufficient_balance"`) {
		t.Fatalf("expected 400 insufficient_balance, got %d: %s", w.Code, w.Body.String())
	}
	var orders int64
	config.DB.Model(&models.Order{}).Where("buyer_id = ?", buyer.ID).Count(&orders)
	if orders != 0 {
		t.Fatalf("a refused order must not be created, got %d", orders)
	}
}
