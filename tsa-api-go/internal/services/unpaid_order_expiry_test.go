package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	testSellerWallet = "0x62bA9b24e72E4f1F93f50F2308aCE932066c1b96"
	testBuyerWallet  = "0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce"
)

func setupUnpaidOrderDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	// Raw DDL, not AutoMigrate: the models carry Postgres-only defaults.
	for _, ddl := range []string{
		`CREATE TABLE orders (id TEXT PRIMARY KEY, buyer_id TEXT, seller_id TEXT, product_id TEXT,
			quantity INTEGER, kind TEXT, token TEXT, status TEXT,
			product_amount TEXT, shipping_amount TEXT, platform_fee TEXT, total_amount TEXT,
			contract_order_id TEXT, escrow_contract_address TEXT, escrow_seller_wallet TEXT, escrow_order_key TEXT, escrow_buyer_wallet TEXT, escrow_handling_days INTEGER, release_tx_hash TEXT, merchant_fee TEXT, flat_fee TEXT, deliver_by DATETIME, confirm_by DATETIME, resolve_by DATETIME, payout_fee TEXT, escrow_expires_at DATETIME,
			approve_tx_hash TEXT, escrow_tx_hash TEXT, payment_source TEXT DEFAULT '',
			created_at DATETIME, updated_at DATETIME)`,
		orderMcgpPaymentsDDL,
		`CREATE TABLE products (id TEXT PRIMARY KEY, stock INTEGER, handling_days INTEGER DEFAULT 0, deleted_at DATETIME)`,
		`CREATE TABLE service_bookings (id TEXT PRIMARY KEY, order_id TEXT, slot_end DATETIME)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, wallet_address TEXT)`,
		`CREATE TABLE booking_requests (id TEXT PRIMARY KEY, status TEXT, order_id TEXT, updated_at DATETIME)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	return db
}

func writeProduct(t *testing.T, db *gorm.DB, stock int) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := db.Exec(`INSERT INTO products (id, stock) VALUES (?, ?)`, id, stock).Error; err != nil {
		t.Fatalf("insert product: %v", err)
	}
	return id
}

// writeOrder stores a 1-unit USDC order (0.0784 product, no shipping) placed
// `placed` ago whose last payment activity was `active` ago, sold by a seller
// whose wallet is testSellerWallet.
func writeOrder(t *testing.T, db *gorm.DB, productID uuid.UUID, kind, status string, placed, active time.Duration) uuid.UUID {
	t.Helper()
	id, seller, buyer := uuid.New(), uuid.New(), uuid.New()
	if err := db.Exec(`INSERT INTO users (id, wallet_address) VALUES (?, ?), (?, ?)`,
		seller, testSellerWallet, buyer, testBuyerWallet).Error; err != nil {
		t.Fatalf("insert users: %v", err)
	}
	if err := db.Exec(`INSERT INTO orders (id, buyer_id, seller_id, product_id, quantity, kind, token, status,
		product_amount, shipping_amount, platform_fee, total_amount, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, 'USDC', ?, '78400', '0', '1600', '80000', ?, ?)`,
		id, buyer, seller, productID, kind, status, time.Now().Add(-placed), time.Now().Add(-active)).Error; err != nil {
		t.Fatalf("insert order: %v", err)
	}
	return id
}

func orderStatus(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var status string
	db.Raw(`SELECT status FROM orders WHERE id = ?`, id).Scan(&status)
	return status
}

func productStock(t *testing.T, db *gorm.DB, id uuid.UUID) int {
	t.Helper()
	var stock int
	db.Raw(`SELECT stock FROM products WHERE id = ?`, id).Scan(&stock)
	return stock
}

// paidFor is the escrow's record of a payment matching writeOrder's order.
func paidFor() *OnChainOrder {
	return &OnChainOrder{
		Buyer:         common.HexToAddress(testBuyerWallet),
		Seller:        common.HexToAddress(testSellerWallet),
		Token:         common.HexToAddress(testUSDC),
		ProductAmount: big.NewInt(78400), ShippingAmount: big.NewInt(0), MerchantFee: big.NewInt(1568),
		State: OrderStateFunded, CreatedAt: time.Now().Add(-90 * time.Minute), DeliverBy: time.Now().Add(7 * 24 * time.Hour),
	}
}

// testKey is writeOrder's order key on the escrow: funded from the buyer's wallet.
func testKey(id uuid.UUID) [32]byte { return OrderKey(testBuyerWallet, GenerateOrderID(id)) }

// fakeOrderEscrow answers EscrowedOrder from the orders it holds.
type fakeOrderEscrow struct {
	held map[[32]byte]*OnChainOrder
	fail bool
	none bool // no OrderEscrow configured
}

func (f *fakeOrderEscrow) EscrowedOrder(_ context.Context, id [32]byte) (*OnChainOrder, error) {
	if f.fail {
		return nil, errors.New("rpc down")
	}
	if f.none {
		return nil, ErrOrderEscrowUnavailable
	}
	return f.held[id], nil
}

func (f *fakeOrderEscrow) CurrentEscrowAddress() string { return testEscrow }

// fakeTokens resolves USDC on Sonic.
type fakeTokens struct{}

func (fakeTokens) TokenAddress(chain, symbol string) string {
	if chain == "sonic" && symbol == "USDC" {
		return testUSDC
	}
	return ""
}

func newSweep(db *gorm.DB, escrow escrowOrderReader, bus *events.Bus) *UnpaidOrderExpiryService {
	return NewUnpaidOrderExpiryService(db, escrow, fakeTokens{}, bus)
}

func TestExpireUnpaidOrders_ReleasesStockOnlyForIdleUnescrowedOrders(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	service := writeProduct(t, db, 5)
	h2, m10 := 2*time.Hour, 10*time.Minute
	beforeLaunch := time.Since(unpaidOrderExpiryFrom) + 24*time.Hour

	abandoned := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, h2, h2)
	justPlaced := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, m10, m10)
	retriedNow := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, h2, m10)
	historical := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, beforeLaunch, beforeLaunch)
	paid := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusEscrowed, h2, h2)
	serviceOrder := writeOrder(t, db, service, models.OrderKindService, models.OrderStatusPendingPayment, h2, h2)
	// The booking request the service order was paying for.
	request := uuid.New()
	db.Exec(`INSERT INTO booking_requests (id, status, order_id, updated_at) VALUES (?, ?, ?, ?)`,
		request, models.BookingRequestConverted, serviceOrder, time.Now().Add(-h2))

	bus := events.NewBus()
	defer bus.Stop()
	notified := make(chan uuid.UUID, 4)
	bus.Subscribe(events.OrderRefunded, func(e events.Event) { notified <- e.UserID })

	n, err := newSweep(db, &fakeOrderEscrow{}, bus).ExpireUnpaidOrders(context.Background())
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 orders expired, got %d", n)
	}

	want := map[uuid.UUID]string{
		abandoned:    models.OrderStatusCancelled,
		justPlaced:   models.OrderStatusPendingPayment,
		retriedNow:   models.OrderStatusPendingPayment, // the buyer is paying right now
		historical:   models.OrderStatusPendingPayment, // placed before the sweep shipped
		paid:         models.OrderStatusEscrowed,
		serviceOrder: models.OrderStatusCancelled, // releases its booking hold
	}
	for id, status := range want {
		if got := orderStatus(t, db, id); got != status {
			t.Errorf("order %s: status %q, want %q", id, got, status)
		}
	}
	if got := productStock(t, db, product); got != 1 {
		t.Errorf("product stock %d, want 1 (only the abandoned order's unit returns)", got)
	}
	if got := productStock(t, db, service); got != 5 {
		t.Errorf("service stock %d, want 5 (a service holds no stock)", got)
	}
	// Its request is payable again, with a new order.
	var reqStatus string
	var reqOrder *string
	db.Raw(`SELECT status, order_id FROM booking_requests WHERE id = ?`, request).Row().Scan(&reqStatus, &reqOrder)
	if reqStatus != models.BookingRequestApproved || reqOrder != nil {
		t.Errorf("booking request: status %q order %v, want approved with no order", reqStatus, reqOrder)
	}

	got := map[string]bool{}
	for i := 0; i < 4; i++ {
		select {
		case id := <-notified:
			got[id.String()] = true
		case <-time.After(2 * time.Second):
			t.Fatal("buyer and seller must both be told the order was cancelled")
		}
	}
	for _, id := range []uuid.UUID{abandoned, serviceOrder} {
		var buyer, seller string
		db.Raw(`SELECT buyer_id, seller_id FROM orders WHERE id = ?`, id).Row().Scan(&buyer, &seller)
		if !got[buyer] || !got[seller] {
			t.Fatalf("order %s: notified %v, want buyer %s and seller %s", id, got, buyer, seller)
		}
	}
}

// A payment the escrow holds but the app never reported (closed mid-flow, or
// a lost tx hash) is recorded, and the seller told — never left pending.
func TestExpireUnpaidOrders_RecordsAPaymentTheAppNeverReported(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)

	bus := events.NewBus()
	defer bus.Stop()
	escrowed := make(chan events.Event, 4)
	bus.Subscribe(events.OrderEscrowed, func(e events.Event) { escrowed <- e })

	escrow := &fakeOrderEscrow{held: map[[32]byte]*OnChainOrder{testKey(id): paidFor()}}
	n, err := newSweep(db, escrow, bus).ExpireUnpaidOrders(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("expected nothing cancelled, got %d, %v", n, err)
	}
	var row struct {
		Status, EscrowContractAddress, PlatformFee, TotalAmount string
	}
	db.Raw(`SELECT status, escrow_contract_address, platform_fee, total_amount FROM orders WHERE id = ?`, id).Scan(&row)
	if row.Status != models.OrderStatusEscrowed || row.EscrowContractAddress != testEscrow ||
		row.PlatformFee != "1568" || row.TotalAmount != "78400" {
		t.Fatalf("paid order must be recorded as the escrow holds it: %+v", row)
	}
	if productStock(t, db, product) != 0 {
		t.Fatal("a paid order keeps its unit")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-escrowed:
		case <-time.After(2 * time.Second):
			t.Fatal("buyer and seller must be told the payment is held")
		}
	}
}

// The escrow's orderId derives from the order's UUID, so anyone can escrow
// under it. A record that does not pay this order's seller, in its token, the
// priced amounts, is neither trusted as payment nor cancelled over.
func TestExpireUnpaidOrders_LeavesAMismatchedEscrowRecordAlone(t *testing.T) {
	cases := map[string]func(*OnChainOrder){
		"another seller":   func(o *OnChainOrder) { o.Seller = common.HexToAddress(testBuyerWallet) },
		"another buyer":    func(o *OnChainOrder) { o.Buyer = common.HexToAddress(testSellerWallet) },
		"already paid out": func(o *OnChainOrder) { o.State = OrderStateReleased },
		"another token":    func(o *OnChainOrder) { o.Token = common.HexToAddress(testFeeManager) },
		"a smaller price":  func(o *OnChainOrder) { o.ProductAmount = big.NewInt(1) },
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			db := setupUnpaidOrderDB(t)
			product := writeProduct(t, db, 0)
			id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)
			record := paidFor()
			tamper(record)

			escrow := &fakeOrderEscrow{held: map[[32]byte]*OnChainOrder{testKey(id): record}}
			if _, err := newSweep(db, escrow, nil).ExpireUnpaidOrders(context.Background()); err != nil {
				t.Fatalf("expire: %v", err)
			}
			if orderStatus(t, db, id) != models.OrderStatusPendingPayment || productStock(t, db, product) != 0 {
				t.Fatal("a mismatched record must leave the order pending, stock held")
			}
		})
	}
}

// An order the sweep cannot settle is skipped every tick; it must not stop the
// sweep reaching abandoned orders behind it.
func TestExpireUnpaidOrders_PagesPastOrdersItSkips(t *testing.T) {
	defer func(n int) { unpaidOrderBatch = n }(unpaidOrderBatch)
	unpaidOrderBatch = 2

	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	mismatched := paidFor()
	mismatched.ProductAmount = big.NewInt(1)
	escrow := &fakeOrderEscrow{held: map[[32]byte]*OnChainOrder{}}
	for i := 0; i < 5; i++ {
		id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)
		if i%2 == 0 {
			escrow.held[testKey(id)] = mismatched
		}
	}

	n, err := newSweep(db, escrow, nil).ExpireUnpaidOrders(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("expected the 2 unescrowed orders expired across pages, got %d, %v", n, err)
	}
}

func TestExpireUnpaidOrders_UnreadableEscrowCancelsNothing(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	order := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)

	n, err := newSweep(db, &fakeOrderEscrow{fail: true}, nil).ExpireUnpaidOrders(context.Background())
	if err == nil {
		t.Fatal("expected the failed escrow read to be reported")
	}
	if n != 0 || orderStatus(t, db, order) != models.OrderStatusPendingPayment || productStock(t, db, product) != 0 {
		t.Fatalf("an unreadable escrow must not release stock: n=%d status=%s stock=%d",
			n, orderStatus(t, db, order), productStock(t, db, product))
	}
}

// With no OrderEscrow configured, an order never prepared on one still expires and gives its stock
// back; one that was prepared on one waits, since its money may be there.
func TestExpireUnpaidOrders_NoOrderEscrowConfigured(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	unprepared := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)
	prepared := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)
	db.Exec(`UPDATE orders SET escrow_order_key = ? WHERE id = ?`, "0x01", prepared.String())

	_, err := newSweep(db, &fakeOrderEscrow{none: true}, nil).ExpireUnpaidOrders(context.Background())
	if err == nil {
		t.Fatal("the prepared order's unreadable escrow must still be reported")
	}
	if orderStatus(t, db, unprepared) != models.OrderStatusCancelled {
		t.Fatalf("unprepared order = %s, want cancelled", orderStatus(t, db, unprepared))
	}
	if orderStatus(t, db, prepared) != models.OrderStatusPendingPayment {
		t.Fatalf("prepared order = %s, want left pending", orderStatus(t, db, prepared))
	}
}

// A buyer who starts paying after the sweep picked the order (and while it
// read the escrow) keeps it: the idle condition is part of the cancel itself.
func TestExpireUnpaidOrders_SparesAnOrderPaidForDuringTheSweep(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)

	n, err := newSweep(db, touchingEscrow{db: db, id: id}, nil).ExpireUnpaidOrders(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("expected nothing expired, got %d, %v", n, err)
	}
	if orderStatus(t, db, id) != models.OrderStatusPendingPayment || productStock(t, db, product) != 0 {
		t.Fatal("an order being paid for must not be cancelled")
	}
}

// touchingEscrow holds nothing, and the buyer's prepare-approve touches the
// order while it is being read — i.e. mid-sweep.
type touchingEscrow struct {
	db *gorm.DB
	id uuid.UUID
}

func (e touchingEscrow) EscrowedOrder(context.Context, [32]byte) (*OnChainOrder, error) {
	return nil, TouchUnpaidOrder(e.db, e.id)
}

func (touchingEscrow) CurrentEscrowAddress() string { return testEscrow }

func TestCancelUnpaidOrder_LeavesAPaidOrderAlone(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusEscrowed, time.Minute, time.Minute)

	cancelled, err := CancelUnpaidOrder(db, &models.Order{ID: id, ProductID: product, Quantity: 1, Kind: models.OrderKindProduct})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled || orderStatus(t, db, id) != models.OrderStatusEscrowed || productStock(t, db, product) != 0 {
		t.Fatal("a paid order must not be cancelled or have its stock returned")
	}
}

func TestTouchUnpaidOrder_RestartsTheClockOnlyWhileUnpaid(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	pending := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)
	paid := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusEscrowed, 2*time.Hour, 2*time.Hour)

	for _, id := range []uuid.UUID{pending, paid} {
		if err := TouchUnpaidOrder(db, id); err != nil {
			t.Fatalf("touch: %v", err)
		}
	}
	var fresh int
	db.Raw(`SELECT COUNT(*) FROM orders WHERE updated_at > ?`, time.Now().Add(-time.Minute)).Scan(&fresh)
	if fresh != 1 {
		t.Fatalf("only the unpaid order's clock restarts; %d orders touched", fresh)
	}
}

func TestMarkOrderEscrowed_OnlyAnOrderAwaitingPayment(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 1)
	pending := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, time.Minute, time.Minute)
	cancelled := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusCancelled, time.Hour, time.Minute)
	updates := func() map[string]interface{} {
		return EscrowedOrderUpdates(EscrowRecord{OrderID: GenerateOrderID(uuid.New()), Buyer: common.HexToAddress(testBuyerWallet),
			ProductAmount: big.NewInt(78400), ShippingAmount: big.NewInt(0), MerchantFee: big.NewInt(1568),
			DeliverBy: time.Now().Add(7 * 24 * time.Hour), EscrowAddress: testEscrow})
	}

	if err := MarkOrderEscrowed(db, pending, updates()); err != nil || orderStatus(t, db, pending) != models.OrderStatusEscrowed {
		t.Fatalf("pending → escrowed: %v, %s", err, orderStatus(t, db, pending))
	}
	// A cancelled order is never revived: its unit may be resold, and the
	// cancel may have been the seller's.
	if err := MarkOrderEscrowed(db, cancelled, updates()); !errors.Is(err, ErrOrderNotPayable) {
		t.Fatalf("expected ErrOrderNotPayable, got %v", err)
	}
	if orderStatus(t, db, cancelled) != models.OrderStatusCancelled || productStock(t, db, product) != 1 {
		t.Fatal("a cancelled order must stay cancelled, its stock untouched")
	}
}

func TestEscrowedOrder_ReadsTheRecord(t *testing.T) {
	key := testKey(uuid.New())
	want := paidFor()
	chain := NewFakeEscrowChain()
	chain.SetOrder(key, *want)
	held := &EscrowService{escrowAddress: testEscrow, caller: chain}
	got, err := held.EscrowedOrder(context.Background(), key)
	if err != nil || got == nil {
		t.Fatalf("escrowed order: %v, %v", got, err)
	}
	if got.Seller != want.Seller || got.Token != want.Token || got.ProductAmount.Cmp(want.ProductAmount) != 0 ||
		got.MerchantFee.Cmp(want.MerchantFee) != 0 || got.CreatedAt.Unix() != want.CreatedAt.Unix() ||
		got.DeliverBy.Unix() != want.DeliverBy.Unix() || got.State != OrderStateFunded || got.Resolved() {
		t.Fatalf("record read wrong: %+v", got)
	}

	if got, err := held.EscrowedOrder(context.Background(), testKey(uuid.New())); err != nil || got != nil {
		t.Fatalf("unknown order: got %v, %v", got, err)
	}
	if _, err := (&EscrowService{escrowAddress: testEscrow}).EscrowedOrder(context.Background(), key); err == nil {
		t.Fatal("no client must be an error, never \"not escrowed\"")
	}
}

// The seller wallet the buyer was told to pay is the one the order is checked
// against, even after the seller registers another.
func TestExpireUnpaidOrders_UsesTheWalletTheBuyerWasToldToPay(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)
	// The seller has since moved to another wallet; the buyer paid the old one.
	db.Exec(`UPDATE orders SET escrow_seller_wallet = ? WHERE id = ?`, testSellerWallet, id)
	db.Exec(`UPDATE users SET wallet_address = ? WHERE id = (SELECT seller_id FROM orders WHERE id = ?)`, testBuyerWallet, id)

	escrow := &fakeOrderEscrow{held: map[[32]byte]*OnChainOrder{testKey(id): paidFor()}}
	if _, err := newSweep(db, escrow, nil).ExpireUnpaidOrders(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	db.Raw(`SELECT status FROM orders WHERE id = ?`, id).Scan(&status)
	if status != models.OrderStatusEscrowed {
		t.Fatalf("a correct payment to the wallet the buyer was given must be recorded, got %q", status)
	}
}
