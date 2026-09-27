package services

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/events"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
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
			quantity INTEGER, kind TEXT, status TEXT, escrow_tx_hash TEXT, created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE products (id TEXT PRIMARY KEY, stock INTEGER, deleted_at DATETIME)`,
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

// writeOrder stores an order placed `placed` ago whose last payment activity
// was `active` ago.
func writeOrder(t *testing.T, db *gorm.DB, productID uuid.UUID, kind, status string, placed, active time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if err := db.Exec(`INSERT INTO orders (id, buyer_id, seller_id, product_id, quantity, kind, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		id, uuid.New(), uuid.New(), productID, kind, status, time.Now().Add(-placed), time.Now().Add(-active)).Error; err != nil {
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

// fakeOrderEscrow answers OrderEscrowed from a set of escrowed order ids.
type fakeOrderEscrow struct {
	escrowed map[[32]byte]bool
	fail     bool
}

func (f *fakeOrderEscrow) OrderEscrowed(_ context.Context, id [32]byte) (bool, error) {
	if f.fail {
		return false, errors.New("rpc down")
	}
	return f.escrowed[id], nil
}

func TestExpireUnpaidOrders_ReleasesStockOnlyForIdleUnescrowedProductOrders(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	service := writeProduct(t, db, 5)
	h2, m10 := 2*time.Hour, 10*time.Minute
	beforeLaunch := time.Since(unpaidOrderExpiryFrom) + 24*time.Hour

	abandoned := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, h2, h2)
	paidUnconfirmed := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, h2, h2)
	justPlaced := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, m10, m10)
	retriedNow := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, h2, m10)
	historical := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, beforeLaunch, beforeLaunch)
	paid := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusEscrowed, h2, h2)
	serviceOrder := writeOrder(t, db, service, models.OrderKindService, models.OrderStatusPendingPayment, h2, h2)

	bus := events.NewBus()
	defer bus.Stop()
	notified := make(chan uuid.UUID, 4)
	bus.Subscribe(events.OrderRefunded, func(e events.Event) { notified <- e.UserID })

	escrow := &fakeOrderEscrow{escrowed: map[[32]byte]bool{GenerateOrderID(paidUnconfirmed): true}}
	n, err := NewUnpaidOrderExpiryService(db, escrow, bus).ExpireUnpaidOrders(context.Background())
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 order expired, got %d", n)
	}

	want := map[uuid.UUID]string{
		abandoned:       models.OrderStatusCancelled,
		paidUnconfirmed: models.OrderStatusPendingPayment, // the escrow holds the buyer's money
		justPlaced:      models.OrderStatusPendingPayment,
		retriedNow:      models.OrderStatusPendingPayment, // the buyer is paying right now
		historical:      models.OrderStatusPendingPayment, // placed before the sweep shipped
		paid:            models.OrderStatusEscrowed,
		serviceOrder:    models.OrderStatusPendingPayment, // services hold no stock
	}
	for id, status := range want {
		if got := orderStatus(t, db, id); got != status {
			t.Errorf("order %s: status %q, want %q", id, got, status)
		}
	}
	if got := productStock(t, db, product); got != 1 {
		t.Errorf("product stock %d, want 1 (only the abandoned order's unit returns)", got)
	}

	var buyer, seller string
	db.Raw(`SELECT buyer_id, seller_id FROM orders WHERE id = ?`, abandoned).Row().Scan(&buyer, &seller)
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case id := <-notified:
			got[id.String()] = true
		case <-time.After(2 * time.Second):
			t.Fatal("buyer and seller must both be told the order was cancelled")
		}
	}
	if !got[buyer] || !got[seller] {
		t.Fatalf("notified %v, want buyer %s and seller %s", got, buyer, seller)
	}
}

// An order the escrow holds is skipped every tick; it must not stop the
// sweep reaching abandoned orders behind it.
func TestExpireUnpaidOrders_PagesPastOrdersItSkips(t *testing.T) {
	defer func(n int) { unpaidOrderBatch = n }(unpaidOrderBatch)
	unpaidOrderBatch = 2

	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	escrow := &fakeOrderEscrow{escrowed: map[[32]byte]bool{}}
	for i := 0; i < 5; i++ {
		id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)
		if i%2 == 0 {
			escrow.escrowed[GenerateOrderID(id)] = true
		}
	}

	n, err := NewUnpaidOrderExpiryService(db, escrow, nil).ExpireUnpaidOrders(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("expected the 2 unescrowed orders expired across pages, got %d, %v", n, err)
	}
}

