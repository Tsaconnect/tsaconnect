package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gin-gonic/gin"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"github.com/ojimcy/tsa-api-go/internal/services"
	"gorm.io/gorm"
)

const (
	proofSellerWallet = "0x00000000000000000000000000000000000000a1"
	proofBuyerWallet  = "0x00000000000000000000000000000000000000b2"
	proofUSDC         = "0x9f8AfF2706F52Ddb02921E245ec95Ade96767379"
)

// proofFixture is a buyer, a seller and an order, with a handler reading a fake
// escrow instead of a node.
type proofFixture struct {
	db      *gorm.DB
	ch      *CheckoutHandler
	chain   *services.FakeEscrowChain
	buyer   *models.User
	seller  *models.User
	order   *models.Order
	orderID [32]byte
}

func newProofFixture(t *testing.T, status string) *proofFixture {
	t.Helper()
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	chain := services.NewFakeEscrowChain()
	ch.EscrowService = services.NewEscrowServiceWithBackend(ch.Config, chain, chain)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, proofBuyerWallet)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, proofSellerWallet)
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, status, "USDC")
	return &proofFixture{db: db, ch: ch, chain: chain, buyer: buyer, seller: seller, order: order,
		orderID: services.GenerateOrderID(order.ID)}
}

// holdOrder makes the fake escrow hold this order as priced.
func (f *proofFixture) holdOrder(mutate func(*services.OnChainOrder)) {
	o := services.OnChainOrder{
		Buyer: common.HexToAddress(proofBuyerWallet), Seller: common.HexToAddress(proofSellerWallet),
		Token:         common.HexToAddress(proofUSDC),
		ProductAmount: big.NewInt(10000000), ShippingAmount: big.NewInt(5000000), PlatformFee: big.NewInt(1000000),
		CreatedAt: time.Now(),
	}
	if mutate != nil {
		mutate(&o)
	}
	f.chain.SetOrder(f.orderID, o)
}

func (f *proofFixture) call(h func(*gin.Context), user *models.User, body any) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set("user", user)
	c.Params = gin.Params{{Key: "id", Value: f.order.ID.String()}}
	raw, _ := json.Marshal(body)
	c.Request, _ = http.NewRequest("POST", "/", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Type", "application/json")
	h(c)
	return w
}

func (f *proofFixture) status(t *testing.T) string {
	t.Helper()
	var o models.Order
	if err := f.db.First(&o, "id = ?", f.order.ID).Error; err != nil {
		t.Fatal(err)
	}
	return o.Status
}

func (f *proofFixture) escrow() string { return f.ch.Config.ProductEscrowAddress }

func TestSubmitConfirm_NeedsThisOrdersReceiptConfirmed(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusDelivered)
	var other [32]byte
	other[0] = 9

	f.chain.MineEvent("0xaaa", f.escrow(), "ReceiptConfirmed", other, nil)
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusBadRequest {
		t.Fatalf("another order's confirm: got %d %s", w.Code, w.Body)
	}
	f.chain.MineEvent("0xbbb", f.escrow(), "OrderCancelled", f.orderID, nil)
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xbbb"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a cancel as a confirm: got %d %s", w.Code, w.Body)
	}
	f.chain.MineFailed("0xccc")
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xccc"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a reverted tx: got %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusDelivered {
		t.Fatalf("refused confirms moved the order to %q", got)
	}

	// Not mined yet: retry, not a verdict.
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xddd"}); w.Code != http.StatusConflict {
		t.Fatalf("an unmined tx: got %d %s", w.Code, w.Body)
	}
	f.chain.Err = errors.New("rpc down")
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xddd"}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("an unreachable node: got %d %s", w.Code, w.Body)
	}
	f.chain.Err = nil

	f.chain.MineEvent("0xeee", f.escrow(), "ReceiptConfirmed", f.orderID, nil)
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xeee"}); w.Code != http.StatusOK {
		t.Fatalf("this order's confirm: got %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusCompleted {
		t.Fatalf("status %q", got)
	}
}

func TestSubmitConfirm_UsesTheOrdersOwnEscrow(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusDelivered)
	older := "0x00000000000000000000000000000000000000c3"
	f.db.Model(f.order).Update("escrow_contract_address", older)

	f.chain.MineEvent("0xaaa", f.escrow(), "ReceiptConfirmed", f.orderID, nil)
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a confirm from the current escrow for an order on an older one: got %d", w.Code)
	}
	f.chain.MineEvent("0xbbb", older, "ReceiptConfirmed", f.orderID, nil)
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xbbb"}); w.Code != http.StatusOK {
		t.Fatalf("a confirm from its own escrow: got %d %s", w.Code, w.Body)
	}
}

func TestSubmitCancelOrder_NeedsOrderCancelled(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusCancelRequested)
	f.chain.MineEvent("0xaaa", f.escrow(), "ReceiptConfirmed", f.orderID, nil)
	if w := f.call(f.ch.SubmitCancelOrder, f.seller, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a confirm as a cancel: got %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusCancelRequested {
		t.Fatalf("a refused cancel moved the order to %q", got)
	}
}

// The chain, not the admin's request, says which way a dispute went.
func TestSubmitAdminResolve_TakesTheDirectionFromTheChain(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusRefundRequested)
	admin := createTestUserWithLocation(t, f.db, "Lagos", "Lagos", "Nigeria")

	f.chain.MineEvent("0xaaa", f.escrow(), "DisputeResolved", f.orderID, services.DisputeResolvedData(true))
	w := f.call(f.ch.SubmitAdminResolve, admin, gin.H{"txHash": "0xaaa", "refundBuyer": false})
	if w.Code != http.StatusOK {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusRefunded {
		t.Fatalf("the chain refunded the buyer but the order is %q", got)
	}
}

