package services

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupBookingRequestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	// Raw DDL, not AutoMigrate: the model carries Postgres-only defaults
	// (gen_random_uuid, jsonb) that SQLite cannot parse.
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS booking_requests (
		id TEXT PRIMARY KEY,
		product_id TEXT NOT NULL,
		buyer_id TEXT NOT NULL,
		seller_id TEXT NOT NULL,
		token TEXT,
		service_tier TEXT,
		buyer_city TEXT,
		buyer_state TEXT,
		buyer_country TEXT,
		quantity INTEGER NOT NULL DEFAULT 0,
		answers TEXT,
		status TEXT NOT NULL,
		reject_reason TEXT,
		quoted_amount_usd REAL,
		order_id TEXT,
		approved_at DATETIME,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	return db
}

func writeRequest(t *testing.T, db *gorm.DB, status string, age time.Duration) uuid.UUID {
	t.Helper()
	at := time.Now().Add(-age)
	r := &models.BookingRequest{
		ID:        uuid.New(),
		ProductID: uuid.New(),
		BuyerID:   uuid.New(),
		SellerID:  uuid.New(),
		Token:     "USDC",
		Status:    status,
		CreatedAt: at,
		UpdatedAt: at,
	}
	if err := db.Create(r).Error; err != nil {
		t.Fatalf("failed to write request: %v", err)
	}
	return r.ID
}

func statusOf(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var r models.BookingRequest
	if err := db.First(&r, "id = ?", id).Error; err != nil {
		t.Fatalf("failed to load request: %v", err)
	}
	return r.Status
}

// The sweep closes what has gone stale and leaves everything else alone. The
// approved window is the one that matters for money: an approved request can
// carry a provider-named price, and without expiry a buyer could pay a quote
// long after the provider's costs moved.
func TestExpireStaleBookingRequests(t *testing.T) {
	db := setupBookingRequestDB(t)
	svc := NewBookingRequestService(db)

	staleRequested := writeRequest(t, db, models.BookingRequestRequested, 8*24*time.Hour)
	freshRequested := writeRequest(t, db, models.BookingRequestRequested, 2*24*time.Hour)
	staleApproved := writeRequest(t, db, models.BookingRequestApproved, 72*time.Hour)
	freshApproved := writeRequest(t, db, models.BookingRequestApproved, 12*time.Hour)
	// Terminal states must never be rewritten, however old.
	oldConverted := writeRequest(t, db, models.BookingRequestConverted, 90*24*time.Hour)
	oldRejected := writeRequest(t, db, models.BookingRequestRejected, 90*24*time.Hour)

	n, err := svc.ExpireStaleBookingRequests(context.Background())
	if err != nil {
		t.Fatalf("sweep failed: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 rows expired, got %d", n)
	}

	if got := statusOf(t, db, staleRequested); got != models.BookingRequestExpired {
		t.Errorf("stale requested: want expired, got %q", got)
	}
	if got := statusOf(t, db, staleApproved); got != models.BookingRequestExpired {
		t.Errorf("stale approved: want expired, got %q", got)
	}
	if got := statusOf(t, db, freshRequested); got != models.BookingRequestRequested {
		t.Errorf("fresh requested: want untouched, got %q", got)
	}
	if got := statusOf(t, db, freshApproved); got != models.BookingRequestApproved {
		t.Errorf("fresh approved: want untouched, got %q", got)
	}
	if got := statusOf(t, db, oldConverted); got != models.BookingRequestConverted {
		t.Errorf("converted: want untouched, got %q", got)
	}
	if got := statusOf(t, db, oldRejected); got != models.BookingRequestRejected {
		t.Errorf("rejected: want untouched, got %q", got)
	}
}

// A missed tick is recovered by the next one, so the sweep has to be safe to
// run repeatedly — the second pass must find nothing left to do.
func TestExpireStaleBookingRequestsIsIdempotent(t *testing.T) {
	db := setupBookingRequestDB(t)
	svc := NewBookingRequestService(db)

	writeRequest(t, db, models.BookingRequestRequested, 8*24*time.Hour)
	writeRequest(t, db, models.BookingRequestApproved, 72*time.Hour)

	first, err := svc.ExpireStaleBookingRequests(context.Background())
	if err != nil {
		t.Fatalf("first sweep failed: %v", err)
	}
	if first != 2 {
		t.Fatalf("first sweep: want 2, got %d", first)
	}

	second, err := svc.ExpireStaleBookingRequests(context.Background())
	if err != nil {
		t.Fatalf("second sweep failed: %v", err)
	}
	if second != 0 {
		t.Errorf("second sweep: want 0, got %d", second)
	}
}

// An approval expires from when it was given, not from the request's last
// change: paying and cancelling (which reopens a request) cannot extend it.
func TestExpireStaleBookingRequests_ApprovalRunsFromApprovedAt(t *testing.T) {
	db := setupBookingRequestDB(t)
	stale := writeRequest(t, db, models.BookingRequestApproved, time.Minute) // touched just now
	db.Model(&models.BookingRequest{}).Where("id = ?", stale).
		Update("approved_at", time.Now().Add(-BookingRequestApprovalTTL-time.Hour))
	fresh := writeRequest(t, db, models.BookingRequestApproved, BookingRequestApprovalTTL+time.Hour) // old row, recent approval
	db.Model(&models.BookingRequest{}).Where("id = ?", fresh).Update("approved_at", time.Now().Add(-time.Hour))

	if _, err := NewBookingRequestService(db).ExpireStaleBookingRequests(context.Background()); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if got := statusOf(t, db, stale); got != models.BookingRequestExpired {
		t.Errorf("approval given long ago: %q, want expired", got)
	}
	if got := statusOf(t, db, fresh); got != models.BookingRequestApproved {
		t.Errorf("recent approval: %q, want approved", got)
	}
}