func TestExpireUnpaidOrders_UnreadableEscrowCancelsNothing(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	order := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 2*time.Hour, 2*time.Hour)

	n, err := NewUnpaidOrderExpiryService(db, &fakeOrderEscrow{fail: true}, nil).ExpireUnpaidOrders(context.Background())
	if err == nil {
		t.Fatal("expected the failed escrow read to be reported")
	}
	if n != 0 || orderStatus(t, db, order) != models.OrderStatusPendingPayment || productStock(t, db, product) != 0 {
		t.Fatalf("an unreadable escrow must not release stock: n=%d status=%s stock=%d",
			n, orderStatus(t, db, order), productStock(t, db, product))
	}
}

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

func escrowedUpdates() map[string]interface{} {
	return map[string]interface{}{"status": models.OrderStatusEscrowed, "escrow_tx_hash": "0xabc"}
}

func TestMarkOrderEscrowed_PendingOrderBecomesEscrowed(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, time.Minute, time.Minute)

	revived, err := MarkOrderEscrowed(db, &models.Order{ID: id, ProductID: product, Quantity: 1}, escrowedUpdates())
	if err != nil || revived || orderStatus(t, db, id) != models.OrderStatusEscrowed || productStock(t, db, product) != 0 {
		t.Fatalf("pending → escrowed with stock untouched: revived=%v err=%v status=%s stock=%d",
			revived, err, orderStatus(t, db, id), productStock(t, db, product))
	}
}

// The expiry (or a buyer cancel) may beat a slow submit-escrow. The buyer's
// money is in escrow either way, so a proven payment restores the order and
// takes its stock back.
func TestMarkOrderEscrowed_ProvenPaymentRestoresACancelledOrder(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 1) // the cancel returned the unit
	id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusCancelled, time.Hour, time.Minute)

	revived, err := MarkOrderEscrowed(db, &models.Order{ID: id, ProductID: product, Quantity: 1, Kind: models.OrderKindProduct}, escrowedUpdates())
	if err != nil || !revived {
		t.Fatalf("expected the cancelled order restored, got revived=%v err=%v", revived, err)
	}
	if orderStatus(t, db, id) != models.OrderStatusEscrowed || productStock(t, db, product) != 0 {
		t.Fatalf("restored order must be escrowed and hold its unit: status=%s stock=%d",
			orderStatus(t, db, id), productStock(t, db, product))
	}
}

func TestMarkOrderEscrowed_RefusesAnOrderPastPayment(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusShipped, time.Hour, time.Minute)

	if _, err := MarkOrderEscrowed(db, &models.Order{ID: id, ProductID: product, Quantity: 1}, escrowedUpdates()); !errors.Is(err, ErrOrderNotPayable) {
		t.Fatalf("expected ErrOrderNotPayable, got %v", err)
	}
	if orderStatus(t, db, id) != models.OrderStatusShipped {
		t.Fatal("a shipped order must not be moved back to escrowed")
	}
}

// ordersChain answers the escrow's orders(bytes32) getter.
type ordersChain struct{ buyer common.Address }

func (c ordersChain) CallContract(_ context.Context, _ ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	z := common.Address{}
	return parsedEscrowABI.Methods["orders"].Outputs.Pack(
		c.buyer, z, z, big.NewInt(0), big.NewInt(0), big.NewInt(0), z, big.NewInt(0), false, false, false, false)
}

func TestOrderEscrowed_ReadsTheBuyer(t *testing.T) {
	id := GenerateOrderID(uuid.New())
	held := &EscrowService{escrowAddress: testEscrow, caller: ordersChain{buyer: common.HexToAddress("0x87C2422Be9a11B71a5C530Ecd156134d3B04E3Ce")}}
	if ok, err := held.OrderEscrowed(context.Background(), id); err != nil || !ok {
		t.Fatalf("escrowed order: got %v, %v", ok, err)
	}
	empty := &EscrowService{escrowAddress: testEscrow, caller: ordersChain{}}
	if ok, err := empty.OrderEscrowed(context.Background(), id); err != nil || ok {
		t.Fatalf("unknown order: got %v, %v", ok, err)
	}
	if _, err := (&EscrowService{escrowAddress: testEscrow}).OrderEscrowed(context.Background(), id); err == nil {
		t.Fatal("no client must be an error, never \"not escrowed\"")
	}
}
