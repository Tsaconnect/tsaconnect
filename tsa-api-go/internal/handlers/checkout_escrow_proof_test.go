package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	key     [32]byte // the order's key on OrderEscrow
}

func newProofFixture(t *testing.T, status string) *proofFixture {
	t.Helper()
	db := setupCheckoutTestDB(t)
	ch := setupCheckoutHandler(t)
	chain := services.NewFakeEscrowChain()
	ch.EscrowService = services.NewEscrowServiceWithBackend(ch.Config, 146, chain, chain)

	buyer := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, buyer, proofBuyerWallet)
	seller := createTestUserWithLocation(t, db, "Lagos", "Lagos", "Nigeria")
	setUserWallet(t, db, seller, proofSellerWallet)
	product := createTestProduct(t, db, seller.ID, 10.0)
	order := createTestOrder(t, db, buyer.ID, seller.ID, product.ID, status, "USDC")
	// Funded from the buyer's wallet: the order's key on OrderEscrow.
	key := services.OrderKey(proofBuyerWallet, services.GenerateOrderID(order.ID))
	order.EscrowOrderKey = common.Hash(key).Hex()
	db.Model(order).Update("escrow_order_key", order.EscrowOrderKey)
	return &proofFixture{db: db, ch: ch, chain: chain, buyer: buyer, seller: seller, order: order, key: key}
}

// holdOrder makes the fake escrow hold this order as priced.
func (f *proofFixture) holdOrder(mutate func(*services.OnChainOrder)) {
	o := services.OnChainOrder{
		Buyer: common.HexToAddress(proofBuyerWallet), Seller: common.HexToAddress(proofSellerWallet),
		Token:         common.HexToAddress(proofUSDC),
		ProductAmount: big.NewInt(10000000), ShippingAmount: big.NewInt(5000000), MerchantFee: big.NewInt(200000),
		State: services.OrderStateFunded, CreatedAt: time.Now(), DeliverBy: time.Now().Add(7 * 24 * time.Hour),
	}
	if mutate != nil {
		mutate(&o)
	}
	f.chain.SetOrder(f.key, o)
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

func (f *proofFixture) escrow() string { return f.ch.EscrowService.CurrentEscrowAddress() }

func TestSubmitConfirm_NeedsThisOrdersReceiptConfirmed(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusDelivered)
	var other [32]byte
	other[0] = 9

	f.chain.MineEvent("0xaaa", f.escrow(), "OrderReleased", other, nil)
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusBadRequest {
		t.Fatalf("another order's confirm: got %d %s", w.Code, w.Body)
	}
	f.chain.MineEvent("0xbbb", f.escrow(), "OrderRefunded", f.key, nil)
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

	f.chain.MineEvent("0xeee", f.escrow(), "OrderReleased", f.key, nil)
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

	f.chain.MineEvent("0xaaa", f.escrow(), "OrderReleased", f.key, nil)
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusBadRequest {
		t.Fatalf("a confirm from the current escrow for an order on an older one: got %d", w.Code)
	}
	f.chain.MineEvent("0xbbb", older, "OrderReleased", f.key, nil)
	if w := f.call(f.ch.SubmitConfirm, f.buyer, gin.H{"txHash": "0xbbb"}); w.Code != http.StatusOK {
		t.Fatalf("a confirm from its own escrow: got %d %s", w.Code, w.Body)
	}
}

