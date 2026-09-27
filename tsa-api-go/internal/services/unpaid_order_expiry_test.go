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
		`CREATE TABLE orders (id TEXT PRIMARY KEY, product_id TEXT, quantity INTEGER, kind TEXT,
			status TEXT, created_at DATETIME, updated_at DATETIME)`,
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

func writeOrder(t *testing.T, db *gorm.DB, productID uuid.UUID, kind, status string, qty int, age time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	at := time.Now().Add(-age)
	if err := db.Exec(`INSERT INTO orders (id, product_id, quantity, kind, status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, productID, qty, kind, status, at, at).Error; err != nil {
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

func TestExpireUnpaidOrders_ReleasesStockOnlyForStaleUnescrowedOrders(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	service := writeProduct(t, db, 5)

	abandoned := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 1, 2*time.Hour)
	paidUnconfirmed := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 1, 2*time.Hour)
	inFlight := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 1, 10*time.Minute)
	paid := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusEscrowed, 1, 2*time.Hour)
	serviceOrder := writeOrder(t, db, service, models.OrderKindService, models.OrderStatusPendingPayment, 1, 2*time.Hour)

	escrow := &fakeOrderEscrow{escrowed: map[[32]byte]bool{GenerateOrderID(paidUnconfirmed): true}}
	n, err := NewUnpaidOrderExpiryService(db, escrow).ExpireUnpaidOrders(context.Background())
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 orders expired, got %d", n)
	}

	want := map[uuid.UUID]string{
		abandoned:       models.OrderStatusCancelled,
		paidUnconfirmed: models.OrderStatusPendingPayment, // the escrow holds the buyer's money
		inFlight:        models.OrderStatusPendingPayment, // payment may still be landing
		paid:            models.OrderStatusEscrowed,
		serviceOrder:    models.OrderStatusCancelled,
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
		t.Errorf("service stock %d, want 5 (services hold no inventory)", got)
	}
}

func TestExpireUnpaidOrders_UnreadableEscrowCancelsNothing(t *testing.T) {
	db := setupUnpaidOrderDB(t)
	product := writeProduct(t, db, 0)
	order := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusPendingPayment, 1, 2*time.Hour)

	n, err := NewUnpaidOrderExpiryService(db, &fakeOrderEscrow{fail: true}).ExpireUnpaidOrders(context.Background())
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
	id := writeOrder(t, db, product, models.OrderKindProduct, models.OrderStatusEscrowed, 1, time.Minute)

	cancelled, err := CancelUnpaidOrder(db, &models.Order{ID: id, ProductID: product, Quantity: 1, Kind: models.OrderKindProduct})
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if cancelled || orderStatus(t, db, id) != models.OrderStatusEscrowed || productStock(t, db, product) != 0 {
		t.Fatal("a paid order must not be cancelled or have its stock returned")
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
