package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func fwTransferReplying(t *testing.T, status int, cacheHit bool, body map[string]any) *FlutterwaveGateway {
	t.Helper()
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/direct-transfers", func(w http.ResponseWriter, r *http.Request) {
		if cacheHit {
			w.Header().Set("X-Idempotency-Cache-Hit", "true")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	})
	c, _ := newFWTestServer(t, mux)
	return NewFlutterwaveGateway(c, "TSA Connect")
}

var testPayoutReq = PayoutRequest{
	BankCode: "044", AccountNumber: "0690000031", AccountName: "Ada Obi",
	Amount: "1500.00", SourceCurrency: "NGN", Reference: "IT-SELL-0123456789abcdef0123456789abcdef",
}

func TestFlutterwavePayout_4xxIsProviderRejection(t *testing.T) {
	g := fwTransferReplying(t, http.StatusBadRequest, false, map[string]any{
		"status": "failed", "error": map[string]any{"type": "REQUEST_NOT_VALID", "message": "Insufficient balance"},
	})
	_, err := g.InitiatePayout(context.Background(), testPayoutReq)
	var rej *ProviderRejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want a ProviderRejectedError", err)
	}
	if rej.Replayed {
		t.Fatal("a response without the idempotency cache-hit header must not read as replayed")
	}
	if !strings.Contains(err.Error(), "HTTP 400") || !strings.Contains(err.Error(), "Insufficient balance") {
		t.Fatalf("error text lost provider detail: %v", err)
	}
}

func TestFlutterwavePayout_CacheHitMarksReplayed(t *testing.T) {
	g := fwTransferReplying(t, http.StatusBadRequest, true, map[string]any{"status": "failed", "message": "Insufficient balance"})
	_, err := g.InitiatePayout(context.Background(), testPayoutReq)
	var rej *ProviderRejectedError
	if !errors.As(err, &rej) || !rej.Replayed {
		t.Fatalf("err = %v, want a replayed ProviderRejectedError", err)
	}
}

// A 5xx, a conflict or a rate-limit says nothing about whether a transfer
// exists, so none of them may be classified as a rejection.
func TestFlutterwavePayout_AmbiguousStatusesAreNotRejections(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusConflict, http.StatusTooManyRequests} {
		g := fwTransferReplying(t, code, false, map[string]any{"status": "failed", "message": "x"})
		_, err := g.InitiatePayout(context.Background(), testPayoutReq)
		var rej *ProviderRejectedError
		if err == nil || errors.As(err, &rej) {
			t.Fatalf("HTTP %d: err = %v, want an error that is NOT a rejection", code, err)
		}
	}
}

// REGRESSION: a 2xx envelope whose transfer is already FAILED was returned as
// a live payout id, so the sell waited for a success that could never come.
func TestFlutterwavePayout_FailedTransferIn2xxIsRejection(t *testing.T) {
	g := fwTransferReplying(t, http.StatusCreated, false, map[string]any{
		"status": "success",
		"data":   map[string]any{"id": "trf_dead", "reference": testPayoutReq.Reference, "status": "FAILED"},
	})
	id, err := g.InitiatePayout(context.Background(), testPayoutReq)
	var rej *ProviderRejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("id=%q err=%v, want a ProviderRejectedError for a FAILED transfer", id, err)
	}
	if !strings.Contains(err.Error(), "trf_dead") {
		t.Fatalf("rejection should name the failed transfer for operators: %v", err)
	}
}

func TestFlutterwavePayout_DisabledGatewayIsRejection(t *testing.T) {
	g := NewFlutterwaveGateway(nil, "TSA Connect")
	_, err := g.InitiatePayout(context.Background(), testPayoutReq)
	var rej *ProviderRejectedError
	if !errors.As(err, &rej) || !errors.Is(err, ErrFlutterwaveDisabled) {
		t.Fatalf("err = %v, want a rejection that still matches ErrFlutterwaveDisabled", err)
	}
}

func TestFindTransferByReference_PagesUntilFound(t *testing.T) {
	var tokenCalls int32
	var pages []string
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("from") == "" || q.Get("to") == "" {
			http.Error(w, "window required", http.StatusBadRequest)
			return
		}
		pages = append(pages, q.Get("next"))
		if q.Get("next") == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
				"cursor":    map[string]any{"next": "cur2", "has_more_items": true},
				"transfers": []map[string]any{{"id": "trf_other", "reference": "someone-else", "status": "SUCCESSFUL"}},
			}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
			"cursor":    map[string]any{"has_more_items": false},
			"transfers": []map[string]any{{"id": "trf_mine", "reference": "IT-SELL-abc", "status": "PENDING"}},
		}})
	})
	c, _ := newFWTestServer(t, mux)

	tr, found, err := c.FindTransferByReference(context.Background(), "IT-SELL-abc", time.Now().Add(-time.Hour), time.Now())
	if err != nil || !found || tr.ID != "trf_mine" || tr.Status != "PENDING" {
		t.Fatalf("got tr=%+v found=%v err=%v, want trf_mine PENDING", tr, found, err)
	}
	if len(pages) != 2 || pages[1] != "cur2" {
		t.Fatalf("pages requested = %v, want first page then cursor cur2", pages)
	}

	_, found, err = c.FindTransferByReference(context.Background(), "IT-SELL-missing", time.Now().Add(-time.Hour), time.Now())
	if err != nil || found {
		t.Fatalf("missing reference: found=%v err=%v, want not found and no error", found, err)
	}
}

// A provider that keeps handing back the same cursor must not be read as
// "searched everything, not there" — absence is only evidence after a
// complete scan.
func TestFindTransferByReference_StuckCursorIsAnError(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
			"cursor":    map[string]any{"next": "same", "has_more_items": true},
			"transfers": []map[string]any{{"id": "trf_x", "reference": "other", "status": "SUCCESSFUL"}},
		}})
	})
	c, _ := newFWTestServer(t, mux)
	if _, found, err := c.FindTransferByReference(context.Background(), "IT-SELL-abc", time.Now().Add(-time.Hour), time.Now()); err == nil || found {
		t.Fatalf("found=%v err=%v, want an error for a cursor that never advances", found, err)
	}
}