func TestSubmitCancelOrder_NeedsOrderCancelled(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusCancelRequested)
	f.chain.MineEvent("0xaaa", f.escrow(), "OrderReleased", f.key, nil)
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

	f.chain.MineEvent("0xaaa", f.escrow(), "OrderResolved", f.key, services.OrderResolvedData(big.NewInt(15000000), big.NewInt(0), big.NewInt(0)))
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
	f.chain.MineEvent("0xaaa", f.escrow(), "OrderReleased", f.key, nil)
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
		if f.ch.requireEscrowFundsOrder(c, f.order, f.key, proofBuyerWallet) {
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
		"already paid out": func(o *services.OnChainOrder) { o.State = services.OrderStateReleased },
		// The buyer is compared with the funding wallet, not read back from the chain itself.
		"another buyer": func(o *services.OnChainOrder) { o.Buyer = common.HexToAddress(proofSellerWallet) },
		// Product and shipping are checked apart: the fee applies to the product alone.
		"the split moved to shipping": func(o *services.OnChainOrder) {
			o.ProductAmount, o.ShippingAmount = big.NewInt(9000000), big.NewInt(6000000)
		},
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
		f.chain.MineEvent("0xaaa", f.escrow(), "OrderDelivered", f.key, nil)
		if w := deliver(f, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusDelivered {
			t.Fatalf("status %q", got)
		}
	})
	t.Run("another event is not a delivery", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusShipped)
		f.chain.MineEvent("0xaaa", f.escrow(), "OrderRefunded", f.key, nil)
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
		f.holdOrder(func(o *services.OnChainOrder) { o.State, o.ConfirmBy = services.OrderStateDelivered, time.Now().Add(7*24*time.Hour) })
		if w := deliver(f, gin.H{}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusDelivered {
			t.Fatalf("status %q", got)
		}
	})
	t.Run("an order delivered before the mark was on-chain", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusDelivered)
		f.chain.MineEvent("0xaaa", f.escrow(), "OrderDelivered", f.key, nil)
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
		f.holdOrder(func(o *services.OnChainOrder) { o.State, o.ConfirmBy = services.OrderStateDelivered, time.Now().Add(7*24*time.Hour) })
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

func TestRequestRefund_AfterDelivery(t *testing.T) {
	t.Run("delivery on-chain: the buyer signs the dispute, the status waits", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusDelivered)
		f.holdOrder(func(o *services.OnChainOrder) { o.State, o.ConfirmBy = services.OrderStateDelivered, time.Now().Add(7*24*time.Hour) })
		w := f.call(f.ch.RequestRefund, f.buyer, gin.H{})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "refundTx") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusDelivered {
			t.Fatalf("the status moved to %q before the transaction was proven", got)
		}
	})
	t.Run("past the confirm window: no dispute, the release is due", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusDelivered)
		f.holdOrder(func(o *services.OnChainOrder) { o.State, o.ConfirmBy = services.OrderStateDelivered, time.Now().Add(-time.Hour) })
		w := f.call(f.ch.RequestRefund, f.buyer, gin.H{})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "dispute_window_closed") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
}

// Before delivery OrderEscrow has no instant refund (owner M3): the buyer is told the deadline, and
// past it signs claimTimeout for the full refund.
func TestRequestRefund_BeforeDelivery(t *testing.T) {
	t.Run("inside the delivery window: the deadline, no transaction", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusEscrowed)
		f.holdOrder(nil)
		w := f.call(f.ch.RequestRefund, f.buyer, gin.H{})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "refund_not_yet_claimable") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
	t.Run("past the delivery deadline: the buyer claims the refund", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusEscrowed)
		f.holdOrder(func(o *services.OnChainOrder) { o.DeliverBy = time.Now().Add(-time.Hour) })
		w := f.call(f.ch.RequestRefund, f.buyer, gin.H{})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "refundTx") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusEscrowed {
			t.Fatalf("the status moved to %q before the claim was proven", got)
		}
	})
}

func TestSubmitRefund(t *testing.T) {
	t.Run("OrderDisputed makes it a dispute for a resolver", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusDelivered)
		f.chain.MineEvent("0xaaa", f.escrow(), "OrderDisputed", f.key, services.OrderDeadlineData("OrderDisputed", time.Now().Add(14*24*time.Hour)))
		if w := f.call(f.ch.SubmitRefund, f.buyer, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusRefundRequested {
			t.Fatalf("status %q", got)
		}
	})
	t.Run("OrderRefunded (a claim past the deadline) refunds", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusEscrowed)
		f.chain.MineEvent("0xaaa", f.escrow(), "OrderRefunded", f.key, nil)
		if w := f.call(f.ch.SubmitRefund, f.buyer, gin.H{"txHash": "0xaaa"}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusRefunded {
			t.Fatalf("status %q", got)
		}
	})
	t.Run("another event, another order, or another user is refused", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusDelivered)
		var other [32]byte
		other[0] = 9
		f.chain.MineEvent("0xaaa", f.escrow(), "OrderReleased", f.key, nil)
		f.chain.MineEvent("0xbbb", f.escrow(), "OrderDisputed", other, services.OrderDeadlineData("OrderDisputed", time.Now()))
		f.chain.MineEvent("0xccc", f.escrow(), "OrderDisputed", f.key, services.OrderDeadlineData("OrderDisputed", time.Now()))
		for _, tx := range []string{"0xaaa", "0xbbb"} {
			if w := f.call(f.ch.SubmitRefund, f.buyer, gin.H{"txHash": tx}); w.Code != http.StatusBadRequest {
				t.Fatalf("%s: got %d %s", tx, w.Code, w.Body)
			}
		}
		if w := f.call(f.ch.SubmitRefund, f.seller, gin.H{"txHash": "0xccc"}); w.Code != http.StatusForbidden {
			t.Fatalf("a seller submitting the buyer's refund: got %d", w.Code)
		}
		if w := f.call(f.ch.SubmitRefund, f.buyer, gin.H{"txHash": "0xddd"}); w.Code != http.StatusConflict {
			t.Fatalf("an unmined tx: got %d", w.Code)
		}
		if got := f.status(t); got != models.OrderStatusDelivered {
			t.Fatalf("refused submits moved the order to %q", got)
		}
	})
}

