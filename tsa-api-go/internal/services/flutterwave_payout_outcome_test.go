package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
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

// fwTransfersServer serves GET /transfers from a page function keyed by the
// cursor the client sends, recording every query.
func fwTransfersServer(t *testing.T, page func(q url.Values) any) (*FlutterwaveClient, *[]url.Values) {
	t.Helper()
	var tokenCalls int32
	var seen []url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/flutterwave/protocol/openid-connect/token", fwTokenHandler(&tokenCalls))
	mux.HandleFunc("/transfers", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Query())
		_ = json.NewEncoder(w).Encode(page(r.URL.Query()))
	})
	c, _ := newFWTestServer(t, mux)
	return c, &seen
}

var fwAround = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

func fwTr(id, ref, status string, at time.Time) map[string]any {
	return map[string]any{"id": id, "reference": ref, "status": status, "created_datetime": at.Format(time.RFC3339)}
}

func TestFindTransferByReference_ScansWholeWindowWithDateOnlyBounds(t *testing.T) {
	c, seen := fwTransfersServer(t, func(q url.Values) any {
		if q.Get("next") == "" {
			return map[string]any{"status": "success", "data": map[string]any{
				"cursor":    map[string]any{"next": "cur2", "has_more_items": true},
				"transfers": []any{fwTr("trf_mine", "IT-SELL-abc", "PENDING", fwAround)},
			}}
		}
		return map[string]any{"status": "success", "data": map[string]any{
			"cursor":    map[string]any{"has_more_items": false},
			"transfers": []any{fwTr("trf_other", "someone-else", "SUCCESSFUL", fwAround)},
		}}
	})
	tr, found, err := c.FindTransferByReference(context.Background(), "IT-SELL-abc", fwAround)
	if err != nil || !found || tr.ID != "trf_mine" {
		t.Fatalf("tr=%+v found=%v err=%v", tr, found, err)
	}
	if len(*seen) != 2 {
		t.Fatalf("pages = %d, want the whole window scanned even after a match", len(*seen))
	}
	// Date-only bounds (the listing convention); `to` is exclusive so it is
	// pushed a day past the end of the ±1 day window.
	if q := (*seen)[0]; q.Get("from") != "2026-09-09" || q.Get("to") != "2026-09-12" {
		t.Fatalf("from/to = %q/%q, want 2026-09-09/2026-09-12", q.Get("from"), q.Get("to"))
	}
}

func TestFindTransferByReference_AbsentOnlyAfterCompleteScan(t *testing.T) {
	c, _ := fwTransfersServer(t, func(url.Values) any {
		return map[string]any{"status": "success", "data": map[string]any{
			"cursor":    map[string]any{"has_more_items": false},
			"transfers": []any{fwTr("trf_x", "other", "SUCCESSFUL", fwAround)},
		}}
	})
	if _, found, err := c.FindTransferByReference(context.Background(), "IT-SELL-missing", fwAround); err != nil || found {
		t.Fatalf("found=%v err=%v, want a clean not-found", found, err)
	}
}

// Any shape we do not positively recognise must be an error: read as
// "absent", it would let an admin reverse a sell whose payout exists.
func TestFindTransferByReference_UnrecognisedShapesAreErrors(t *testing.T) {
	cases := map[string]any{
		"missing has_more_items": map[string]any{"status": "success", "data": map[string]any{
			"cursor": map[string]any{"next": ""}, "transfers": []any{}}},
		"missing cursor": map[string]any{"status": "success", "data": map[string]any{"transfers": []any{}}},
		"missing list key": map[string]any{"status": "success", "data": map[string]any{
			"cursor": map[string]any{"has_more_items": false}, "items": []any{}}},
		"data is an array": map[string]any{"status": "success", "data": []any{fwTr("trf_x", "IT-SELL-abc", "SUCCESSFUL", fwAround)}},
		"transfer without reference": map[string]any{"status": "success", "data": map[string]any{
			"cursor": map[string]any{"has_more_items": false}, "transfers": []any{map[string]any{"id": "trf_x", "status": "SUCCESSFUL", "created_datetime": fwAround.Format(time.RFC3339)}}}},
		"transfer without created_datetime": map[string]any{"status": "success", "data": map[string]any{
			"cursor": map[string]any{"has_more_items": false}, "transfers": []any{map[string]any{"id": "trf_x", "reference": "other", "status": "SUCCESSFUL"}}}},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := fwTransfersServer(t, func(url.Values) any { return body })
			if _, found, err := c.FindTransferByReference(context.Background(), "IT-SELL-abc", fwAround); err == nil || found {
				t.Fatalf("found=%v err=%v, want an error", found, err)
			}
		})
	}
}

func TestFindTransferByReference_DuplicateReferenceIsAnError(t *testing.T) {
	c, _ := fwTransfersServer(t, func(url.Values) any {
		return map[string]any{"status": "success", "data": map[string]any{
			"cursor": map[string]any{"has_more_items": false},
			"transfers": []any{
				fwTr("trf_1", "IT-SELL-abc", "FAILED", fwAround),
				fwTr("trf_2", "IT-SELL-abc", "SUCCESSFUL", fwAround.Add(time.Minute)),
			},
		}}
	})
	if _, _, err := c.FindTransferByReference(context.Background(), "IT-SELL-abc", fwAround); err == nil {
		t.Fatal("two transfers on one reference must be an error, never a pick")
	}
}

// The date-only window is wider than ±1 day; rows outside it are filtered
// client-side by created_datetime.
func TestFindTransferByReference_FiltersByCreatedDatetime(t *testing.T) {
	c, _ := fwTransfersServer(t, func(url.Values) any {
		return map[string]any{"status": "success", "data": map[string]any{
			"cursor":    map[string]any{"has_more_items": false},
			"transfers": []any{fwTr("trf_old", "IT-SELL-abc", "FAILED", fwAround.Add(-50*time.Hour))},
		}}
	})
	if _, found, err := c.FindTransferByReference(context.Background(), "IT-SELL-abc", fwAround); err != nil || found {
		t.Fatalf("found=%v err=%v, want the out-of-window transfer ignored", found, err)
	}
}

func TestFindTransferByReference_StuckCursorIsAnError(t *testing.T) {
	c, _ := fwTransfersServer(t, func(url.Values) any {
		return map[string]any{"status": "success", "data": map[string]any{
			"cursor":    map[string]any{"next": "same", "has_more_items": true},
			"transfers": []any{fwTr("trf_x", "other", "SUCCESSFUL", fwAround)},
		}}
	})
	if _, found, err := c.FindTransferByReference(context.Background(), "IT-SELL-abc", fwAround); err == nil || found {
		t.Fatalf("found=%v err=%v, want an error for a cursor that never advances", found, err)
	}
}
