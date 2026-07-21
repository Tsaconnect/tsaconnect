package services

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/ojimcy/tsa-api-go/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupFWCustomerDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.Exec(`CREATE TABLE flutterwave_customers (
		id TEXT PRIMARY KEY,
		base_url TEXT NOT NULL UNIQUE,
		customer_id TEXT NOT NULL,
		email TEXT,
		created_at DATETIME,
		updated_at DATETIME
	)`).Error; err != nil {
		t.Fatalf("ddl: %v", err)
	}
	return db
}

const testBaseURL = "https://sandbox.example"

func companyParams() CompanyCustomerParams {
	return CompanyCustomerParams{Name: "TSA", Email: "pay@tsa.test", Phone: "+2348000000000"}
}

func TestResolveCustomer_EnvOverrideWins(t *testing.T) {
	db := setupFWCustomerDB(t)
	// No client needed — the env override short-circuits before any API/DB.
	id, err := ResolveFlutterwaveCompanyCustomer(context.Background(), db, nil, testBaseURL, "cust_env_123", companyParams())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id != "cust_env_123" {
		t.Fatalf("id = %q, want the env override", id)
	}
	var n int64
	db.Model(&models.FlutterwaveCustomer{}).Count(&n)
	if n != 0 {
		t.Fatalf("env override must not persist anything; got %d rows", n)
	}
}

func TestResolveCustomer_DormantWhenFWDisabled(t *testing.T) {
	db := setupFWCustomerDB(t)
	id, err := ResolveFlutterwaveCompanyCustomer(context.Background(), db, nil, testBaseURL, "", companyParams())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty (FW not configured → dormant)", id)
	}
}

func TestResolveCustomer_CreatesOnceThenReuses(t *testing.T) {
	db := setupFWCustomerDB(t)
	var createCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(new(int32)))
	mux.HandleFunc("/customers", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&createCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "success",
			"data":   map[string]any{"id": "cust_minted_1"},
		})
	})
	c, _ := newFWTestServer(t, mux)

	// First resolve: mints + persists.
	id, err := ResolveFlutterwaveCompanyCustomer(context.Background(), db, c, testBaseURL, "", companyParams())
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if id != "cust_minted_1" {
		t.Fatalf("id = %q, want cust_minted_1", id)
	}
	var row models.FlutterwaveCustomer
	if err := db.First(&row, "base_url = ?", testBaseURL).Error; err != nil {
		t.Fatalf("expected a persisted row: %v", err)
	}

	// Second resolve: reuses the persisted id, NO second API call.
	id2, err := ResolveFlutterwaveCompanyCustomer(context.Background(), db, c, testBaseURL, "", companyParams())
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if id2 != "cust_minted_1" {
		t.Fatalf("second id = %q, want the reused id", id2)
	}
	if atomic.LoadInt32(&createCalls) != 1 {
		t.Fatalf("customers endpoint hit %d times, want 1 (create once)", createCalls)
	}
}

func TestResolveCustomer_DifferentBaseURLMintsSeparately(t *testing.T) {
	db := setupFWCustomerDB(t)
	// Pre-persist a sandbox customer.
	db.Create(&models.FlutterwaveCustomer{BaseURL: testBaseURL, CustomerID: "cust_sandbox", Email: "x@y.z"})

	var createCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(new(int32)))
	mux.HandleFunc("/customers", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&createCalls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"id": "cust_live"}})
	})
	c, srv := newFWTestServer(t, mux)

	// A DIFFERENT base URL must NOT reuse the sandbox id — it mints its own,
	// so a sandbox↔live switch is safe.
	id, err := ResolveFlutterwaveCompanyCustomer(context.Background(), db, c, srv.URL, "", companyParams())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id != "cust_live" {
		t.Fatalf("id = %q, want a freshly minted id for the new environment", id)
	}
	if atomic.LoadInt32(&createCalls) != 1 {
		t.Fatalf("expected exactly one create for the new environment; got %d", createCalls)
	}
}

func TestResolveCustomer_RequiresEmailToMint(t *testing.T) {
	db := setupFWCustomerDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(new(int32)))
	c, _ := newFWTestServer(t, mux)

	_, err := ResolveFlutterwaveCompanyCustomer(context.Background(), db, c, testBaseURL, "",
		CompanyCustomerParams{Name: "TSA", Email: "", Phone: ""})
	if err == nil {
		t.Fatal("expected an error when no company email is available to mint a customer")
	}
}
