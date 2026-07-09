package tron

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetAccountParsesBalances(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"balance":1500000,"trc20":[{"TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t":"2000000"}]}]}`))
	}))
	defer srv.Close()

	tg := NewTronGrid(srv.URL, "")
	acc, err := tg.GetAccount(context.Background(), "TXYZ")
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if acc.BalanceSun != 1500000 {
		t.Fatalf("want 1500000 got %d", acc.BalanceSun)
	}
	if acc.TRC20["TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"] != "2000000" {
		t.Fatalf("trc20 not parsed: %+v", acc.TRC20)
	}
}

func TestGetAccountInactiveIsNotError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":[]}`)) // never-activated account
	}))
	defer srv.Close()

	tg := NewTronGrid(srv.URL, "")
	acc, err := tg.GetAccount(context.Background(), "TNEW")
	if err != nil {
		t.Fatalf("expected nil err for inactive account, got %v", err)
	}
	if acc.Active {
		t.Fatal("inactive account must report Active=false")
	}
}