// The same refund proof submitted twice at once restores the unit once.
func TestSubmitRefund_RestoresStockOnce(t *testing.T) {
	f := newProofFixture(t, models.OrderStatusEscrowed)
	f.chain.MineEvent("0xaaa", f.escrow(), "OrderRefunded", f.key, nil)
	// In-memory SQLite is one database per connection; the goroutines still
	// interleave their statements on the one connection.
	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	var before models.Product
	f.db.First(&before, "id = ?", f.order.ProductID)

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.call(f.ch.SubmitRefund, f.buyer, gin.H{"txHash": "0xaaa"})
		}()
	}
	wg.Wait()

	var after models.Product
	f.db.First(&after, "id = ?", f.order.ProductID)
	if after.Stock != before.Stock+f.order.Quantity {
		t.Fatalf("stock %d -> %d: want exactly one unit back", before.Stock, after.Stock)
	}
	if got := f.status(t); got != models.OrderStatusRefunded {
		t.Fatalf("status %q", got)
	}
}

// Only a dispute the chain holds is resolved, by a RESOLVER_ROLE wallet, with the split the admin
// chose; the recorded outcome is the one the OrderResolved log reports.
func TestAdminResolve_OnOrderEscrow(t *testing.T) {
	t.Run("not disputed on-chain", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusRefundRequested)
		f.holdOrder(nil) // Funded: a refund is a claim after the deadline, not a resolve
		admin := createTestUserWithLocation(t, f.db, "Lagos", "Lagos", "Nigeria")
		setUserWallet(t, f.db, admin, "0x00000000000000000000000000000000000000ad")
		w := f.call(f.ch.AdminResolveDispute, admin, gin.H{"refundBuyer": true})
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "order_not_disputed_on_chain") {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
	})
	t.Run("a split is recorded as completed, the stock kept", func(t *testing.T) {
		f := newProofFixture(t, models.OrderStatusRefundRequested)
		f.chain.MineEvent("0xaaa", f.escrow(), "OrderResolved", f.key,
			services.OrderResolvedData(big.NewInt(5000000), big.NewInt(9800000), big.NewInt(200000)))
		if w := f.call(f.ch.SubmitAdminResolve, f.buyer, gin.H{"txHash": "0xaaa", "refundBuyer": true}); w.Code != http.StatusOK {
			t.Fatalf("got %d %s", w.Code, w.Body)
		}
		if got := f.status(t); got != models.OrderStatusCompleted {
			t.Fatalf("status %q; a split paid the seller, so the order completed", got)
		}
	})
}

// Buyer-signed escrow calls come from the wallet the escrow names (only it may confirm or dispute),
// not whatever the account is linked to now.
func TestEscrowBuyer_IsTheFundingWallet(t *testing.T) {
	user := &models.User{WalletAddress: proofSellerWallet}
	if got := escrowBuyer(&models.Order{EscrowBuyerWallet: proofBuyerWallet}, user); got != proofBuyerWallet {
		t.Fatalf("escrowBuyer = %s, want the escrow's buyer", got)
	}
	if got := escrowBuyer(&models.Order{}, user); got != proofSellerWallet {
		t.Fatalf("an order not on OrderEscrow yet uses the account wallet, got %s", got)
	}
}