func TestSubmitAdminResolve_RefusesAnUnrelatedTx(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusRefundRequested)
	admin := createTestUserWithLocation(t, f.db, "Lagos", "Lagos", "Nigeria")
	f.chain.MineEvent("0xaaa", f.escrow(), "ReceiptConfirmed", f.orderID, nil)
	if w := f.call(f.ch.SubmitAdminResolve, admin, gin.H{"txHash": "0xaaa", "refundBuyer": true}); w.Code != http.StatusBadRequest {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
	if got := f.status(t); got != models.OrderStatusRefundRequested {
		t.Fatalf("status %q", got)
	}
}

func TestRequireEscrowFundsOrder(t *testing.T) {
	check := func(f *proofFixture) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request, _ = http.NewRequest("POST", "/", nil)
		if f.ch.requireEscrowFundsOrder(c, f.order) {
			return nil
		}
		return w
	}

	t.Run("the order as priced", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusPendingPayment)
		f.holdOrder(nil)
		if w := check(f); w != nil {
			t.Fatalf("refused a correct payment: %d %s", w.Code, w.Body)
		}
	})
	for name, mutate := range map[string]func(*services.OnChainOrder){
		"another seller":   func(o *services.OnChainOrder) { o.Seller = common.HexToAddress(proofBuyerWallet) },
		"another token":    func(o *services.OnChainOrder) { o.Token = common.HexToAddress(proofBuyerWallet) },
		"a smaller price":  func(o *services.OnChainOrder) { o.ProductAmount = big.NewInt(1) },
		"already paid out": func(o *services.OnChainOrder) { o.Resolved = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := newProofFixture(t, models.OrderStatusPendingPayment)
			f.holdOrder(mutate)
			w := check(f)
			if w == nil || w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "escrow_mismatch") {
				t.Fatalf("got %v", w)
			}
		})
	}
	t.Run("a node that has not seen the order", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusPendingPayment)
		if w := check(f); w == nil || w.Code != http.StatusServiceUnavailable {
			t.Fatalf("got %v", w)
		}
	})
	t.Run("the wallet the buyer was told to pay survives a wallet change", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusPendingPayment)
		f.holdOrder(nil)
		f.db.Model(f.order).Update("escrow_seller_wallet", proofSellerWallet)
		f.order.EscrowSellerWallet = proofSellerWallet
		setUserWallet(t, f.db, f.seller, proofBuyerWallet)
		if w := check(f); w != nil {
			t.Fatalf("refused a payment to the wallet the buyer was given: %d %s", w.Code, w.Body)
		}
	})
}

func TestMarkDelivered_NeedsTheOnChainMark(t *testing.T) {
	deliver := func(f *proofFixture, body gin.H) *httptest.ResponseRecorder {
		body["deliveryProofUrl"] = "https://example.com/proof.jpg"
		return f.call(f.ch.MarkDelivered, f.seller, body)
	}

	t.Run("a DeliveryMarked hash", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.chain.MineEvent("0xaaa", f.escrow(), "DeliveryMarked", f.orderID, nil)
		if w := deliver(f, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusDelivered {
			t.Fatalf("status %q", got)
		}
	})
	t.Run("another event is not a delivery", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.chain.MineEvent("0xaaa", f.escrow(), "OrderCancelled", f.orderID, nil)
		if w := deliver(f, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusBadRequest {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusShipped {
			t.Fatalf("status %q", got)
		}
	})
	t.Run("no hash, nothing on-chain", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.holdOrder(nil)
		w := deliver(f, gin.H{})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "delivery_tx_required") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
	t.Run("no hash, but the escrow already has it", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.holdOrder(func(o *services.OnChainOrder) { o.SellerDelivered = true })
		if w := deliver(f, gin.H{}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusDelivered {
			t.Fatalf("status %q", got)
		}
	})
	t.Run("an order delivered before the mark was on-chain", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusDelivered)
		f.chain.MineEvent("0xaaa", f.escrow(), "DeliveryMarked", f.orderID, nil)
		if w := deliver(f, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
	t.Run("a Spendable MCGP order has no escrow to ask", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusEscrowed)
		f.db.Model(f.order).Update("payment_source", "spendable_mcgp")
		if w := deliver(f, gin.H{}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
}

func TestPrepareDeliver(t *testing.T) {
	t.Run("builds the seller's transaction", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.holdOrder(nil)
		if w := f.call(f.ch.PrepareDeliver, f.seller, gin.H{}); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "deliverTx") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
	t.Run("only the seller", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.holdOrder(nil)
		if w := f.call(f.ch.PrepareDeliver, f.buyer, gin.H{}); w.Code != http.StatusForbidden {
			t.Fatalf("got %d", w.Code)
		}
	})
	t.Run("already on-chain", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.holdOrder(func(o *services.OnChainOrder) { o.SellerDelivered = true })
		w := f.call(f.ch.PrepareDeliver, f.seller, gin.H{})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "delivery_on_chain") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
	t.Run("the seller changed wallets since the payment", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.holdOrder(nil)
		setUserWallet(t, f.db, f.seller, "0x00000000000000000000000000000000000000d4")
		w := f.call(f.ch.PrepareDeliver, f.seller, gin.H{})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "seller_wallet_changed") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
	t.Run("an order delivered before the mark was on-chain", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusDelivered)
		f.holdOrder(nil)
		if w := f.call(f.ch.PrepareDeliver, f.seller, gin.H{}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
}
